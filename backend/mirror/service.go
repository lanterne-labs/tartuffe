// Package mirror is the site's record of what politicians say and do: the
// persons, their facts, stances and contradictions, the feed, the shared
// links, the score explanations, and what the signed-in users add to it
// (their +1 and their reports). It serves those /api routes and the SSE
// stream. Who is signed in comes from accounts; the mirror never reads the
// accounts' tables, nor the polls'.
package mirror

import (
	"context"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"

	"github.com/go-chi/chi/v5"
)

// Store is what the mirror reads and writes (storage.PostgresStorer).
type Store interface {
	GetPersonsRankedByScore(ctx context.Context, limit int, offset int, entityType string, search string, sort string, minFacts int, candidate string) ([]storage.PersonSummary, error)
	GetPersonByID(ctx context.Context, personID int64) (*storage.PersonSummary, error)
	GetPersonIDBySlug(ctx context.Context, slug string) (int64, error)
	GetPersonFacts(ctx context.Context, personID int64, limit int, offset int) (*storage.PaginatedFacts, error)
	GetPersonContradictions(ctx context.Context, personID int64, limit int, offset int) (*storage.PaginatedContradictions, error)
	GetPersonMembers(ctx context.Context, personID int64) ([]storage.MemberInfo, error)
	GetPersonHistory(ctx context.Context, personID int64) ([]storage.HistoryPoint, error)
	GetPersonCoverage(ctx context.Context, personID int64) (*storage.Coverage, error)
	GetFactPersons(ctx context.Context, factID int64, limit int, offset int) ([]storage.PersonSummary, error)
	GetStanceDocumentsForPerson(ctx context.Context, personID int64) ([]core.StanceDocument, error)
	GetStanceContradictionCounts(ctx context.Context, personID int64) (map[int64]int, error)
	GetStanceGroupCounts(ctx context.Context, personID int64) (map[int64]int, error)
	GetStanceContradictionGroups(ctx context.Context, stanceID, viewerID int64) ([]storage.ContradictionGroup, error)
	GetFactsInStance(ctx context.Context, stanceID, viewerID int64) ([]storage.FactInStance, error)
	GetMaxEventID(ctx context.Context) (int64, error)
	GetEventsAfter(ctx context.Context, sinceID int64, limit int) ([]storage.Event, error)

	// The feed, the shared links and the score explanations.
	GetFeed(ctx context.Context, f storage.FeedFilter, after *storage.FeedCursor, limit, minFacts int) ([]storage.FeedItem, error)
	GetShareTarget(ctx context.Context, kind string, id, personID int64) (*storage.ShareTarget, error)
	GetScoreExplanation(ctx context.Context, personID int64) (*storage.ScoreExplanation, error)
	ScoreBaseline(ctx context.Context) (core.ScoreBaseline, error)

	// What the users add: their +1 and their reports.
	SetLike(ctx context.Context, userID int64, kind string, targetID int64, on bool) (int, error)
	CreateUserReport(ctx context.Context, userID int64, kind string, targetID int64, reason string) (*storage.UserReport, error)
	GetUserReports(ctx context.Context, userID int64) ([]storage.UserReport, error)
	ListCorrections(ctx context.Context, beforeID int64, limit int) ([]storage.Correction, error)
}

// Service serves the mirror.
type Service struct {
	store    Store
	accounts *accounts.Service
	baseline baselineCache
}

// NewService serves the mirror of the store, for the users of accounts.
func NewService(store Store, acc *accounts.Service) *Service {
	return &Service{store: store, accounts: acc}
}

// Routes mounts the mirror's endpoints on the /api router.
func (s *Service) Routes(r chi.Router) {
	need := s.accounts.RequireUser
	r.Get("/persons", s.handleGetPersons)
	r.Get("/person/{id}", s.handleGetPerson)
	r.Get("/person/{id}/facts", s.handleGetPersonFacts)
	r.Get("/person/{id}/contradictions", s.handleGetPersonContradictions)
	r.Get("/person/{id}/members", s.handleGetPersonMembers)
	r.Get("/person/{id}/history", s.handleGetPersonHistory)
	r.Get("/person/{id}/coverage", s.handleGetPersonCoverage)
	r.Get("/facts/{id}/persons", s.handleGetFactPersons)
	r.Get("/person/{id}/stances", s.handleGetPersonStances)
	r.Get("/stances/{id}/contradictions", s.handleGetStanceContradictions)
	r.Get("/stances/{id}/facts", s.handleGetStanceFacts)
	r.Get("/person/{id}/score", s.handleScoreExplanation)
	r.Get("/events", s.handleEventsSSE)
	r.Get("/feed", s.handleFeed)
	r.Get("/share/{kind}/{id}", s.handleShare)
	r.Get("/corrections", s.handleCorrections)
	r.Post("/reports", need(s.handleCreateReport))
	r.Get("/me/reports", need(s.handleMyReports))
	r.Put("/likes/{kind}/{id}", need(s.handleLike))
	r.Delete("/likes/{kind}/{id}", need(s.handleLike))
}
