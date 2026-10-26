package vote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/rs/zerolog/log"

	"digital-mirror/backend/polls"
	"digital-mirror/backend/vote/ballot"
	"digital-mirror/backend/vote/poseidon2"
)

// PassportVerifier checks a registration's ZKPassport proof for the event:
// an adult French national's valid passport, bound to customData. It
// returns the passport's unique identifier for our domain and scope.
type PassportVerifier interface {
	VerifyRegistration(ctx context.Context, proof json.RawMessage, customData string) (uid string, err error)
}

// Config is what the ingest server runs with.
type Config struct {
	Event    *polls.Event
	Mail     Mailbox
	Signer   *polls.LogSigner
	Passport PassportVerifier
	BB       string
	VK       []byte
	Now      func() time.Time
	// Stamp timestamps a checkpoint note (OpenTimestamps); nil stamps nothing.
	Stamp func(ctx context.Context, size int64, note []byte) error
	// Mirror is the public data mirror; nil writes none.
	Mirror Mirror
	// Artifact is the compiled circuit (target/ballot.json of the setup),
	// published on the mirror for the browser.
	Artifact []byte
	// Extra are other files published once on the mirror (event.json, the
	// candidates' cards): path → content.
	Extra map[string][]byte
}

// Ingest reads the mailbox, validates registrations and ballots, buffers
// them until the next checkpoint, then logs them ordered by hash, signs
// the checkpoint and posts it (SPEC §7–8). It is the only writer of the
// log while it runs.
type Ingest struct {
	cfg    Config
	store  *Store
	setups []LoggedSetup
	ver    *Verifier
	slug   string

	mu           sync.Mutex
	tree         *ballot.Tree
	nextIndex    int64
	pending      []pendingEntry
	pendingNull  map[string]uint32 // nullifier → highest pending seq
	pendingPI    map[string]bool
	pendingUID   map[string]bool
	pendingID    map[string]bool
	closedSeq    int64 // the closed entry, 0 while open
	lastStampErr time.Time
}

type pendingEntry struct {
	kind string
	body []byte // canonical
	hash string // the ordering key: pi_hash for ballots, body hash otherwise
	msg  Message
	// For ballots and registrations.
	nullifier string
	seq       uint32
	uid, id   string
	proofDate time.Time
	reason    string
}

// NewIngest prepares the server: loads the setups, the tree and the last
// checkpoint.
func NewIngest(ctx context.Context, store *Store, cfg Config) (*Ingest, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	setups, err := store.Setups(ctx, cfg.Event.ID)
	if err != nil {
		return nil, err
	}
	if len(setups) == 0 {
		return nil, fmt.Errorf("vote: no setup logged for %s (dm vote setup)", cfg.Event.Slug)
	}
	ver, err := NewVerifier(cfg.BB, cfg.VK)
	if err != nil {
		return nil, err
	}
	tree, err := store.LoadTree(ctx, cfg.Event.ID)
	if err != nil {
		return nil, err
	}
	in := &Ingest{cfg: cfg, store: store, setups: setups, ver: ver, slug: cfg.Event.Slug, tree: tree,
		pendingNull: map[string]uint32{}, pendingPI: map[string]bool{}, pendingUID: map[string]bool{}, pendingID: map[string]bool{}}
	last, err := store.LastCheckpoint(ctx)
	if err != nil {
		return nil, err
	}
	in.nextIndex = 1
	if last != nil {
		in.nextIndex = last.Index + 1
	}
	// What the browser needs once: the circuit, the log's verifier key.
	if cfg.Mirror != nil {
		if len(cfg.Artifact) > 0 {
			if err := cfg.Mirror.Stage("circuit/ballot.json", cfg.Artifact); err != nil {
				return nil, err
			}
		}
		if err := cfg.Mirror.Stage("log-key.txt", []byte(cfg.Signer.VerifierKey()+"\n")); err != nil {
			return nil, err
		}
		for path, b := range cfg.Extra {
			if err := cfg.Mirror.Stage(path, b); err != nil {
				return nil, err
			}
		}
		if err := in.stageCurrent(ctx, cfg.Now()); err != nil {
			return nil, err
		}
		if _, err := cfg.Mirror.Commit(ctx, "setup files"); err != nil {
			return nil, err
		}
	}
	return in, nil
}

// ActiveBoxes lists the active relays of the latest setup: what the
// mailbox is built from.
func ActiveBoxes(setups []LoggedSetup) []string {
	var urls []string
	for _, r := range setups[len(setups)-1].Setup.Relays {
		if r.State == "active" {
			urls = append(urls, r.URL)
		}
	}
	return urls
}

// Setups returns the event's setups, for the mailbox's construction.
func (in *Ingest) Setups() []LoggedSetup { return in.setups }

// current is the latest setup in force at now (a later setup takes effect
// 24 h after it is logged; the first at once).
func (in *Ingest) current(now time.Time) LoggedSetup {
	cur := in.setups[0]
	for _, s := range in.setups[1:] {
		if !now.Before(s.LoggedAt.Add(24 * time.Hour)) {
			cur = s
		}
	}
	return cur
}

// setupInForce returns the setup of that seq if a ballot may name it now.
func (in *Ingest) setupInForce(seq int64, now time.Time) (LoggedSetup, bool) {
	for i, s := range in.setups {
		if s.Seq != seq {
			continue
		}
		if i == 0 || !now.Before(s.LoggedAt.Add(24*time.Hour)) {
			return s, true
		}
	}
	return LoggedSetup{}, false
}

// Run reads the mailbox and checkpoints until ctx ends.
func (in *Ingest) Run(ctx context.Context) error {
	incoming := make(chan Message, 256)
	var wg sync.WaitGroup
	for _, b := range in.cfg.Mail.Boxes() {
		wg.Add(1)
		go func(box string) {
			defer wg.Done()
			in.readBox(ctx, box, incoming)
		}(b)
	}
	period := time.Duration(in.current(in.cfg.Now()).Setup.CheckpointPeriod) * time.Second
	tick := time.NewTicker(period)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case m := <-incoming:
			in.Handle(ctx, m)
		case <-tick.C:
			if err := in.Checkpoint(ctx); err != nil {
				log.Error().Err(err).Msg("vote: checkpoint")
			}
		}
	}
}

// readBox keeps reading one box from its cursor minus a slack,
// reconnecting with a backoff.
func (in *Ingest) readBox(ctx context.Context, box string, out chan<- Message) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		since, err := in.store.Cursor(ctx, box)
		if err != nil {
			log.Error().Err(err).Str("box", box).Msg("vote: cursor")
		}
		if since > 0 {
			since -= 2 * 3600
		}
		err = in.cfg.Mail.Read(ctx, box, since, out)
		if ctx.Err() != nil {
			return
		}
		log.Warn().Err(err).Str("box", box).Dur("retry", backoff).Msg("vote: box read ended")
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

// Handle validates one message and buffers its entry, acknowledging it on
// the box it came from.
func (in *Ingest) Handle(ctx context.Context, m Message) {
	if h, err := in.store.Handled(ctx, m.ID); err != nil {
		log.Error().Err(err).Msg("vote: handled lookup")
		return
	} else if h != nil {
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	for _, p := range in.pending {
		if p.msg.ID == m.ID {
			return
		}
	}
	now := in.cfg.Now()
	var entry pendingEntry
	var reason string
	switch m.Kind {
	case MsgBallot:
		entry, reason = in.checkBallot(ctx, m.Body, now)
	case MsgRegister:
		entry, reason = in.checkRegister(ctx, m.Body, now)
	case MsgComplaint:
		entry = pendingEntry{kind: KindSeen, hash: BodyHash(m.Body)}
		entry.body, _ = Canonical(Seen{V: Version, NostrID: m.ID, Hash: entry.hash})
	default:
		return
	}
	entry.msg = m
	ack := Ack{V: Version, NostrID: m.ID, BodyHash: BodyHash(m.Body), Checkpoint: in.nextIndex, Outcome: "logged"}
	if reason != "" {
		content := m.Body
		if len(content) > 64*1024 {
			content = content[:64*1024]
		}
		rj := Reject{V: Version, Event: in.cfg.Event.ID, NostrID: m.ID, Body: json.RawMessage(content), Reason: reason}
		if !json.Valid(content) {
			rj.Body, _ = json.Marshal(string(content))
		}
		entry = pendingEntry{kind: KindReject, reason: reason, msg: m}
		entry.body, _ = Canonical(rj)
		entry.hash = BodyHash(entry.body)
		ack.Outcome, ack.Reason = "rejected", reason
	}
	switch entry.kind {
	case KindBallot:
		ack.PIHash = entry.hash
		in.pendingNull[entry.nullifier] = entry.seq
		in.pendingPI[entry.hash] = true
	case KindRegister:
		in.pendingUID[entry.uid] = true
		in.pendingID[entry.id] = true
	}
	in.pending = append(in.pending, entry)
	raw, _ := Canonical(ack)
	if err := in.cfg.Mail.Post(ctx, m.Box, Outgoing{Kind: OutAck, Body: string(raw)}); err != nil {
		log.Warn().Err(err).Msg("vote: post ack")
	}
	log.Info().Str("kind", entry.kind).Str("outcome", ack.Outcome).Str("reason", reason).Str("box", m.Box).Msg("vote: message handled")
}

// checkBallot applies SPEC §8.1 to a ballot body.
func (in *Ingest) checkBallot(ctx context.Context, content []byte, now time.Time) (pendingEntry, string) {
	p, err := ParseBallot(content)
	if err != nil {
		return pendingEntry{}, ReasonMalformed
	}
	if p.Event != in.cfg.Event.ID {
		return pendingEntry{}, ReasonMalformed
	}
	if in.closedSeq != 0 || !now.Before(in.current(now).Setup.Closes) {
		return pendingEntry{}, ReasonClosed
	}
	setup, ok := in.setupInForce(p.Setup, now)
	if !ok {
		return pendingEntry{}, ReasonUnknownSetup
	}
	at, known, err := in.store.RootAge(ctx, p.Root)
	if err != nil {
		log.Error().Err(err).Msg("vote: root age")
		return pendingEntry{}, ReasonOldRoot
	}
	if !known || now.Sub(at) > time.Duration(setup.Setup.AnchorAge)*time.Second {
		return pendingEntry{}, ReasonOldRoot
	}
	pi := p.PIHash(setup.Setup.Mask)
	if in.pendingPI[pi] {
		return pendingEntry{}, ReasonDuplicate
	}
	if dup, err := in.store.HasPIHash(ctx, pi); err != nil || dup {
		return pendingEntry{}, ReasonDuplicate
	}
	latest, err := in.store.LatestSeq(ctx, in.cfg.Event.ID, p.Nullifier)
	if err != nil {
		log.Error().Err(err).Msg("vote: latest seq")
		return pendingEntry{}, ReasonStaleSeq
	}
	if s := in.pendingNull[p.Nullifier]; s > latest {
		latest = s
	}
	if p.Seq <= latest {
		return pendingEntry{}, ReasonStaleSeq
	}
	if err := in.ver.Verify(ctx, p.ProofBytes, p.PublicInputs32(setup.Setup.Mask)); err != nil {
		if !errors.Is(err, ErrBadProof) {
			log.Error().Err(err).Msg("vote: verifier")
		}
		return pendingEntry{}, ReasonBadProof
	}
	body, _ := Canonical(p.Ballot)
	return pendingEntry{kind: KindBallot, body: body, hash: pi, nullifier: p.Nullifier, seq: p.Seq}, ""
}

// checkRegister applies SPEC §4 to a registration body.
func (in *Ingest) checkRegister(ctx context.Context, content []byte, now time.Time) (pendingEntry, string) {
	var r Register
	if err := json.Unmarshal(content, &r); err != nil || r.V != Version || r.Event != in.cfg.Event.ID || r.UID == "" || len(r.Proof) == 0 {
		return pendingEntry{}, ReasonMalformed
	}
	id, err := parseField(r.ID)
	if err != nil {
		return pendingEntry{}, ReasonMalformed
	}
	if in.closedSeq != 0 || !now.Before(in.current(now).Setup.Closes) {
		return pendingEntry{}, ReasonClosed
	}
	validity := time.Duration(in.current(now).Setup.Validity) * time.Second
	if d := now.Sub(r.ProofDate); d > validity+5*time.Minute || d < -(validity+5*time.Minute) {
		return pendingEntry{}, ReasonStaleDate
	}
	if in.pendingUID[r.UID] {
		return pendingEntry{}, ReasonUIDUsed
	}
	if used, err := in.store.HasUID(ctx, in.cfg.Event.ID, r.UID); err != nil || used {
		return pendingEntry{}, ReasonUIDUsed
	}
	if in.pendingID[r.ID] {
		return pendingEntry{}, ReasonIDUsed
	}
	if used, err := in.store.HasID(ctx, in.cfg.Event.ID, r.ID); err != nil || used {
		return pendingEntry{}, ReasonIDUsed
	}
	custom := RegistrationBinding(in.cfg.Event.ID, id)
	uid, err := in.cfg.Passport.VerifyRegistration(ctx, r.Proof, custom)
	if err != nil || uid != r.UID {
		if err != nil {
			log.Debug().Err(err).Msg("vote: registration proof refused")
		}
		return pendingEntry{}, ReasonBadProof
	}
	body, _ := Canonical(r)
	return pendingEntry{kind: KindRegister, body: body, hash: BodyHash(body), uid: r.UID, id: r.ID, proofDate: r.ProofDate}, ""
}

// RegistrationBinding is custom_data = H(T_REG, event, ID), lower-case hex.
func RegistrationBinding(eventID int64, id fr.Element) string {
	h := poseidon2.Hash(ballot.TagReg, fieldOf(eventID), id)
	b := h.Bytes()
	return "0x" + hexOf(b[:])
}

// Checkpoint logs the pending entries ordered by hash, assigns the new
// leaves their positions, signs the checkpoint with its extension lines,
// stores it, posts it to every box and stamps it. Nothing happens when
// nothing is pending.
func (in *Ingest) Checkpoint(ctx context.Context) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if len(in.pending) == 0 {
		return nil
	}
	sort.SliceStable(in.pending, func(i, j int) bool { return in.pending[i].hash < in.pending[j].hash })
	index := in.nextIndex
	for _, p := range in.pending {
		seq, err := in.store.Log.Append(ctx, p.kind, p.body)
		if err != nil {
			return fmt.Errorf("append %s: %w", p.kind, err)
		}
		h := HandledEvent{NostrID: p.msg.ID, Relay: p.msg.Box, Kind: int(p.msg.Kind), BodyHash: BodyHash(p.msg.Body), Outcome: "logged", LogSeq: seq, Checkpoint: index}
		switch p.kind {
		case KindBallot:
			h.PIHash = p.hash
			if err := in.store.AddBallot(ctx, LoggedBallot{EventID: in.cfg.Event.ID, Nullifier: p.nullifier, Seq: p.seq, PIHash: p.hash, LogSeq: seq, Checkpoint: index, Body: p.body, NostrID: p.msg.ID, Relay: p.msg.Box}); err != nil {
				return err
			}
		case KindRegister:
			leaf, _ := parseField(p.id)
			pos, err := in.tree.Append(leaf)
			if err != nil {
				return err
			}
			if err := in.store.AddRegistration(ctx, Registration{EventID: in.cfg.Event.ID, UID: p.uid, ID: p.id, Position: pos, LogSeq: seq, ProofDate: p.proofDate, NostrID: p.msg.ID, Relay: p.msg.Box}); err != nil {
				return err
			}
		case KindReject:
			h.Outcome, h.Reason = "rejected", p.reason
		}
		if err := in.store.AddHandled(ctx, h); err != nil {
			return err
		}
	}
	in.pending = in.pending[:0]
	in.pendingNull = map[string]uint32{}
	in.pendingPI = map[string]bool{}
	in.pendingUID = map[string]bool{}
	in.pendingID = map[string]bool{}

	head, err := in.store.Log.Head(ctx)
	if err != nil {
		return err
	}
	root := in.tree.Root()
	rootHex := "0x" + hexOf(root.Marshal())
	now := in.cfg.Now()
	// The mirror's files for this checkpoint go out first: the note names
	// their commit.
	var prevSize int64
	if last, err := in.store.LastCheckpoint(ctx); err == nil && last != nil {
		prevSize = last.LogSize
	}
	token, err := in.stageMirror(ctx, index, prevSize, head.N, now)
	if err != nil {
		return fmt.Errorf("mirror: %w", err)
	}
	text := polls.CheckpointText(head) +
		"tree " + rootHex + " " + strconv.Itoa(in.tree.Size()) + "\n" +
		"index " + strconv.FormatInt(index, 10) + "\n" +
		"event " + strconv.FormatInt(in.cfg.Event.ID, 10) + "\n" +
		"time " + strconv.FormatInt(now.Unix(), 10) + "\n"
	if token != "" {
		text += "mirror " + token + "\n"
	}
	msg, err := in.cfg.Signer.SignText(text)
	if err != nil {
		return err
	}
	if err := in.store.AddCheckpoint(ctx, Checkpoint{Index: index, LogSize: head.N, TreeRoot: rootHex, TreeSize: in.tree.Size(), Commit: token, Note: string(msg)}); err != nil {
		return err
	}
	in.nextIndex = index + 1
	if err := in.stageNote(index, msg); err != nil {
		log.Error().Err(err).Msg("vote: mirror note")
	}
	n := in.cfg.Mail.PostAll(ctx, Outgoing{Kind: OutCheckpoint, Body: string(msg)})
	for _, b := range in.cfg.Mail.Boxes() {
		if err := in.store.SetCursor(ctx, b, now.Unix()); err != nil {
			log.Error().Err(err).Msg("vote: cursor")
		}
	}
	if in.cfg.Stamp != nil {
		if err := in.cfg.Stamp(ctx, head.N, msg); err != nil && now.Sub(in.lastStampErr) > 10*time.Minute {
			in.lastStampErr = now
			log.Warn().Err(err).Msg("vote: OpenTimestamps")
		}
	}
	log.Info().Int64("index", index).Int64("log", head.N).Int("tree", in.tree.Size()).Int("boxes", n).Msg("vote: checkpoint")
	return nil
}

// ParseCheckpoint reads the extension lines of a checkpoint note's text.
func ParseCheckpoint(text string) (treeRoot string, treeSize int, index int64, eventID int64, at time.Time, err error) {
	for _, l := range strings.Split(text, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "tree":
			if len(f) == 3 {
				treeRoot = f[1]
				treeSize, _ = strconv.Atoi(f[2])
			}
		case "index":
			index, _ = strconv.ParseInt(f[1], 10, 64)
		case "event":
			eventID, _ = strconv.ParseInt(f[1], 10, 64)
		case "time":
			u, _ := strconv.ParseInt(f[1], 10, 64)
			at = time.Unix(u, 0).UTC()
		}
	}
	if treeRoot == "" || index == 0 {
		err = errors.New("vote: not a vote checkpoint")
	}
	return
}
