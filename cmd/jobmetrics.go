package cmd

import (
	"context"
	"sort"

	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// jobMetrics builds one storage.JobMetric per entry of values for a job and
// target, in metric-name order so the batch is deterministic.
func jobMetrics(jobType, target string, values map[string]float64) []storage.JobMetric {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]storage.JobMetric, 0, len(names))
	for _, name := range names {
		out = append(out, storage.JobMetric{JobType: jobType, Target: target, Metric: name, Value: values[name]})
	}
	return out
}

// recordJobMetrics persists metrics in one batch. A failure is logged rather
// than returned: a finished job does not fail on its bookkeeping.
func recordJobMetrics(ctx context.Context, storer *storage.PostgresStorer, metrics []storage.JobMetric) {
	if err := storer.RecordJobMetrics(ctx, metrics); err != nil {
		log.Error().Err(err).Msg("Failed to record job metrics")
	}
}
