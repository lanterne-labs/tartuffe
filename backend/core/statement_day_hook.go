package core

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"digital-mirror/backend"
)

// StatementDayFinder finds the exact day of a stored fact whose statement
// the text dates only to a year or a month, and stores it
// (backend/statementday).
type StatementDayFinder interface {
	FindStatementDay(ctx context.Context, factID, personID int64, person, text, quote string, known time.Time, source *time.Time) error
}

var statementDays atomic.Pointer[StatementDayFinder]

// UseStatementDays sets the finder every inserter of the process asks right
// after it stores a candidate's fact dated to a year or a month: the day is
// searched at once, not at the next maintenance pass. nil: no search.
func UseStatementDays(f StatementDayFinder) {
	if f == nil {
		statementDays.Store(nil)
		return
	}
	statementDays.Store(&f)
}

// findStatementDay asks the finder for the day of the fact just stored. A
// failure leaves the fact with its year or month: the maintenance pass
// searches it again.
func (fi *FactInserter) findStatementDay(ctx context.Context, factID, personID int64, input StoreFactInput) {
	f := statementDays.Load()
	if f == nil || input.StatementDate == nil {
		return
	}
	switch backend.DatePrecisionOf(*input.StatementDate) {
	case backend.DatePrecisionYear, backend.DatePrecisionMonth:
	default:
		return
	}
	if candidate, err := fi.isCandidate(ctx, personID); err != nil || !candidate {
		return
	}
	if err := (*f).FindStatementDay(ctx, factID, personID, input.SubjectEntity, input.NormalizedFactText, input.OriginalQuote, *input.StatementDate, input.PublishedAt); err != nil {
		log.Warn().Err(err).Int64("fact_id", factID).Msg("The day of the statement could not be searched; the maintenance pass will")
	}
}
