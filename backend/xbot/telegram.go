package xbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Telegram is the bot's channel to the person who approves the tweets: a
// Telegram bot, read by long polling (no public endpoint), that only listens
// to its one chat.
type Telegram struct {
	token  string
	chatID int64
	client *http.Client
	base   string
	offset int64
}

// NewTelegramFromEnv uses TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID (the
// alerts' bot); XBOT_TELEGRAM_BOT_TOKEN and XBOT_TELEGRAM_CHAT_ID take
// precedence. Nil when unset.
func NewTelegramFromEnv() (*Telegram, error) {
	token, chat := os.Getenv("XBOT_TELEGRAM_BOT_TOKEN"), os.Getenv("XBOT_TELEGRAM_CHAT_ID")
	if token == "" {
		token = os.Getenv("TELEGRAM_BOT_TOKEN")
	}
	if chat == "" {
		chat = os.Getenv("TELEGRAM_CHAT_ID")
	}
	if token == "" || chat == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(chat, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("telegram chat id %q: %w", chat, err)
	}
	return NewTelegram(token, id, "https://api.telegram.org"), nil
}

// NewTelegram talks to the Bot API at base (a test server in the tests).
func NewTelegram(token string, chatID int64, base string) *Telegram {
	return &Telegram{token: token, chatID: chatID, base: base, client: &http.Client{Timeout: 70 * time.Second}}
}

// Button is an inline button: its label and the data its tap sends back, or
// the link its tap opens (URL; then no data).
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	URL  string `json:"url,omitempty"`
}

func (t *Telegram) call(ctx context.Context, method string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/bot%s/%s", t.base, t.token, method), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("telegram %s: status %d: %w", method, resp.StatusCode, err)
	}
	if !env.OK {
		return fmt.Errorf("telegram %s: %s", method, env.Description)
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

func keyboard(rows [][]Button) any {
	if rows == nil {
		rows = [][]Button{}
	}
	return map[string]any{"inline_keyboard": rows}
}

// Send posts an HTML message with buttons to the chat and returns its id.
func (t *Telegram) Send(ctx context.Context, html string, rows [][]Button) (int64, error) {
	var msg struct {
		ID int64 `json:"message_id"`
	}
	err := t.call(ctx, "sendMessage", map[string]any{
		"chat_id": t.chatID, "text": html, "parse_mode": "HTML", "link_preview_options": map[string]bool{"is_disabled": true},
		"reply_markup": keyboard(rows),
	}, &msg)
	return msg.ID, err
}

// Edit replaces a message's text and buttons (nil: none).
func (t *Telegram) Edit(ctx context.Context, messageID int64, html string, rows [][]Button) error {
	return t.call(ctx, "editMessageText", map[string]any{
		"chat_id": t.chatID, "message_id": messageID, "text": html, "parse_mode": "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true}, "reply_markup": keyboard(rows),
	}, nil)
}

// Answer acknowledges a tap, with a short notice.
func (t *Telegram) Answer(ctx context.Context, callbackID, text string) error {
	return t.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID, "text": text}, nil)
}

// Update is what the chat sent: a tap on a button, or a reply to a message.
type Update struct {
	// CallbackID and Data are a tap; MessageID is the message tapped.
	CallbackID string
	Data       string
	MessageID  int64
	// ReplyTo and Text are a reply to a message of the bot.
	ReplyTo int64
	Text    string
}

// Updates waits up to wait for what the chat sent since the last call.
// Anything from another chat is ignored.
func (t *Telegram) Updates(ctx context.Context, wait time.Duration) ([]Update, error) {
	var raw []struct {
		ID       int64 `json:"update_id"`
		Callback *struct {
			ID      string `json:"id"`
			Data    string `json:"data"`
			Message *struct {
				ID   int64 `json:"message_id"`
				Chat struct {
					ID int64 `json:"id"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"callback_query"`
		Message *struct {
			Text string `json:"text"`
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
			ReplyTo *struct {
				ID int64 `json:"message_id"`
			} `json:"reply_to_message"`
		} `json:"message"`
	}
	err := t.call(ctx, "getUpdates", map[string]any{
		"offset": t.offset, "timeout": int(wait.Seconds()), "allowed_updates": []string{"message", "callback_query"},
	}, &raw)
	if err != nil {
		return nil, err
	}
	var out []Update
	for _, u := range raw {
		t.offset = u.ID + 1
		switch {
		case u.Callback != nil && u.Callback.Message != nil && u.Callback.Message.Chat.ID == t.chatID:
			out = append(out, Update{CallbackID: u.Callback.ID, Data: u.Callback.Data, MessageID: u.Callback.Message.ID})
		case u.Message != nil && u.Message.Chat.ID == t.chatID && u.Message.ReplyTo != nil:
			out = append(out, Update{ReplyTo: u.Message.ReplyTo.ID, Text: u.Message.Text})
		}
	}
	return out, nil
}
