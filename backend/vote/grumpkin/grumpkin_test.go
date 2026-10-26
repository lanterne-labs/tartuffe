package grumpkin

import (
	"bufio"
	"math/big"
	"os"
	"strings"
	"testing"
)

func TestGeneratorAndGroupLaw(t *testing.T) {
	g := Generator()
	if !g.IsOnCurve() {
		t.Fatal("G is not on the curve")
	}
	two := g.Double()
	if !two.Equal(g.Add(g)) || !two.IsOnCurve() {
		t.Fatal("2G differs between Double and Add")
	}
	three := two.Add(g)
	if !three.Equal(Mul(big.NewInt(3))) {
		t.Fatal("3G differs from the scalar multiplication")
	}
	if !g.Add(g.Neg()).IsInfinity() {
		t.Fatal("G − G is not infinity")
	}
	if !Mul(Order).IsInfinity() {
		t.Fatal("q·G is not infinity")
	}
	var qm1 big.Int
	qm1.Sub(Order, big.NewInt(1))
	if !Mul(&qm1).Equal(g.Neg()) {
		t.Fatal("(q−1)·G is not −G")
	}
}

func TestCompressRoundTrip(t *testing.T) {
	for _, k := range []int64{1, 2, 3, 12345, 99999999} {
		p := Mul(big.NewInt(k))
		b, err := p.Compress()
		if err != nil {
			t.Fatal(err)
		}
		q, err := Decompress(b)
		if err != nil {
			t.Fatal(err)
		}
		if !q.Equal(p) {
			t.Fatalf("%d·G does not round-trip", k)
		}
	}
	var zero [32]byte
	if _, err := Decompress(zero); err == nil {
		t.Fatal("x = 0 decoded, but 0³ − 17 has no square root")
	}
}

// The development keys of the circuit: PK_j = sk_j·G printed by
// circuits/keygen for the secret keys of its Prover.toml. The public keys
// are checked to be on the curve here; the scalar multiplication itself is
// checked against the circuit's own output in the ballot package.
func TestDevKeysAreOnTheCurve(t *testing.T) {
	f, err := os.Open("../../../circuits/ballot/dev-keys.txt")
	if err != nil {
		t.Skip("no development keys:", err)
	}
	defer f.Close()
	var coords []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); strings.HasPrefix(l, "0x") {
			coords = append(coords, l)
		}
	}
	if len(coords) != 20 {
		t.Fatalf("%d coordinates", len(coords))
	}
	for j := 0; j < 10; j++ {
		var p Point
		p.X.SetString(coords[2*j])
		p.Y.SetString(coords[2*j+1])
		if !p.IsOnCurve() {
			t.Fatalf("PK_%d is not on the curve", j)
		}
	}
}
