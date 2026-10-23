// Package polls is the site's votes: the polls (the 2027 presidential
// pre-poll first, any question later), their anonymous ballots and their
// results. A document (accounts.ProveDocument) admits a voter's commitment;
// a ballot proves that one of the admitted voters cast it, never which
// (package semaphore); every registration, ballot and tally goes to a public
// log anyone can recount. It serves /api/polls/*, /api/voters/*,
// /api/vote-log, /api/vault/* and /api/links/*. Who the candidates are comes
// from People: the polls keep only their ids. No ballot names an account.
package polls

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"digital-mirror/backend/accounts"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// People gives the cards of the candidates (persons of the mirror today).
type People interface {
	Cards(ctx context.Context, ids []int64) (map[int64]Card, error)
}

// Service serves the polls.
type Service struct {
	store Store
	// box keeps the anonymous ballots: the voters' commitments, the
	// ballots, the public log, the vault and the device links.
	box      *PGStore
	people   People
	accounts *accounts.Service
	results  *resultsCache
	batches  BatchConfig
	// signer signs the log's checkpoints; relays are the Nostr relays the
	// ballots come through.
	signer *LogSigner
	relays []string
	// seen are the relay events read, by id, with their date.
	seenMu sync.Mutex
	seen   map[string]int64
}

// Config is how the polls run: when the ballots are published, the key of
// the log's checkpoints, and the relays the ballots come through (none: the
// ballots are only posted).
type Config struct {
	Batches BatchConfig
	Signer  *LogSigner
	Relays  []string
}

// BatchConfig says when the waiting ballots are published: every Interval,
// once at least Min wait, or whatever waits once the oldest waited MaxWait.
type BatchConfig struct {
	Interval time.Duration
	Min      int
	MaxWait  time.Duration
}

// DefaultBatches publishes hourly, at least 3 ballots at once, none waiting
// more than 6 hours.
var DefaultBatches = BatchConfig{Interval: time.Hour, Min: 3, MaxWait: 6 * time.Hour}

// nextBatch is when a ballot cast at now is published at the latest.
func (c BatchConfig) nextBatch(now time.Time) time.Time {
	return now.Truncate(c.Interval).Add(c.Interval).Add(c.MaxWait)
}

// NewService serves the polls of the store and the ballots of the box (the
// same PGStore, but in tests); acc checks the documents.
func NewService(store Store, box *PGStore, people People, acc *accounts.Service, cfg Config) *Service {
	return &Service{store: store, box: box, people: people, accounts: acc, results: newResultsCache(resultsTTL),
		batches: cfg.Batches, signer: cfg.Signer, relays: cfg.Relays}
}

// Routes mounts the polls' endpoints on the /api router. signin limits the
// registrations (each costs a document proof check).
func (s *Service) Routes(r chi.Router, signin func(http.Handler) http.Handler) {
	r.Get("/polls", s.handleListPolls)
	r.Get("/polls/{slug}", s.handleGetPoll)
	r.Post("/polls/{slug}/ballots", s.handleCastBallot)
	r.Get("/polls/{slug}/ballots/{series}/{window}", s.handleListBallots)
	r.Get("/voters/trees/{series}/{window}", s.handleVoterTree)
	r.With(signin).Post("/voters/register/{provider}", s.handleRegister)
	r.Get("/vote-log", s.handleLog)
	r.Get("/vote-log/checkpoint", s.handleCheckpoint)
	r.Get("/vote-log/key", s.handleLogKey)
	r.Get("/vote-log/proof/entry", s.handleProveEntry)
	r.Get("/vote-log/proof/append", s.handleProveAppend)
	r.Get("/vote-log/stamps", s.handleStamps)
	r.Get("/vault/{lookup}", s.handleVaultGet)
	r.With(signin).Put("/vault/{lookup}", s.handleVaultPut)
	r.With(signin).Post("/links/{channel}", s.handleLinkPut)
	r.Get("/links/{channel}", s.handleLinkTake)
}

// Run publishes the waiting ballots in batches until ctx ends: at each
// batch it reads the relays' new ballots, publishes, and stamps the log's
// new checkpoint on Bitcoin.
func (s *Service) Run(ctx context.Context) {
	if err := s.box.FillTree(ctx); err != nil {
		log.Error().Err(err).Msg("fill the vote log's tree")
	}
	tick := time.NewTicker(s.batches.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.ReadRelays(ctx)
			events, err := s.box.PublishBatch(ctx, s.batches.Min, s.batches.MaxWait)
			if err != nil {
				log.Error().Err(err).Msg("publish ballots")
				continue
			}
			for _, e := range events {
				s.results.invalidate(e)
			}
			if len(events) > 0 {
				log.Info().Int("polls", len(events)).Msg("ballots published")
			}
			if st, err := s.box.StampHead(ctx, s.signer); err != nil {
				log.Warn().Err(err).Msg("stamp the vote log's checkpoint")
			} else if st != nil {
				log.Info().Int64("size", st.Size).Msg("vote log checkpoint stamped")
			}
		}
	}
}

// tallies are the event's counts with the candidates' cards, by name.
func (s *Service) tallies(ctx context.Context, eventID int64) ([]CandidateTally, Turnout, error) {
	return TalliesWithCards(ctx, s.store, s.people, eventID)
}

// TalliesWithCards are the event's counts of the store with the candidates'
// cards of people, by name.
func TalliesWithCards(ctx context.Context, store Store, people People, eventID int64) ([]CandidateTally, Turnout, error) {
	out, turnout, err := store.Tallies(ctx, eventID)
	if err != nil {
		return nil, Turnout{}, err
	}
	ids := make([]int64, len(out))
	for i, c := range out {
		ids[i] = c.PersonID
	}
	cards, err := people.Cards(ctx, ids)
	if err != nil {
		return nil, Turnout{}, err
	}
	for i := range out {
		out[i].Card = cards[out[i].PersonID]
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, turnout, nil
}
