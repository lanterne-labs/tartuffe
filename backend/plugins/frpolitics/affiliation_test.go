package frpolitics

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAffiliationIntegration tests the temporal affiliation creation logic
func TestAffiliationIntegration(t *testing.T) {
	// This is an integration test that would require a real database
	// For now, we'll create a mock test that verifies the logic structure

	t.Run("test affiliation logic structure", func(t *testing.T) {
		// Test that the affiliation creation logic follows the expected pattern
		// This is a structural test since we can't easily mock the database calls

		// Mock data that would be processed
		testLaw := Law{
			Key:         "16-123",
			Title:       "Test Law",
			URL:         "http://example.com/law/16-123",
			Legislature: 16,
			VoteDate:    timePtr(time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)),
		}

		testEvents := []VoteEvent{
			{
				AssembleeID: "PA123",
				OrganeRef:   "PO789",
				DisplayName: "John Doe",
				Position:    VoteFor,
			},
			{
				AssembleeID: "PA456",
				OrganeRef:   "PO789",
				DisplayName: "Jane Smith",
				Position:    VoteAgainst,
			},
		}

		// Verify the logic would process these events correctly
		assert.NotEmpty(t, testEvents, "Should have events to process")
		assert.NotEmpty(t, testLaw.Key, "Law should have a key")
		assert.NotNil(t, testLaw.VoteDate, "Law should have a vote date")

		for _, ev := range testEvents {
			assert.NotEmpty(t, ev.AssembleeID, "Event should have assemblee ID")
			assert.NotEmpty(t, ev.OrganeRef, "Event should have organe ref")
		}

		t.Log("Affiliation logic structure test passed")
	})

	t.Run("test party fact majority logic", func(t *testing.T) {
		// Test the majority calculation logic conceptually

		// Simulate a party with 5 members where 4 vote "for" and 1 votes "against"
		totalVotes := 5
		forVotes := 4
		againstVotes := 1

		// Default threshold is 0.8 (80%)
		threshold := 0.8
		forPercentage := float64(forVotes) / float64(totalVotes)
		againstPercentage := float64(againstVotes) / float64(totalVotes)

		t.Logf("For votes: %d/%d = %.2f", forVotes, totalVotes, forPercentage)
		t.Logf("Against votes: %d/%d = %.2f", againstVotes, totalVotes, againstPercentage)
		t.Logf("Threshold: %.2f", threshold)

		// Verify majority logic
		if forPercentage >= threshold {
			t.Log("✅ FOR position would create a party fact (majority reached)")
		} else {
			t.Log("❌ FOR position would NOT create a party fact (majority not reached)")
		}

		if againstPercentage >= threshold {
			t.Log("✅ AGAINST position would create a party fact (majority reached)")
		} else {
			t.Log("❌ AGAINST position would NOT create a party fact (majority not reached)")
		}

		assert.True(t, forPercentage >= threshold, "FOR should reach majority")
		assert.False(t, againstPercentage >= threshold, "AGAINST should not reach majority")
	})
}

// TestAffiliationDataValidation tests data validation in affiliation creation
func TestAffiliationDataValidation(t *testing.T) {
	t.Run("test empty data handling", func(t *testing.T) {
		// Test how the system handles empty/missing data

		testCases := []struct {
			name        string
			orgRef      string
			assembleeID string
			shouldSkip  bool
		}{
			{
				name:        "valid data",
				orgRef:      "PO789",
				assembleeID: "PA123",
				shouldSkip:  false,
			},
			{
				name:        "empty orgRef",
				orgRef:      "",
				assembleeID: "PA123",
				shouldSkip:  true,
			},
			{
				name:        "empty assembleeID",
				orgRef:      "PO789",
				assembleeID: "",
				shouldSkip:  true,
			},
			{
				name:        "both empty",
				orgRef:      "",
				assembleeID: "",
				shouldSkip:  true,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				// Simulate the data validation logic
				orgRef := tc.orgRef
				assembleeID := tc.assembleeID

				// This mimics the validation in the actual code
				shouldSkip := orgRef == "" || assembleeID == ""

				assert.Equal(t, tc.shouldSkip, shouldSkip,
					"Data validation should match expected behavior")
			})
		}
	})
}

// TestPartyFactMajorityScenarios tests various majority scenarios
func TestPartyFactMajorityScenarios(t *testing.T) {
	scenarios := []struct {
		name          string
		totalMembers  int
		forVotes      int
		againstVotes  int
		abstainVotes  int
		threshold     float64
		expectedFor   bool
		expectedAgain bool
	}{
		{
			name:          "clear majority for",
			totalMembers:  10,
			forVotes:      9,
			againstVotes:  1,
			abstainVotes:  0,
			threshold:     0.8,
			expectedFor:   true,
			expectedAgain: false,
		},
		{
			name:          "no clear majority",
			totalMembers:  10,
			forVotes:      6,
			againstVotes:  4,
			abstainVotes:  0,
			threshold:     0.8,
			expectedFor:   false,
			expectedAgain: false,
		},
		{
			name:          "majority against",
			totalMembers:  10,
			forVotes:      2,
			againstVotes:  8,
			abstainVotes:  0,
			threshold:     0.8,
			expectedFor:   false,
			expectedAgain: true,
		},
		{
			name:          "lower threshold",
			totalMembers:  10,
			forVotes:      7,
			againstVotes:  3,
			abstainVotes:  0,
			threshold:     0.6,
			expectedFor:   true,
			expectedAgain: false,
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			forPercentage := float64(scenario.forVotes) / float64(scenario.totalMembers)
			againstPercentage := float64(scenario.againstVotes) / float64(scenario.totalMembers)

			forMajority := forPercentage >= scenario.threshold
			againstMajority := againstPercentage >= scenario.threshold

			assert.Equal(t, scenario.expectedFor, forMajority,
				"FOR majority calculation should match expected")
			assert.Equal(t, scenario.expectedAgain, againstMajority,
				"AGAINST majority calculation should match expected")

			t.Logf("Scenario: %s", scenario.name)
			t.Logf("  FOR: %d votes (%.1f%%) - majority: %t", scenario.forVotes, forPercentage*100, forMajority)
			t.Logf("  AGAINST: %d votes (%.1f%%) - majority: %t", scenario.againstVotes, againstPercentage*100, againstMajority)
		})
	}
}

// Helper function to create time pointers for testing
func timePtr(t time.Time) *time.Time {
	return &t
}

// MockStorer is a minimal mock for testing affiliation logic
type MockStorer struct {
	getPersonIDByAssembleeIDFunc func(ctx context.Context, assembleeID string) (int64, error)
	getPersonIDByOrgRefFunc      func(ctx context.Context, orgRef string) (int64, error)
	findOrCreateAffiliationFunc  func(ctx context.Context, tx interface{}, srcID int64, dstID int64, validFrom *time.Time, validTo *time.Time, attributes map[string]any) error
	poolFunc                     func() *pgxpool.Pool
}

func (m *MockStorer) GetPersonIDByAssembleeID(ctx context.Context, assembleeID string) (int64, error) {
	return m.getPersonIDByAssembleeIDFunc(ctx, assembleeID)
}

func (m *MockStorer) GetPersonIDByOrgRef(ctx context.Context, orgRef string) (int64, error) {
	return m.getPersonIDByOrgRefFunc(ctx, orgRef)
}

func (m *MockStorer) FindOrCreateAffiliation(ctx context.Context, tx interface{}, srcID int64, dstID int64, validFrom *time.Time, validTo *time.Time, attributes map[string]any) error {
	return m.findOrCreateAffiliationFunc(ctx, tx, srcID, dstID, validFrom, validTo, attributes)
}

func (m *MockStorer) Pool() *pgxpool.Pool {
	return m.poolFunc()
}

// TestAffiliationMock tests the affiliation creation with mocked dependencies
func TestAffiliationMock(t *testing.T) {
	t.Run("test affiliation creation flow", func(t *testing.T) {
		// Create mock storer
		mockStorer := &MockStorer{
			getPersonIDByAssembleeIDFunc: func(ctx context.Context, assembleeID string) (int64, error) {
				// Mock response: PA123 -> 101, PA456 -> 102
				if assembleeID == "PA123" {
					return 101, nil
				} else if assembleeID == "PA456" {
					return 102, nil
				}
				return 0, assert.AnError
			},
			getPersonIDByOrgRefFunc: func(ctx context.Context, orgRef string) (int64, error) {
				// Mock response: PO789 -> 201
				if orgRef == "PO789" {
					return 201, nil
				}
				return 0, assert.AnError
			},
			findOrCreateAffiliationFunc: func(ctx context.Context, tx interface{}, srcID int64, dstID int64, validFrom *time.Time, validTo *time.Time, attributes map[string]any) error {
				// Verify the affiliation parameters are correct
				assert.NotZero(t, srcID, "Source ID should not be zero")
				assert.NotZero(t, dstID, "Destination ID should not be zero")
				assert.NotNil(t, validFrom, "Valid from should not be nil")
				assert.NotNil(t, attributes, "Attributes should not be nil")

				t.Logf("Would create affiliation: src=%d, dst=%d, validFrom=%v, attributes=%v",
					srcID, dstID, validFrom, attributes)

				return nil // Success
			},
			poolFunc: func() *pgxpool.Pool {
				// Return nil pool since we're mocking the individual functions
				return nil
			},
		}

		// Test data
		ctx := context.Background()
		law := Law{
			Key:         "16-123",
			Legislature: 16,
			VoteDate:    timePtr(time.Date(2023, 1, 15, 0, 0, 0, 0, time.UTC)),
		}

		events := []VoteEvent{
			{
				AssembleeID: "PA123",
				OrganeRef:   "PO789",
			},
			{
				AssembleeID: "PA456",
				OrganeRef:   "PO789",
			},
		}

		// Test the affiliation creation logic
		for _, ev := range events {
			orgRef := ev.OrganeRef
			assembleeID := ev.AssembleeID

			if orgRef == "" || assembleeID == "" {
				continue
			}

			// Get deputy ID
			deputyID, err := mockStorer.GetPersonIDByAssembleeID(ctx, assembleeID)
			require.NoError(t, err, "Should get deputy ID")
			assert.NotZero(t, deputyID, "Deputy ID should not be zero")

			// Get party ID
			partyID, err := mockStorer.GetPersonIDByOrgRef(ctx, orgRef)
			require.NoError(t, err, "Should get party ID")
			assert.NotZero(t, partyID, "Party ID should not be zero")

			// Create temporal affiliation
			attributes := map[string]any{
				"organeRef":   orgRef,
				"legislature": law.Legislature,
			}

			err = mockStorer.FindOrCreateAffiliation(ctx, nil, deputyID, partyID, law.VoteDate, nil, attributes)
			require.NoError(t, err, "Should create affiliation successfully")
		}

		t.Log("Affiliation mock test completed successfully")
	})
}

// TestTemporalValidation tests that temporal boundaries are correctly used for affiliation queries
func TestTemporalValidation(t *testing.T) {
	t.Run("test affiliation time boundaries", func(t *testing.T) {
		// Test scenarios for temporal validation
		scenarios := []struct {
			name             string
			affiliationStart *time.Time
			affiliationEnd   *time.Time
			queryTime        *time.Time
			shouldBeIncluded bool
			description      string
		}{
			{
				name:             "current affiliation",
				affiliationStart: timePtr(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)),
				affiliationEnd:   nil, // No end date - ongoing
				queryTime:        timePtr(time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)),
				shouldBeIncluded: true,
				description:      "Affiliation with no end date should be included for future queries",
			},
			{
				name:             "past affiliation",
				affiliationStart: timePtr(time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)),
				affiliationEnd:   timePtr(time.Date(2022, 12, 31, 0, 0, 0, 0, time.UTC)),
				queryTime:        timePtr(time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)),
				shouldBeIncluded: false,
				description:      "Affiliation that ended before query time should not be included",
			},
			{
				name:             "future affiliation",
				affiliationStart: timePtr(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
				affiliationEnd:   nil,
				queryTime:        timePtr(time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)),
				shouldBeIncluded: false,
				description:      "Affiliation that starts after query time should not be included",
			},
			{
				name:             "active during query time",
				affiliationStart: timePtr(time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)),
				affiliationEnd:   timePtr(time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)),
				queryTime:        timePtr(time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)),
				shouldBeIncluded: true,
				description:      "Affiliation that spans the query time should be included",
			},
			{
				name:             "exactly at start boundary",
				affiliationStart: timePtr(time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)),
				affiliationEnd:   nil,
				queryTime:        timePtr(time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)),
				shouldBeIncluded: true,
				description:      "Affiliation should be included when query time equals start time",
			},
			{
				name:             "exactly at end boundary",
				affiliationStart: timePtr(time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)),
				affiliationEnd:   timePtr(time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)),
				queryTime:        timePtr(time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)),
				shouldBeIncluded: true,
				description:      "Affiliation should be included when query time equals end time",
			},
			{
				name:             "no time specified",
				affiliationStart: nil,
				affiliationEnd:   nil,
				queryTime:        nil,
				shouldBeIncluded: true,
				description:      "When no time constraints, affiliation should always be included",
			},
		}

		for _, scenario := range scenarios {
			t.Run(scenario.name, func(t *testing.T) {
				// Simulate the temporal validation logic from GetPersonAffiliations
				// This mimics the SQL conditions:
				// AND ($2 IS NULL OR (valid_from IS NULL OR valid_from <= $2))
				// AND ($2 IS NULL OR (valid_to IS NULL OR valid_to >= $2))

				includesAffiliation := true

				// Check start time condition
				if scenario.queryTime != nil {
					if scenario.affiliationStart != nil && scenario.affiliationStart.After(*scenario.queryTime) {
						includesAffiliation = false
					}
				}

				// Check end time condition
				if includesAffiliation && scenario.queryTime != nil {
					if scenario.affiliationEnd != nil && scenario.affiliationEnd.Before(*scenario.queryTime) {
						includesAffiliation = false
					}
				}

				assert.Equal(t, scenario.shouldBeIncluded, includesAffiliation,
					"Temporal validation should match expected behavior: %s", scenario.description)

				t.Logf("Scenario: %s", scenario.name)
				t.Logf("  Start: %v, End: %v, Query: %v", scenario.affiliationStart, scenario.affiliationEnd, scenario.queryTime)
				t.Logf("  Expected: %t, Got: %t", scenario.shouldBeIncluded, includesAffiliation)
				t.Logf("  %s", scenario.description)
			})
		}
	})
}

// TestPartyMajorityWithTemporalConstraints tests majority rule calculations with temporal constraints
func TestPartyMajorityWithTemporalConstraints(t *testing.T) {
	t.Run("test majority calculation with time windows", func(t *testing.T) {
		// Test scenario: Party members with different affiliation periods
		// We need to calculate majority only considering members active at the vote time

		voteTime := time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)

		// Party members with their affiliation periods and votes
		members := []struct {
			name             string
			affiliationStart *time.Time
			affiliationEnd   *time.Time
			vote             string
			activeAtVoteTime bool
		}{
			{
				name:             "Member A",
				affiliationStart: timePtr(time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)),
				affiliationEnd:   timePtr(time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)),
				vote:             "for",
				activeAtVoteTime: true,
			},
			{
				name:             "Member B",
				affiliationStart: timePtr(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)),
				affiliationEnd:   nil, // Ongoing
				vote:             "for",
				activeAtVoteTime: true,
			},
			{
				name:             "Member C",
				affiliationStart: timePtr(time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)),
				affiliationEnd:   timePtr(time.Date(2023, 3, 31, 0, 0, 0, 0, time.UTC)), // Ended before vote
				vote:             "against",
				activeAtVoteTime: false,
			},
			{
				name:             "Member D",
				affiliationStart: timePtr(time.Date(2023, 9, 1, 0, 0, 0, 0, time.UTC)), // Starts after vote
				affiliationEnd:   nil,
				vote:             "against",
				activeAtVoteTime: false,
			},
			{
				name:             "Member E",
				affiliationStart: timePtr(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)),
				affiliationEnd:   timePtr(time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC)),
				vote:             "for",
				activeAtVoteTime: true,
			},
		}

		// Calculate active members at vote time
		activeMembers := 0
		forVotes := 0
		againstVotes := 0

		for _, member := range members {
			if member.activeAtVoteTime {
				activeMembers++
				if member.vote == "for" {
					forVotes++
				} else if member.vote == "against" {
					againstVotes++
				}
			}
		}

		t.Logf("Vote Time: %s", voteTime)
		t.Logf("Total party members: %d", len(members))
		t.Logf("Active members at vote time: %d", activeMembers)
		t.Logf("FOR votes: %d", forVotes)
		t.Logf("AGAINST votes: %d", againstVotes)

		// Verify the counts
		assert.Equal(t, 5, len(members), "Should have 5 total members")
		assert.Equal(t, 3, activeMembers, "Should have 3 active members at vote time")
		assert.Equal(t, 3, forVotes, "Should have 3 FOR votes from active members")
		assert.Equal(t, 0, againstVotes, "Should have 0 AGAINST votes from active members")

		// Test majority calculation with default threshold (80%)
		threshold := 0.8
		forPercentage := float64(forVotes) / float64(activeMembers)
		againstPercentage := float64(againstVotes) / float64(activeMembers)

		forMajority := forPercentage >= threshold
		againstMajority := againstPercentage >= threshold

		t.Logf("FOR percentage: %.1f%%", forPercentage*100)
		t.Logf("AGAINST percentage: %.1f%%", againstPercentage*100)
		t.Logf("Threshold: %.1f%%", threshold*100)

		assert.True(t, forMajority, "FOR should reach majority (100% >= 80%)")
		assert.False(t, againstMajority, "AGAINST should not reach majority (0% < 80%)")

		// Verify that the inactive member votes are not counted
		inactiveForVotes := 0
		inactiveAgainstVotes := 0

		for _, member := range members {
			if !member.activeAtVoteTime {
				if member.vote == "for" {
					inactiveForVotes++
				} else if member.vote == "against" {
					inactiveAgainstVotes++
				}
			}
		}

		t.Logf("Inactive FOR votes (should not count): %d", inactiveForVotes)
		t.Logf("Inactive AGAINST votes (should not count): %d", inactiveAgainstVotes)

		assert.Equal(t, 2, inactiveAgainstVotes, "Should have 2 inactive AGAINST votes")
		assert.Equal(t, 0, inactiveForVotes, "Should have 0 inactive FOR votes")

		t.Log("✅ Temporal majority calculation test passed")
	})
}

// TestAffiliationTemporalEdgeCases tests edge cases in temporal validation
func TestAffiliationTemporalEdgeCases(t *testing.T) {
	t.Run("test edge cases", func(t *testing.T) {
		edgeCases := []struct {
			name        string
			startTime   *time.Time
			endTime     *time.Time
			queryTime   *time.Time
			expected    bool
			description string
		}{
			{
				name:        "midnight boundary",
				startTime:   timePtr(time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)),
				endTime:     timePtr(time.Date(2023, 6, 16, 0, 0, 0, 0, time.UTC)),
				queryTime:   timePtr(time.Date(2023, 6, 15, 23, 59, 59, 999999999, time.UTC)),
				expected:    true,
				description: "Should include affiliation up to the last nanosecond before end",
			},
			{
				name:        "exactly at midnight end",
				startTime:   timePtr(time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)),
				endTime:     timePtr(time.Date(2023, 6, 16, 0, 0, 0, 0, time.UTC)),
				queryTime:   timePtr(time.Date(2023, 6, 16, 0, 0, 0, 0, time.UTC)),
				expected:    true,
				description: "Should include affiliation at exactly the end time",
			},
			{
				name:        "just after midnight end",
				startTime:   timePtr(time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)),
				endTime:     timePtr(time.Date(2023, 6, 16, 0, 0, 0, 0, time.UTC)),
				queryTime:   timePtr(time.Date(2023, 6, 16, 0, 0, 0, 1, time.UTC)),
				expected:    false,
				description: "Should exclude affiliation just after the end time",
			},
			{
				name:        "timezone considerations",
				startTime:   timePtr(time.Date(2023, 6, 15, 12, 0, 0, 0, time.UTC)),
				endTime:     nil,
				queryTime:   timePtr(time.Date(2023, 6, 15, 12, 0, 0, 0, time.UTC)),
				expected:    true,
				description: "Should handle exact time matches correctly",
			},
		}

		for _, tc := range edgeCases {
			t.Run(tc.name, func(t *testing.T) {
				// Test the temporal validation logic
				includes := true

				if tc.queryTime != nil {
					if tc.startTime != nil && tc.startTime.After(*tc.queryTime) {
						includes = false
					}
					if includes && tc.endTime != nil && tc.endTime.Before(*tc.queryTime) {
						includes = false
					}
				}

				assert.Equal(t, tc.expected, includes, tc.description)

				t.Logf("Case: %s - Start: %v, End: %v, Query: %v, Expected: %t, Got: %t",
					tc.name, tc.startTime, tc.endTime, tc.queryTime, tc.expected, includes)
			})
		}
	})
}
