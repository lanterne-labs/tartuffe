package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func (m *memPolls) SavePollBallot(_ context.Context, _ int64, userID int64, ids []int64) error {
	m.init()
	m.ballots[userID] = ids
	for _, u := range m.users {
		if u.ID == userID && u.ConsentAt == nil {
			now := time.Now()
			u.ConsentAt = &now
		}
	}
	return nil
}

func (m *memPolls) UpsertAppUser(_ context.Context, provider, subject, name string) (*storage.AppUser, error) {
	m.init()
	key := provider + "/" + subject
	if u, ok := m.users[key]; ok {
		return u, nil
	}
	u := &storage.AppUser{ID: int64(len(m.users) + 1), Provider: provider, DisplayName: name}
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
	assert.JSONEq(t, `{"person_ids":[10,20],"consented":true}`, rec.Body.String())

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
