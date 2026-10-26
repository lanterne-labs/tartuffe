package poseidon2

import (
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

func el(s string) fr.Element {
	var e fr.Element
	if _, err := e.SetString(s); err != nil {
		panic(err)
	}
	return e
}

// The permutation vector of Barretenberg's poseidon2_params.hpp.
func TestPermutationVector(t *testing.T) {
	var in [4]fr.Element
	for i := range in {
		in[i] = el(testInput[i])
	}
	out := Permutation(in)
	for i := range out {
		if want := el(testOutput[i]); !out[i].Equal(&want) {
			t.Fatalf("output %d: %s, want %s", i, out[i].String(), want.String())
		}
	}
}

// The value the circuit prints in its poseidon2_vector test
// (circuits/ballot, `h3(1, 2, 3)`), also checked by bb.js in vectors.mjs.
func TestHashMatchesTheCircuit(t *testing.T) {
	h := Hash(el("1"), el("2"), el("3"))
	want := el("0x23864adb160dddf590f1d3303683ebcb914f828e2635f6e85a32f0a1aecd3dd8")
	if !h.Equal(&want) {
		t.Fatalf("h(1,2,3) = %s, want %s", h.Text(16), want.Text(16))
	}
}

// Lengths that are not a multiple of the rate take one more permutation;
// the hash must still depend on every input and on the length.
func TestHashLengths(t *testing.T) {
	seen := map[string]bool{}
	for n := 0; n <= 7; n++ {
		in := make([]fr.Element, n)
		for i := range in {
			in[i].SetUint64(uint64(i + 1))
		}
		hh := Hash(in...)
		h := hh.Text(16)
		if seen[h] {
			t.Fatalf("length %d collides with a shorter input", n)
		}
		seen[h] = true
	}
}
