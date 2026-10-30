package vote

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"digital-mirror/backend/vote/ballot"
	"digital-mirror/backend/vote/poseidon2"
)

// The registration key (SPEC §4): the passport is scanned before the voter
// secret exists, so the proof cannot bind the leaf. It binds a throwaway
// secp256k1 key the browser makes before the scan
// (`custom_data = H(T_REG, event, key_lo, key_hi)`), and the registration
// carries that key with its BIP340 signature over the body: the leaf is
// the one the key's holder chose, and a relay that saw the registration
// cannot post the proof again under a leaf of its own.

const registerSigPrefix = "tartuffe-vote/2/register\n"

// RegistrationBinding is custom_data = H(T_REG, event, key_lo, key_hi),
// lower-case hex: the x-only public key of the registration key in two
// 16-byte limbs, as the PRF output (§4.1).
func RegistrationBinding(eventID int64, key [32]byte) string {
	var lo, hi fr.Element
	lo.SetBytes(key[:16])
	hi.SetBytes(key[16:])
	h := poseidon2.Hash(ballot.TagReg, fieldOf(eventID), lo, hi)
	b := h.Bytes()
	return "0x" + hexOf(b[:])
}

// registerKey parses the registration key of a body: 32 bytes of
// lower-case hex, a valid x-only public key.
func registerKey(r Register) ([32]byte, *btcec.PublicKey, error) {
	var key [32]byte
	b, err := hex.DecodeString(r.Key)
	if err != nil || len(b) != 32 || r.Key != hexOf(b) {
		return key, nil, errors.New("key: 32 bytes of lower-case hex")
	}
	pub, err := schnorr.ParsePubKey(b)
	if err != nil {
		return key, nil, fmt.Errorf("key: %w", err)
	}
	copy(key[:], b)
	return key, pub, nil
}

// registerSigningHash gives what the registration key signs: SHA-256 of
// the domain prefix and the body without `sig`. The body's values are
// kept as the browser wrote them (a date with its milliseconds, the proof
// bundle as is): only the `sig` member is dropped and the top-level keys
// sorted, so both sides hash the same bytes.
func registerSigningHash(body []byte) ([32]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return [32]byte{}, err
	}
	delete(m, "sig")
	unsigned, err := json.Marshal(m)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append([]byte(registerSigPrefix), unsigned...)), nil
}

// VerifyRegisterSig checks a register body's `sig` under its `key` and
// returns the key.
func VerifyRegisterSig(body []byte) ([32]byte, error) {
	var r Register
	if err := json.Unmarshal(body, &r); err != nil {
		return [32]byte{}, err
	}
	key, pub, err := registerKey(r)
	if err != nil {
		return key, err
	}
	sb, err := hex.DecodeString(r.Sig)
	if err != nil || len(sb) != 64 || r.Sig != hexOf(sb) {
		return key, errors.New("sig: 64 bytes of lower-case hex")
	}
	sig, err := schnorr.ParseSignature(sb)
	if err != nil {
		return key, fmt.Errorf("sig: %w", err)
	}
	msg, err := registerSigningHash(body)
	if err != nil {
		return key, err
	}
	if !sig.Verify(msg[:], pub) {
		return key, errors.New("sig: does not verify under key")
	}
	return key, nil
}

// SignRegister sets the body's `key` and `sig` for a registration key
// (tests, local tools; the browser signs its own).
func SignRegister(r *Register, sk *btcec.PrivateKey) error {
	r.Key = hexOf(schnorr.SerializePubKey(sk.PubKey()))
	r.Sig = ""
	body, err := Canonical(*r)
	if err != nil {
		return err
	}
	msg, err := registerSigningHash(body)
	if err != nil {
		return err
	}
	sig, err := schnorr.Sign(sk, msg[:])
	if err != nil {
		return err
	}
	r.Sig = hexOf(sig.Serialize())
	return nil
}

// compactBody gives a posted body as the log keeps it: its bytes with the
// whitespace removed, nothing re-serialised (the signature covers them).
func compactBody(content []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, content); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RegistrationBindingOf gives the custom data a registration key binds.
func RegistrationBindingOf(eventID int64, sk *btcec.PrivateKey) string {
	var key [32]byte
	copy(key[:], schnorr.SerializePubKey(sk.PubKey()))
	return RegistrationBinding(eventID, key)
}
