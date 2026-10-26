package vote

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"digital-mirror/backend/polls"
)

// memMailbox is a Mailbox in memory: what the ingest posted, per box.
type memMailbox struct {
	mu    sync.Mutex
	boxes []string
	posts map[string][]Outgoing
}

func (m *memMailbox) Boxes() []string { return m.boxes }
func (m *memMailbox) Read(ctx context.Context, box string, since int64, out chan<- Message) error {
	<-ctx.Done()
	return ctx.Err()
}
func (m *memMailbox) Post(ctx context.Context, box string, msg Outgoing) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.posts == nil {
		m.posts = map[string][]Outgoing{}
	}
	m.posts[box] = append(m.posts[box], msg)
	return nil
}
func (m *memMailbox) PostAll(ctx context.Context, msg Outgoing) int {
	for _, b := range m.boxes {
		_ = m.Post(ctx, b, msg)
	}
	return len(m.boxes)
}

type fakePassport struct{ uid string }

func (f fakePassport) VerifyRegistration(ctx context.Context, proof json.RawMessage, customData string) (string, error) {
	return f.uid, nil
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()
	for _, q := range []string{
		`TRUNCATE vote2_events, vote2_ballots, vote2_registrations, vote2_checkpoints, vote2_cursors`,
		`TRUNCATE vote_log, vote_log_hashes, vote_log_stamps`,
		`INSERT INTO poll_events (id, slug, title_fr, title_en, description_fr, description_en, max_choices, min_voters, opens_at, closes_at)
		 VALUES (1, 'test-vote', 't', 't', 'd', 'd', 3, 1, now(), now() + interval '1 day') ON CONFLICT (id) DO UPDATE SET closes_at = now() + interval '1 day'`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return pool
}

// The ingest on the circuit's vectors: the setup logged where the vector
// ballot names it, the vector tree registered, then the ballot is logged
// at a checkpoint and the reject reasons come out for the bad variants.
func TestIngestOnDevVectors(t *testing.T) {
	root := "../../"
	tools, err := FindTools(root)
	if err != nil {
		t.Skip(err)
	}
	vk, err := os.ReadFile(filepath.Join(root, "circuits/ballot/out/native/vk"))
	if err != nil {
		t.Skip("no vk for the development keys:", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "circuits/ballot/vectors/dev.json"))
	if err != nil {
		t.Skip(err)
	}
	var v struct {
		Tree struct {
			Leaves []string `json:"leaves"`
			Root   string   `json:"root"`
		} `json:"tree"`
		Ballot struct {
			Ciphertexts []string `json:"ciphertexts"`
			Inputs      struct {
				Root      string `json:"root"`
				Nullifier string `json:"nullifier"`
				Seq       uint32 `json:"seq"`
				Mask      uint32 `json:"mask"`
			} `json:"inputs"`
		} `json:"ballot"`
		Proof string `json:"proof"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	pool := testPool(t)
	ctx := context.Background()
	store := NewStore(pool)

	// The setup at seq 5, as the vector ballot names it.
	keys, _ := GenerateKeys(1)
	for i := 0; i < 4; i++ {
		if _, err := store.Log.Append(ctx, KindSeen, json.RawMessage(`{"v":2,"filler":true}`)); err != nil {
			t.Fatal(err)
		}
	}
	setup := Setup{V: Version, Event: 1, Version: 1, Blank: 7, NCap: 10, Mask: v.Ballot.Inputs.Mask, PK: keys.PK, PKPoK: keys.PoK,
		VKHash: "0x01", Circuit: Circuit{SourceHash: "0x02"}, Relays: []Relay{{URL: "wss://a.test", State: "active"}, {URL: "wss://b.test", State: "active"}},
		KReal: 10, RMin: 32, AnchorAge: DefaultAnchorAge, CheckpointPeriod: 20, Validity: 3600, Closes: time.Now().Add(24 * time.Hour)}
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
	seq, err := store.Log.Append(ctx, KindSetup, body)
	if err != nil || seq != 5 {
		t.Fatalf("setup at seq %d: %v", seq, err)
	}

	skey, _, _ := polls.GenerateLogKey()
	signer, _ := polls.NewLogSigner(skey)
	mail := &memMailbox{boxes: []string{"wss://a.test", "wss://b.test"}}
	event := &polls.Event{ID: 1, Slug: "test-vote"}
	mirror := &DirMirror{Dir: t.TempDir()}
	in, err := NewIngest(ctx, store, Config{Event: event, Mail: mail, Signer: signer, Passport: fakePassport{uid: "passport-1"}, BB: tools.BB, VK: vk, Mirror: mirror})
	if err != nil {
		t.Fatal(err)
	}

	// Register the vector tree's leaves, one checkpoint each so that the
	// positions follow the vector's order (a checkpoint orders its entries
	// by hash).
	for i, leaf := range v.Tree.Leaves {
		reg, _ := Canonical(Register{V: Version, Event: 1, ID: leaf, UID: "uid-" + string(rune('a'+i)), Proof: json.RawMessage(`{"proofs":[],"result":{}}`), ProofDate: time.Now()})
		in.cfg.Passport = fakePassport{uid: "uid-" + string(rune('a'+i))}
		in.Handle(ctx, Message{ID: "reg" + string(rune('a'+i)), Box: "wss://a.test", Kind: MsgRegister, Body: reg})
		if err := in.Checkpoint(ctx); err != nil {
			t.Fatal(err)
		}
	}
	cp, err := store.LastCheckpoint(ctx)
	if err != nil || cp == nil {
		t.Fatalf("no checkpoint: %v", err)
	}
	if cp.TreeSize != 5 || cp.TreeRoot != v.Tree.Root {
		t.Fatalf("tree size %d root %s, the vectors' is %s", cp.TreeSize, cp.TreeRoot, v.Tree.Root)
	}

	// The vector ballot.
	var c []string
	var c0 string
	for i := 0; i < len(v.Ballot.Ciphertexts); i += 2 {
		pt, err := pointOf(v.Ballot.Ciphertexts[i], v.Ballot.Ciphertexts[i+1])
		if err != nil {
			t.Fatal(err)
		}
		comp, _ := pt.Compress()
		if i == 0 {
			c0 = "0x" + hexOf(comp[:])
		} else {
			c = append(c, "0x"+hexOf(comp[:]))
		}
	}
	bal := Ballot{V: Version, Event: 1, Setup: 5, Root: v.Ballot.Inputs.Root, Nullifier: v.Ballot.Inputs.Nullifier, Seq: v.Ballot.Inputs.Seq, C0: c0, C: c, Proof: v.Proof}
	post := func(id string, b Ballot) *Ack {
		raw, _ := Canonical(b)
		in.Handle(ctx, Message{ID: id, Box: "wss://b.test", Kind: MsgBallot, Body: raw})
		posts := mail.posts["wss://b.test"]
		var ack Ack
		if err := json.Unmarshal([]byte(posts[len(posts)-1].Body), &ack); err != nil {
			t.Fatal(err)
		}
		if ack.NostrID != id {
			t.Fatalf("ack for %s, want %s", ack.NostrID, id)
		}
		return &ack
	}
	if ack := post("b1", bal); ack.Outcome != "logged" || ack.PIHash == "" {
		t.Fatalf("ballot: %+v", ack)
	}
	if ack := post("b1-again", bal); ack.Reason != ReasonDuplicate {
		t.Fatalf("repost: %+v", ack)
	}
	other := bal
	other.Setup = 6
	if ack := post("b2", other); ack.Reason != ReasonUnknownSetup {
		t.Fatalf("unknown setup: %+v", ack)
	}
	other = bal
	other.Root = "0x" + hexOf(make([]byte, 32))
	if ack := post("b3", other); ack.Reason != ReasonOldRoot {
		t.Fatalf("unknown root: %+v", ack)
	}
	other = bal
	other.Seq = 2
	if ack := post("b4", other); ack.Reason != ReasonBadProof {
		t.Fatalf("a changed seq must fail the proof: %+v", ack)
	}
	if err := in.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	cp, _ = store.LastCheckpoint(ctx)
	if cp.Index != 6 {
		t.Fatalf("checkpoint %d", cp.Index)
	}
	treeRoot, size, index, eventID, at, err := ParseCheckpoint(cp.Note)
	if err != nil || index != 6 || eventID != 1 || size != 5 || treeRoot == "" || at.IsZero() {
		t.Fatalf("checkpoint note: %v %d %d %d", err, index, eventID, size)
	}
	if _, err := polls.OpenCheckpoint([]byte(cp.Note), signer.VerifierKey()); err != nil {
		t.Fatal(err)
	}
	latest, err := store.LatestSeq(ctx, 1, bal.Nullifier)
	if err != nil || latest != 1 {
		t.Fatalf("latest seq %d: %v", latest, err)
	}
	h, err := store.Handled(ctx, "b4")
	if err != nil || h == nil || h.Outcome != "rejected" || h.Reason != ReasonBadProof {
		t.Fatalf("handled b4: %+v %v", h, err)
	}
	// The log holds: 4 fillers, the setup, 5 registrations, 1 ballot, 4 rejects.
	head, _ := store.Log.Head(ctx)
	if head.N != 15 {
		t.Fatalf("log size %d", head.N)
	}
	// A second ballot of the same pseudonym with a stale seq.
	if ack := post("b5", bal); ack.Reason != ReasonDuplicate {
		t.Fatalf("logged ballot reposted: %+v", ack)
	}
	// The mirror holds the files of SPEC §8.3 and the note names its commit.
	day := time.Now().UTC().Format("2006-01-02")
	hour := time.Now().UTC().Format("2006-01-02T15")
	for _, f := range []string{"leaves/" + day + ".json", "blocks/open.json", "blocks/roots.json", "entries/" + hour + ".json", "setup.json", "checkpoint.txt", "checkpoints/6.txt", "index/" + bal.Nullifier[2:4] + ".json", "positions/" + v.Tree.Leaves[0][2:3] + ".json"} {
		if b, err := mirror.Read(f); err != nil || b == nil {
			t.Fatalf("mirror file %s: %v", f, err)
		}
	}
	if !strings.Contains(cp.Note, "\nmirror dir-") {
		t.Fatalf("note without a mirror line:\n%s", cp.Note)
	}
	var leaves []MirrorLeaf
	b, _ := mirror.Read("leaves/" + day + ".json")
	if json.Unmarshal(b, &leaves) != nil || len(leaves) != 5 {
		t.Fatalf("leaves file: %s", b)
	}
	var idx map[string]uint32
	b, _ = mirror.Read("index/" + bal.Nullifier[2:4] + ".json")
	if json.Unmarshal(b, &idx) != nil || idx[bal.Nullifier] != 1 {
		t.Fatalf("index shard: %s", b)
	}

	// The auditor on the same log: the checkpoints verify, the ballot's
	// proof verifies, the rejects have their reasons.
	rep, err := Audit(ctx, StoreSource{store}, Options{EventID: 1, LogKey: signer.VerifierKey(), Verifier: in.ver})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		t.Fatalf("audit problems: %v", rep.Problems)
	}
	if rep.Checkpoints != 6 || rep.Registrations != 5 || rep.Ballots != 1 || rep.Rejects != 4 {
		t.Fatalf("audit counts: %+v", *rep)
	}
	// The close: the snapshot lists every event handled, its entry and
	// the checkpoint covering it are logged, it is posted to every box;
	// a ballot after it is rejected as closed; nothing happens twice.
	in.cfg.Now = func() time.Time { return setup.Closes.Add(time.Second) }
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}
	snap, err := store.SnapshotEntry(ctx, 1)
	if err != nil || snap == nil {
		t.Fatalf("no snapshot entry: %v", err)
	}
	ids, _ := store.HandledIDs(ctx)
	// The pending reject (b5) was flushed in checkpoint 7 before the
	// snapshot; checkpoint 8 covers the snapshot entry.
	if snap.Body.Count != len(ids) || snap.Body.Root != SnapshotRoot(ids) || snap.Body.Checkpoint != 7 {
		t.Fatalf("snapshot %+v, %d events handled", snap.Body, len(ids))
	}
	cp, _ = store.LastCheckpoint(ctx)
	if cp.Index != 8 || cp.LogSize != snap.Seq {
		t.Fatalf("checkpoint %d covers %d, the snapshot is entry %d", cp.Index, cp.LogSize, snap.Seq)
	}
	posts := mail.posts["wss://a.test"]
	if posts[len(posts)-2].Kind != OutSnapshot {
		t.Fatalf("the snapshot was not posted before the checkpoint: %v", posts[len(posts)-2].Kind)
	}
	var list SnapshotList
	if b, _ := mirror.Read("snapshot.json"); json.Unmarshal(b, &list) != nil || len(list.IDs) != len(ids) || list.Root != snap.Body.Root {
		t.Fatalf("mirror snapshot: %s", b)
	}
	if ack := post("b6", bal); ack.Reason != ReasonClosed {
		t.Fatalf("after the close: %+v", ack)
	}
	if err := in.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if again, _ := store.SnapshotEntry(ctx, 1); again.Seq != snap.Seq {
		t.Fatal("a second snapshot")
	}
	// The log grew by the reject: the next checkpoint covers it.
	if err := in.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if cp, _ = store.LastCheckpoint(ctx); cp.Index != 9 {
		t.Fatalf("checkpoint %d after the reject", cp.Index)
	}
	rep, err = Audit(ctx, StoreSource{store}, Options{EventID: 1, LogKey: signer.VerifierKey(), Verifier: in.ver})
	if err != nil || !rep.OK() {
		t.Fatalf("audit after the close: %v %v", err, rep.Problems)
	}
	// A closed entry naming another root fails the audit.
	closedBody, _ := Canonical(Closed{V: Version, Event: 1, At: setup.Closes, Final: 0, SnapshotRoot: SnapshotRoot([]string{"x"})})
	if _, err := store.Log.Append(ctx, KindClosed, closedBody); err != nil {
		t.Fatal(err)
	}
	if err := in.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	rep, _ = Audit(ctx, StoreSource{store}, Options{EventID: 1, LogKey: signer.VerifierKey()})
	if len(rep.Problems) != 1 || !strings.Contains(rep.Problems[0], "snapshot root") {
		t.Fatalf("audit problems: %v", rep.Problems)
	}
	// A tampered entry breaks the checkpoint's tree.
	if _, err := pool.Exec(ctx, `UPDATE vote_log SET body = replace(body, '"seq":1', '"seq":2') WHERE kind = 'ballot'`); err != nil {
		t.Fatal(err)
	}
	rep, _ = Audit(ctx, StoreSource{store}, Options{EventID: 1, LogKey: signer.VerifierKey()})
	if rep.OK() {
		t.Fatal("a tampered ballot passed the audit")
	}
}
