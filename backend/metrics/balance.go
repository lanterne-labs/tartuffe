package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// LLMBalanceUSD tracks the remaining balance in USD per LLM provider.
	LLMBalanceUSD = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      "llm_balance_usd",
			Help:      "Remaining LLM credit balance in USD by provider",
		},
		[]string{"provider"},
	)
)

func init() {
	Registry.MustRegister(LLMBalanceUSD)
}

// SetLLMBalance updates the balance gauge for a provider.
func SetLLMBalance(provider string, balance float64) {
	LLMBalanceUSD.WithLabelValues(provider).Set(balance)
}
