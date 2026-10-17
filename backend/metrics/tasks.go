package metrics

import "github.com/prometheus/client_golang/prometheus"

// CollectionTaskAttemptsTotal counts the attempts of collection tasks the
// worker ended, by plugin and outcome: done, failed, released (retried
// later), cancelled.
var CollectionTaskAttemptsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: Namespace,
		Name:      "collection_task_attempts_total",
		Help:      "Attempts of collection tasks ended by the worker, by plugin and outcome",
	},
	[]string{"plugin", "outcome"},
)

func init() {
	Registry.MustRegister(CollectionTaskAttemptsTotal)
}
