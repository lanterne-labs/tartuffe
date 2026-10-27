package vote

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// The tallier's signature on what it appends (SPEC §9.1, §9.3): a tally
// and the closed entry carry `sig`, Ed25519 over a domain prefix and the
// canonical body without `sig`, under the key the setup names
// (tally_pubkey). Anyone with the database could append a tally or a
// close otherwise; the ingest, the auditor and the browser take only the
// tallier's.

const entrySigPrefix = "tartuffe-vote/2/tally\n"

// GenerateTallyKey makes the tallier's signing key: the seed (secret,
// hex) and the public key (hex).
func GenerateTallyKey() (seed, pub string, err error) {
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("tally key: %w", err)
	}
	return hex.EncodeToString(sk.Seed()), hex.EncodeToString(pk), nil
}

// TallyPubkeyOf gives the public key of a seed.
func TallyPubkeyOf(seed string) (string, error) {
	b, err := hex.DecodeString(seed)
	if err != nil || len(b) != ed25519.SeedSize {
		return "", errors.New("tally_sk: 32 bytes of hex")
	}
	return hex.EncodeToString(ed25519.NewKeyFromSeed(b).Public().(ed25519.PublicKey)), nil
}

// signingBytes gives what is signed for a body: its canonical form without
// `sig`, prefixed.
func signingBytes(m map[string]any) ([]byte, error) {
	delete(m, "sig")
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return append([]byte(entrySigPrefix), body...), nil
}

// SignEntry returns the canonical body of an entry with its `sig`.
func SignEntry(seed string, entry any) ([]byte, error) {
	b, err := hex.DecodeString(seed)
	if err != nil || len(b) != ed25519.SeedSize {
		return nil, errors.New("tally_sk: 32 bytes of hex")
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	msg, err := signingBytes(m)
	if err != nil {
		return nil, err
	}
	m["sig"] = hex.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(b), msg))
	return json.Marshal(m)
}

// VerifyEntry checks the `sig` of an entry body under the tallier's key.
func VerifyEntry(pub string, body []byte) error {
	pk, err := hex.DecodeString(pub)
	if err != nil || len(pk) != ed25519.PublicKeySize {
		return errors.New("tally_pubkey: 32 bytes of hex")
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return err
	}
	sigHex, _ := m["sig"].(string)
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("no tallier signature")
	}
	msg, err := signingBytes(m)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(pk), msg, sig) {
		return errors.New("the tallier's signature does not verify")
	}
	return nil
}
