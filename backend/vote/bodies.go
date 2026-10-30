package vote

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"digital-mirror/backend/vote/ballot"
)

// The bodies voters post and the log holds (SPEC §4, §6.4, §8.1).

// Register is a registration: the leaf ID, the ZKPassport unique
// identifier and proof bundle, the proof's date, and the registration key
// the proof binds with its signature over the body (regsign.go).
type Register struct {
	V         int             `json:"v"`
	Event     int64           `json:"event"`
	ID        string          `json:"id"`
	UID       string          `json:"uid"`
	Proof     json.RawMessage `json:"proof"`
	ProofDate time.Time       `json:"proof_date"`
	Key       string          `json:"key"`
	Sig       string          `json:"sig,omitempty"`
}

// Ballot is a ballot as posted: its public inputs, the compressed
// ciphertexts and the proof.
type Ballot struct {
	V         int      `json:"v"`
	Event     int64    `json:"event"`
	Setup     int64    `json:"setup"`
	Root      string   `json:"root"`
	Nullifier string   `json:"nullifier"`
	Seq       uint32   `json:"seq"`
	C0        string   `json:"c0"`
	C         []string `json:"c"`
	Proof     string   `json:"proof"`
}

// Reject records an event refused with its reason (SPEC §8.1).
type Reject struct {
	V       int             `json:"v"`
	Event   int64           `json:"event"`
	NostrID string          `json:"nostr_id"`
	Body    json.RawMessage `json:"body"`
	Reason  string          `json:"reason"`
}

// Seen records a tagged event that is not a protocol body.
type Seen struct {
	V       int    `json:"v"`
	NostrID string `json:"nostr_id"`
	Hash    string `json:"hash"`
}

// The reject reasons, a closed list.
const (
	ReasonMalformed    = "malformed"
	ReasonUnknownSetup = "unknown_setup"
	ReasonOldRoot      = "old_root"
	ReasonBadProof     = "bad_proof"
	ReasonBadSig       = "bad_sig"
	ReasonDuplicate    = "duplicate"
	ReasonStaleSeq     = "stale_seq"
	ReasonStaleDate    = "stale_date"
	ReasonUIDUsed      = "uid_used"
	ReasonIDUsed       = "id_used"
	ReasonClosed       = "closed"
)

// Ack is the acknowledgement our server posts for every event it ingests
// (SPEC §7.2).
type Ack struct {
	V          int    `json:"v"`
	NostrID    string `json:"nostr_id"`
	BodyHash   string `json:"body_hash"`
	PIHash     string `json:"pi_hash,omitempty"`
	Checkpoint int64  `json:"checkpoint"`
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason,omitempty"`
	Position   *int   `json:"position,omitempty"`
}

// BodyHash is SHA-256 of a body's bytes, hex.
func BodyHash(b []byte) string {
	h := sha256.Sum256(b)
	return "0x" + hex.EncodeToString(h[:])
}

func parseField(s string) (fr.Element, error) {
	var e fr.Element
	if !strings.HasPrefix(s, "0x") || len(s) != 66 || s != strings.ToLower(s) {
		// Lower-case only (SPEC §3): the strings key the pseudonyms and
		// the leaves, so one element must have one spelling.
		return e, errors.New("a field element is 32 bytes of lower-case 0x hex")
	}
	b, err := hex.DecodeString(s[2:])
	if err != nil {
		return e, err
	}
	if err := e.SetBytesCanonical(b); err != nil {
		return e, errors.New("not a canonical field element")
	}
	return e, nil
}

// Parsed is a ballot decoded and structurally checked: its ciphertext,
// its public inputs in the circuit's order, and the proof bytes.
type Parsed struct {
	Ballot
	Ciphertext   ballot.Ciphertext
	RootEl       fr.Element
	NullifierEl  fr.Element
	PublicInputs []fr.Element
	ProofBytes   []byte
}

// ParseBallot decodes a ballot body (structure only: setup, root age,
// proof and seq are judged by the ingest).
func ParseBallot(raw []byte) (*Parsed, error) {
	var b Ballot
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("ballot json: %w", err)
	}
	if b.V != Version {
		return nil, fmt.Errorf("ballot version %d", b.V)
	}
	if len(b.C) != ballot.J {
		return nil, fmt.Errorf("%d ciphertexts, want %d", len(b.C), ballot.J)
	}
	if b.Seq == 0 {
		return nil, errors.New("seq starts at 1")
	}
	p := &Parsed{Ballot: b}
	var err error
	if p.RootEl, err = parseField(b.Root); err != nil {
		return nil, fmt.Errorf("root: %w", err)
	}
	if p.NullifierEl, err = parseField(b.Nullifier); err != nil {
		return nil, fmt.Errorf("nullifier: %w", err)
	}
	if p.Ciphertext.C0, err = ParsePoint(b.C0); err != nil {
		return nil, fmt.Errorf("c0: %w", err)
	}
	for j := range b.C {
		if p.Ciphertext.C[j], err = ParsePoint(b.C[j]); err != nil {
			return nil, fmt.Errorf("c[%d]: %w", j, err)
		}
	}
	if p.ProofBytes, err = base64.StdEncoding.DecodeString(b.Proof); err != nil {
		return nil, fmt.Errorf("proof: %w", err)
	}
	// Public inputs: event, setup, mask (added by the verifier from the
	// setup), root, nullifier, seq, then the points.
	p.PublicInputs = []fr.Element{fieldOf(b.Event), fieldOf(b.Setup)}
	return p, nil
}

// PublicInputs returns the circuit's public inputs for the mask of the
// setup the ballot names, in order, as 32-byte big-endian fields.
func (p *Parsed) PublicInputs32(mask uint32) [][32]byte {
	var m fr.Element
	m.SetUint64(uint64(mask))
	var s fr.Element
	s.SetUint64(uint64(p.Seq))
	els := []fr.Element{fieldOf(p.Event), fieldOf(p.Setup), m, p.RootEl, p.NullifierEl, s, p.Ciphertext.C0.X, p.Ciphertext.C0.Y}
	for j := range p.Ciphertext.C {
		els = append(els, p.Ciphertext.C[j].X, p.Ciphertext.C[j].Y)
	}
	out := make([][32]byte, len(els))
	for i := range els {
		out[i] = els[i].Bytes()
	}
	return out
}

// PIHash is the ballot's identity: SHA-256 over its public inputs, hex.
func (p *Parsed) PIHash(mask uint32) string {
	h := sha256.New()
	for _, b := range p.PublicInputs32(mask) {
		h.Write(b[:])
	}
	return "0x" + hex.EncodeToString(h.Sum(nil))
}

// bigField reads a field as an integer.
func bigField(e fr.Element) *big.Int { return e.BigInt(new(big.Int)) }
