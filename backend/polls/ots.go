package polls

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/crypto/ripemd160"
	"golang.org/x/crypto/sha3"
)

// The OpenTimestamps proof format (opentimestamps.org, the Python client's
// serialization): a digest, then a tree of operations ending in
// attestations. A pending attestation names the calendar that holds the
// rest; once Bitcoin confirmed, the calendar serves the operations down to
// a Bitcoin attestation (a block height), and the message at that point is
// the block's Merkle root. Verifying a stamp is evaluating the operations
// and comparing with the block header.

// Attestation tags.
var (
	otsTagBitcoin = [8]byte{0x05, 0x88, 0x96, 0x0d, 0x73, 0xd7, 0x19, 0x01}
	otsTagPending = [8]byte{0x83, 0xdf, 0xe3, 0x0d, 0x2e, 0xf9, 0x0c, 0x8e}
)

// Operation codes.
const (
	otsOpSHA1      = 0x02
	otsOpRIPEMD160 = 0x03
	otsOpSHA256    = 0x08
	otsOpKeccak256 = 0x67
	otsOpAppend    = 0xf0
	otsOpPrepend   = 0xf1
	otsOpReverse   = 0xf2
	otsOpHexlify   = 0xf3
)

// OTSAttestation is a leaf of a timestamp: Bitcoin (Height) or pending at
// a calendar (URI), with the message the operations gave at that point.
type OTSAttestation struct {
	Bitcoin bool
	Height  int64
	URI     string
	Msg     []byte
}

// otsStamp is a timestamp node: the message, its attestations, and the
// operations leading to child nodes.
type otsStamp struct {
	msg          []byte
	attestations []otsAtt
	ops          []otsOp
}

type otsAtt struct {
	tag     [8]byte
	payload []byte
}

type otsOp struct {
	code  byte
	arg   []byte
	child *otsStamp
}

// OTS is a parsed .ots file.
type OTS struct {
	Digest []byte
	root   *otsStamp
}

func otsReadVarint(r *bytes.Reader) (uint64, error) {
	var v uint64
	var shift uint
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		v |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return v, nil
		}
		shift += 7
		if shift > 63 {
			return 0, errors.New("ots: varint too long")
		}
	}
}

func otsWriteVarint(w *bytes.Buffer, v uint64) {
	for v >= 0x80 {
		w.WriteByte(byte(v) | 0x80)
		v >>= 7
	}
	w.WriteByte(byte(v))
}

func otsReadVarbytes(r *bytes.Reader, max int) ([]byte, error) {
	n, err := otsReadVarint(r)
	if err != nil {
		return nil, err
	}
	if n > uint64(max) {
		return nil, fmt.Errorf("ots: %d bytes where at most %d are expected", n, max)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

func otsWriteVarbytes(w *bytes.Buffer, b []byte) {
	otsWriteVarint(w, uint64(len(b)))
	w.Write(b)
}

// otsApply evaluates an operation on a message.
func otsApply(code byte, arg, msg []byte) ([]byte, error) {
	switch code {
	case otsOpSHA1:
		h := sha1.Sum(msg)
		return h[:], nil
	case otsOpRIPEMD160:
		h := ripemd160.New()
		h.Write(msg)
		return h.Sum(nil), nil
	case otsOpSHA256:
		h := sha256.Sum256(msg)
		return h[:], nil
	case otsOpKeccak256:
		h := sha3.NewLegacyKeccak256()
		h.Write(msg)
		return h.Sum(nil), nil
	case otsOpAppend:
		return append(append([]byte{}, msg...), arg...), nil
	case otsOpPrepend:
		return append(append([]byte{}, arg...), msg...), nil
	case otsOpReverse:
		out := make([]byte, len(msg))
		for i, b := range msg {
			out[len(msg)-1-i] = b
		}
		return out, nil
	case otsOpHexlify:
		return []byte(hex.EncodeToString(msg)), nil
	}
	return nil, fmt.Errorf("ots: unknown operation 0x%02x", code)
}

// otsParseStamp reads a timestamp whose initial message is msg.
func otsParseStamp(r *bytes.Reader, msg []byte, depth int) (*otsStamp, error) {
	if depth > 256 {
		return nil, errors.New("ots: too deep")
	}
	s := &otsStamp{msg: msg}
	item := func(tag byte) error {
		if tag == 0x00 {
			var a otsAtt
			if _, err := io.ReadFull(r, a.tag[:]); err != nil {
				return err
			}
			p, err := otsReadVarbytes(r, 8192)
			if err != nil {
				return err
			}
			a.payload = p
			s.attestations = append(s.attestations, a)
			return nil
		}
		op := otsOp{code: tag}
		if tag == otsOpAppend || tag == otsOpPrepend {
			arg, err := otsReadVarbytes(r, 4096)
			if err != nil {
				return err
			}
			op.arg = arg
		}
		next, err := otsApply(op.code, op.arg, msg)
		if err != nil {
			return err
		}
		child, err := otsParseStamp(r, next, depth+1)
		if err != nil {
			return err
		}
		op.child = child
		s.ops = append(s.ops, op)
		return nil
	}
	tag, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	for tag == 0xff {
		next, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if err := item(next); err != nil {
			return nil, err
		}
		if tag, err = r.ReadByte(); err != nil {
			return nil, err
		}
	}
	if err := item(tag); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *otsStamp) write(w *bytes.Buffer) {
	n := len(s.attestations) + len(s.ops)
	i := 0
	sep := func() {
		if i < n-1 {
			w.WriteByte(0xff)
		}
		i++
	}
	for _, a := range s.attestations {
		sep()
		w.WriteByte(0x00)
		w.Write(a.tag[:])
		otsWriteVarbytes(w, a.payload)
	}
	for _, op := range s.ops {
		sep()
		w.WriteByte(op.code)
		if op.code == otsOpAppend || op.code == otsOpPrepend {
			otsWriteVarbytes(w, op.arg)
		}
		op.child.write(w)
	}
}

// ParseOTS reads a .ots file.
func ParseOTS(file []byte) (*OTS, error) {
	if !bytes.HasPrefix(file, otsHeader) {
		return nil, errors.New("ots: not an OpenTimestamps proof of a SHA-256 digest")
	}
	r := bytes.NewReader(file[len(otsHeader):])
	digest := make([]byte, 32)
	if _, err := io.ReadFull(r, digest); err != nil {
		return nil, fmt.Errorf("ots: %w", err)
	}
	root, err := otsParseStamp(r, digest, 0)
	if err != nil {
		return nil, fmt.Errorf("ots: %w", err)
	}
	if r.Len() != 0 {
		return nil, fmt.Errorf("ots: %d bytes after the timestamp", r.Len())
	}
	return &OTS{Digest: digest, root: root}, nil
}

// Bytes serializes the proof.
func (o *OTS) Bytes() []byte {
	var w bytes.Buffer
	w.Write(otsHeader)
	w.Write(o.Digest)
	o.root.write(&w)
	return w.Bytes()
}

// Attestations lists the proof's attestations with the message at each.
func (o *OTS) Attestations() []OTSAttestation {
	var out []OTSAttestation
	var walk func(s *otsStamp)
	walk = func(s *otsStamp) {
		for _, a := range s.attestations {
			at := OTSAttestation{Msg: s.msg}
			switch a.tag {
			case otsTagBitcoin:
				h, err := otsReadVarint(bytes.NewReader(a.payload))
				if err != nil {
					continue
				}
				at.Bitcoin, at.Height = true, int64(h)
			case otsTagPending:
				uri, err := otsReadVarbytes(bytes.NewReader(a.payload), 1000)
				if err != nil {
					continue
				}
				at.URI = string(uri)
			default:
				continue
			}
			out = append(out, at)
		}
		for _, op := range s.ops {
			walk(op.child)
		}
	}
	walk(o.root)
	return out
}

// Upgrade asks the calendars of the pending attestations for the rest of
// the proof and splices it in; it reports whether anything changed. A
// calendar that has nothing new yet (Bitcoin not confirmed) is left as it
// is.
func (o *OTS) Upgrade(ctx context.Context, client *http.Client) (bool, error) {
	changed := false
	var errs []error
	var walk func(s *otsStamp)
	walk = func(s *otsStamp) {
		// Each pending attestation is asked on its own; a calendar's
		// continuation (operations down to a Bitcoin attestation) joins
		// the node in its place, and the other branches stay.
		var kept []otsAtt
		var spliced []otsOp
		for _, a := range s.attestations {
			if a.tag != otsTagPending {
				kept = append(kept, a)
				continue
			}
			uri, err := otsReadVarbytes(bytes.NewReader(a.payload), 1000)
			if err != nil {
				kept = append(kept, a)
				continue
			}
			more, err := otsFetchUpgrade(ctx, client, string(uri), s.msg)
			if err != nil {
				errs = append(errs, err)
				kept = append(kept, a)
				continue
			}
			if more == nil {
				kept = append(kept, a)
				continue
			}
			kept = append(kept, more.attestations...)
			spliced = append(spliced, more.ops...)
			changed = true
		}
		s.attestations = kept
		existing := s.ops
		s.ops = append(existing, spliced...)
		for _, op := range existing {
			walk(op.child)
		}
	}
	walk(o.root)
	return changed, errors.Join(errs...)
}

// otsFetchUpgrade asks a calendar for the timestamp of a commitment; nil
// when it has nothing more than the pending attestation.
func otsFetchUpgrade(ctx context.Context, client *http.Client, uri string, commitment []byte) (*otsStamp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(uri, "/")+"/timestamp/"+hex.EncodeToString(commitment), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.opentimestamps.v1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", uri, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 100_000))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", uri, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // not yet
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", uri, resp.StatusCode)
	}
	s, err := otsParseStamp(bytes.NewReader(body), commitment, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", uri, err)
	}
	// Still pending there: nothing to splice.
	bitcoin := false
	for _, a := range (&OTS{Digest: commitment, root: s}).Attestations() {
		if a.Bitcoin {
			bitcoin = true
		}
	}
	if !bitcoin {
		return nil, nil
	}
	return s, nil
}

// BlockRoots gives the Merkle root of a Bitcoin block by height, as the
// block header holds it (internal byte order).
type BlockRoots interface {
	MerkleRoot(ctx context.Context, height int64) ([]byte, error)
}

// VerifyOTS checks a proof of the digest: it returns the heights of the
// Bitcoin blocks that attest it (each checked against the block's Merkle
// root when roots is not nil), and the calendars still pending.
func VerifyOTS(ctx context.Context, file []byte, digest [32]byte, roots BlockRoots) (heights []int64, pending []string, err error) {
	o, err := ParseOTS(file)
	if err != nil {
		return nil, nil, err
	}
	if !bytes.Equal(o.Digest, digest[:]) {
		return nil, nil, errors.New("ots: the proof is of another digest")
	}
	for _, a := range o.Attestations() {
		if !a.Bitcoin {
			pending = append(pending, a.URI)
			continue
		}
		if roots != nil {
			root, err := roots.MerkleRoot(ctx, a.Height)
			if err != nil {
				return heights, pending, fmt.Errorf("block %d: %w", a.Height, err)
			}
			if !bytes.Equal(root, a.Msg) {
				return heights, pending, fmt.Errorf("block %d: the proof leads to %x, the block's Merkle root is %x", a.Height, a.Msg, root)
			}
		}
		heights = append(heights, a.Height)
	}
	return heights, pending, nil
}

// ExplorerRoots reads block headers from public Esplora instances
// (blockstream.info, mempool.space): a read, no account.
type ExplorerRoots struct {
	Client *http.Client
	Bases  []string
	cache  map[int64][]byte
}

// NewExplorerRoots returns the public instances.
func NewExplorerRoots(client *http.Client) *ExplorerRoots {
	if client == nil {
		client = http.DefaultClient
	}
	return &ExplorerRoots{Client: client, Bases: []string{"https://blockstream.info/api", "https://mempool.space/api"}, cache: map[int64][]byte{}}
}

// MerkleRoot implements BlockRoots.
func (e *ExplorerRoots) MerkleRoot(ctx context.Context, height int64) ([]byte, error) {
	if r, ok := e.cache[height]; ok {
		return r, nil
	}
	var errs []error
	for _, base := range e.Bases {
		hash, err := e.get(ctx, fmt.Sprintf("%s/block-height/%d", base, height))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		header, err := e.get(ctx, fmt.Sprintf("%s/block/%s/header", base, strings.TrimSpace(string(hash))))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		raw, err := hex.DecodeString(strings.TrimSpace(string(header)))
		if err != nil || len(raw) != 80 {
			errs = append(errs, fmt.Errorf("%s: a block header of %d bytes", base, len(raw)))
			continue
		}
		// version(4) prev(32) merkle(32) time(4) bits(4) nonce(4).
		root := append([]byte{}, raw[36:68]...)
		e.cache[height] = root
		return root, nil
	}
	return nil, errors.Join(errs...)
}

func (e *ExplorerRoots) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10_000))
}
