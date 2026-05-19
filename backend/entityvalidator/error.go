package entityvalidator

import (
	"errors"
	"fmt"
)

// RejectedError is the typed error a storage layer returns when the validator
// classifies a candidate as not-a-real-entity. Callers that iterate over
// batches (fact ingestion, the validate CLI) should detect this via IsRejected
// and treat it as a per-item skip rather than a fatal batch error — the rest
// of the work should keep going.
//
// Lives in this package (rather than in storage) so any layer can reference it
// without creating an import cycle.
type RejectedError struct {
	Name       string
	EntityType string
	Filter     string // which validator filter made the call ("wiki", "llm", "default")
	Reason     string // short human-readable explanation
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("entity rejected by %s: %q (%s) — %s",
		e.Filter, e.Name, e.EntityType, e.Reason)
}

// IsRejected reports whether err (or any wrapped error) is *RejectedError.
// Convenience wrapper around errors.As for the common "log and skip" pattern.
func IsRejected(err error) bool {
	var target *RejectedError
	return errors.As(err, &target)
}
