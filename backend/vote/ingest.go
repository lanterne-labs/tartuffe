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

	"digital-mirror/backend/accounts/providers"
	"digital-mirror/backend/polls"
	"digital-mirror/backend/vote/ballot"
	"digital-mirror/backend/vote/poseidon2"
)

// PassportVerifier checks a registration's ZKPassport proof for the event:
// an adult French national's passport, not expired at proofDate, bound to
// customData. It returns the passport's unique identifier for our domain
// and scope.
type PassportVerifier interface {
	VerifyRegistration(ctx context.Context, proof json.RawMessage, customData string, proofDate time.Time) (uid string, err error)
}

// Config is what the ingest server runs with.
type Config struct {
	Event    *polls.Event
	Mail     Mailbox
	Signer   *polls.LogSigner
	Passport PassportVerifier
	// NostrPubkey is the key the mailbox signs with: the setup must name
	// it (nostr_pubkey), or every acknowledgement on the relays passes for
	// the server's.
	NostrPubkey string
	BB          string
	VK          []byte
	Now         func() time.Time
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
	snapshotSeq  int64 // the close snapshot's entry, 0 before the close
	lastStampErr time.Time
	lastUpgrade  time.Time
	lease        *Lease
	deferred     []Message            // put off by an infrastructure failure
	deferredAt   map[string]time.Time // when each was first put off
	unacked      []unacked            // acknowledgements a box refused
	seen         map[string]int64     // box → newest created_at delivered
	// The readers of the boxes, started by Run and by a later setup.
	incoming chan Message
	readers  sync.WaitGroup
	reading  map[string]bool
	runCtx   context.Context
}

type unacked struct {
	box, body string
	since     time.Time
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
	for _, s := range setups {
		if s.Setup.NostrPubkey != "" && cfg.NostrPubkey != "" && s.Setup.NostrPubkey != cfg.NostrPubkey {
			return nil, fmt.Errorf("vote: the setup at seq %d names the server key %s, this server signs with %s", s.Seq, short(s.Setup.NostrPubkey), short(cfg.NostrPubkey))
		}
		if s.Setup.NostrPubkey == "" {
			log.Warn().Int64("seq", s.Seq).Msg("vote: the setup names no nostr_pubkey: any acknowledgement on the relays passes for ours")
		}
	}
	lease, err := store.Acquire(ctx, leaseIngest, "ingest")
	if err != nil {
		return nil, err
	}
	in := &Ingest{cfg: cfg, store: store, setups: setups, ver: ver, slug: cfg.Event.Slug, tree: tree, lease: lease,
		pendingNull: map[string]uint32{}, pendingPI: map[string]bool{}, pendingUID: map[string]bool{}, pendingID: map[string]bool{},
		deferredAt: map[string]time.Time{}, seen: map[string]int64{}}
	last, err := store.LastCheckpoint(ctx)
	if err != nil {
		lease.Release(ctx)
		return nil, err
	}
	in.nextIndex = 1
	if last != nil {
		in.nextIndex = last.Index + 1
	}
	// What was acknowledged and not logged before a stop: logged at the
	// next checkpoint, the one the acknowledgements named.
	rows, err := store.Pending(ctx)
	if err != nil {
		lease.Release(ctx)
		return nil, err
	}
	for _, r := range rows {
		if r.Checkpoint != in.nextIndex {
			log.Error().Str("id", r.Msg.ID).Int64("promised", r.Checkpoint).Int64("next", in.nextIndex).Msg("vote: a pending entry was promised another checkpoint; logged at the next one")
		}
		in.buffer(entryOf(r))
	}
	if len(rows) > 0 {
		log.Info().Int("entries", len(rows)).Int64("checkpoint", in.nextIndex).Msg("vote: pending entries restored")
	}
	if c, err := store.ClosedEntrySigned(ctx, cfg.Event.ID, setups[len(setups)-1].Setup.TallyPubkey); err != nil {
		return nil, err
	} else if c != nil {
		in.closedSeq = c.Final
	}
	if s, err := store.SnapshotEntry(ctx, cfg.Event.ID); err != nil {
		return nil, err
	} else if s != nil {
		in.snapshotSeq = s.Seq
		// Posted again: a stop between its logging and its posting would
		// have kept it from the relays (the browsers take it once).
		if body, err := Canonical(s.Body); err == nil {
			cfg.Mail.PostAll(ctx, Outgoing{Kind: OutSnapshot, Body: string(body)})
		}
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

// ActiveBoxes lists the relays active in any setup of the event: what the
// mailbox is built from. A later setup takes effect 24 h after it is
// logged (SPEC §11), and browsers on the older one post to its relays
// until then: every relay still in force is read.
func ActiveBoxes(setups []LoggedSetup) []string {
	var urls []string
	seen := map[string]bool{}
	for _, s := range setups {
		for _, r := range s.Setup.Relays {
			if r.State == "active" && !seen[r.URL] {
				seen[r.URL] = true
				urls = append(urls, r.URL)
			}
		}
	}
	return urls
}

// Release gives the writer's lease back (Run does it on exit).
func (in *Ingest) Release(ctx context.Context) { in.lease.Release(ctx) }

// startReader reads a box from Run's context (under the lock, or before
// Run's loop).
func (in *Ingest) startReader(box string) {
	if in.runCtx == nil || in.reading[box] {
		return
	}
	in.reading[box] = true
	in.readers.Add(1)
	go func() {
		defer in.readers.Done()
		in.readBox(in.runCtx, box, in.incoming)
	}()
}

// reloadSetups takes a setup logged while running into account: its
// relays are read from now on (browsers on the older setup keep posting
// to the older relays, which stay read).
func (in *Ingest) reloadSetups(ctx context.Context) {
	setups, err := in.store.Setups(ctx, in.cfg.Event.ID)
	if err != nil {
		log.Error().Err(err).Msg("vote: setups")
		return
	}
	if len(setups) <= len(in.setups) {
		return
	}
	boxes := map[string]bool{}
	for _, b := range in.cfg.Mail.Boxes() {
		boxes[b] = true
	}
	in.setups = setups
	for _, r := range setups[len(setups)-1].Setup.Relays {
		if r.State == "active" && !boxes[r.URL] {
			in.cfg.Mail.AddBox(r.URL)
			in.startReader(r.URL)
			log.Info().Str("relay", r.URL).Msg("vote: reading the relay of a new setup")
		}
	}
	log.Info().Int("setups", len(setups)).Msg("vote: a new setup was logged")
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
	in.incoming = make(chan Message, 256)
	in.reading = map[string]bool{}
	in.runCtx = ctx
	for _, b := range in.cfg.Mail.Boxes() {
		in.startReader(b)
	}
	period := time.Duration(in.current(in.cfg.Now()).Setup.CheckpointPeriod) * time.Second
	tick := time.NewTicker(period)
	defer tick.Stop()
	defer in.lease.Release(context.Background())
	for {
		select {
		case <-ctx.Done():
			in.readers.Wait()
			return nil
		case m := <-in.incoming:
			in.Handle(ctx, m)
		case <-tick.C:
			in.Tick(ctx)
		}
	}
}

// Tick is what a checkpoint period ends with: the setups reloaded, the
// messages put off tried again, the acknowledgements a box refused posted
// again, then the checkpoint and, after the close, the snapshot.
func (in *Ingest) Tick(ctx context.Context) {
	in.mu.Lock()
	in.reloadSetups(ctx)
	in.retryDeferred(ctx)
	in.repostAcks(ctx)
	err := in.checkpoint(ctx)
	in.mu.Unlock()
	// The Bitcoin part of the stamps, once the calendars have it.
	if now := in.cfg.Now(); in.cfg.Stamp != nil && now.Sub(in.lastUpgrade) > time.Hour {
		in.lastUpgrade = now
		if n, err := in.store.Log.UpgradeStamps(ctx); err != nil {
			log.Warn().Err(err).Int("upgraded", n).Msg("vote: OpenTimestamps upgrade")
		} else if n > 0 {
			log.Info().Int("upgraded", n).Msg("vote: stamps now attested in Bitcoin")
		}
	}
	if err != nil {
		log.Error().Err(err).Msg("vote: checkpoint")
	}
	if err := in.Close(ctx); err != nil {
		log.Error().Err(err).Msg("vote: close")
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
	in.mu.Lock()
	defer in.mu.Unlock()
	if m.Time > in.seen[m.Box] {
		in.seen[m.Box] = m.Time
	}
	in.handle(ctx, m)
}

// handle judges one message under the lock. An infrastructure failure (the
// database, the verifier process) puts the message off to the next tick:
// a signed outcome must be the body's own, re-checkable by anyone from the
// log (SPEC §8.1), never the server's hiccup.
func (in *Ingest) handle(ctx context.Context, m Message) {
	if h, err := in.store.Handled(ctx, m.ID); err != nil {
		log.Error().Err(err).Msg("vote: handled lookup")
		in.putOff(m)
		return
	} else if h != nil {
		return
	}
	for _, p := range in.pending {
		if p.msg.ID == m.ID {
			return
		}
	}
	now := in.cfg.Now()
	var entry pendingEntry
	var reason string
	var err error
	switch m.Kind {
	case MsgBallot:
		entry, reason, err = in.checkBallot(ctx, m.Body, now)
	case MsgRegister:
		entry, reason, err = in.checkRegister(ctx, m.Body, now)
	case MsgComplaint:
		entry = pendingEntry{kind: KindSeen, hash: BodyHash(m.Body)}
		entry.body, _ = Canonical(Seen{V: Version, NostrID: m.ID, Hash: entry.hash})
	default:
		return
	}
	if err != nil {
		log.Error().Err(err).Str("box", m.Box).Str("id", m.ID).Msg("vote: message put off to the next tick")
		in.putOff(m)
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
	if entry.kind == KindBallot {
		ack.PIHash = entry.hash
	}
	// The obligation is written before it is given (SPEC §7.2): a restart
	// logs the entry at the checkpoint the acknowledgement names.
	if err := in.store.AddPending(ctx, entry.row(in.nextIndex)); err != nil {
		log.Error().Err(err).Msg("vote: pending row")
		in.putOff(m)
		return
	}
	in.buffer(entry)
	in.postAck(ctx, m.Box, ack)
	log.Info().Str("kind", entry.kind).Str("outcome", ack.Outcome).Str("reason", reason).Str("box", m.Box).Msg("vote: message handled")
}

// putOff keeps a message for another attempt at the next tick, up to an
// hour: after that the relay's redelivery at a restart is the way back.
func (in *Ingest) putOff(m Message) {
	if _, dup := in.deferredAt[m.ID]; dup {
		return
	}
	in.deferredAt[m.ID] = in.cfg.Now()
	in.deferred = append(in.deferred, m)
}

// retryDeferred handles the messages put off, under the lock.
func (in *Ingest) retryDeferred(ctx context.Context) {
	if len(in.deferred) == 0 {
		return
	}
	msgs := in.deferred
	in.deferred = nil
	now := in.cfg.Now()
	for _, m := range msgs {
		since := in.deferredAt[m.ID]
		delete(in.deferredAt, m.ID)
		if now.Sub(since) > time.Hour {
			log.Error().Str("id", m.ID).Str("box", m.Box).Msg("vote: message given up after an hour of failures")
			continue
		}
		in.handle(ctx, m)
		if _, again := in.deferredAt[m.ID]; again {
			in.deferredAt[m.ID] = since
		}
	}
}

// buffer adds an entry to the pending set and its lookups.
func (in *Ingest) buffer(entry pendingEntry) {
	switch entry.kind {
	case KindBallot:
		if entry.seq > in.pendingNull[entry.nullifier] {
			in.pendingNull[entry.nullifier] = entry.seq
		}
		in.pendingPI[entry.hash] = true
	case KindRegister:
		in.pendingUID[entry.uid] = true
		in.pendingID[entry.id] = true
	}
	in.pending = append(in.pending, entry)
}

// postAck posts an acknowledgement; a box that refuses it gets it again at
// each tick for an hour (its content does not change: the checkpoint it
// names is the one the entry is logged at).
func (in *Ingest) postAck(ctx context.Context, box string, ack Ack) {
	raw, _ := Canonical(ack)
	if err := in.cfg.Mail.Post(ctx, box, Outgoing{Kind: OutAck, Body: string(raw)}); err != nil {
		log.Warn().Err(err).Str("box", box).Msg("vote: post ack (kept for the next tick)")
		in.unacked = append(in.unacked, unacked{box: box, body: string(raw), since: in.cfg.Now()})
	}
}

// repostAcks tries the acknowledgements a box refused.
func (in *Ingest) repostAcks(ctx context.Context) {
	if len(in.unacked) == 0 {
		return
	}
	list := in.unacked
	in.unacked = nil
	now := in.cfg.Now()
	for _, u := range list {
		if now.Sub(u.since) > time.Hour {
			log.Error().Str("box", u.box).Msg("vote: an acknowledgement could not be posted for an hour")
			continue
		}
		if err := in.cfg.Mail.Post(ctx, u.box, Outgoing{Kind: OutAck, Body: u.body}); err != nil {
			in.unacked = append(in.unacked, u)
		}
	}
}

// row is the pending entry as stored before its acknowledgement.
func (p pendingEntry) row(index int64) PendingRow {
	return PendingRow{Msg: p.msg, Kind: p.kind, Body: p.body, Hash: p.hash, Nullifier: p.nullifier, Seq: p.seq, UID: p.uid, ID: p.id, ProofDate: p.proofDate, Reason: p.reason, Checkpoint: index}
}

func entryOf(r PendingRow) pendingEntry {
	return pendingEntry{kind: r.Kind, body: r.Body, hash: r.Hash, msg: r.Msg, nullifier: r.Nullifier, seq: r.Seq, uid: r.UID, id: r.ID, proofDate: r.ProofDate, reason: r.Reason}
}

// checkBallot applies SPEC §8.1 to a ballot body: the entry and "" when it
// is accepted, the reason when it is rejected, an error when the server
// could not judge it.
func (in *Ingest) checkBallot(ctx context.Context, content []byte, now time.Time) (pendingEntry, string, error) {
	p, err := ParseBallot(content)
	if err != nil {
		return pendingEntry{}, ReasonMalformed, nil
	}
	if p.Event != in.cfg.Event.ID {
		return pendingEntry{}, ReasonMalformed, nil
	}
	if in.closedSeq != 0 || !now.Before(in.current(now).Setup.Closes) {
		return pendingEntry{}, ReasonClosed, nil
	}
	setup, ok := in.setupInForce(p.Setup, now)
	if !ok {
		return pendingEntry{}, ReasonUnknownSetup, nil
	}
	at, known, err := in.store.RootAge(ctx, p.Root)
	if err != nil {
		return pendingEntry{}, "", fmt.Errorf("root age: %w", err)
	}
	// The auditor judges the age at the checkpoint that logs the ballot:
	// a root about to pass anchor_age by then is already too old.
	period := time.Duration(setup.Setup.CheckpointPeriod) * time.Second
	if !known || now.Sub(at)+period > time.Duration(setup.Setup.AnchorAge)*time.Second {
		return pendingEntry{}, ReasonOldRoot, nil
	}
	pi := p.PIHash(setup.Setup.Mask)
	if in.pendingPI[pi] {
		return pendingEntry{}, ReasonDuplicate, nil
	}
	if dup, err := in.store.HasPIHash(ctx, pi); err != nil {
		return pendingEntry{}, "", fmt.Errorf("pi_hash lookup: %w", err)
	} else if dup {
		return pendingEntry{}, ReasonDuplicate, nil
	}
	latest, err := in.store.LatestSeq(ctx, in.cfg.Event.ID, p.Nullifier)
	if err != nil {
		return pendingEntry{}, "", fmt.Errorf("latest seq: %w", err)
	}
	if s := in.pendingNull[p.Nullifier]; s > latest {
		latest = s
	}
	if p.Seq <= latest {
		return pendingEntry{}, ReasonStaleSeq, nil
	}
	if err := in.ver.Verify(ctx, p.ProofBytes, p.PublicInputs32(setup.Setup.Mask)); err != nil {
		if !errors.Is(err, ErrBadProof) {
			return pendingEntry{}, "", fmt.Errorf("verifier: %w", err)
		}
		return pendingEntry{}, ReasonBadProof, nil
	}
	body, _ := Canonical(p.Ballot)
	return pendingEntry{kind: KindBallot, body: body, hash: pi, nullifier: p.Nullifier, seq: p.Seq}, "", nil
}

// checkRegister applies SPEC §4 to a registration body (see checkBallot
// for the three outcomes).
func (in *Ingest) checkRegister(ctx context.Context, content []byte, now time.Time) (pendingEntry, string, error) {
	var r Register
	if err := json.Unmarshal(content, &r); err != nil || r.V != Version || r.Event != in.cfg.Event.ID || r.UID == "" || len(r.Proof) == 0 {
		return pendingEntry{}, ReasonMalformed, nil
	}
	id, err := parseField(r.ID)
	if err != nil {
		return pendingEntry{}, ReasonMalformed, nil
	}
	if in.closedSeq != 0 || !now.Before(in.current(now).Setup.Closes) {
		return pendingEntry{}, ReasonClosed, nil
	}
	validity := time.Duration(in.current(now).Setup.Validity) * time.Second
	if d := now.Sub(r.ProofDate); d > validity+5*time.Minute || d < -(validity+5*time.Minute) {
		return pendingEntry{}, ReasonStaleDate, nil
	}
	// The same registration again (a repost after a missed ack) is a
	// duplicate; another leaf for a registered passport is uid_used.
	for _, p := range in.pending {
		if p.kind == KindRegister && p.uid == r.UID {
			if p.id == r.ID {
				return pendingEntry{}, ReasonDuplicate, nil
			}
			return pendingEntry{}, ReasonUIDUsed, nil
		}
	}
	if id, err := in.store.IDOfUID(ctx, in.cfg.Event.ID, r.UID); err != nil {
		return pendingEntry{}, "", fmt.Errorf("uid lookup: %w", err)
	} else if id == r.ID {
		return pendingEntry{}, ReasonDuplicate, nil
	} else if id != "" {
		return pendingEntry{}, ReasonUIDUsed, nil
	}
	if in.pendingID[r.ID] {
		return pendingEntry{}, ReasonIDUsed, nil
	}
	if used, err := in.store.HasID(ctx, in.cfg.Event.ID, r.ID); err != nil {
		return pendingEntry{}, "", fmt.Errorf("id lookup: %w", err)
	} else if used {
		return pendingEntry{}, ReasonIDUsed, nil
	}
	custom := RegistrationBinding(in.cfg.Event.ID, id)
	uid, err := in.cfg.Passport.VerifyRegistration(ctx, r.Proof, custom, r.ProofDate)
	if err != nil {
		if !errors.Is(err, providers.ErrInvalidProof) {
			return pendingEntry{}, "", fmt.Errorf("passport verifier: %w", err)
		}
		log.Debug().Err(err).Msg("vote: registration proof refused")
		return pendingEntry{}, ReasonBadProof, nil
	}
	if uid != r.UID {
		return pendingEntry{}, ReasonBadProof, nil
	}
	body, _ := Canonical(r)
	return pendingEntry{kind: KindRegister, body: body, hash: BodyHash(body), uid: r.UID, id: r.ID, proofDate: r.ProofDate}, "", nil
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
// nothing is pending and the log did not grow (a tally, the snapshot, the
// closed entry: written by others, covered by the next checkpoint).
func (in *Ingest) Checkpoint(ctx context.Context) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.checkpoint(ctx)
}

func (in *Ingest) checkpoint(ctx context.Context) error {
	if len(in.pending) == 0 {
		head, err := in.store.Log.Head(ctx)
		if err != nil {
			return err
		}
		last, err := in.store.LastCheckpoint(ctx)
		if err != nil {
			return err
		}
		if last != nil && head.N == last.LogSize {
			return nil
		}
	}
	sort.SliceStable(in.pending, func(i, j int) bool { return in.pending[i].hash < in.pending[j].hash })
	index := in.nextIndex
	// The entries, their index rows and the pending rows' deletion are one
	// transaction: a failure leaves the log as it was, and the tree is
	// touched only once they are committed.
	pos0 := in.tree.Size()
	var leaves []fr.Element
	err := in.store.Tx(ctx, func(ctx context.Context, tx *Store) error {
		leaves = leaves[:0]
		for _, p := range in.pending {
			seq, err := tx.AppendLog(ctx, p.kind, p.body)
			if err != nil {
				return fmt.Errorf("append %s: %w", p.kind, err)
			}
			h := HandledEvent{NostrID: p.msg.ID, Relay: p.msg.Box, Kind: int(p.msg.Kind), BodyHash: BodyHash(p.msg.Body), Outcome: "logged", LogSeq: seq, Checkpoint: index}
			switch p.kind {
			case KindBallot:
				h.PIHash = p.hash
				if err := tx.AddBallot(ctx, LoggedBallot{EventID: in.cfg.Event.ID, Nullifier: p.nullifier, Seq: p.seq, PIHash: p.hash, LogSeq: seq, Checkpoint: index, Body: p.body, NostrID: p.msg.ID, Relay: p.msg.Box}); err != nil {
					return err
				}
			case KindRegister:
				leaf, err := parseField(p.id)
				if err != nil {
					return fmt.Errorf("pending leaf: %w", err)
				}
				pos := pos0 + len(leaves)
				if err := tx.AddRegistration(ctx, Registration{EventID: in.cfg.Event.ID, UID: p.uid, ID: p.id, Position: pos, LogSeq: seq, ProofDate: p.proofDate, NostrID: p.msg.ID, Relay: p.msg.Box}); err != nil {
					return err
				}
				leaves = append(leaves, leaf)
			case KindReject:
				h.Outcome, h.Reason = "rejected", p.reason
			}
			if err := tx.AddHandled(ctx, h); err != nil {
				return err
			}
			if err := tx.DeletePending(ctx, p.msg.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, l := range leaves {
		if _, err := in.tree.Append(l); err != nil {
			// The tree is full: the registrations are logged and indexed;
			// the next start rebuilds the tree and fails the same way.
			return fmt.Errorf("tree: %w", err)
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
	// A box's cursor is the newest time it delivered: a box out of reach
	// for a while is read again from where it was left, not from now.
	for b, t := range in.seen {
		if err := in.store.SetCursor(ctx, b, t); err != nil {
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
	if in.closedSeq == 0 {
		if c, err := in.store.ClosedEntrySigned(ctx, in.cfg.Event.ID, in.current(in.cfg.Now()).Setup.TallyPubkey); err == nil && c != nil {
			in.closedSeq = c.Final
			if in.closedSeq == 0 {
				in.closedSeq = -1
			}
		}
	}
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
