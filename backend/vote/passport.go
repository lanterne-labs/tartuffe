package vote

import (
	"context"
	"encoding/json"
	"errors"
	"time"

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

// VerifyRegistration implements PassportVerifier: the passport must not be
// expired at the proof's date (a query of the proof, checked against that
// date, not today's: an audit runs later).
func (p Passport) VerifyRegistration(ctx context.Context, proof json.RawMessage, customData string, proofDate time.Time) (string, error) {
	extra := map[string]any{"minAge": 18, "nationality": "FRA", "documentType": "passport", "requireExpiry": true, "proofDate": proofDate.UTC().Format(time.RFC3339)}
	if p.Scope != "" {
		extra["scope"] = p.Scope
	}
	return p.Z.VerifyBound(ctx, proof, customData, extra)
}

// DevPassport accepts any registration, with the hash of its proof bundle
// as the passport identifier: for local trials without the ZKPassport app.
// Never in production: the command refuses it when DM_PRODUCTION is
// set.
type DevPassport struct{}

// VerifyRegistration implements PassportVerifier: the identifier is the one
// the bundle's result claims.
func (DevPassport) VerifyRegistration(ctx context.Context, proof json.RawMessage, customData string, proofDate time.Time) (string, error) {
	var b struct {
		Result struct {
			UID string `json:"uniqueIdentifier"`
		} `json:"result"`
	}
	if err := json.Unmarshal(proof, &b); err != nil || b.Result.UID == "" {
		return "", errors.New("dev passport: no uniqueIdentifier in the result")
	}
	return b.Result.UID, nil
}
