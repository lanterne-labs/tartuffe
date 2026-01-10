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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // 60-second timeout
	defer cancel()

	testCases := []struct {
		name                    string
		factA                   string
		factB                   string
		expectedContradiction   bool
		expectedReasonSubstring string // Check if the reason contains this substring (if contradictory)
	}{
		{
			name:                    "Clear Contradiction - Opposite Actions",
			factA:                   "Alice stated on Monday that the project proposal was approved.",
			factB:                   "Alice confirmed on Tuesday that the project proposal was rejected.",
			expectedContradiction:   true,
			expectedReasonSubstring: "opposite", // Expect a reason mentioning opposite outcomes/statements
		},
		{
			name:                  "No Contradiction - Different People",
			factA:                 "Bob believes the economy is improving.",
			factB:                 "Charlie thinks the economy is stagnant.",
			expectedContradiction: false,
		},
		{
			name:                  "No Contradiction - Different Topics/Events",
			factA:                 "Alice presented the budget report on March 1st.",
			factB:                 "Alice attended the tech conference on March 15th.",
			expectedContradiction: false,
		},
		{
			name:                    "Potential Contradiction - Conflicting Details",
			factA:                   "The report indicates John was in Paris last week.",
			factB:                   "Eyewitness accounts place John in Tokyo during the same period.",
			expectedContradiction:   true,
			expectedReasonSubstring: "location", // Expect a reason related to conflicting locations
		},
		{
			name:                  "No Contradiction - Compatible Information",
			factA:                 "Maria enjoys classical music.",
			factB:                 "Maria attended a symphony concert yesterday.",
			expectedContradiction: false,
		},
		{
			name:                  "No Contradiction - Change of Stance (Implicit Time)",
			factA:                 "In 2020, David argued against the policy.",
			factB:                 "David now supports the revised policy.",
			expectedContradiction: false, // Should recognize the temporal shift or change in stance
		},
		{
			name:                    "Contradiction - Direct Negation",
			factA:                   "The CEO announced record profits for Q1.",
			factB:                   "The official quarterly report showed a significant loss for Q1.",
			expectedContradiction:   true,
			expectedReasonSubstring: "financial performance", // Expect a reason about profit vs loss
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			log.Info().Str("factA", tc.factA).Str("factB", tc.factB).Msg("Running live contradiction check")

			startTime := time.Now()
			isContradictory, reason, checkErr := detector.AreContradictory(ctx, tc.factA, tc.factB)
			duration := time.Since(startTime)

			log.Info().Dur("duration", duration).Bool("isContradictory", isContradictory).Str("reason", reason).Msg("Live contradiction check completed")

			require.NoError(t, checkErr, "Contradiction check API call failed")

			assert.Equal(t, tc.expectedContradiction, isContradictory, "Contradiction flag mismatch")

			if tc.expectedContradiction {
				require.NotEmpty(t, reason, "Expected a reason for contradiction, but got empty string")
				if tc.expectedReasonSubstring != "" {
					assert.Contains(t, reason, tc.expectedReasonSubstring, "Reason string does not contain expected substring")
				}
			} else {
				// Optionally check if the reason is empty or specific "No contradiction" text if the prompt guarantees it
				// assert.Empty(t, reason, "Expected empty reason for non-contradiction, but got: %s", reason)
			}
		})
	}
}
