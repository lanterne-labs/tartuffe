package core

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

// TestTranslatorToEnglish is a local-only smoke test for the translation-repair
// path. It self-skips unless OPEN_AI_KEY is set (endpoint is the free local GPU).
//
//	source .env; go test -v -run TestTranslatorToEnglish ./backend/core/
func TestTranslatorToEnglish(t *testing.T) {
	_ = godotenv.Overload()
	_ = godotenv.Overload("../../.env")
	if os.Getenv("OPEN_AI_KEY") == "" {
		t.Skip("Skipping translator test: OPEN_AI_KEY not set (local-only).")
	}

	tr, err := NewTranslatorFromEnv()
	if err != nil {
		t.Fatalf("failed to create translator: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// A real prod fact (id 6824) that came back without a translation.
	src := "Marine Le Pen a affirmé que la sécurité des enfants doit redevenir une priorité suite au décès de Lyhanna."
	en, err := tr.TranslateToEnglish(ctx, src, "fr")
	if err != nil {
		t.Fatalf("TranslateToEnglish error: %v", err)
	}
	if strings.TrimSpace(en) == "" {
		t.Fatal("expected a non-empty English translation")
	}
	// Faithfulness sanity: the named entity must survive.
	if !strings.Contains(en, "Le Pen") {
		t.Errorf("translation should preserve 'Le Pen'; got: %s", en)
	}
	t.Logf("FR: %s\nEN: %s", src, en)
}
