// Package ballot is the arithmetic of tartuffe-vote/2 ballots
// (docs/vote/SPEC.md §6, §9): the ranking rules and their values, the
// packing into plaintexts, exponential ElGamal on Grumpkin, the per-ballot
// and summed decryptions, the Chaum-Pedersen proofs of decryption, the
// proofs of key knowledge, the voter tree and the pseudonyms. It mirrors
// circuits/ballot and is checked against its vectors.
package ballot

import (
	"math/big"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

const (
	// N is the number of option slots; Blank the blank vote's slot.
	N     = 10
	Blank = 7
	// V is the number of values per ballot: voter, first[N], ranked[N],
	// margin[i<j].
	V = 1 + 2*N + N*(N-1)/2
	// PerCT values share one plaintext; J is the number of ciphertexts.
	PerCT = 7
	J     = (V + PerCT - 1) / PerCT
	// DigitBits is the width of a packed digit.
	DigitBits = 36
	// Depth of the voter tree.
	Depth = 20
	// MaxSeq bounds a ballot's sequence number (seq < 2^32 in the circuit).
	MaxSeq = 1 << 32
)

// Digit is 2^DigitBits.
var Digit = new(big.Int).Lsh(big.NewInt(1), DigitBits)

// Tag returns the domain tag of a name: the ASCII string
// "tartuffe-vote/2/<name>" read as a big-endian integer (SPEC §3).
func Tag(name string) fr.Element {
	s := "tartuffe-vote/2/" + name
	if len(s) > 31 {
		panic("ballot: tag too long: " + name)
	}
	var e fr.Element
	e.SetBigInt(new(big.Int).SetBytes([]byte(s)))
	return e
}

var (
	TagID    = Tag("id")
	TagNull  = Tag("null")
	TagNode  = Tag("node")
	TagReg   = Tag("reg")
	TagPRF   = Tag("prf")
	TagRelay = Tag("relay")
	TagDLEQ  = Tag("dleq")
	TagNonce = Tag("nonce")
	TagPoK   = Tag("pok")
	TagCT    = Tag("ct")
)
