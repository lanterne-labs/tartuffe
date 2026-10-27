// Package vote is tartuffe-vote/2 (docs/vote/SPEC.md): the setup of an
// event, the ingestion of registrations and ballots from the relays, the
// log entries, the acknowledgements and checkpoints, the tallies. It builds
// on backend/polls for the events, the public log, its checkpoints and the
// OpenTimestamps stamps, and on backend/vote/ballot for the arithmetic.
package vote

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"digital-mirror/backend/vote/ballot"
	"digital-mirror/backend/vote/grumpkin"
)

// The kinds of the log's entries (SPEC §8.1, §9, §11).
const (
	KindSetup     = "setup"
	KindRegister  = "register"
	KindBallot    = "ballot"
	KindReject    = "reject"
	KindSeen      = "seen"
	KindTally     = "tally"
	KindSnapshot  = "snapshot"
	KindClosed    = "closed"
	KindDestroyed = "destroyed"
	KindAbort     = "abort"
)

// Version is the protocol version every body carries.
const Version = 2

// Option is one slot of the ballot: a candidate (by person id), the blank
// vote, a reserved slot, or a withdrawn candidate.
type Option struct {
	Slot     int    `json:"slot"`
	Kind     string `json:"kind"` // candidate, blank, reserved, withdrawn
	PersonID int64  `json:"person_id,omitempty"`
	Name     string `json:"name,omitempty"`
}

// Relay is a relay of the setup and its state (active or retired).
type Relay struct {
	URL   string `json:"url"`
	State string `json:"state"`
}

// Circuit names the toolchain and source the verification key comes from.
type Circuit struct {
	Noir       string `json:"noir"`
	BB         string `json:"bb"`
	SourceHash string `json:"source_hash"`
}

// Setup is the setup entry of SPEC §11: everything a verifier needs to
// check a ballot, and the rules of the event. Fixed fields (SPEC §11) may
// not change between the setups of an event.
type Setup struct {
	V           int       `json:"v"`
	Event       int64     `json:"event"`
	Version     int       `json:"version"`
	Options     []Option  `json:"options"`
	Blank       int       `json:"blank"`
	Mask        uint32    `json:"mask"`
	NCap        int       `json:"n_cap"`
	PK          []string  `json:"pk"`
	PKPoK       []PoKJSON `json:"pk_pok"`
	VKHash      string    `json:"vk_hash"`
	Circuit     Circuit   `json:"circuit"`
	AppHash     string    `json:"app_hash"`
	SRSHash     string    `json:"srs_hash"`
	Relays      []Relay   `json:"relays"`
	NostrPubkey string    `json:"nostr_pubkey"`
	// TallyPubkey signs the tallies and the closed entry (sign.go).
	TallyPubkey      string     `json:"tally_pubkey"`
	KReal            int        `json:"k_real"`
	RMin             int        `json:"r_min"`
	TallyMinGap      int64      `json:"tally_min_gap"`
	TallyAt          string     `json:"tally_at,omitempty"`
	AnchorAge        int64      `json:"anchor_age"`
	CheckpointPeriod int64      `json:"checkpoint_period"`
	Validity         int64      `json:"validity"`
	Closes           time.Time  `json:"closes"`
	Blackouts        []Blackout `json:"blackouts"`
}

// Blackout is a period without tallies (SPEC §9.2).
type Blackout struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// PoKJSON is a proof of key knowledge in a body.
type PoKJSON struct {
	A string `json:"a"`
	Z string `json:"z"`
}

// Defaults of SPEC §13.
const (
	DefaultKReal            = 10
	DefaultRMin             = 32
	DefaultAnchorAge        = 24 * 3600
	DefaultCheckpointPeriod = 20
	DefaultValidity         = 3600
)

// Canonical marshals a body as canonical JSON: sorted keys, no spaces.
func Canonical(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic) // encoding/json sorts map keys
}

// Keys is what the tallier holds: the event's secret keys, and what it
// publishes: the public keys with their proofs of knowledge.
type Keys struct {
	Event int64     `json:"event"`
	SK    []string  `json:"sk"`
	PK    []string  `json:"pk"`
	PoK   []PoKJSON `json:"pk_pok"`
	// TallySK signs the tallies and the closed entry (sign.go); the setup
	// names its public half.
	TallySK     string    `json:"tally_sk"`
	TallyPubkey string    `json:"tally_pubkey"`
	Made        time.Time `json:"made"`
}

// PublicKeys is the public half of Keys, safe to copy off the tallier.
type PublicKeys struct {
	Event       int64     `json:"event"`
	PK          []string  `json:"pk"`
	PoK         []PoKJSON `json:"pk_pok"`
	TallyPubkey string    `json:"tally_pubkey"`
}

// GenerateKeys makes an event's election keys with their proofs.
func GenerateKeys(eventID int64) (*Keys, error) {
	sk, pk, err := ballot.GenerateKeys()
	if err != nil {
		return nil, err
	}
	k := &Keys{Event: eventID, Made: time.Now().UTC()}
	if k.TallySK, k.TallyPubkey, err = GenerateTallyKey(); err != nil {
		return nil, err
	}
	event := fieldOf(eventID)
	for j := 0; j < ballot.J; j++ {
		c, err := pk[j].Compress()
		if err != nil {
			return nil, err
		}
		pok, err := ballot.ProvePoK(sk[j], event, j)
		if err != nil {
			return nil, err
		}
		k.SK = append(k.SK, "0x"+sk[j].Text(16))
		k.PK = append(k.PK, "0x"+hex.EncodeToString(c[:]))
		pj, err := pokJSON(pok)
		if err != nil {
			return nil, err
		}
		k.PoK = append(k.PoK, pj)
	}
	return k, nil
}

// Public returns the public half.
func (k *Keys) Public() PublicKeys {
	return PublicKeys{Event: k.Event, PK: k.PK, PoK: k.PoK, TallyPubkey: k.TallyPubkey}
}

// Secret parses the secret keys.
func (k *Keys) Secret() (ballot.SecretKeys, error) {
	var sk ballot.SecretKeys
	if len(k.SK) != ballot.J {
		return sk, fmt.Errorf("vote: %d secret keys, not %d", len(k.SK), ballot.J)
	}
	for j, s := range k.SK {
		x, ok := new(big.Int).SetString(strings.TrimPrefix(s, "0x"), 16)
		if !ok || x.Sign() <= 0 {
			return sk, fmt.Errorf("vote: secret key %d is not a hex number", j)
		}
		sk[j] = x
	}
	return sk, nil
}

func pokJSON(p ballot.PoK) (PoKJSON, error) {
	a, err := p.A.Compress()
	if err != nil {
		return PoKJSON{}, err
	}
	return PoKJSON{A: "0x" + hex.EncodeToString(a[:]), Z: "0x" + p.Z.Text(16)}, nil
}

func fieldOf(eventID int64) fr.Element {
	var e fr.Element
	e.SetInt64(eventID)
	return e
}

// ParsePoint decodes a compressed point.
func ParsePoint(s string) (grumpkin.Point, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(b) != 32 {
		return grumpkin.Point{}, errors.New("vote: a point is 32 bytes of hex")
	}
	var arr [32]byte
	copy(arr[:], b)
	return grumpkin.Decompress(arr)
}

// ParsedKeys decodes and checks the public keys and their proofs.
func (p PublicKeys) ParsedKeys() (ballot.PublicKeys, error) {
	var pk ballot.PublicKeys
	if len(p.PK) != ballot.J || len(p.PoK) != ballot.J {
		return pk, fmt.Errorf("vote: %d keys and %d proofs, want %d", len(p.PK), len(p.PoK), ballot.J)
	}
	event := fieldOf(p.Event)
	seen := map[string]bool{}
	for j := range pk {
		pt, err := ParsePoint(p.PK[j])
		if err != nil {
			return pk, fmt.Errorf("vote: key %d: %w", j, err)
		}
		if seen[p.PK[j]] {
			return pk, fmt.Errorf("vote: key %d repeats another", j)
		}
		seen[p.PK[j]] = true
		a, err := ParsePoint(p.PoK[j].A)
		if err != nil {
			return pk, fmt.Errorf("vote: pok %d: %w", j, err)
		}
		z, ok := new(big.Int).SetString(strings.TrimPrefix(p.PoK[j].Z, "0x"), 16)
		if !ok {
			return pk, fmt.Errorf("vote: pok %d: z is not hex", j)
		}
		if err := ballot.VerifyPoK(ballot.PoK{A: a, Z: z}, pt, event, j); err != nil {
			return pk, fmt.Errorf("vote: key %d: %w", j, err)
		}
		pk[j] = pt
	}
	return pk, nil
}

// Tools are the circuit toolchain the setup runs.
type Tools struct {
	Nargo string
	BB    string
}

// FindTools looks for nargo (PATH, then ~/.nargo/bin) and bb (FindBB): what
// the setup needs to compile the circuit.
func FindTools(repo string) (Tools, error) {
	var t Tools
	if p, err := exec.LookPath("nargo"); err == nil {
		t.Nargo = p
	} else if home, err := os.UserHomeDir(); err == nil {
		t.Nargo = filepath.Join(home, ".nargo", "bin", "nargo")
	}
	if _, err := os.Stat(t.Nargo); err != nil {
		return t, errors.New("vote: nargo not found (PATH or ~/.nargo/bin)")
	}
	bb, err := FindBB(repo)
	if err != nil {
		return t, err
	}
	t.BB = bb
	return t, nil
}

// FindBB looks for bb, the proof verifier: the BB environment variable
// (the ingest's image sets it), then the PATH, then the binary shipped with
// bb.js under frontend/node_modules. The ingest and the audit need bb alone.
func FindBB(repo string) (string, error) {
	if p := os.Getenv("BB"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("bb"); err == nil {
		return p, nil
	}
	matches, _ := filepath.Glob(filepath.Join(repo, "frontend", "node_modules", "@aztec", "bb.js", "build", "*", "bb"))
	for _, m := range matches {
		if out, err := exec.Command(m, "--version").Output(); err == nil && len(out) > 0 {
			return m, nil
		}
	}
	return "", errors.New("vote: bb not found (BB env, PATH or frontend/node_modules/@aztec/bb.js/build)")
}

// Built is what compiling the circuit for a key set gives.
type Built struct {
	Circuit    Circuit
	VKHash     string
	VK         []byte
	ArtifactJS []byte // target/ballot.json, what the browser loads
}

var hexLine = regexp.MustCompile(`^0x[0-9a-f]+$`)

// BuildCircuit regenerates circuits/ballot/src/consts.nr for the public
// keys, compiles the circuit, writes its verification key and returns the
// hashes the setup records. It changes the working tree of circuits/ballot.
func BuildCircuit(src string, t Tools, pk ballot.PublicKeys) (*Built, error) {
	// A staged copy: the repository's circuit keeps its development keys.
	dir, err := os.MkdirTemp("", "ballot-setup-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for _, f := range []string{"Nargo.toml", "src/main.nr", "scripts/consts.mjs"} {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			return nil, err
		}
	}
	// consts.nr through the same generator as the development keys.
	var coords bytes.Buffer
	for j := range pk {
		fmt.Fprintf(&coords, "0x%s\n0x%s\n", pk[j].X.Text(16), pk[j].Y.Text(16))
	}
	keysFile := filepath.Join(dir, "setup-keys.txt")
	if err := os.WriteFile(keysFile, coords.Bytes(), 0o644); err != nil {
		return nil, err
	}
	consts, err := exec.Command("node", filepath.Join(dir, "scripts", "consts.mjs"), keysFile).Output()
	if err != nil {
		return nil, fmt.Errorf("vote: consts.mjs: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "consts.nr"), consts, 0o644); err != nil {
		return nil, err
	}
	if out, err := run(t.Nargo, dir, "compile"); err != nil {
		return nil, fmt.Errorf("vote: nargo compile: %w\n%s", err, out)
	}
	artifact, err := os.ReadFile(filepath.Join(dir, "target", "ballot.json"))
	if err != nil {
		return nil, err
	}
	outDir := filepath.Join(dir, "target", "setup")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	if out, err := run(t.BB, dir, "write_vk", "-b", "target/ballot.json", "-o", outDir, "-t", "noir-recursive"); err != nil {
		return nil, fmt.Errorf("vote: bb write_vk: %w\n%s", err, out)
	}
	vk, err := os.ReadFile(filepath.Join(outDir, "vk"))
	if err != nil {
		return nil, err
	}
	vkHash, err := os.ReadFile(filepath.Join(outDir, "vk_hash"))
	if err != nil {
		return nil, err
	}
	noirV, _ := run(t.Nargo, dir, "--version")
	bbV, _ := run(t.BB, dir, "--version")
	srcHash := sha256.New()
	for _, f := range []string{"Nargo.toml", "src/main.nr", "src/consts.nr"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return nil, err
		}
		srcHash.Write(b)
	}
	return &Built{
		Circuit: Circuit{
			Noir:       firstLine(noirV),
			BB:         strings.TrimSpace(string(bbV)),
			SourceHash: "0x" + hex.EncodeToString(srcHash.Sum(nil)),
		},
		VKHash:     "0x" + hex.EncodeToString(vkHash),
		VK:         vk,
		ArtifactJS: artifact,
	}, nil
}

func run(bin, dir string, args ...string) ([]byte, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// Validate checks a setup's own consistency (not its relation to earlier
// setups, which the log checks: SPEC §11).
func (s *Setup) Validate() error { return s.validate(true) }

// ValidateLogged checks a setup read from the log: as Validate, but a
// setup without the server keys (a trial log) is the auditor's warning,
// not an invalid setup.
func (s *Setup) ValidateLogged() error { return s.validate(false) }

func (s *Setup) validate(requireKeys bool) error {
	if s.V != Version {
		return fmt.Errorf("vote: setup version %d", s.V)
	}
	if s.NCap != ballot.N || s.Blank != ballot.Blank {
		return errors.New("vote: n_cap and blank must match the circuit")
	}
	if len(s.Options) != ballot.N {
		return fmt.Errorf("vote: %d options, want %d", len(s.Options), ballot.N)
	}
	if requireKeys && (len(s.TallyPubkey) != 64 || s.TallyPubkey != strings.ToLower(s.TallyPubkey)) {
		// Without it anyone with the database could append a tally or
		// close the vote (sign.go).
		return errors.New("vote: tally_pubkey (the tallier's signing key of keygen, 32 bytes of lower-case hex) is required")
	}
	if requireKeys && (len(s.NostrPubkey) != 64 || s.NostrPubkey != strings.ToLower(s.NostrPubkey)) {
		// Without it every acknowledgement on the relays passes for the
		// server's, and a forged one proves a withholding (SPEC §7.2).
		return errors.New("vote: nostr_pubkey (the ingest's key, 32 bytes of lower-case hex) is required")
	}
	var mask uint32
	for i, o := range s.Options {
		if o.Slot != i {
			return fmt.Errorf("vote: option %d has slot %d", i, o.Slot)
		}
		switch o.Kind {
		case "candidate":
			if o.PersonID == 0 {
				return fmt.Errorf("vote: candidate at slot %d has no person", i)
			}
			mask |= 1 << uint(i)
		case "blank":
			if i != ballot.Blank {
				return errors.New("vote: the blank vote is not at its slot")
			}
			mask |= 1 << uint(i)
		case "reserved", "withdrawn":
		default:
			return fmt.Errorf("vote: option kind %q", o.Kind)
		}
	}
	if s.Mask != mask {
		return fmt.Errorf("vote: mask %#x does not match the options (%#x)", s.Mask, mask)
	}
	if _, err := (PublicKeys{Event: s.Event, PK: s.PK, PoK: s.PKPoK}).ParsedKeys(); err != nil {
		return err
	}
	if !hexLine.MatchString(s.VKHash) || !hexLine.MatchString(s.Circuit.SourceHash) {
		return errors.New("vote: vk_hash and source_hash are hex")
	}
	active := 0
	for _, r := range s.Relays {
		if !strings.HasPrefix(r.URL, "wss://") {
			return fmt.Errorf("vote: relay %q is not wss", r.URL)
		}
		if r.State == "active" {
			active++
		}
	}
	if active < 1 {
		return errors.New("vote: no active relay")
	}
	if s.KReal < 1 || s.RMin < 0 || s.AnchorAge <= 0 || s.CheckpointPeriod <= 0 || s.Validity <= 0 {
		return errors.New("vote: a parameter is out of range")
	}
	if s.Closes.IsZero() {
		return errors.New("vote: no close")
	}
	for _, b := range s.Blackouts {
		if !b.To.After(b.From) {
			return errors.New("vote: a blackout ends before it starts")
		}
	}
	return nil
}
