package frpolitics

import (
	"strings"
)

// CleanAndNameEvents normalizes nominative vote events from a scrutin:
// - ensures AssembleeID and OrganeRef are present
// - fills missing or code-like DisplayName from the actorNames map
// - drops events still lacking a human-readable name
// Returns the cleaned events and the number of dropped events.
func CleanAndNameEvents(s scrutinJSON, actorNames map[string]string) ([]VoteEvent, int) {
	raw := ParseNominatif(s)
	if len(raw) == 0 {
		return nil, 0
	}
	cleaned := make([]VoteEvent, 0, len(raw))
	dropped := 0
	for _, ev := range raw {
		if strings.TrimSpace(ev.AssembleeID) == "" || strings.TrimSpace(ev.OrganeRef) == "" {
			dropped++
			continue
		}
		if needsNameResolution(ev.DisplayName, ev.AssembleeID) {
			if name, ok := actorNames[ev.AssembleeID]; ok && strings.TrimSpace(name) != "" {
				ev.DisplayName = name
			}
		}
		if strings.TrimSpace(ev.DisplayName) == "" {
			dropped++
			continue
		}
		cleaned = append(cleaned, ev)
	}
	return cleaned, dropped
}

// needsNameResolution returns true if the provided displayName is empty,
// equals the ID, or looks like a code rather than a human name.
func needsNameResolution(displayName string, assembleeID string) bool {
	n := strings.TrimSpace(displayName)
	if n == "" {
		return true
	}
	if strings.EqualFold(n, strings.TrimSpace(assembleeID)) {
		return true
	}
	return isCodeLikeName(n)
}

// isCodeLikeName heuristically detects code-like identifiers (PA123, PO123, etc.).
func isCodeLikeName(name string) bool {
	u := strings.ToUpper(strings.TrimSpace(name))
	if u == "" {
		return true
	}
	if strings.HasPrefix(u, "PA") || strings.HasPrefix(u, "PO") {
		for i := 2; i < len(u); i++ {
			if u[i] < '0' || u[i] > '9' {
				return false
			}
		}
		return true
	}
	// No spaces and purely alnum/underscore/dash is suspicious for a human name
	if !strings.Contains(u, " ") {
		all := true
		for i := 0; i < len(u); i++ {
			c := u[i]
			if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}
