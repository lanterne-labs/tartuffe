package storage

import (
	"context"
)

// ProgressJob is the job_metrics job_type of the progress of the long
// operations that have no table of their own (the fold, the detection over
// every person): target names the operation, the metrics done and total
// count its persons. Grafana (Operations) draws them; the collection tasks
// carry their own progress (collection_tasks.operation).
const ProgressJob = "progress"

// RecordProgress stores that operation has done of total persons now.
func (s *PostgresStorer) RecordProgress(ctx context.Context, operation string, done, total int) error {
	return s.RecordJobMetrics(ctx, []JobMetric{
		{JobType: ProgressJob, Target: operation, Metric: "done", Value: float64(done)},
		{JobType: ProgressJob, Target: operation, Metric: "total", Value: float64(total)},
	})
}
