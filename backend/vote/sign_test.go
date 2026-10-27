package vote

import (
	"strings"
	"testing"
)

// A fixed seed and body give a fixed signature: the browser's check
// (frontend/src/lib/vote/tally.ts) holds the same vector.
func TestSignEntryVector(t *testing.T) {
	seed := strings.Repeat("11", 32)
	pub, err := TallyPubkeyOf(seed)
	if err != nil {
		t.Fatal(err)
	}
	body, err := SignEntry(seed, map[string]any{"v": 2, "event": 1, "upto": 5, "c": 3, "s0": "0x01", "s": []string{"0x02"}, "p": []string{"0x03"}, "dleq": []map[string]string{{"a": "0x04", "b": "0x05", "z": "0x06"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pub %s\nbody %s", pub, body)
	if err := VerifyEntry(pub, body); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEntry(pub, []byte(strings.Replace(string(body), `"upto":5`, `"upto":6`, 1))); err == nil {
		t.Fatal("a changed body verified")
	}
	if err := VerifyEntry(pub, []byte(strings.Replace(string(body), `"sig":"`, `"sig":"00`, 1)[:len(body)])); err == nil {
		t.Fatal("a changed signature verified")
	}
}
