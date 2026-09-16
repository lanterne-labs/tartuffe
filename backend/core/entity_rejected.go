package core

import (
	"errors"
	"fmt"
)

// EntityRejectedError is returned by the person-insertion path when the entity
// validation gate classifies a candidate as not a trackable entity. Batch
// callers (fact ingestion, the validate CLI) detect it with IsEntityRejected
// and treat it as a per-item skip rather than a fatal error.
type EntityRejectedError struct {
	Name       string
	EntityType string
	Filter     string // which validator filter made the call ("wiki", "llm", "default")
	Reason     string // short human-readable explanation
}

func (e *EntityRejectedError) Error() string {
	return fmt.Sprintf("entity rejected by %s: %q (%s) — %s",
		e.Filter, e.Name, e.EntityType, e.Reason)
}

// IsEntityRejected reports whether err (or any wrapped error) is an
// *EntityRejectedError.
func IsEntityRejected(err error) bool {
	var target *EntityRejectedError
	return errors.As(err, &target)
}
