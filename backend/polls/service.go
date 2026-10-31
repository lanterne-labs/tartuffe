// Package polls is the site's events: the polls (the 2027 presidential
// consultation first), their candidates and blackouts. It serves
// /api/polls/*: what the event is and who its candidates are, with their
// cards. The votes themselves are isoloir's (github.com/tartuffeme/isoloir):
// the ballots, the log and the results live on the vote ledger's mirror,
// and the vote app reads them there. Who the candidates are comes from
// People: the polls keep only their ids.
package polls

import (
	"context"
	"sort"

	"github.com/go-chi/chi/v5"
)

// People gives the cards of the candidates (persons of the mirror today).
type People interface {
	Cards(ctx context.Context, ids []int64) (map[int64]Card, error)
}

// Service serves the events.
type Service struct {
	store  Store
	people People
}

// NewService assembles the polls on their store.
func NewService(store Store, people People) *Service {
	return &Service{store: store, people: people}
}

// Routes mounts the polls' endpoints under r (the API's /api).
func (s *Service) Routes(r chi.Router) {
	r.Get("/polls", s.handleListPolls)
	r.Get("/polls/{slug}", s.handleGetPoll)
}

// Cards gives the cards of persons (the candidates' names and photos).
func (s *Service) Cards(ctx context.Context, ids []int64) (map[int64]Card, error) {
	return s.people.Cards(ctx, ids)
}

// Option is one option of an event as the site lists it: a candidate with
// their card, or the blank.
type Option struct {
	Kind     string  `json:"kind"`
	PersonID int64   `json:"person_id,omitempty"`
	Name     string  `json:"name"`
	ImageURL *string `json:"image_url,omitempty"`
	Honesty  float64 `json:"honesty_score"`
}

// Options lists an event's candidates, by name, then the blank: the order
// an isoloir poll's options take (dm poll isoloir-setup).
func (s *Service) Options(ctx context.Context, e *Event) ([]Option, error) {
	ids, err := s.store.CandidateIDs(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	cards, err := s.people.Cards(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Option, 0, len(ids)+1)
	for _, id := range ids {
		c := cards[id]
		out = append(out, Option{Kind: "candidate", PersonID: id, Name: c.Name, ImageURL: c.ImageURL, Honesty: c.HonestyScore})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return append(out, Option{Kind: "blank"}), nil
}
