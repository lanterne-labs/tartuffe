package vote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend/polls"
)

// A field element has one spelling: upper-case hex is refused, or one
// pseudonym could vote under several strings.
func TestParseFieldLowerCaseOnly(t *testing.T) {
	lower := "0x" + strings.Repeat("ab", 32)
	if _, err := parseField(lower); err == nil {
		t.Fatal("a canonical value above the field passed") // ab… is above r
	}
	lower = "0x" + strings.Repeat("00", 31) + "2a"
	if _, err := parseField(lower); err != nil {
		t.Fatal(err)
	}
	if _, err := parseField("0x" + strings.Repeat("00", 31) + "2A"); err == nil {
		t.Fatal("upper-case hex passed")
	}
}

// A seq in the upper half of uint32 is stored and read back.
func TestBallotSeqAboveInt32(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewStore(pool)
	seq := uint32(1<<31 + 5)
	n := "0x" + strings.Repeat("00", 31) + "07"
	if err := store.AddBallot(ctx, LoggedBallot{EventID: 1, Nullifier: n, Seq: seq, PIHash: "0x01", LogSeq: 1, Checkpoint: 1, Body: []byte(`{}`), NostrID: "x", Relay: "r"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.LatestSeq(ctx, 1, n)
	if err != nil || got != seq {
		t.Fatalf("latest seq %d: %v", got, err)
	}
}

// failingPassport stands for a verifier process that cannot run.
type failingPassport struct{}

func (failingPassport) VerifyRegistration(context.Context, json.RawMessage, string, time.Time) (string, error) {
	return "", errors.New("node: not found")
}

// The acknowledgement is an obligation: it is written before it is given,
// a restart logs it at the checkpoint it named, and a server failure puts
// a message off instead of rejecting it.
func TestPendingSurvivesRestartAndFailuresDefer(t *testing.T) {
	root := "../../"
	bb, err := FindBB(root)
	if err != nil {
		t.Skip(err)
	}
	vk, err := os.ReadFile(filepath.Join(root, "circuits/ballot/out/native/vk"))
	if err != nil {
		t.Skip("no vk for the development keys:", err)
	}
	pool := testPool(t)
	ctx := context.Background()
	store := NewStore(pool)
	keys, _ := GenerateKeys(1)
	setup := Setup{V: Version, Event: 1, Version: 1, Blank: 7, NCap: 10, Mask: 255, PK: keys.PK, PKPoK: keys.PoK,
		VKHash: "0x01", Circuit: Circuit{SourceHash: "0x02"}, Relays: []Relay{{URL: "wss://a.test", State: "active"}},
		KReal: 10, RMin: 32, AnchorAge: DefaultAnchorAge, CheckpointPeriod: 20, Validity: 3600, Closes: time.Now().Add(24 * time.Hour), NostrPubkey: testNostrPubkey}
	for i := 0; i < 10; i++ {
		o := Option{Slot: i, Kind: "reserved"}
		if i < 7 {
			o = Option{Slot: i, Kind: "candidate", PersonID: int64(i + 1)}
		} else if i == 7 {
			o = Option{Slot: i, Kind: "blank"}
		}
		setup.Options = append(setup.Options, o)
	}
	body, _ := Canonical(setup)
	if _, err := store.Log.Append(ctx, KindSetup, body); err != nil {
		t.Fatal(err)
	}
	skey, _, _ := polls.GenerateLogKey()
	signer, _ := polls.NewLogSigner(skey)
	mail := &memMailbox{boxes: []string{"wss://a.test"}}
	event := &polls.Event{ID: 1, Slug: "test-vote"}
	cfg := Config{Event: event, Mail: mail, Signer: signer, Passport: failingPassport{}, BB: bb, VK: vk, NostrPubkey: testNostrPubkey}
	in, err := NewIngest(ctx, store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Two ingests at once: refused.
	if _, err := NewIngest(ctx, store, cfg); err == nil || !strings.Contains(err.Error(), "another ingest") {
		t.Fatalf("a second ingest started: %v", err)
	}
	leaf := "0x" + strings.Repeat("00", 31) + "2a"
	reg, _ := Canonical(Register{V: Version, Event: 1, ID: leaf, UID: "uid-1", Proof: json.RawMessage(`{"proofs":[],"result":{}}`), ProofDate: time.Now()})
	m := Message{ID: "reg1", Box: "wss://a.test", Kind: MsgRegister, Body: reg, Time: time.Now().Unix()}
	// The verifier cannot run: no acknowledgement, nothing pending, the
	// message waits for the next tick.
	in.Handle(ctx, m)
	if len(mail.posts["wss://a.test"]) != 0 {
		t.Fatalf("an ack was posted while the verifier failed: %+v", mail.posts["wss://a.test"])
	}
	if rows, _ := store.Pending(ctx); len(rows) != 0 {
		t.Fatalf("pending rows: %d", len(rows))
	}
	if len(in.deferred) != 1 {
		t.Fatalf("deferred: %d", len(in.deferred))
	}
	// The verifier is back: the tick handles it, the pending row exists
	// before the ack names checkpoint 1.
	in.cfg.Passport = fakePassport{uid: "uid-1"}
	in.mu.Lock()
	in.retryDeferred(ctx)
	in.mu.Unlock()
	posts := mail.posts["wss://a.test"]
	if len(posts) != 1 {
		t.Fatalf("posts: %d", len(posts))
	}
	var ack Ack
	if err := json.Unmarshal([]byte(posts[0].Body), &ack); err != nil || ack.Outcome != "logged" || ack.Checkpoint != 1 {
		t.Fatalf("ack %+v: %v", ack, err)
	}
	rows, err := store.Pending(ctx)
	if err != nil || len(rows) != 1 || rows[0].Checkpoint != 1 || rows[0].ID != leaf {
		t.Fatalf("pending rows %+v: %v", rows, err)
	}
	// A stop before the checkpoint: the next ingest restores the entry
	// and logs it at checkpoint 1, with its leaf at position 0.
	in.Release(ctx)
	in2, err := NewIngest(ctx, store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in2.Release(ctx) })
	if len(in2.pending) != 1 || !in2.pendingUID["uid-1"] {
		t.Fatalf("pending not restored: %d", len(in2.pending))
	}
	// The same registration again meanwhile is a duplicate, not a second
	// leaf.
	in2.Handle(ctx, Message{ID: "reg1-again", Box: "wss://a.test", Kind: MsgRegister, Body: reg, Time: time.Now().Unix()})
	if err := in2.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	cp, _ := store.LastCheckpoint(ctx)
	if cp == nil || cp.Index != 1 || cp.TreeSize != 1 {
		t.Fatalf("checkpoint %+v", cp)
	}
	if rows, _ := store.Pending(ctx); len(rows) != 0 {
		t.Fatalf("pending rows after the checkpoint: %d", len(rows))
	}
	h, err := store.Handled(ctx, "reg1")
	if err != nil || h == nil || h.Outcome != "logged" || h.Checkpoint != 1 {
		t.Fatalf("handled: %+v %v", h, err)
	}
	if h, _ := store.Handled(ctx, "reg1-again"); h == nil || h.Reason != ReasonDuplicate {
		t.Fatalf("repost: %+v", h)
	}
	// The cursor of the box is the newest time it delivered, not now.
	if c, _ := store.Cursor(ctx, "wss://a.test"); c != m.Time {
		t.Fatalf("cursor %d, delivered %d", c, m.Time)
	}
	// An upper-case leaf is malformed.
	upper, _ := Canonical(Register{V: Version, Event: 1, ID: leaf[:64] + "2A", UID: "uid-2", Proof: json.RawMessage(`{"proofs":[],"result":{}}`), ProofDate: time.Now()})
	in2.Handle(ctx, Message{ID: "reg-upper", Box: "wss://a.test", Kind: MsgRegister, Body: upper, Time: time.Now().Unix()})
	posts = mail.posts["wss://a.test"]
	if err := json.Unmarshal([]byte(posts[len(posts)-1].Body), &ack); err != nil || ack.Reason != ReasonMalformed {
		t.Fatalf("upper-case leaf: %+v", ack)
	}
}
