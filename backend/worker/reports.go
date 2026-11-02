package worker

import (
	"context"
	"errors"
	"time"

	"digital-mirror/backend/llm"

	"github.com/rs/zerolog/log"
)

// ReportsEvery is the pause between two passes over the users' reports.
const ReportsEvery = time.Minute

// RunReports judges the users' reports every interval until ctx ends, beside
// the tasks: a task can run for hours, and a report is answered within
// minutes. judge returns the number of reports judged. An unavailable LLM
// leaves the reports pending until the next pass.
func RunReports(ctx context.Context, judge func(ctx context.Context) (int, error), every time.Duration) {
	for {
		n, err := judge(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, llm.ErrUnavailable):
			log.Warn().Err(err).Msg("Worker: LLM unavailable for the user reports; they wait for the next pass")
		case err != nil:
			log.Error().Err(err).Msg("Worker: judging the user reports failed")
		case n > 0:
			log.Info().Int("judged", n).Msg("Worker: user reports judged")
		}
		if n > 0 && err == nil {
			continue // more may be waiting
		}
		select {
		case <-time.After(every):
		case <-ctx.Done():
			return
		}
	}
}
