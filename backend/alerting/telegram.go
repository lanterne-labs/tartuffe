package alerting

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/rs/zerolog/log"
)

// TelegramNotifier sends messages via the Telegram Bot API.
type TelegramNotifier struct {
	botToken   string
	chatID     string
	httpClient *http.Client
}

// NewTelegramNotifierFromEnv creates a TelegramNotifier from TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID.
// Returns nil if either env var is missing.
func NewTelegramNotifierFromEnv() *TelegramNotifier {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		return nil
	}
	return &TelegramNotifier{
		botToken:   token,
		chatID:     chatID,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Send posts a message to the configured Telegram chat using HTML parse mode.
func (t *TelegramNotifier) Send(ctx context.Context, message string) error {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)

	params := url.Values{}
	params.Set("chat_id", t.chatID)
	params.Set("text", "mirror: "+message)
	params.Set("parse_mode", "HTML")

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return fmt.Errorf("create telegram request: %w", err)
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram API call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		log.Error().Int("status", resp.StatusCode).Str("body", string(body)).Msg("Telegram API error")
		return fmt.Errorf("telegram API returned status %d", resp.StatusCode)
	}

	return nil
}
