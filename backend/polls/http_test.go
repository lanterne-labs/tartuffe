package polls

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/accounts/accountstest"
	"digital-mirror/backend/accounts/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var do = accountstest.Do

// memStore keeps the polls in memory; verified stands for LockVerified.
type memStore struct {
	mu         sync.Mutex
	event      *Event
	candidates []int64
	cards      map[int64]Card
	ballots    map[int64][]int64
	consents   map[int64]bool
	verified   func(userID int64) bool
}

func (m *memStore) addCandidate(id int64, c Card) {
	m.candidates = append(m.candidates, id)
	m.cards[id] = c
}

func (m *memStore) Cards(_ context.Context, ids []int64) (map[int64]Card, error) {
	out := map[int64]Card{}
	for _, id := range ids {
		if c, ok := m.cards[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

func (m *memStore) ListEvents(context.Context) ([]Event, error) {
	if m.event == nil {
		return []Event{}, nil
	}
	return []Event{*m.event}, nil
}

func (m *memStore) GetEvent(_ context.Context, slug string) (*Event, error) {
	if m.event == nil || m.event.Slug != slug {
		return nil, nil
	}
	return m.event, nil
}

func (m *memStore) CandidateIDs(context.Context, int64) ([]int64, error) {
	return slices.Clone(m.candidates), nil
}

func (m *memStore) Tallies(context.Context, int64) ([]CandidateTally, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []CandidateTally{}
	for _, id := range m.candidates {
		out = append(out, CandidateTally{PersonID: id})
	}
	voters := 0
	for _, b := range m.ballots {
		if len(b) == 0 {
			continue
		}
		voters++
		for i, id := range b {
			for j := range out {
				if out[j].PersonID == id {
					out[j].Chosen++
					if i == 0 {
						out[j].FirstChoices++
					}
				}
			}
		}
	}
	return out, voters, nil
}

func (m *memStore) Ballot(_ context.Context, _ int64, userID int64) ([]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.ballots[userID]; b != nil {
		return slices.Clone(b), nil
	}
	return []int64{}, nil
}

func (m *memStore) Consented(_ context.Context, userID int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.consents[userID], nil
}

func (m *memStore) UpdateBallot(_ context.Context, _ int64, userID int64, consent bool, edit func([]int64) ([]int64, error)) ([]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := slices.Clone(m.ballots[userID])
	if current == nil {
		current = []int64{}
	}
	next, err := edit(current)
	if err != nil {
		return nil, err
	}
	if slices.Equal(current, next) {
		return current, nil
	}
	if !m.verified(userID) && slices.ContainsFunc(next, func(id int64) bool { return !slices.Contains(current, id) }) {
		return nil, ErrNotVerified
	}
	if consent {
		m.consents[userID] = true
	}
	m.ballots[userID] = next
	return next, nil
}

func (m *memStore) UserChoices(_ context.Context, userID int64) ([]UserChoice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []UserChoice{}
	for i, id := range m.ballots[userID] {
		out = append(out, UserChoice{EventID: m.event.ID, Rank: i + 1, PersonID: id})
	}
	return out, nil
}

// pollFixture is an open event with two candidates, and the router of the
// polls and the accounts (noop), the accounts' hooks wired to the store.
func pollFixture() (*memStore, http.Handler) {
	closes := time.Now().Add(time.Hour)
	fs := &memStore{
		event: &Event{ID: 1, Slug: "presidentielle-2027", MaxChoices: 2, MinVoters: 2,
			OpensAt: time.Now().Add(-time.Hour), ClosesAt: &closes},
		cards: map[int64]Card{}, ballots: map[int64][]int64{}, consents: map[int64]bool{},
	}
	fs.addCandidate(10, Card{Name: "Alice", HonestyScore: 0.4})
	fs.addCandidate(20, Card{Name: "Bruno", HonestyScore: 0.9})
	accStore := accountstest.NewMemStore()
	fs.verified = accStore.Verified
	accStore.OnMoved = func(from, to int64) {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		fs.ballots[to], fs.consents[to] = fs.ballots[from], fs.consents[from]
		delete(fs.ballots, from)
	}
	accStore.OnDeleting = func(id int64) {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		delete(fs.ballots, id)
		delete(fs.consents, id)
	}
	acc := accounts.NewService(accStore, providers.NewRegistry(providers.Noop{}))
	svc := NewService(fs, fs, acc)
	acc.Changed = append(acc.Changed, svc.Changed)
	return fs, accountstest.Router(acc, svc.Routes)
}

// TestPollFlow: sign in, vote with consent, and the results show once
// min_voters ballots are in.
func TestPollFlow(t *testing.T) {
	_, h := pollFixture()

	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/ballot", "", map[string]any{"person_ids": []int64{10}}).Code)

	alice := accountstest.SignIn(t, h, "alice")
	rec := do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/ballot", alice, map[string]any{"person_ids": []int64{10}})
	assert.Equal(t, http.StatusBadRequest, rec.Code, "the first ballot needs the consent")
	assert.Contains(t, rec.Body.String(), "consent")

	rec = do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/ballot", alice, map[string]any{"person_ids": []int64{10, 20}, "consent": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/ballot", alice,
		map[string]any{"person_ids": []int64{10, 20, 30}}).Code, "too many choices")

	rec = do(t, h, http.MethodGet, "/api/polls/presidentielle-2027/ballot", alice, nil)
	assert.JSONEq(t, `{"person_ids":[10,20],"consented":true,"max_choices":2,"open":true}`, rec.Body.String())

	type pollResponse struct {
		Results struct {
			Voters     int    `json:"voters"`
			Hidden     string `json:"hidden"`
			Candidates []struct {
				Name             string   `json:"name"`
				FirstChoiceShare *float64 `json:"first_choice_share"`
				PreferenceRank   *int     `json:"preference_rank"`
				HonestyRank      int      `json:"honesty_rank"`
			} `json:"candidates"`
		} `json:"results"`
	}
	var got pollResponse
	rec = do(t, h, http.MethodGet, "/api/polls/presidentielle-2027", "", nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, 1, got.Results.Voters)
	assert.Equal(t, "too_few_voters", got.Results.Hidden)
	assert.Nil(t, got.Results.Candidates[0].FirstChoiceShare)

	bruno := accountstest.SignIn(t, h, "Bruno")
	rec = do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/ballot", bruno, map[string]any{"person_ids": []int64{10}, "consent": true})
	require.Equal(t, http.StatusOK, rec.Code)
	got = pollResponse{}
	rec = do(t, h, http.MethodGet, "/api/polls/presidentielle-2027", "", nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, 2, got.Results.Voters)
	assert.Empty(t, got.Results.Hidden)
	require.Len(t, got.Results.Candidates, 2)
	assert.Equal(t, "Alice", got.Results.Candidates[0].Name)
	assert.InDelta(t, 1.0, *got.Results.Candidates[0].FirstChoiceShare, 1e-9)
	assert.Equal(t, 1, *got.Results.Candidates[0].PreferenceRank)
	assert.Equal(t, 2, got.Results.Candidates[0].HonestyRank)

	assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/api/polls/nope", "", nil).Code)

	assert.Equal(t, http.StatusNoContent, do(t, h, http.MethodPost, "/api/auth/signout", alice, nil).Code)
	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodGet, "/api/auth/me", alice, nil).Code)
}

// TestClosedPoll: a ballot after closes_at is refused with 403.
func TestClosedPoll(t *testing.T) {
	fs, h := pollFixture()
	past := time.Now().Add(-time.Minute)
	fs.event.ClosesAt = &past
	token := accountstest.SignIn(t, h, "alice")
	assert.Equal(t, http.StatusForbidden, do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/ballot", token,
		map[string]any{"person_ids": []int64{10}, "consent": true}).Code)
}

// ballotOf decodes a ballot answer.
func ballotOf(t *testing.T, rec *httptest.ResponseRecorder) ballotResponse {
	t.Helper()
	var b ballotResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &b), rec.Body.String())
	return b
}

// TestVoteCredits: votes are cast one at a time up to max_choices; one more is
// refused with the ballot so the page can ask which vote to give up, and a
// replace swaps it in at the same rank. Removing frees a credit.
func TestVoteCredits(t *testing.T) {
	fs, h := pollFixture()
	fs.addCandidate(30, Card{Name: "Chloé", HonestyScore: 0.7})
	votes := "/api/polls/presidentielle-2027/votes"
	alice := accountstest.SignIn(t, h, "alice")

	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodPost, votes, "", map[string]any{"person_id": 10}).Code)
	rec := do(t, h, http.MethodPost, votes, alice, map[string]any{"person_id": 10})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"consent_required"`)

	rec = do(t, h, http.MethodPost, votes, alice, map[string]any{"person_id": 10, "consent": true})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []int64{10}, ballotOf(t, rec).PersonIDs)
	rec = do(t, h, http.MethodPost, votes, alice, map[string]any{"person_id": 10})
	assert.Equal(t, []int64{10}, ballotOf(t, rec).PersonIDs, "a repeated vote changes nothing")
	rec = do(t, h, http.MethodPost, votes, alice, map[string]any{"person_id": 20})
	assert.Equal(t, []int64{10, 20}, ballotOf(t, rec).PersonIDs, "consent is asked once")

	rec = do(t, h, http.MethodPost, votes, alice, map[string]any{"person_id": 30})
	require.Equal(t, http.StatusConflict, rec.Code, "every vote is used")
	var full ballotErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &full))
	assert.Equal(t, codeBallotFull, full.Code)
	require.NotNil(t, full.Ballot)
	assert.Equal(t, []int64{10, 20}, full.Ballot.PersonIDs)
	assert.Equal(t, 2, full.Ballot.MaxChoices)

	rec = do(t, h, http.MethodPost, votes, alice, map[string]any{"person_id": 30, "replace": 99})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), codeNotOnBallot)
	rec = do(t, h, http.MethodPost, votes, alice, map[string]any{"person_id": 30, "replace": 10})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, []int64{30, 20}, ballotOf(t, rec).PersonIDs, "the new vote takes the rank of the one replaced")

	rec = do(t, h, http.MethodPost, votes, alice, map[string]any{"person_id": 77, "replace": 20})
	assert.Equal(t, http.StatusBadRequest, rec.Code, "not a candidate")

	rec = do(t, h, http.MethodDelete, votes+"/30", alice, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []int64{20}, ballotOf(t, rec).PersonIDs, "the vote below moves up")
	rec = do(t, h, http.MethodDelete, votes+"/30", alice, nil)
	assert.Equal(t, http.StatusOK, rec.Code, "removing twice is harmless")
	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodDelete, votes+"/abc", alice, nil).Code)
	rec = do(t, h, http.MethodPost, votes, alice, map[string]any{"person_id": 10})
	assert.Equal(t, []int64{20, 10}, ballotOf(t, rec).PersonIDs, "a freed credit can be used again")

	rec = do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/ballot", alice, map[string]any{"person_ids": []int64{10, 10}})
	assert.Equal(t, http.StatusBadRequest, rec.Code, "a reorder cannot rank one candidate twice")
	rec = do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/ballot", alice, map[string]any{"person_ids": []int64{10, 20}})
	assert.Equal(t, []int64{10, 20}, ballotOf(t, rec).PersonIDs, "reorder")

	// max_choices lowered after the ballot was cast: the votes can still be
	// reordered and removed, not added.
	fs.event.MaxChoices = 1
	rec = do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/ballot", alice, map[string]any{"person_ids": []int64{20, 10}})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = do(t, h, http.MethodPost, votes, alice, map[string]any{"person_id": 30, "replace": 10})
	assert.Equal(t, http.StatusBadRequest, rec.Code, "a replace on an over-full ballot must remove first")
	rec = do(t, h, http.MethodDelete, votes+"/10", alice, nil)
	assert.Equal(t, []int64{20}, ballotOf(t, rec).PersonIDs)
}

// TestClosedVotes: once voting closes, the votes can be neither cast nor
// removed, and the profile still lists them.
func TestClosedVotes(t *testing.T) {
	fs, h := pollFixture()
	alice := accountstest.SignIn(t, h, "alice")
	require.Equal(t, http.StatusOK, do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/votes", alice, map[string]any{"person_id": 10, "consent": true}).Code)

	past := time.Now().Add(-time.Minute)
	fs.event.ClosesAt = &past
	rec := do(t, h, http.MethodDelete, "/api/polls/presidentielle-2027/votes/10", alice, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), codeClosed)
	assert.Equal(t, http.StatusForbidden, do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/votes", alice, map[string]any{"person_id": 20}).Code)

	rec = do(t, h, http.MethodGet, "/api/me/votes", alice, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var mine struct {
		Events []myEventVotes `json:"events"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &mine))
	require.Len(t, mine.Events, 1)
	assert.False(t, mine.Events[0].Open)
	require.Len(t, mine.Events[0].Votes, 1)
	assert.Equal(t, "Alice", mine.Events[0].Votes[0].Name)

	bruno := accountstest.SignIn(t, h, "bruno")
	rec = do(t, h, http.MethodGet, "/api/me/votes", bruno, nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &mine))
	assert.Empty(t, mine.Events, "a closed event without a vote is not listed")
}

// TestMyVotesAndDeleteAccount: the profile lists the open event even with no
// vote; deleting the account erases the votes and ends the session.
func TestMyVotesAndDeleteAccount(t *testing.T) {
	fs, h := pollFixture()
	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodGet, "/api/me/votes", "", nil).Code)
	alice := accountstest.SignIn(t, h, "alice")

	var mine struct {
		Consented bool           `json:"consented"`
		Events    []myEventVotes `json:"events"`
	}
	rec := do(t, h, http.MethodGet, "/api/me/votes", alice, nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &mine))
	require.Len(t, mine.Events, 1)
	assert.True(t, mine.Events[0].Open)
	assert.Empty(t, mine.Events[0].Votes)
	assert.False(t, mine.Consented)

	require.Equal(t, http.StatusOK, do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/votes", alice, map[string]any{"person_id": 20, "consent": true}).Code)
	rec = do(t, h, http.MethodGet, "/api/me/votes", alice, nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &mine))
	assert.True(t, mine.Consented)
	require.Len(t, mine.Events[0].Votes, 1)
	assert.Equal(t, int64(20), mine.Events[0].Votes[0].PersonID)

	assert.Equal(t, http.StatusNoContent, do(t, h, http.MethodDelete, "/api/auth/me", alice, nil).Code)
	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodGet, "/api/me/votes", alice, nil).Code)
	_, voters, err := fs.Tallies(context.Background(), 1)
	require.NoError(t, err)
	assert.Zero(t, voters, "the deleted user's votes are gone")

	again := accountstest.SignIn(t, h, "alice")
	rec = do(t, h, http.MethodGet, "/api/polls/presidentielle-2027/ballot", again, nil)
	assert.Empty(t, ballotOf(t, rec).PersonIDs, "signing in again starts an empty account")
	assert.False(t, ballotOf(t, rec).Consented)
}

// TestVotingNeedsVerification: an account votes only once verified; a
// document shown on another account takes its ballot there.
func TestVotingNeedsVerification(t *testing.T) {
	fs, h := pollFixture()
	vote := func(token string, person int64) *httptest.ResponseRecorder {
		return do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/votes", token, map[string]any{"person_id": person, "consent": true})
	}
	first := accountstest.SignInUnverified(t, h, "first")
	rec := vote(first, 10)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), codeVerificationRequired)

	require.Equal(t, http.StatusOK, do(t, h, http.MethodPost, "/api/me/verify/noop", first, map[string]string{"pseudonym": "doc"}).Code)
	require.Equal(t, http.StatusOK, vote(first, 10).Code)

	second := accountstest.SignInUnverified(t, h, "second")
	accountstest.MoveHere(t, h, second, "doc")
	b := ballotOf(t, do(t, h, http.MethodGet, "/api/polls/presidentielle-2027/ballot", second, nil))
	assert.Equal(t, []int64{10}, b.PersonIDs, "the ballot moved")
	assert.True(t, b.Consented, "with its consent")
	assert.Empty(t, ballotOf(t, do(t, h, http.MethodGet, "/api/polls/presidentielle-2027/ballot", first, nil)).PersonIDs)
	assert.Equal(t, http.StatusForbidden, vote(first, 20).Code, "the first account lost the right to vote")
	_, voters, err := fs.Tallies(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, 1, voters, "still one vote for the document")
}
