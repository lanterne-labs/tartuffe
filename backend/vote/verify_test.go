package vote

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"digital-mirror/backend/vote/ballot"
)

// The circuit's vectors: the proof of one ballot with the development
// keys, verified with bb against the vk built for those keys.
func TestVerifyDevVectors(t *testing.T) {
	root := "../../"
	tools, err := FindTools(root)
	if err != nil {
		t.Skip(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "circuits/ballot/vectors/dev.json"))
	if err != nil {
		t.Skip(err)
	}
	var v struct {
		Ballot struct {
			PublicInputs []string `json:"public_inputs"`
			Ciphertexts  []string `json:"ciphertexts"`
			Inputs       struct {
				Event     string `json:"event"`
				Setup     string `json:"setup"`
				Mask      uint32 `json:"mask"`
				Root      string `json:"root"`
				Nullifier string `json:"nullifier"`
				Seq       uint32 `json:"seq"`
			} `json:"inputs"`
		} `json:"ballot"`
		Proof string `json:"proof"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	// The vk of the development keys: built on the fly from the repository's
	// circuit (its consts.nr holds the development keys).
	vkPath := filepath.Join(root, "circuits/ballot/out/native/vk")
	vk, err := os.ReadFile(vkPath)
	if err != nil {
		t.Skip("no vk for the development keys (run bb write_vk):", err)
	}
	ver, err := NewVerifier(tools.BB, vk)
	if err != nil {
		t.Fatal(err)
	}
	// The ballot body as a voter would post it.
	var c []string
	var c0 string
	for i, x := range v.Ballot.Ciphertexts {
		if i%2 == 1 {
			continue
		}
		var p struct{ X, Y string }
		p.X, p.Y = x, v.Ballot.Ciphertexts[i+1]
		pt, err := pointOf(p.X, p.Y)
		if err != nil {
			t.Fatal(err)
		}
		comp, err := pt.Compress()
		if err != nil {
			t.Fatal(err)
		}
		enc := "0x" + hexOf(comp[:])
		if i == 0 {
			c0 = enc
		} else {
			c = append(c, enc)
		}
	}
	body, _ := json.Marshal(Ballot{V: Version, Event: 1, Setup: 5, Root: v.Ballot.Inputs.Root, Nullifier: v.Ballot.Inputs.Nullifier,
		Seq: v.Ballot.Inputs.Seq, C0: c0, C: c, Proof: v.Proof})
	p, err := ParseBallot(body)
	if err != nil {
		t.Fatal(err)
	}
	pi := p.PublicInputs32(v.Ballot.Inputs.Mask)
	if len(pi) != len(v.Ballot.PublicInputs) {
		t.Fatalf("%d public inputs, the vectors have %d", len(pi), len(v.Ballot.PublicInputs))
	}
	for i := range pi {
		if got := "0x" + hexOf(pi[i][:]); got != v.Ballot.PublicInputs[i] {
			t.Fatalf("public input %d: %s, want %s", i, got, v.Ballot.PublicInputs[i])
		}
	}
	if err := ver.Verify(context.Background(), p.ProofBytes, pi); err != nil {
		t.Fatal(err)
	}
	// A changed public input fails.
	bad := p.PublicInputs32(v.Ballot.Inputs.Mask ^ 1)
	err = ver.Verify(context.Background(), p.ProofBytes, bad)
	if !errors.Is(err, ErrBadProof) {
		t.Fatalf("a wrong mask verified: %v", err)
	}
	// A truncated proof fails as a bad proof, not as a tool error.
	short, _ := base64.StdEncoding.DecodeString(v.Proof)
	err = ver.Verify(context.Background(), short[:len(short)-32], pi)
	if !errors.Is(err, ErrBadProof) {
		t.Fatalf("a truncated proof: %v", err)
	}
	_ = ballot.J
}
