package vote

import (
	"context"
	"encoding/json"

	"digital-mirror/backend/accounts/providers"
)

// Passport verifies registrations with the ZKPassport provider's local
// verifier: an adult French national's passport, bound to the registration
// (SPEC §4, step 2). The provider's domain and RPC settings are its own;
// the vote's scope and conditions are added per call.
type Passport struct {
	Z     *providers.ZKPassport
	Scope string
}

// VerifyRegistration implements PassportVerifier.
func (p Passport) VerifyRegistration(ctx context.Context, proof json.RawMessage, customData string) (string, error) {
	extra := map[string]any{"minAge": 18, "nationality": "FRA", "documentType": "passport"}
	if p.Scope != "" {
		extra["scope"] = p.Scope
	}
	return p.Z.VerifyBound(ctx, proof, customData, extra)
}
