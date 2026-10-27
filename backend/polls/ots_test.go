package polls

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A hand-made proof: digest → append "ab" → sha256 → bitcoin block 7, and
// a second branch pending at a calendar; parsing, evaluation, serialization
// and the upgrade splice.
func TestOTSParseVerifyUpgrade(t *testing.T) {
	digest := sha256.Sum256([]byte("checkpoint"))
	cal := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// The continuation of the commitment: prepend "cd" → sha256 → block 9.
		var c bytes.Buffer
		c.WriteByte(otsOpPrepend)
		otsWriteVarbytes(&c, []byte("cd"))
		c.WriteByte(otsOpSHA256)
		c.WriteByte(0x00)
		c.Write(otsTagBitcoin[:])
		var p bytes.Buffer
		otsWriteVarint(&p, 9)
		otsWriteVarbytes(&c, p.Bytes())
		rw.Write(c.Bytes())
	}))
	defer cal.Close()
	// A proof pending at the calendar given (branch 1, 0xff first) with a
	// second branch (last): append "ab", sha256, bitcoin 7.
	build := func(calendar string) []byte {
		var w bytes.Buffer
		w.Write(otsHeader)
		w.Write(digest[:])
		w.WriteByte(0xff)
		w.WriteByte(0x00)
		w.Write(otsTagPending[:])
		var uri bytes.Buffer
		otsWriteVarbytes(&uri, []byte(calendar))
		otsWriteVarbytes(&w, uri.Bytes())
		w.WriteByte(otsOpAppend)
		otsWriteVarbytes(&w, []byte("ab"))
		w.WriteByte(otsOpSHA256)
		w.WriteByte(0x00)
		w.Write(otsTagBitcoin[:])
		var h bytes.Buffer
		otsWriteVarint(&h, 7)
		otsWriteVarbytes(&w, h.Bytes())
		return w.Bytes()
	}
	file := build(cal.URL)

	o, err := ParseOTS(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(o.Bytes(), file) {
		t.Fatal("serialization differs")
	}
	want := sha256.Sum256(append(append([]byte{}, digest[:]...), []byte("ab")...))
	roots := fakeRoots{7: want[:]}
	heights, pending, err := VerifyOTS(context.Background(), file, digest, roots)
	if err != nil || len(heights) != 1 || heights[0] != 7 || len(pending) != 1 || pending[0] != cal.URL {
		t.Fatalf("verify: %v %v %v", heights, pending, err)
	}
	wrong := fakeRoots{7: []byte("no")}
	if _, _, err := VerifyOTS(context.Background(), file, digest, wrong); err == nil || !strings.Contains(err.Error(), "Merkle root") {
		t.Fatalf("a wrong root passed: %v", err)
	}
	other := sha256.Sum256([]byte("other"))
	if _, _, err := VerifyOTS(context.Background(), file, other, roots); err == nil {
		t.Fatal("another digest passed")
	}
	// The upgrade: the pending branch becomes prepend → sha256 → block 9.
	changed, err := o.Upgrade(context.Background(), cal.Client())
	if err != nil || !changed {
		t.Fatalf("upgrade: %v %v", changed, err)
	}
	want9 := sha256.Sum256(append([]byte("cd"), digest[:]...))
	roots[9] = want9[:]
	heights, pending, err = VerifyOTS(context.Background(), o.Bytes(), digest, roots)
	if err != nil || len(heights) != 2 || len(pending) != 0 {
		t.Fatalf("after upgrade: %v %v %v", heights, pending, err)
	}
	// A calendar with nothing yet leaves the stamp as it is.
	none := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(http.StatusNotFound) }))
	defer none.Close()
	o2, _ := ParseOTS(build(none.URL))
	if changed, err := o2.Upgrade(context.Background(), none.Client()); err != nil || changed {
		t.Fatalf("a 404 changed the stamp: %v %v", changed, err)
	}
}

type fakeRoots map[int64][]byte

func (f fakeRoots) MerkleRoot(ctx context.Context, height int64) ([]byte, error) {
	r, ok := f[height]
	if !ok {
		return nil, context.Canceled
	}
	return r, nil
}
