// Package poseidon2 is the Poseidon2 hash of Barretenberg over the BN254
// scalar field, the H of docs/vote/SPEC.md §3: the permutation with
// Barretenberg's parameters (t = 4, 8 external and 56 internal rounds) and
// the sponge of noir-lang/poseidon (rate 3, IV = len << 64, one permutation
// per full block, one more when the input is empty or ends in a partial
// block). It reproduces `Poseidon2::hash(input, len)` in the circuit and
// `poseidon2Hash` of bb.js.
package poseidon2

import (
	"math/big"
	"sync"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

const (
	width     = 4
	rate      = 3
	roundsF   = 8
	roundsP   = 56
	allRounds = roundsF + roundsP
)

var (
	once sync.Once
	diag [width]fr.Element
	rc   [allRounds][width]fr.Element
)

func load() {
	for i := range diag {
		diag[i].SetString(diagMinusOne[i])
	}
	for r := range rc {
		for i := range rc[r] {
			rc[r][i].SetString(roundConstants[r][i])
		}
	}
}

func sbox(x *fr.Element) {
	var x2, x4 fr.Element
	x2.Square(x)
	x4.Square(&x2)
	x.Mul(&x4, x)
}

// externalMatrix applies the circulant MDS matrix of Poseidon2 for t = 4.
func externalMatrix(s *[width]fr.Element) {
	var t0, t1, t2, t3, t4, t5, t6, t7 fr.Element
	t0.Add(&s[0], &s[1])
	t1.Add(&s[2], &s[3])
	t2.Double(&s[1])
	t2.Add(&t2, &t1)
	t3.Double(&s[3])
	t3.Add(&t3, &t0)
	t4.Double(&t1)
	t4.Double(&t4)
	t4.Add(&t4, &t3)
	t5.Double(&t0)
	t5.Double(&t5)
	t5.Add(&t5, &t2)
	t6.Add(&t3, &t5)
	t7.Add(&t2, &t4)
	s[0] = t6
	s[1] = t5
	s[2] = t7
	s[3] = t4
}

// internalMatrix applies the internal matrix: x_i ← (D_i − 1)·x_i + Σ x.
func internalMatrix(s *[width]fr.Element) {
	var sum fr.Element
	for i := range s {
		sum.Add(&sum, &s[i])
	}
	for i := range s {
		s[i].Mul(&s[i], &diag[i])
		s[i].Add(&s[i], &sum)
	}
}

// Permutation is the Poseidon2 permutation on a state of 4 field elements.
func Permutation(s [width]fr.Element) [width]fr.Element {
	once.Do(load)
	externalMatrix(&s)
	for r := 0; r < roundsF/2; r++ {
		for i := range s {
			s[i].Add(&s[i], &rc[r][i])
			sbox(&s[i])
		}
		externalMatrix(&s)
	}
	for r := roundsF / 2; r < roundsF/2+roundsP; r++ {
		s[0].Add(&s[0], &rc[r][0])
		sbox(&s[0])
		internalMatrix(&s)
	}
	for r := roundsF/2 + roundsP; r < allRounds; r++ {
		for i := range s {
			s[i].Add(&s[i], &rc[r][i])
			sbox(&s[i])
		}
		externalMatrix(&s)
	}
	return s
}

// Hash is the sponge over the inputs, as the circuit's Poseidon2::hash.
func Hash(inputs ...fr.Element) fr.Element {
	var s [width]fr.Element
	var iv fr.Element
	iv.SetUint64(uint64(len(inputs)))
	var shift fr.Element
	shift.SetString("18446744073709551616") // 2^64
	s[rate].Mul(&iv, &shift)
	full := len(inputs) / rate
	for c := 0; c < full; c++ {
		for i := 0; i < rate; i++ {
			s[i].Add(&s[i], &inputs[c*rate+i])
		}
		s = Permutation(s)
	}
	for i := full * rate; i < len(inputs); i++ {
		s[i-full*rate].Add(&s[i-full*rate], &inputs[i])
	}
	if len(inputs) == 0 || len(inputs)%rate != 0 {
		s = Permutation(s)
	}
	return s[0]
}

// HashBig hashes big integers below the field order.
func HashBig(inputs ...*big.Int) *big.Int {
	els := make([]fr.Element, len(inputs))
	for i, x := range inputs {
		els[i].SetBigInt(x)
	}
	h := Hash(els...)
	return h.BigInt(new(big.Int))
}
