package polls

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/mod/sumdb/note"
	"golang.org/x/mod/sumdb/tlog"
)

// LogOrigin names the log in its checkpoints and its key.
const LogOrigin = "tartuffe.me/vote-log"

// A checkpoint is the signed head of the log's tree, in the format of
// transparency logs (C2SP tlog-checkpoint, a signed note):
//
//	tartuffe.me/vote-log
//	<entries>
//	<base64 tree hash>
//
//	— tartuffe.me/vote-log <base64 key hash and Ed25519 signature>
//
// Two checkpoints of one key that no append proof joins are the proof that
// the log showed two histories.

// CheckpointText is the signed text of the checkpoint of the tree.
func CheckpointText(t LogTree) string {
	return fmt.Sprintf("%s\n%d\n%s\n", LogOrigin, t.N, base64.StdEncoding.EncodeToString(t.Hash[:]))
}

// LogSigner signs the checkpoints.
type LogSigner struct {
	signer   note.Signer
	verifier string
}

// GenerateLogKey makes a new key of the log: the signer key (secret, the
// API's VOTE_LOG_KEY) and the verifier key (public).
func GenerateLogKey() (signer, verifier string, err error) {
	return note.GenerateKey(rand.Reader, LogOrigin)
}

// NewLogSigner reads a signer key of GenerateLogKey.
func NewLogSigner(skey string) (*LogSigner, error) {
	s, err := note.NewSigner(skey)
	if err != nil {
		return nil, fmt.Errorf("vote log key: %w", err)
	}
	if s.Name() != LogOrigin {
		return nil, fmt.Errorf("vote log key: it names %q, not %q", s.Name(), LogOrigin)
	}
	v, err := verifierKey(skey)
	if err != nil {
		return nil, err
	}
	return &LogSigner{signer: s, verifier: v}, nil
}

// verifierKey derives the verifier key from the signer key
// PRIVATE+KEY+<name>+<hash>+<base64 0x01 || seed>.
func verifierKey(skey string) (string, error) {
	// The key data is base64 and may hold "+" too: it is all after the fourth.
	parts := strings.SplitN(skey, "+", 5)
	if len(parts) != 5 || parts[0] != "PRIVATE" || parts[1] != "KEY" {
		return "", fmt.Errorf("vote log key: not a signer key")
	}
	raw, err := base64.StdEncoding.DecodeString(parts[4])
	if err != nil || len(raw) != 1+ed25519.SeedSize || raw[0] != 1 {
		return "", fmt.Errorf("vote log key: not an Ed25519 signer key")
	}
	pub := ed25519.NewKeyFromSeed(raw[1:]).Public().(ed25519.PublicKey)
	return parts[2] + "+" + parts[3] + "+" + base64.StdEncoding.EncodeToString(append([]byte{1}, pub...)), nil
}

// VerifierKey is the public key the checkpoints are checked with.
func (s *LogSigner) VerifierKey() string { return s.verifier }

// Sign signs the checkpoint of the tree.
func (s *LogSigner) Sign(t LogTree) ([]byte, error) {
	msg, err := note.Sign(&note.Note{Text: CheckpointText(t)}, s.signer)
	if err != nil {
		return nil, fmt.Errorf("sign checkpoint: %w", err)
	}
	return msg, nil
}

// OpenCheckpoint checks a checkpoint against the verifier key and returns
// its tree.
func OpenCheckpoint(msg []byte, vkey string) (LogTree, error) {
	v, err := note.NewVerifier(vkey)
	if err != nil {
		return LogTree{}, fmt.Errorf("vote log verifier key: %w", err)
	}
	n, err := note.Open(msg, note.VerifierList(v))
	if err != nil {
		return LogTree{}, fmt.Errorf("checkpoint: %w", err)
	}
	lines := strings.Split(n.Text, "\n")
	if len(lines) < 4 || lines[0] != LogOrigin {
		return LogTree{}, fmt.Errorf("checkpoint: not one of %s", LogOrigin)
	}
	size, err := strconv.ParseInt(lines[1], 10, 64)
	if err != nil || size < 0 {
		return LogTree{}, fmt.Errorf("checkpoint: bad size %q", lines[1])
	}
	raw, err := base64.StdEncoding.DecodeString(lines[2])
	if err != nil || len(raw) != tlog.HashSize {
		return LogTree{}, fmt.Errorf("checkpoint: bad hash %q", lines[2])
	}
	t := LogTree{N: size}
	copy(t.Hash[:], raw)
	return t, nil
}

// CheckpointDigest is what OpenTimestamps stamps: the SHA-256 of the signed
// checkpoint, so `ots verify` takes the checkpoint file as it is served.
func CheckpointDigest(msg []byte) [32]byte { return sha256.Sum256(msg) }
