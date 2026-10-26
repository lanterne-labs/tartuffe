package ballot

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"digital-mirror/backend/vote/grumpkin"
)

// The vectors written by circuits/ballot/scripts/vectors.mjs.
type vectors struct {
	Poseidon2 struct {
		Inputs []string `json:"inputs"`
		Hash   string   `json:"hash"`
	} `json:"poseidon2"`
	Tags map[string]string `json:"tags"`
	Tree struct {
		Leaves []string `json:"leaves"`
		Index  int      `json:"index"`
		Root   string   `json:"root"`
		Path   []string `json:"path"`
	} `json:"tree"`
	Ballot struct {
		Inputs struct {
			Event     string   `json:"event"`
			Mask      uint32   `json:"mask"`
			Root      string   `json:"root"`
			Nullifier string   `json:"nullifier"`
			S         string   `json:"s"`
			Index     int      `json:"index"`
			Slots     [3]int   `json:"slots"`
			Len       int      `json:"len"`
			R         string   `json:"r"`
			Path      []string `json:"path"`
		} `json:"inputs"`
		Digits      []string `json:"digits"`
		Packed      []string `json:"packed"`
		Ciphertexts []string `json:"ciphertexts"`
	} `json:"ballot"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile("../../../circuits/ballot/vectors/dev.json")
	if err != nil {
		t.Skip("no vectors:", err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func el(s string) fr.Element {
	var e fr.Element
	if _, err := e.SetString(s); err != nil {
		panic(err)
	}
	return e
}

func bigOf(s string) *big.Int {
	x, ok := new(big.Int).SetString(s, 0)
	if !ok {
		panic(s)
	}
	return x
}

func devKeys(t *testing.T) PublicKeys {
	t.Helper()
	b, err := os.ReadFile("../../../circuits/ballot/dev-keys.txt")
	if err != nil {
		t.Skip("no development keys:", err)
	}
	var pk PublicKeys
	var coords []string
	for _, l := range splitLines(string(b)) {
		if len(l) > 2 && l[:2] == "0x" {
			coords = append(coords, l)
		}
	}
	for j := 0; j < J; j++ {
		pk[j] = grumpkin.Point{X: el(coords[2*j]), Y: el(coords[2*j+1])}
	}
	return pk
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == '\n' {
			out = append(out, cur)
			cur = ""
		} else if c != '\r' && c != ' ' {
			cur += string(c)
		}
	}
	return append(out, cur)
}

func TestTagsMatchTheCircuit(t *testing.T) {
	v := loadVectors(t)
	for name, want := range map[string]fr.Element{"T_ID": TagID, "T_NULL": TagNull, "T_NODE": TagNode} {
		if got := el(v.Tags[name]); !got.Equal(&want) {
			t.Fatalf("%s differs from the circuit", name)
		}
	}
}

func TestTreeMatchesTheCircuit(t *testing.T) {
	v := loadVectors(t)
	tree := NewTree()
	for _, l := range v.Tree.Leaves {
		if _, err := tree.Append(el(l)); err != nil {
			t.Fatal(err)
		}
	}
	root := tree.Root()
	if want := el(v.Tree.Root); !root.Equal(&want) {
		t.Fatal("root differs from the vectors")
	}
	path, err := tree.Path(v.Tree.Index)
	if err != nil {
		t.Fatal(err)
	}
	for i := range path {
		if want := el(v.Tree.Path[i]); !path[i].Equal(&want) {
			t.Fatalf("sibling %d differs", i)
		}
	}
	leaf := el(v.Tree.Leaves[v.Tree.Index])
	if r := RootFromPath(leaf, path, v.Tree.Index); !r.Equal(&root) {
		t.Fatal("RootFromPath differs from Root")
	}
}

func TestIdentityMatchesTheCircuit(t *testing.T) {
	v := loadVectors(t)
	event, s := el(v.Ballot.Inputs.Event), el(v.Ballot.Inputs.S)
	if id, want := ID(event, s), el(v.Tree.Leaves[v.Ballot.Inputs.Index]); !id.Equal(&want) {
		t.Fatal("ID differs")
	}
	if n, want := Nullifier(event, s), el(v.Ballot.Inputs.Nullifier); !n.Equal(&want) {
		t.Fatal("nullifier differs")
	}
}

func TestValuesAndPackingMatchTheCircuit(t *testing.T) {
	v := loadVectors(t)
	r := Ranking{Len: v.Ballot.Inputs.Len, Slots: v.Ballot.Inputs.Slots}
	if err := r.Validate(v.Ballot.Inputs.Mask); err != nil {
		t.Fatal(err)
	}
	vals := r.Values()
	for i, d := range v.Ballot.Digits {
		if bigOf(d).Int64() != int64(vals[i])+1 {
			t.Fatalf("digit %d: %s, want %d", i, d, vals[i]+1)
		}
	}
	p := Pack(vals)
	for j := range p {
		if p[j].Cmp(bigOf(v.Ballot.Packed[j])) != 0 {
			t.Fatalf("plaintext %d differs", j)
		}
	}
	u, err := Unpack(p, 1)
	if err != nil {
		t.Fatal(err)
	}
	back, err := FromValues(u)
	if err != nil || back != r {
		t.Fatalf("FromValues: %v, %v", back, err)
	}
}

func TestEncryptionMatchesTheCircuit(t *testing.T) {
	v := loadVectors(t)
	pk := devKeys(t)
	r := Ranking{Len: v.Ballot.Inputs.Len, Slots: v.Ballot.Inputs.Slots}
	ct, err := Encrypt(pk, Pack(r.Values()), bigOf(v.Ballot.Inputs.R))
	if err != nil {
		t.Fatal(err)
	}
	got := v.Ballot.Ciphertexts
	check := func(i int, p grumpkin.Point) {
		if x, y := el(got[2*i]), el(got[2*i+1]); !p.X.Equal(&x) || !p.Y.Equal(&y) {
			t.Fatalf("ciphertext %d differs from the circuit", i)
		}
	}
	check(0, ct.C0)
	for j := 0; j < J; j++ {
		check(1+j, ct.C[j])
	}
}

func TestDecryptSumAndProofs(t *testing.T) {
	sk, pk, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	event := el("1")
	rankings := []Ranking{
		{Len: 3, Slots: [3]int{4, 2, Blank}},
		{Len: 1, Slots: [3]int{4}},
		{Len: 0},
		{Len: 2, Slots: [3]int{0, 4}},
	}
	var cts []Ciphertext
	var total [V]int64
	for i, r := range rankings {
		if err := r.Validate(255); err != nil {
			t.Fatal(err)
		}
		rb, _ := grumpkin.RandomScalar()
		ct, err := Encrypt(pk, Pack(r.Values()), rb)
		if err != nil {
			t.Fatal(err)
		}
		if err := ct.Valid(); err != nil {
			t.Fatal(err)
		}
		vals, err := DecryptBallot(sk, ct)
		if err != nil {
			t.Fatalf("ballot %d: %v", i, err)
		}
		back, err := FromValues(vals)
		if err != nil || back != r {
			t.Fatalf("ballot %d decrypts to %v: %v", i, back, err)
		}
		for k := range total {
			total[k] += vals[k]
		}
		cts = append(cts, ct)
	}
	// The sum: voters 3, first[4] = 2, first[0] = 1, margin(0,4): +1 −1 +0 = 0 …
	if total[0] != 3 || total[1+4] != 2 || total[1+0] != 1 {
		t.Fatalf("unexpected totals %v", total[:1+N])
	}
	s := Sum(cts)
	// The tallier knows P_j from the per-ballot decryptions.
	var P [J]*big.Int
	count := int64(len(cts))
	for j := 0; j < J; j++ {
		P[j] = new(big.Int)
		w := big.NewInt(1)
		for k := 0; k < PerCT; k++ {
			idx := PerCT*j + k
			d := count
			if idx < V {
				d = total[idx] + count
			}
			P[j].Add(P[j], new(big.Int).Mul(big.NewInt(d), w))
			w = new(big.Int).Mul(w, Digit)
		}
	}
	if _, err := DecryptSum(sk, s, P); err != nil {
		t.Fatal(err)
	}
	u, err := Unpack(P, count)
	if err != nil {
		t.Fatal(err)
	}
	if u != total {
		t.Fatal("Unpack of the sum differs from the summed values")
	}
	for j := 0; j < J; j++ {
		pr := ProveDLEQ(sk[j], pk[j], event, 42, j, s.C0, s.C[j], P[j])
		if err := VerifyDLEQ(pr, pk[j], event, 42, j, s.C0, s.C[j], P[j]); err != nil {
			t.Fatalf("dleq %d: %v", j, err)
		}
		wrong := new(big.Int).Add(P[j], big.NewInt(1))
		if VerifyDLEQ(pr, pk[j], event, 42, j, s.C0, s.C[j], wrong) == nil {
			t.Fatalf("dleq %d accepts a wrong plaintext", j)
		}
		if VerifyDLEQ(pr, pk[j], event, 43, j, s.C0, s.C[j], P[j]) == nil {
			t.Fatalf("dleq %d accepts another upto", j)
		}
		pok, err := ProvePoK(sk[j], event, j)
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyPoK(pok, pk[j], event, j); err != nil {
			t.Fatalf("pok %d: %v", j, err)
		}
		if VerifyPoK(pok, pk[(j+1)%J], event, j) == nil {
			t.Fatalf("pok %d accepts another key", j)
		}
	}
}

func TestRankingRules(t *testing.T) {
	bad := []Ranking{
		{Len: 2, Slots: [3]int{Blank, 1}},
		{Len: 2, Slots: [3]int{3, 3}},
		{Len: 1, Slots: [3]int{8}},
		{Len: 1, Slots: [3]int{3, 5}},
		{Len: 4},
	}
	for _, r := range bad {
		if r.Validate(255) == nil {
			t.Fatalf("%v accepted", r)
		}
	}
	if (Ranking{Len: 1, Slots: [3]int{Blank}}).Validate(255) != nil {
		t.Fatal("blank alone refused")
	}
	// A forged value vector: two first choices.
	v := (Ranking{Len: 1, Slots: [3]int{2}}).Values()
	var forged [V]int64
	for i := range v {
		forged[i] = int64(v[i])
	}
	forged[1+3] = 1
	if _, err := FromValues(forged); err == nil {
		t.Fatal("two first choices accepted")
	}
}
