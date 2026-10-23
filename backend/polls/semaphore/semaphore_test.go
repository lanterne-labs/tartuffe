package semaphore

import (
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"testing"
)

// browserProof is a proof made by @semaphore-protocol/proof 4.14.5 in
// Chrome, for a group of 100 members, depth 20.
type browserProof struct {
	Proof   Proof    `json:"proof"`
	Members []string `json:"members"`
	Root    string   `json:"root"`
}

func loadBrowserProof(t *testing.T) browserProof {
	t.Helper()
	raw, err := os.ReadFile("testdata/browser_proof.json")
	if err != nil {
		t.Fatal(err)
	}
	var b browserProof
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVerifyBrowserProof(t *testing.T) {
	b := loadBrowserProof(t)
	if err := Verify(b.Proof); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyRefusesAnotherMessage(t *testing.T) {
	b := loadBrowserProof(t)
	p := b.Proof
	p.Message = "12345"
	if err := Verify(p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Verify of a changed message = %v, want ErrInvalid", err)
	}
	p = b.Proof
	p.Scope = "12345"
	if err := Verify(p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Verify of a changed scope = %v, want ErrInvalid", err)
	}
}

func TestVerifyRefusesMalformed(t *testing.T) {
	b := loadBrowserProof(t)
	for name, edit := range map[string]func(*Proof){
		"depth 0":       func(p *Proof) { p.MerkleTreeDepth = 0 },
		"depth 33":      func(p *Proof) { p.MerkleTreeDepth = 33 },
		"7 points":      func(p *Proof) { p.Points = p.Points[:7] },
		"root over p":   func(p *Proof) { p.MerkleTreeRoot = FieldModulus.String() },
		"not a number":  func(p *Proof) { p.Nullifier = "abc" },
		"another depth": func(p *Proof) { p.MerkleTreeDepth = 19 },
	} {
		p := b.Proof
		p.Points = append([]string(nil), b.Proof.Points...)
		edit(&p)
		if err := Verify(p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Verify = %v, want ErrInvalid", name, err)
		}
	}
}

func TestTreeRootMatchesBrowser(t *testing.T) {
	b := loadBrowserProof(t)
	leaves := make([]*big.Int, len(b.Members))
	for i, m := range b.Members {
		leaves[i], _ = new(big.Int).SetString(m, 10)
	}
	tree, err := NewTree(leaves)
	if err != nil {
		t.Fatal(err)
	}
	if got := tree.Root().String(); got != b.Root {
		t.Fatalf("root %s, browser %s", got, b.Root)
	}
	if tree.Depth() != 7 {
		t.Errorf("depth %d, want 7 for 100 leaves", tree.Depth())
	}
}

func TestTreeSmall(t *testing.T) {
	tree, _ := NewTree(nil)
	if tree.Root().Sign() != 0 {
		t.Fatalf("empty root %s, want 0", tree.Root())
	}
	one := big.NewInt(1)
	tree.Insert(one)
	if tree.Root().Cmp(one) != 0 || tree.Depth() != 0 {
		t.Fatalf("one leaf: root %s depth %d", tree.Root(), tree.Depth())
	}
	// Three leaves: H(H(1,2),3), the third going up unchanged.
	tree.Insert(big.NewInt(2))
	tree.Insert(big.NewInt(3))
	h12, _ := hash2(big.NewInt(1), big.NewInt(2))
	want, _ := hash2(h12, big.NewInt(3))
	if tree.Root().Cmp(want) != 0 {
		t.Fatalf("three leaves: root %s, want %s", tree.Root(), want)
	}
}
