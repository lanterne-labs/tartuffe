package vote

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// The vector the browser's test shares (protocol.test.ts): a fixed
// registration key, the binding and the signature it gives.
func TestRegisterSigVector(t *testing.T) {
	skb, _ := hex.DecodeString("1111111111111111111111111111111111111111111111111111111111111111")
	sk, _ := btcec.PrivKeyFromBytes(skb)
	r := Register{V: Version, Event: 7, ID: "0x" + "00000000000000000000000000000000000000000000000000000000000000" + "2a", UID: "uid-vector", Proof: json.RawMessage(`{"proofs":[],"result":{"a":1}}`), ProofDate: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	if err := SignRegister(&r, sk); err != nil {
		t.Fatal(err)
	}
	if want := hex.EncodeToString(schnorr.SerializePubKey(sk.PubKey())); r.Key != want {
		t.Fatalf("key %s, want %s", r.Key, want)
	}
	body, _ := Canonical(r)
	key, err := VerifyRegisterSig(body)
	if err != nil {
		t.Fatal(err)
	}
	// The browser writes the date with its milliseconds and Go trims the
	// zeros: the signature covers the bytes as posted, so a body the
	// browser wrote verifies as is.
	posted := []byte(strings.Replace(string(body), `"proof_date":"2026-10-30T12:00:00Z"`, `"proof_date":"2026-10-30T12:00:00.000Z"`, 1))
	if bytes.Equal(posted, body) {
		t.Fatal("the vector's date was not rewritten")
	}
	var rp Register
	if err := json.Unmarshal(posted, &rp); err != nil {
		t.Fatal(err)
	}
	if err := SignRegister(&rp, sk); err != nil {
		t.Fatal(err)
	}
	// Signed over Go's bytes, then checked over the browser's spelling of
	// the same body: refused, as it should be, since the bytes differ…
	if _, err := VerifyRegisterSig(posted); err == nil {
		t.Fatal("a signature over other bytes verified")
	}
	// …and a signature over the posted bytes verifies over them.
	msg, _ := registerSigningHash(posted)
	sig2, _ := schnorr.Sign(sk, msg[:])
	posted2 := bytes.Replace(posted, []byte(r.Sig), []byte(hex.EncodeToString(sig2.Serialize())), 1)
	if _, err := VerifyRegisterSig(posted2); err != nil {
		t.Fatalf("the posted bytes: %v", err)
	}
	// BIP340 signatures are deterministic without aux randomness only;
	// btcec draws aux data, so the vector pins the binding and the
	// verification, not the signature bytes.
	if got := RegistrationBinding(7, key); got != RegistrationBindingOf(7, sk) {
		t.Fatalf("binding %s", got)
	}
	t.Logf("key %s binding %s", r.Key, RegistrationBinding(7, key))
	// A changed leaf, a changed key, a truncated signature: refused.
	check := func(bad Register) error {
		b, _ := Canonical(bad)
		_, err := VerifyRegisterSig(b)
		return err
	}
	bad := r
	bad.ID = "0x" + "00000000000000000000000000000000000000000000000000000000000000" + "2b"
	if check(bad) == nil {
		t.Fatal("a changed leaf verified")
	}
	bad = r
	bad.Key = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	if check(bad) == nil {
		t.Fatal("a changed key verified")
	}
	bad = r
	bad.Sig = r.Sig[:126]
	if check(bad) == nil {
		t.Fatal("a truncated signature verified")
	}
	bad = r
	bad.Key = "ZZ" + r.Key[2:]
	if _, _, err := registerKey(bad); err == nil {
		t.Fatal("a non-hex key parsed")
	}
}
