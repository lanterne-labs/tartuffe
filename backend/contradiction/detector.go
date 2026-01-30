package contradiction

import (
	"context"
	"time"
)

// ContradictionDetector defines the interface for checking if two pieces of text contradict each other.
type ContradictionDetector interface {
	// AreContradictory checks if textA and textB are contradictory, considering their publication dates.
	// dateA and dateB may be nil if publication dates are unknown.
	// Returns true if they contradict, a string justification from the detector, and an error if the check fails.
	AreContradictory(ctx context.Context, textA string, dateA *time.Time, textB string, dateB *time.Time) (isContradictory bool, reason string, err error)
}

// NewNoOpContradictionDetector creates a detector that never finds contradictions. Useful for testing or disabling the feature.
func NewNoOpContradictionDetector() ContradictionDetector {
	return &noOpContradictionDetector{}
}

type noOpContradictionDetector struct{}

func (d *noOpContradictionDetector) AreContradictory(ctx context.Context, textA string, dateA *time.Time, textB string, dateB *time.Time) (bool, string, error) {
	// This implementation always returns false (no contradiction found).
	return false, "", nil
}
