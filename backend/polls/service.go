// Package polls is the site's events: the polls (the 2027 presidential
// consultation first, any question later), their candidates and blackouts,
// and the public log (votelog.go, checkpoint.go, stamps.go) that the vote
// protocol writes to and anyone recounts (backend/vote, docs/vote/SPEC.md).
// It serves /api/polls/* and /api/vote-log*. Who the candidates are comes
// from People: the polls keep only their ids.
package polls

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// People gives the cards of the candidates (persons of the mirror today).
type People interface {
	Cards(ctx context.Context, ids []int64) (map[int64]Card, error)
}

// Service serves the events and the log.
type Service struct {
	// Extra mounts the routes of the vote protocol beside the log's
	// (backend/vote).
	Extra func(r chi.Router)
	// ResultsOf gives an event's results as the site shows them (the vote
	// protocol's latest tally); nil serves none.
	ResultsOf func(ctx context.Context, e *Event) (any, error)
	store     Store
	// box is the public log.
	box    *PGStore
	people People
	// signer holds the log's key: the verifier key is served, the signing
	// is the ingest server's (backend/vote).
	signer *LogSigner
}

// Config is how the polls run: the key of the log's checkpoints.
type Config struct {
	Signer *LogSigner
}

// NewService assembles the polls on their store and log.
func NewService(store Store, box *PGStore, people People, cfg Config) *Service {
	return &Service{store: store, box: box, people: people, signer: cfg.Signer}
}

// Routes mounts the polls' endpoints under r (the API's /api).
func (s *Service) Routes(r chi.Router) {
	r.Get("/polls", s.handleListPolls)
	r.Get("/polls/{slug}", s.handleGetPoll)
	r.Get("/vote-log", s.handleLog)
	r.Get("/vote-log/key", s.handleLogKey)
	r.Get("/vote-log/proof/entry", s.handleProveEntry)
	r.Get("/vote-log/proof/append", s.handleProveAppend)
	r.Get("/vote-log/stamps", s.handleStamps)
	if s.Extra != nil {
		s.Extra(r)
	}
}

// FillTree fills the log's Merkle tree from the entries logged before it
// was kept (a start-up repair).
func (s *Service) FillTree(ctx context.Context) error { return s.box.FillTree(ctx) }

// Cards is the People's cards, for the results.
func (s *Service) Cards(ctx context.Context, ids []int64) (map[int64]Card, error) {
	return s.people.Cards(ctx, ids)
}

var _ = http.StatusOK
