package core

import "digital-mirror/backend/entityvalidator"

// isEntityRejected is the call-site predicate the fact-inserter uses to
// distinguish "the subject isn't a real entity, skip this fact" from any other
// failure that should abort the batch. It defers to entityvalidator.IsRejected
// so the recognition logic lives in one place.
func isEntityRejected(err error) bool { return entityvalidator.IsRejected(err) }
