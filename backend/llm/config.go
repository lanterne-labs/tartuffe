package llm

import (
	"fmt"
	"os"
	"strings"
)

// DefaultChatModel is the model used when OPEN_AI_CHAT_MODEL is unset
// (llama-swap aliases it to the local default).
const DefaultChatModel = "gpt-4o-mini"

// DefaultBaseURL is the OpenAI API root. Only call sites that must keep
// working without OPEN_AI_API fall back to it; the rest require the variable.
const DefaultBaseURL = "https://api.openai.com/v1"

// Config is the OpenAI-compatible endpoint configuration read from the
// environment: the API root (or full chat-completions URL), the key and the
// chat model.
type Config struct {
	BaseURL   string // OPEN_AI_API, trimmed; empty when unset
	APIKey    string // OPEN_AI_KEY; empty when unset
	ChatModel string // OPEN_AI_CHAT_MODEL, trimmed; DefaultChatModel when blank
}

// ConfigFromEnv reads OPEN_AI_API, OPEN_AI_KEY and OPEN_AI_CHAT_MODEL. It does
// not validate: callers that need the key or the base URL check with Require
// (or decide their own fallback).
func ConfigFromEnv() Config {
	cfg := Config{
		BaseURL:   strings.TrimSpace(os.Getenv("OPEN_AI_API")),
		APIKey:    os.Getenv("OPEN_AI_KEY"),
		ChatModel: strings.TrimSpace(os.Getenv("OPEN_AI_CHAT_MODEL")),
	}
	if cfg.ChatModel == "" {
		cfg.ChatModel = DefaultChatModel
	}
	return cfg
}

// Require reports the first missing required variable: OPEN_AI_KEY, then
// OPEN_AI_API.
func (c Config) Require() error {
	if c.APIKey == "" {
		return fmt.Errorf("OPEN_AI_KEY environment variable not set")
	}
	if c.BaseURL == "" {
		return fmt.Errorf("OPEN_AI_API environment variable not set")
	}
	return nil
}

// NewClient binds a fresh BaseLLMClient tagged with component to this
// configuration's chat-completions endpoint.
func (c Config) NewClient(component string) *Client {
	return &Client{
		BaseLLMClient: NewBaseLLMClient(component),
		URL:           ChatCompletionsURL(c.BaseURL),
		Key:           c.APIKey,
		Model:         c.ChatModel,
	}
}

// NewClientFromEnv is ConfigFromEnv + Require + NewClient: the constructor
// for every call site that needs both OPEN_AI_KEY and OPEN_AI_API set.
func NewClientFromEnv(component string) (*Client, error) {
	cfg := ConfigFromEnv()
	if err := cfg.Require(); err != nil {
		return nil, err
	}
	return cfg.NewClient(component), nil
}
