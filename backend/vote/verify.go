package vote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// Verifier checks ballot proofs with Barretenberg's `bb verify`, against
// the verification key of a setup. UltraHonk has no Go verifier, so each
// check runs the binary (about 50 ms); the ingest verifies ballots one
// after the other per relay.
type Verifier struct {
	bb string
	vk string // path of the verification key file
	mu sync.Mutex
}

// NewVerifier writes the verification key beside a temp dir and returns
// the verifier.
func NewVerifier(bb string, vk []byte) (*Verifier, error) {
	dir, err := os.MkdirTemp("", "vote-vk-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "vk")
	if err := os.WriteFile(path, vk, 0o600); err != nil {
		return nil, err
	}
	return &Verifier{bb: bb, vk: path}, nil
}

// Verify checks one proof against its public inputs (32-byte fields in
// the circuit's order). It returns nil when the proof verifies, an error
// that wraps ErrBadProof when it does not, and another error when bb
// could not run.
func (v *Verifier) Verify(ctx context.Context, proof []byte, publicInputs [][32]byte) error {
	dir, err := os.MkdirTemp("", "vote-proof-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	var pi bytes.Buffer
	for _, f := range publicInputs {
		pi.Write(f[:])
	}
	if err := os.WriteFile(filepath.Join(dir, "proof"), proof, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "public_inputs"), pi.Bytes(), 0o600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, v.bb, "verify", "-k", v.vk, "-p", filepath.Join(dir, "proof"), "-i", filepath.Join(dir, "public_inputs"), "-t", "noir-recursive")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("%w: %s", ErrBadProof, bytes.TrimSpace(out))
	}
	return fmt.Errorf("bb verify: %w", err)
}

// ErrBadProof is returned for a proof that does not verify.
var ErrBadProof = errors.New("the proof does not verify")
