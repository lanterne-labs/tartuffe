package llm

import (
	"testing"
)

func TestConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv("OPEN_AI_API", "")
	t.Setenv("OPEN_AI_KEY", "")
	t.Setenv("OPEN_AI_CHAT_MODEL", "  ")

	cfg := ConfigFromEnv()
	if cfg.BaseURL != "" || cfg.APIKey != "" {
		t.Fatalf("unset vars must stay empty, got %+v", cfg)
	}
	if cfg.ChatModel != DefaultChatModel {
		t.Fatalf("blank OPEN_AI_CHAT_MODEL must default to %q, got %q", DefaultChatModel, cfg.ChatModel)
	}
	if err := cfg.Require(); err == nil || err.Error() != "OPEN_AI_KEY environment variable not set" {
		t.Fatalf("Require must report the key first, got %v", err)
	}
	if _, err := NewClientFromEnv("test"); err == nil {
		t.Fatal("NewClientFromEnv must fail without a key")
	}
}

func TestConfigFromEnv_RequireBaseURLAfterKey(t *testing.T) {
	t.Setenv("OPEN_AI_API", "")
	t.Setenv("OPEN_AI_KEY", "k")
	if err := ConfigFromEnv().Require(); err == nil || err.Error() != "OPEN_AI_API environment variable not set" {
		t.Fatalf("Require must report the base URL once the key is set, got %v", err)
	}
}

func TestConfigFromEnv_Set(t *testing.T) {
	t.Setenv("OPEN_AI_API", " http://box:8080/v1/ ")
	t.Setenv("OPEN_AI_KEY", "k")
	t.Setenv("OPEN_AI_CHAT_MODEL", "local-model")

	cfg := ConfigFromEnv()
	if cfg.BaseURL != "http://box:8080/v1/" || cfg.APIKey != "k" || cfg.ChatModel != "local-model" {
		t.Fatalf("unexpected config %+v", cfg)
	}
	if err := cfg.Require(); err != nil {
		t.Fatal(err)
	}
	c, err := NewClientFromEnv("test")
	if err != nil {
		t.Fatal(err)
	}
	if c.URL != "http://box:8080/v1/chat/completions" || c.Key != "k" || c.Model != "local-model" {
		t.Fatalf("client not bound to config: %+v", c)
	}
	if c.BaseLLMClient == nil || c.component != "test" {
		t.Fatal("client must carry a BaseLLMClient tagged with the component")
	}
}
