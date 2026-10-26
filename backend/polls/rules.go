package polls

import "time"

// The reasons the results are hidden.
const (
	// HiddenTooFewVoters: below the event's min_voters.
	HiddenTooFewVoters = "too_few_voters"
	// HiddenBlackout: during a blackout (the polling law).
	HiddenBlackout = "blackout"
)

// IsOpen tells whether the event takes ballots at now.
func IsOpen(e *Event, now time.Time) bool {
	return !now.Before(e.OpensAt) && (e.ClosesAt == nil || now.Before(*e.ClosesAt))
}

// InBlackout tells whether the results of the event are under a blackout at now.
func InBlackout(e *Event, now time.Time) bool {
	for _, b := range e.Blackouts {
		if !now.Before(b.StartsAt) && now.Before(b.EndsAt) {
			return true
		}
	}
	return false
}

// Hidden says why an event's results are hidden at now, "" when shown.
func Hidden(e *Event, voters int64, now time.Time) string {
	if InBlackout(e, now) {
		return HiddenBlackout
	}
	if voters < int64(e.MinVoters) {
		return HiddenTooFewVoters
	}
	return ""
}
