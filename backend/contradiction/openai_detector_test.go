package contradiction

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv" // Import godotenv
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper to create a time pointer from a date string (format: "2006-01-02")
func parseDate(dateStr string) *time.Time {
	if dateStr == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return nil
	}
	return &t
}

// TestOpenAIContradictionDetector_AreContradictory_Live performs a live test against the chat API endpoint.
// It requires the OPEN_AI_KEY environment variable to be set.
func TestOpenAIContradictionDetector_AreContradictory_Live(t *testing.T) {
	// Load .env file if it exists (ignoring errors if it doesn't)
	_ = godotenv.Overload()

	apiKey := os.Getenv("OPEN_AI_KEY")
	if apiKey == "" {
		t.Skip("Skipping live Contradiction Detector test: OPEN_AI_KEY not set.")
	}

	detector, err := NewOpenAIContradictionDetectorFromEnv()
	require.NoError(t, err, "Failed to create OpenAIContradictionDetector from environment")

	// Use a context with a timeout for the API call
	ctx, cancel := context.WithTimeout(context.Background(), 450*time.Second) // 450-second timeout for 15 sequential API calls
	defer cancel()

	testCases := []struct {
		name                    string
		factA                   string
		dateA                   string // Empty string means nil/unknown date
		factB                   string
		dateB                   string // Empty string means nil/unknown date
		expectedContradiction   bool
		expectedReasonSubstring string // Check if the reason contains this substring (if contradictory)
	}{
		{
			name:                    "Clear Contradiction - Opposite Actions (Same Date)",
			factA:                   "Alice stated that the project proposal was approved.",
			dateA:                   "2024-02-01",
			factB:                   "Alice confirmed that the project proposal was rejected.",
			dateB:                   "2024-02-01",
			expectedContradiction:   true,
			expectedReasonSubstring: "", // LLM may use different wording
		},
		{
			name:                  "No Contradiction - Different People",
			factA:                 "Bob believes the economy is improving.",
			dateA:                 "2024-01-15",
			factB:                 "Charlie thinks the economy is stagnant.",
			dateB:                 "2024-01-15",
			expectedContradiction: false,
		},
		{
			name:                  "No Contradiction - Different Topics/Events",
			factA:                 "Alice presented the budget report.",
			dateA:                 "2024-03-01",
			factB:                 "Alice attended the tech conference.",
			dateB:                 "2024-03-15",
			expectedContradiction: false,
		},
		{
			// Third-party narratives (a report, eyewitness accounts) about John, with
			// no first-person claim or authoritative finding — the HARD GATE makes
			// this a source disagreement, not a personal contradiction.
			name:                  "No Contradiction - Third-Party Narratives Only",
			factA:                 "The report indicates John was in Paris last week.",
			dateA:                 "2024-02-10",
			factB:                 "Eyewitness accounts place John in Tokyo during the same period.",
			dateB:                 "2024-02-10",
			expectedContradiction: false,
		},
		{
			// Same factual conflict, but now anchored to John's own claim — this IS
			// a contradiction (his statement vs an authoritative finding about him).
			name:                    "Contradiction - Own Claim vs Authoritative Finding",
			factA:                   "John stated under oath he was in Paris last week.",
			dateA:                   "2024-02-10",
			factB:                   "Court records confirmed John was in Tokyo during the same period.",
			dateB:                   "2024-02-10",
			expectedContradiction:   true,
			expectedReasonSubstring: "",
		},
		{
			// Date-only conflict with no broken personal commitment — under the
			// DATE-ONLY CONFLICTS rule this is source noise, not a contradiction.
			name:                  "No Contradiction - Date-Only Conflict, No Commitment",
			factA:                 "Mayor Garcia signed the order on May 10.",
			dateA:                 "2024-05-12",
			factB:                 "Mayor Garcia signed the order on May 11.",
			dateB:                 "2024-05-13",
			expectedContradiction: false,
		},
		{
			name:                  "No Contradiction - Compatible Information",
			factA:                 "Maria enjoys classical music.",
			dateA:                 "2024-01-01",
			factB:                 "Maria attended a symphony concert yesterday.",
			dateB:                 "2024-06-15",
			expectedContradiction: false,
		},
		{
			name:                  "No Contradiction - Change of Stance Over Time",
			factA:                 "David argued against the policy.",
			dateA:                 "2020-05-10",
			factB:                 "David now supports the revised policy.",
			dateB:                 "2024-01-15",
			expectedContradiction: false,
		},
		{
			// Own claim vs authoritative record on the SAME date — the one case where
			// TEMPORAL rule 3 (same-date default NO) and the HARD GATE (authoritative
			// finding substitutes for the second utterance) must agree on YES. The
			// subject is NAMED like every prod fact: with an anonymous role ("The CEO
			// announced...") gemma was a measured 50/50 on whether the announcement was
			// the person's own utterance or a third-party narrative (logprob probe,
			// 2026-09-11: "entreprise" 0.39 vs "intéressé" 0.35), and the verdict
			// followed the vLLM prefix-cache state. Prod pairs never carry that
			// ambiguity — the detector always judges a named person's facts.
			name:                    "Contradiction - Direct Negation (Same Date)",
			factA:                   "CEO Jane Doe announced record profits for Q1.",
			dateA:                   "2024-04-15",
			factB:                   "The company's official quarterly report showed a significant loss for Q1.",
			dateB:                   "2024-04-15",
			expectedContradiction:   true,
			expectedReasonSubstring: "", // LLM may use different wording
		},
		// --- Temporal Reasoning Test Cases ---
		{
			name:                  "Temporal - Learning Over Time (NOT Contradictory)",
			factA:                 "Marine Le Pen said she didn't know about the mutualization scheme.",
			dateA:                 "2024-01-15",
			factB:                 "Marine Le Pen claimed the EU Parliament knew about mutualization and it was normal practice.",
			dateB:                 "2024-03-20",
			expectedContradiction: false, // She could have learned in between
		},
		{
			name:                    "Temporal - Denial After Claim (Contradictory)",
			factA:                   "Marine Le Pen stated she was fully aware of the mutualization scheme and that it was standard practice.",
			dateA:                   "2024-01-15",
			factB:                   "Marine Le Pen claimed she never knew about any mutualization scheme.",
			dateB:                   "2024-03-20",
			expectedContradiction:   true, // You cannot unknow something
			expectedReasonSubstring: "",
		},
		{
			name:                  "Temporal - Unknown Dates Should Be Skeptical",
			factA:                 "The CEO supported policy X.",
			dateA:                 "", // Unknown
			factB:                 "The CEO opposed policy X.",
			dateB:                 "",    // Unknown
			expectedContradiction: false, // Without dates, could be change of position
		},
		{
			name:                  "Temporal - One Date Unknown Should Be Skeptical",
			factA:                 "Senator Smith voted for the bill.",
			dateA:                 "2024-02-01",
			factB:                 "Senator Smith voted against the bill.",
			dateB:                 "",    // Unknown
			expectedContradiction: false, // Could be different bills or changed vote
		},
		// --- Promise-Based Reasoning Test Cases ---
		{
			name:                  "Promise - Trial vs Denial (NOT Contradictory)",
			factA:                 "Marine Le Pen a été jugée en appel pour détournement de fonds publics européens dans l'affaire des assistants parlementaires du FN.",
			dateA:                 "2024-11-06",
			factB:                 "Marine Le Pen a nié tout détournement de fonds publics et rejeté les accusations dans l'affaire des assistants parlementaires.",
			dateB:                 "2024-11-06",
			expectedContradiction: false, // Being tried is NOT a finding of guilt — does not break denial
		},
		{
			name:                    "Promise - Court Finding vs Denial (Contradictory)",
			factA:                   "Marine Le Pen a été reconnue coupable de détournement de fonds publics par la cour d'appel de Paris.",
			dateA:                   "2025-03-28",
			factB:                   "Marine Le Pen a nié tout détournement de fonds publics et rejeté les accusations dans l'affaire des assistants parlementaires.",
			dateB:                   "2024-11-06",
			expectedContradiction:   true, // Authoritative court finding breaks her denial
			expectedReasonSubstring: "",
		},
		{
			name:                    "Promise - Commitment vs Own Action (Contradictory)",
			factA:                   "Marine Le Pen s'est engagée à ne plus employer de membres de sa famille comme assistants parlementaires.",
			dateA:                   "2017-03-01",
			factB:                   "Marine Le Pen a employé sa nièce Marion Maréchal comme assistante parlementaire au Parlement européen.",
			dateB:                   "2018-06-15",
			expectedContradiction:   true, // Her own action breaks her commitment
			expectedReasonSubstring: "",
		},
		{
			name:                  "Promise - Investigation vs Denial (NOT Contradictory)",
			factA:                 "Marine Le Pen fait l'objet d'une enquête préliminaire pour abus de confiance dans le financement de sa campagne présidentielle.",
			dateA:                 "2024-05-10",
			factB:                 "Marine Le Pen a déclaré n'avoir commis aucun abus de confiance dans le cadre de sa campagne présidentielle.",
			dateB:                 "2024-05-15",
			expectedContradiction: false, // Investigation does NOT establish facts — does not break denial
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dateA := parseDate(tc.dateA)
			dateB := parseDate(tc.dateB)

			log.Info().Str("factA", tc.factA).Str("dateA", tc.dateA).Str("factB", tc.factB).Str("dateB", tc.dateB).Msg("Running live contradiction check")

			startTime := time.Now()
			isContradictory, reason, checkErr := detector.AreContradictory(ctx, tc.factA, dateA, tc.factB, dateB)
			duration := time.Since(startTime)

			log.Info().Dur("duration", duration).Bool("isContradictory", isContradictory).Str("reason", reason).Msg("Live contradiction check completed")

			require.NoError(t, checkErr, "Contradiction check API call failed")

			assert.Equal(t, tc.expectedContradiction, isContradictory, "Contradiction flag mismatch for case: %s", tc.name)

			if tc.expectedContradiction {
				require.NotEmpty(t, reason, "Expected a reason for contradiction, but got empty string")
				if tc.expectedReasonSubstring != "" {
					assert.Contains(t, reason, tc.expectedReasonSubstring, "Reason string does not contain expected substring")
				}
			}
		})
	}
}
