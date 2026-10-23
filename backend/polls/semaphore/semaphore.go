// Package semaphore checks the anonymous ballots: Semaphore v4 proofs (a
// member of a group proves it, without saying which one, and gives a
// nullifier that is the same for each of its proofs in one scope), and the
// group itself, a Lean incremental Merkle tree (LeanIMT) of the members'
// commitments, hashed with Poseidon. Both match the JavaScript libraries the
// browser uses (@semaphore-protocol/proof and @semaphore-protocol/group
// 4.14.5), so a root computed here equals the one the browser proves against.
package semaphore

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/vocdoni/circom2gnark/parser"
	"golang.org/x/crypto/sha3"
)

// MaxDepth is the deepest tree Semaphore v4 has keys for (2^32 members).
const MaxDepth = 32

// FieldModulus is the order of BN254's scalar field: every public value of a
// proof is below it.
var FieldModulus, _ = new(big.Int).SetString("21888242871839275222246405745257275088548364400416034343698204186575808495617", 10)

// Proof is a Semaphore proof as @semaphore-protocol/proof gives it: the
// numbers are decimal strings, Message and Scope the values the browser
// passed (the circuit takes their hashes, see Hash).
type Proof struct {
	MerkleTreeDepth int      `json:"merkleTreeDepth"`
	MerkleTreeRoot  string   `json:"merkleTreeRoot"`
	Nullifier       string   `json:"nullifier"`
	Message         string   `json:"message"`
	Scope           string   `json:"scope"`
	Points          []string `json:"points"`
}

// ErrInvalid is wrapped by every reason a proof is refused.
var ErrInvalid = errors.New("invalid semaphore proof")

// Verify checks the proof against the Semaphore v4 verification key of its
// depth. It says nothing of the root, the scope or the message: the caller
// compares them with what it expects.
func Verify(p Proof) error {
	if p.MerkleTreeDepth < 1 || p.MerkleTreeDepth > MaxDepth {
		return fmt.Errorf("%w: depth %d", ErrInvalid, p.MerkleTreeDepth)
	}
	if len(p.Points) != 8 {
		return fmt.Errorf("%w: %d points, want 8", ErrInvalid, len(p.Points))
	}
	public := make([]string, 0, 4)
	for _, v := range []string{p.MerkleTreeRoot, p.Nullifier} {
		n, err := ParseField(v)
		if err != nil {
			return err
		}
		public = append(public, n.String())
	}
	for _, v := range []string{p.Message, p.Scope} {
		n, ok := new(big.Int).SetString(v, 10)
		if !ok || n.Sign() < 0 || n.BitLen() > 256 {
			return fmt.Errorf("%w: %q is not a 32-byte number", ErrInvalid, v)
		}
		public = append(public, Hash(n).String())
	}
	for _, v := range p.Points {
		if _, err := ParseField(v); err != nil {
			// The points are coordinates in the base field, a little
			// larger than the scalar field: parse checks them.
			if n, ok := new(big.Int).SetString(v, 10); !ok || n.Sign() < 0 {
				return fmt.Errorf("%w: point %q", ErrInvalid, v)
			}
		}
	}
	vk, err := verificationKey(p.MerkleTreeDepth)
	if err != nil {
		return err
	}
	pt := p.Points
	proofJSON, _ := json.Marshal(map[string]any{
		"pi_a":     []string{pt[0], pt[1], "1"},
		"pi_b":     [][]string{{pt[3], pt[2]}, {pt[5], pt[4]}, {"1", "0"}},
		"pi_c":     []string{pt[6], pt[7], "1"},
		"protocol": "groth16",
		"curve":    "bn128",
	})
	circomProof, err := parser.UnmarshalCircomProofJSON(proofJSON)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	gnarkProof, err := parser.ConvertCircomToGnark(circomProof, vk, public)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	ok, err := parser.VerifyProof(gnarkProof)
	if err != nil || !ok {
		return fmt.Errorf("%w: the proof does not check", ErrInvalid)
	}
	return nil
}

// ParseField reads a decimal number of the scalar field.
func ParseField(v string) (*big.Int, error) {
	n, ok := new(big.Int).SetString(v, 10)
	if !ok || n.Sign() < 0 || n.Cmp(FieldModulus) >= 0 {
		return nil, fmt.Errorf("%w: %q is not a field element", ErrInvalid, v)
	}
	return n, nil
}

// Hash is Semaphore's hash of the message and the scope before they enter
// the circuit: keccak256 of the value on 32 bytes, shifted right by 8 bits.
func Hash(v *big.Int) *big.Int {
	b := make([]byte, 32)
	v.FillBytes(b)
	h := sha3.NewLegacyKeccak256()
	h.Write(b)
	out := new(big.Int).SetBytes(h.Sum(nil))
	return out.Rsh(out, 8)
}

// Digest turns bytes (a ballot, a scope's name) into the value the browser
// passes as message or scope: the first 31 bytes of their SHA-256, so it fits
// in the 32 bytes Semaphore takes.
func Digest(b []byte) *big.Int {
	h := sha256.Sum256(b)
	return new(big.Int).SetBytes(h[:31])
}

//go:embed vkeys.json
var vkeysJSON []byte

// vkeys are Semaphore v4's verification keys, from its trusted setup: one
// delta and one IC per depth, the rest shared.
var vkeys struct {
	once sync.Once
	err  error
	all  map[string]json.RawMessage
	keys sync.Map // depth -> *parser.CircomVerificationKey
}

func verificationKey(depth int) (*parser.CircomVerificationKey, error) {
	if k, ok := vkeys.keys.Load(depth); ok {
		return k.(*parser.CircomVerificationKey), nil
	}
	vkeys.once.Do(func() { vkeys.err = json.Unmarshal(vkeysJSON, &vkeys.all) })
	if vkeys.err != nil {
		return nil, fmt.Errorf("semaphore verification keys: %w", vkeys.err)
	}
	var deltas, ics []json.RawMessage
	if err := json.Unmarshal(vkeys.all["vk_delta_2"], &deltas); err != nil {
		return nil, fmt.Errorf("semaphore verification keys: %w", err)
	}
	if err := json.Unmarshal(vkeys.all["IC"], &ics); err != nil {
		return nil, fmt.Errorf("semaphore verification keys: %w", err)
	}
	if depth > len(deltas) || depth > len(ics) {
		return nil, fmt.Errorf("%w: no key for depth %d", ErrInvalid, depth)
	}
	one := map[string]json.RawMessage{}
	for k, v := range vkeys.all {
		one[k] = v
	}
	one["vk_delta_2"], one["IC"] = deltas[depth-1], ics[depth-1]
	raw, _ := json.Marshal(one)
	k, err := parser.UnmarshalCircomVerificationKeyJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("semaphore verification key of depth %d: %w", depth, err)
	}
	vkeys.keys.Store(depth, k)
	return k, nil
}
