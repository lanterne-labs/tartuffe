// Package polls is the site's votes: the polls (the 2027 presidential
// pre-poll first, any question later), their ballots, the consent to keep
// them, and their results. It serves /api/polls/* and /api/me/votes. Who is
// signed in, and whether their identity is verified, comes from accounts;
// who the candidates are comes from People. The polls keep only ids of both.
package polls

import (
	"context"
	"sort"

	"digital-mirror/backend/accounts"

	"github.com/go-chi/chi/v5"
)

// People gives the cards of the candidates (persons of the mirror today).
type People interface {
	Cards(ctx context.Context, ids []int64) (map[int64]Card, error)
}

// Service serves the polls.
type Service struct {
	store    Store
	people   People
	accounts *accounts.Service
	results  *resultsCache
}

// NewService serves the polls of the store, for the users of accounts.
func NewService(store Store, people People, acc *accounts.Service) *Service {
	return &Service{store: store, people: people, accounts: acc, results: newResultsCache(resultsTTL)}
}

// Changed drops the cached results: ballots changed out of the polls' sight
// (an identity moved, an account deleted).
func (s *Service) Changed() { s.results.invalidate(0) }

// Routes mounts the polls' endpoints on the /api router.
func (s *Service) Routes(r chi.Router) {
	need := s.accounts.RequireUser
	r.Get("/polls", s.handleListPolls)
	r.Get("/polls/{slug}", s.handleGetPoll)
	r.Get("/polls/{slug}/ballot", need(s.handleGetBallot))
	r.Post("/polls/{slug}/ballot", need(s.handleSaveBallot))
	r.Post("/polls/{slug}/votes", need(s.handleAddVote))
	r.Delete("/polls/{slug}/votes/{personID}", need(s.handleRemoveVote))
	r.Get("/me/votes", need(s.handleMyVotes))
}

// tallies are the event's counts with the candidates' cards, by name.
func (s *Service) tallies(ctx context.Context, eventID int64) ([]CandidateTally, int, error) {
	return TalliesWithCards(ctx, s.store, s.people, eventID)
}

// TalliesWithCards are the event's counts of the store with the candidates'
// cards of people, by name.
func TalliesWithCards(ctx context.Context, store Store, people People, eventID int64) ([]CandidateTally, int, error) {
	out, voters, err := store.Tallies(ctx, eventID)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]int64, len(out))
	for i, c := range out {
		ids[i] = c.PersonID
	}
	cards, err := people.Cards(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range out {
		out[i].Card = cards[out[i].PersonID]
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, voters, nil
}

// choices are the user's votes with the candidates' cards.
func (s *Service) choices(ctx context.Context, userID int64) ([]UserChoice, error) {
	out, err := s.store.UserChoices(ctx, userID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(out))
	for i, c := range out {
		ids[i] = c.PersonID
	}
	cards, err := s.people.Cards(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		c := cards[out[i].PersonID]
		out[i].Name, out[i].ImageURL, out[i].HonestyScore, out[i].FactCount = c.Name, c.ImageURL, c.HonestyScore, c.FactCount
	}
	return out, nil
}
