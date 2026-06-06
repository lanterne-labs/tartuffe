package core

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

// TestFactDedupeAdjudicatorGolden is the local-only golden set for the
// gray-zone fact-dedup adjudicator. It asserts genuine restatements are merged
// (same=true) and that opposing stances, intention-vs-action pairs, and
// different-specifics pairs are kept apart (same=false).
//
// LOCAL ONLY: self-skips unless OPEN_AI_KEY is set, so it never runs in CI. The
// endpoint is the local GPU (OPEN_AI_API), so each run is free.
//
//	source .env; go test -v -run TestFactDedupeAdjudicatorGolden ./backend/core/
func TestFactDedupeAdjudicatorGolden(t *testing.T) {
	_ = godotenv.Overload()
	_ = godotenv.Overload("../../.env")

	if os.Getenv("OPEN_AI_KEY") == "" {
		t.Skip("Skipping fact-dedupe golden test: OPEN_AI_KEY not set (local-only).")
	}

	adj, err := NewFactDedupeAdjudicatorFromEnv()
	if err != nil {
		t.Fatalf("failed to create adjudicator: %v", err)
	}

	cases := []struct {
		name    string
		subject string
		factA   string
		factB   string
		want    bool // true = same claim (dedup)
	}{
		{
			// The real prod pair (facts 4740 / 4897) — cosine 0.78, verbs
			// PROMISED vs CLAIMED — that produced duplicate contradiction #146.
			name:    "retailleau_candidacy_restatement",
			subject: "Bruno Retailleau",
			factA:   "Bruno Retailleau a déclaré qu'il ne renoncera pas à sa candidature et qu'il ira jusqu'au bout de sa candidature.",
			factB:   "Bruno Retailleau a affirmé que sa candidature irait jusqu'au bout.",
			want:    true,
		},
		{
			name:    "opposing_stance_on_primary",
			subject: "Bruno Retailleau",
			factA:   "Bruno Retailleau plaide pour une grande primaire de la droite.",
			factB:   "Bruno Retailleau a refusé l'idée d'une primaire.",
			want:    false,
		},
		{
			name:    "intention_vs_contrary_action",
			subject: "Le Rassemblement national",
			factA:   "Le Rassemblement national a promis de maintenir l'âge de départ à la retraite à 62 ans.",
			factB:   "Le Rassemblement national a renoncé à abroger la réforme portant l'âge de la retraite à 64 ans.",
			want:    false,
		},
		{
			name:    "different_specifics",
			subject: "Gérald Darmanin",
			factA:   "Gérald Darmanin a annoncé le retrait du plaider-coupable criminel pour les crimes de sang.",
			factB:   "Gérald Darmanin présentera son projet de loi le 29 juin devant l'Assemblée nationale.",
			want:    false,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var passed, failed int
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := adj.IsSameClaim(ctx, tc.subject, tc.factA, tc.factB)
			if err != nil {
				t.Fatalf("IsSameClaim error: %v", err)
			}
			if got != tc.want {
				failed++
				t.Errorf("got same=%v want same=%v\n    A: %s\n    B: %s", got, tc.want, tc.factA, tc.factB)
				return
			}
			passed++
			t.Logf("same=%v ✓", got)
		})
	}
	t.Logf("=== FACT-DEDUPE GOLDEN: %d/%d passed (%d failed) ===", passed, passed+failed, failed)
}
