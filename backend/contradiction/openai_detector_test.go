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
	_ = godotenv.Load()

	apiKey := os.Getenv("OPEN_AI_KEY")
	if apiKey == "" {
		t.Skip("Skipping live Contradiction Detector test: OPEN_AI_KEY not set.")
	}

	detector, err := NewOpenAIContradictionDetectorFromEnv()
	require.NoError(t, err, "Failed to create OpenAIContradictionDetector from environment")

	// Use a context with a timeout for the API call
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second) // 120-second timeout for more test cases
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
			name:                    "Potential Contradiction - Conflicting Details (Same Date)",
			factA:                   "The report indicates John was in Paris last week.",
			dateA:                   "2024-02-10",
			factB:                   "Eyewitness accounts place John in Tokyo during the same period.",
			dateB:                   "2024-02-10",
			expectedContradiction:   true,
			expectedReasonSubstring: "", // LLM may use different wording
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
			name:                    "Contradiction - Direct Negation (Same Date)",
			factA:                   "The CEO announced record profits for Q1.",
			dateA:                   "2024-04-15",
			factB:                   "The official quarterly report showed a significant loss for Q1.",
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
			factA:                   "Marine Le Pen stated the EU Parliament was fully aware of the mutualization.",
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
			dateB:                 "", // Unknown
			expectedContradiction: false, // Without dates, could be change of position
		},
		{
			name:                  "Temporal - One Date Unknown Should Be Skeptical",
			factA:                 "Senator Smith voted for the bill.",
			dateA:                 "2024-02-01",
			factB:                 "Senator Smith voted against the bill.",
			dateB:                 "", // Unknown
			expectedContradiction: false, // Could be different bills or changed vote
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
