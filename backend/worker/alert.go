package worker

import (
	"context"
	"fmt"
	"html"
	"sync"

	"github.com/rs/zerolog/log"
)

// CreditAlert returns the llm.OnCreditError hook of the worker: the first
// credit error of each provider sends one message with send. The later ones
// of the same provider send nothing, so many failed calls do not flood the
// channel.
func CreditAlert(send func(context.Context, string) error) func(provider, errMsg string) {
	var mu sync.Mutex
	sent := map[string]bool{}
	return func(provider, errMsg string) {
		mu.Lock()
		first := !sent[provider]
		sent[provider] = true
		mu.Unlock()
		if !first {
			return
		}
		if len(errMsg) > 300 {
			errMsg = errMsg[:300] + "..."
		}
		// Telegram parses the message as HTML: the error text is escaped.
		msg := fmt.Sprintf("<b>LLM credit error</b>\nProvider: %s\nError: %s", html.EscapeString(provider), html.EscapeString(errMsg))
		if err := send(context.Background(), msg); err != nil {
			log.Error().Err(err).Str("provider", provider).Msg("Failed to send the LLM credit alert")
		}
	}
}
