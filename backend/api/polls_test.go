package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"digital-mirror/backend/auth"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memPolls is an in-memory pre-poll store: one event, its candidates, the
// users, sessions and ballots.
type memPolls struct {
	challenges map[string]string
	event      *storage.PollEvent
	candidates []storage.PollCandidateTally
	users      map[string]*storage.AppUser
	sessions   map[string]int64
	ballots    map[int64][]int64
	nextUser   int64

	reports []storage.UserReport
	// targets are the facts and contradictions that exist, as "kind:id".
	targets map[string]bool
	feed    []storage.FeedItem
	// lastFeedFilter is the filter of the last GetFeed.
	lastFeedFilter storage.FeedFilter
	share          map[string]*storage.ShareTarget
	scores         map[int64]*storage.ScoreExplanation
}

func (m *memPolls) init() {
	if m.users == nil {
		m.users, m.sessions, m.ballots = map[string]*storage.AppUser{}, map[string]int64{}, map[int64][]int64{}
	}
}

func (m *memPolls) ListPollEvents(context.Context) ([]storage.PollEvent, error) {
	if m.event == nil {
		return []storage.PollEvent{}, nil
	}
	return []storage.PollEvent{*m.event}, nil
}

func (m *memPolls) GetPollEvent(_ context.Context, slug string) (*storage.PollEvent, error) {
	if m.event == nil || m.event.Slug != slug {
		return nil, nil
	}
	return m.event, nil
}

func (m *memPolls) GetPollTallies(context.Context, int64) ([]storage.PollCandidateTally, int, error) {
	m.init()
	out := make([]storage.PollCandidateTally, len(m.candidates))
	copy(out, m.candidates)
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

func (m *memPolls) GetPollBallot(_ context.Context, _ int64, userID int64) ([]int64, error) {
	m.init()
	if b := m.ballots[userID]; b != nil {
		return b, nil
	}
	return []int64{}, nil
}

func (m *memPolls) GetPollCandidateIDs(context.Context, int64) ([]int64, error) {
	ids := []int64{}
	for _, c := range m.candidates {
		ids = append(ids, c.PersonID)
	}
	return ids, nil
}

func (m *memPolls) UpdatePollBallot(_ context.Context, _ int64, userID int64, consent bool, edit func([]int64) ([]int64, error)) ([]int64, error) {
	m.init()
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
	m.ballots[userID] = next
	for _, u := range m.users {
		if consent && u.ID == userID && u.ConsentAt == nil {
			now := time.Now()
			u.ConsentAt = &now
		}
	}
	return next, nil
}

func (m *memPolls) GetUserPollChoices(_ context.Context, userID int64) ([]storage.UserPollChoice, error) {
	m.init()
	out := []storage.UserPollChoice{}
	for i, id := range m.ballots[userID] {
		for _, c := range m.candidates {
			if c.PersonID == id {
				out = append(out, storage.UserPollChoice{EventID: m.event.ID, Rank: i + 1, PersonID: id, Name: c.Name, HonestyScore: c.HonestyScore})
			}
		}
	}
	return out, nil
}

func (m *memPolls) DeleteAppUser(_ context.Context, userID int64) error {
	m.init()
	for k, u := range m.users {
		if u.ID == userID {
			delete(m.users, k)
		}
	}
	for h, id := range m.sessions {
		if id == userID {
			delete(m.sessions, h)
		}
	}
	delete(m.ballots, userID)
	return nil
}

func (m *memPolls) UpsertAppUser(_ context.Context, provider, subject, name string) (*storage.AppUser, error) {
	m.init()
	key := provider + "/" + subject
	if u, ok := m.users[key]; ok {
		return u, nil
	}
	m.nextUser++
	u := &storage.AppUser{ID: m.nextUser, Provider: provider, DisplayName: name}
	m.users[key] = u
	return u, nil
}

func (m *memPolls) CreateUserSession(_ context.Context, userID int64, hash []byte, _ time.Time) error {
	m.init()
	m.sessions[string(hash)] = userID
	return nil
}

func (m *memPolls) GetSessionUser(_ context.Context, hash []byte) (*storage.AppUser, error) {
	m.init()
	id, ok := m.sessions[string(hash)]
	if !ok {
		return nil, nil
	}
	for _, u := range m.users {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, nil
}

func (m *memPolls) CreateAuthChallenge(_ context.Context, provider, challenge string, _ time.Time) error {
	if m.challenges == nil {
		m.challenges = map[string]string{}
	}
	m.challenges[challenge] = provider
	return nil
}

func (m *memPolls) ConsumeAuthChallenge(_ context.Context, provider, challenge string) (bool, error) {
	if m.challenges[challenge] != provider {
		return false, nil
	}
	delete(m.challenges, challenge)
	return true, nil
}

func (m *memPolls) DeleteUserSession(_ context.Context, hash []byte) error {
	m.init()
	delete(m.sessions, string(hash))
	return nil
}

func do(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func signIn(t *testing.T, h http.Handler, pseudonym string) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/auth/signin/noop", "", map[string]string{"pseudonym": pseudonym})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Token)
	return resp.Token
}

func pollFixture() *fakeStorer {
	closes := time.Now().Add(time.Hour)
	return &fakeStorer{memPolls: memPolls{
		event: &storage.PollEvent{ID: 1, Slug: "presidentielle-2027", MaxChoices: 2, MinVoters: 2,
			OpensAt: time.Now().Add(-time.Hour), ClosesAt: &closes},
		candidates: []storage.PollCandidateTally{
			{PersonID: 10, Name: "Alice", HonestyScore: 0.4},
			{PersonID: 20, Name: "Bruno", HonestyScore: 0.9},
		},
	}}
}

// TestPollFlow: sign in, vote with consent, and the results show once
// min_voters ballots are in.
func TestPollFlow(t *testing.T) {
	fs := pollFixture()
	h := NewServer(fs, auth.NewRegistry(auth.Noop{})).Handler()

	rec := do(t, h, http.MethodGet, "/api/auth/providers", "", nil)
	assert.JSONEq(t, `[{"name":"noop","verified":false}]`, rec.Body.String())

	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/ballot", "", map[string]any{"person_ids": []int64{10}}).Code)

	alice := signIn(t, h, "alice")
	rec = do(t, h, http.MethodPost, "/api/polls/presidentielle-2027/ballot", alice, map[string]any{"person_ids": []int64{10}})
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

	bruno := signIn(t, h, "Bruno")
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

// TestDisabledProvider: when a provider is turned off its sessions stop working.
func TestDisabledProvider(t *testing.T) {
	fs := pollFixture()
	token := signIn(t, NewServer(fs, auth.NewRegistry(auth.Noop{})).Handler(), "alice")
	off := NewServer(fs, auth.NewRegistry()).Handler()
	assert.Equal(t, http.StatusUnauthorized, do(t, off, http.MethodGet, "/api/auth/me", token, nil).Code)
	assert.Equal(t, http.StatusNotFound, do(t, off, http.MethodPost, "/api/auth/signin/noop", "", map[string]string{"pseudonym": "x"}).Code)
}

// TestClosedPoll: a ballot after closes_at is refused with 403.
func TestClosedPoll(t *testing.T) {
	fs := pollFixture()
	past := time.Now().Add(-time.Minute)
	fs.event.ClosesAt = &past
	h := NewServer(fs, auth.NewRegistry(auth.Noop{})).Handler()
	token := signIn(t, h, "alice")
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
	fs := pollFixture()
	fs.candidates = append(fs.candidates, storage.PollCandidateTally{PersonID: 30, Name: "Chloé", HonestyScore: 0.7})
	h := NewServer(fs, auth.NewRegistry(auth.Noop{})).Handler()
	votes := "/api/polls/presidentielle-2027/votes"
	alice := signIn(t, h, "alice")

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
	fs := pollFixture()
	h := NewServer(fs, auth.NewRegistry(auth.Noop{})).Handler()
	alice := signIn(t, h, "alice")
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

	bruno := signIn(t, h, "bruno")
	rec = do(t, h, http.MethodGet, "/api/me/votes", bruno, nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &mine))
	assert.Empty(t, mine.Events, "a closed event without a vote is not listed")
}

// TestMyVotesAndDeleteAccount: the profile lists the open event even with no
// vote; deleting the account erases the votes and ends the session.
func TestMyVotesAndDeleteAccount(t *testing.T) {
	fs := pollFixture()
	h := NewServer(fs, auth.NewRegistry(auth.Noop{})).Handler()
	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodGet, "/api/me/votes", "", nil).Code)
	alice := signIn(t, h, "alice")

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
	_, voters, err := fs.GetPollTallies(context.Background(), 1)
	require.NoError(t, err)
	assert.Zero(t, voters, "the deleted user's votes are gone")

	again := signIn(t, h, "alice")
	rec = do(t, h, http.MethodGet, "/api/polls/presidentielle-2027/ballot", again, nil)
	assert.Empty(t, ballotOf(t, rec).PersonIDs, "signing in again starts an empty account")
	assert.False(t, ballotOf(t, rec).Consented)
}

// challengeProvider is a Challenger that accepts any payload naming the
// challenge it was given.
type challengeProvider struct{}

func (challengeProvider) Name() string { return "zkfake" }
func (challengeProvider) Info() auth.ProviderInfo {
	return auth.ProviderInfo{Name: "zkfake", Verified: true}
}
func (challengeProvider) ChallengeTTL() time.Duration { return time.Minute }
func (challengeProvider) Verify(_ context.Context, payload json.RawMessage) (auth.Identity, error) {
	var p struct {
		Subject string `json:"subject"`
	}
	_ = json.Unmarshal(payload, &p)
	return auth.Identity{Provider: "zkfake", Subject: p.Subject}, nil
}

// TestChallengeFlow: a challenger's sign-in needs a challenge it issued, once.
func TestChallengeFlow(t *testing.T) {
	fs := pollFixture()
	h := NewServer(fs, auth.NewRegistry(auth.Noop{}, challengeProvider{})).Handler()

	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPost, "/api/auth/challenge/noop", "", nil).Code, "noop takes no challenge")

	rec := do(t, h, http.MethodPost, "/api/auth/challenge/zkfake", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var c struct {
		Challenge string `json:"challenge"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &c))
	require.NotEmpty(t, c.Challenge)

	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPost, "/api/auth/signin/zkfake", "", map[string]string{"subject": "s"}).Code, "no challenge")
	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPost, "/api/auth/signin/zkfake", "", map[string]string{"subject": "s", "challenge": "forged"}).Code)
	ok := do(t, h, http.MethodPost, "/api/auth/signin/zkfake", "", map[string]string{"subject": "s", "challenge": c.Challenge})
	assert.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPost, "/api/auth/signin/zkfake", "", map[string]string{"subject": "s", "challenge": c.Challenge}).Code, "a challenge serves once")

	rec = do(t, h, http.MethodGet, "/api/auth/providers", "", nil)
	assert.JSONEq(t, `[{"name":"zkfake","verified":true},{"name":"noop","verified":false}]`, rec.Body.String(), "verified providers first")
}

// TestVotePreflight: the frontend is on another origin, so removing a vote
// (DELETE) needs the CORS preflight to allow the method.
func TestVotePreflight(t *testing.T) {
	h := NewServer(pollFixture(), auth.NewRegistry(auth.Noop{})).Handler()
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequest(http.MethodOptions, "/api/polls/presidentielle-2027/votes/10", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		req.Header.Set("Access-Control-Request-Method", method)
		req.Header.Set("Access-Control-Request-Headers", "Authorization")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, "http://localhost:3000", rec.Header().Get("Access-Control-Allow-Origin"), method)
		assert.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), method)
	}
}
