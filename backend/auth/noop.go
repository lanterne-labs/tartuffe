package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// NoopName is the test provider: it signs in whoever names a pseudonym, with
// no proof at all. Never enable it where the results must mean something.
const NoopName = "noop"

// Noop trusts the pseudonym it is given: the same pseudonym is the same user.
type Noop struct{}

func (Noop) Name() string { return NoopName }

func (Noop) Info() ProviderInfo { return ProviderInfo{Name: NoopName, Role: RoleTest, Verified: false} }

// Verify reads {"pseudonym": "..."}: 1 to 40 characters once trimmed.
func (Noop) Verify(_ context.Context, payload json.RawMessage) (Identity, error) {
	var p struct {
		Pseudonym string `json:"pseudonym"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidProof, err)
	}
	name := strings.TrimSpace(p.Pseudonym)
	if name == "" || utf8.RuneCountInString(name) > 40 {
		return Identity{}, fmt.Errorf("%w: the pseudonym must have 1 to 40 characters", ErrInvalidProof)
	}
	return Identity{Provider: NoopName, Subject: strings.ToLower(name), DisplayName: name}, nil
}
