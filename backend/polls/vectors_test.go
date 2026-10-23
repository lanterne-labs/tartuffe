package polls

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"golang.org/x/mod/sumdb/tlog"
)

// vectorsFile holds proofs of Go's tlog for the browser's checks
// (frontend/src/lib/votelog.test.ts).
const vectorsFile = "../../frontend/src/lib/testdata/votelog-vectors.json"

type vectors struct {
	Entries     []LogEntry    `json:"entries"`
	Heads       []vectorHead  `json:"heads"`
	Inclusion   []vectorProof `json:"inclusion"`
	Consistency []vectorProof `json:"consistency"`
	Key         string        `json:"key"`
	Checkpoint  string        `json:"checkpoint"`
}

type vectorHead struct {
	Size int64  `json:"size"`
	Hash []byte `json:"hash"`
}

type vectorProof struct {
	Size   int64    `json:"size"`
	At     int64    `json:"at"`
	Hashes [][]byte `json:"hashes"`
}

// TestVectors rewrites the vectors (WRITE_VECTORS=1) or checks that the
// file still matches the tree.
func TestVectors(t *testing.T) {
	var v vectors
	var hashes []tlog.Hash
	r := tlog.HashReaderFunc(func(idx []int64) ([]tlog.Hash, error) {
		out := make([]tlog.Hash, len(idx))
		for i, x := range idx {
			out[i] = hashes[x]
		}
		return out, nil
	})
	for i := int64(0); i < 13; i++ {
		e := LogEntry{Seq: i + 1, Kind: LogBallot, Body: fmt.Sprintf(`{"n":%d,"s":"é<"}`, i)}
		hs, err := tlog.StoredHashes(i, LeafData(e.Seq, e.Kind, []byte(e.Body)), r)
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, hs...)
		v.Entries = append(v.Entries, e)
	}
	toBytes := func(p []tlog.Hash) [][]byte {
		out := [][]byte{}
		for _, h := range p {
			out = append(out, append([]byte(nil), h[:]...))
		}
		return out
	}
	for n := int64(1); n <= 13; n++ {
		h, err := tlog.TreeHash(n, r)
		if err != nil {
			t.Fatal(err)
		}
		v.Heads = append(v.Heads, vectorHead{Size: n, Hash: h[:]})
		for i := int64(0); i < n; i++ {
			p, err := tlog.ProveRecord(n, i, r)
			if err != nil {
				t.Fatal(err)
			}
			v.Inclusion = append(v.Inclusion, vectorProof{Size: n, At: i, Hashes: toBytes(p)})
		}
		for m := int64(1); m < n; m++ {
			p, err := tlog.ProveTree(n, m, r)
			if err != nil {
				t.Fatal(err)
			}
			v.Consistency = append(v.Consistency, vectorProof{Size: n, At: m, Hashes: toBytes(p)})
		}
	}
	if os.Getenv("WRITE_VECTORS") == "" {
		raw, err := os.ReadFile(vectorsFile)
		if err != nil {
			t.Fatal(err)
		}
		var old vectors
		if err := json.Unmarshal(raw, &old); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(old.Heads) != fmt.Sprint(v.Heads) {
			t.Fatal("the vectors do not match the tree: WRITE_VECTORS=1")
		}
		return
	}
	skey, vkey, err := GenerateLogKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewLogSigner(skey)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := tlog.TreeHash(13, r)
	msg, err := signer.Sign(LogTree{N: 13, Hash: head})
	if err != nil {
		t.Fatal(err)
	}
	v.Key, v.Checkpoint = vkey, string(msg)
	raw, _ := json.MarshalIndent(v, "", " ")
	if err := os.WriteFile(vectorsFile, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
