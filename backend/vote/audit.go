package vote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"golang.org/x/mod/sumdb/tlog"

	"digital-mirror/backend/polls"
	"digital-mirror/backend/vote/ballot"
)

// The auditor of SPEC §12: anyone recounts the log without trusting the
// site. It takes the log's entries and the checkpoints, and checks the
// log itself, the setup, the registrations, the tree, the ballots, the
// tallies and the close. The relays' archive (P1, §12.6) is a separate
// pass: AuditRelays.

// LogSource is where the auditor reads from: the site's API, or the
// store in tests.
type LogSource interface {
	Entries(ctx context.Context, after int64, limit int) ([]polls.LogEntry, error)
	Checkpoints(ctx context.Context) ([]Checkpoint, error)
	// Stamps lists the OpenTimestamps proofs of the checkpoints.
	Stamps(ctx context.Context) ([]polls.Stamp, error)
}

// Options of an audit.
type Options struct {
	EventID int64
	// LogKey is the log's verifier key; "" skips the signatures (a warning).
	LogKey string
	// Passport verifies registrations; nil skips them (a warning).
	Passport PassportVerifier
	// Verifier checks ballot proofs; nil skips them (a warning).
	Verifier *Verifier
	Now      time.Time
	// Relays are the events read from the relays (DumpRelays, or an
	// archive with first sightings): the withholding check of §12.6; nil
	// skips it (a warning).
	Relays []RelayEvent
	// Snapshot is the mirror's snapshot list, to check against the relays.
	Snapshot *SnapshotList
	// BlockRoots checks the stamps' Bitcoin attestations against the block
	// headers; nil checks the proofs' structure only (a warning).
	BlockRoots polls.BlockRoots
	// Mirror reads the mirror at the commits the notes name; nil skips the
	// mirror (a warning).
	Mirror MirrorReader
}

// Report is what the audit found.
type Report struct {
	Problems []string
	Warnings []string
	// Counts.
	Entries, Checkpoints, Registrations, Ballots, Rejects, Tallies int
	Voters                                                         int64
	Results                                                        *Results
	// The relays: protocol events read, acks, events acknowledged by no
	// one, withholdings proven or attributed, complaints.
	RelayEvents, Acks, Unacknowledged, Withheld, Complaints int
	// The stamps: checkpoints attested in Bitcoin, still pending.
	Stamped, StampsPending int
	// The mirror: checkpoints whose files were checked at their commit.
	MirrorChecked int
}

func (r *Report) problem(format string, args ...any) {
	r.Problems = append(r.Problems, fmt.Sprintf(format, args...))
}

func (r *Report) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// OK tells whether the audit found no problem.
func (r *Report) OK() bool { return len(r.Problems) == 0 }

type auditedBallot struct {
	seq    int64 // log seq
	body   *Parsed
	logged time.Time
}

// Audit runs the checks of SPEC §12.1–§12.5, §12.7 and §12.8.
func Audit(ctx context.Context, src LogSource, opt Options) (*Report, error) {
	rep := &Report{}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	cps, err := src.Checkpoints(ctx)
	if err != nil {
		return nil, err
	}
	if len(cps) == 0 {
		rep.problem("no checkpoint")
		return rep, nil
	}
	sort.Slice(cps, func(i, j int) bool { return cps[i].Index < cps[j].Index })
	last := cps[len(cps)-1]
	rep.Checkpoints = len(cps)

	// 1. The log: every entry up to the last checkpoint, the tree they
	// make, every checkpoint's signature, size and hash.
	var entries []polls.LogEntry
	for int64(len(entries)) < last.LogSize {
		page, err := src.Entries(ctx, int64(len(entries)), 1000)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		entries = append(entries, page...)
	}
	if int64(len(entries)) < last.LogSize {
		rep.problem("the log serves %d entries, the last checkpoint covers %d", len(entries), last.LogSize)
		return rep, nil
	}
	entries = entries[:last.LogSize]
	rep.Entries = len(entries)
	hashes := make([]tlog.Hash, len(entries))
	for i, e := range entries {
		if e.Seq != int64(i+1) {
			rep.problem("entry %d has seq %d", i+1, e.Seq)
		}
		hashes[i] = tlog.RecordHash(polls.LeafData(e.Seq, e.Kind, []byte(e.Body)))
	}
	treeAt := func(n int64) (tlog.Hash, error) {
		return tlog.TreeHash(n, tlog.HashReaderFunc(func(idx []int64) ([]tlog.Hash, error) {
			return storedHashes(hashes, idx)
		}))
	}
	cpByIndex := map[int64]Checkpoint{}
	// The time of a checkpoint is its signed note's `time` line, never the
	// record's created_at, which the server edits freely.
	noteTime := map[int64]time.Time{}
	unsignedTimes := 0
	var prevIndex int64
	for _, cp := range cps {
		cpByIndex[cp.Index] = cp
		if cp.Index != prevIndex+1 {
			rep.problem("checkpoint %d follows %d", cp.Index, prevIndex)
		}
		prevIndex = cp.Index
		if opt.LogKey != "" {
			t, err := polls.OpenCheckpoint([]byte(cp.Note), opt.LogKey)
			if err != nil {
				rep.problem("checkpoint %d: %v", cp.Index, err)
				continue
			}
			if t.N != cp.LogSize {
				rep.problem("checkpoint %d: note says %d entries, record %d", cp.Index, t.N, cp.LogSize)
			}
			want, err := treeAt(t.N)
			if err != nil || want != t.Hash {
				rep.problem("checkpoint %d: the entries do not make its tree hash", cp.Index)
			}
		}
		root, size, index, eventID, at, err := ParseCheckpoint(noteText(cp.Note))
		if err != nil || index != cp.Index || eventID != opt.EventID {
			rep.problem("checkpoint %d: note lines: %v", cp.Index, err)
		}
		if err == nil && !at.IsZero() && opt.LogKey != "" {
			noteTime[cp.Index] = at
		} else {
			noteTime[cp.Index] = cp.CreatedAt
			unsignedTimes++
		}
		if root != cp.TreeRoot || size != cp.TreeSize {
			rep.problem("checkpoint %d: note and record disagree on the tree", cp.Index)
		}
	}
	if opt.LogKey == "" {
		rep.warn("checkpoint signatures not checked (no log key)")
	}
	if unsignedTimes > 0 {
		rep.warn("%d checkpoints judged by the record's time, not a signed one", unsignedTimes)
	}
	cpAt := func(cp Checkpoint) time.Time { return noteTime[cp.Index] }

	// Replay the entries in order.
	var setups []LoggedSetup
	tree := ballot.NewTree()
	regUID := map[string]bool{}
	regID := map[string]bool{}
	// A voter's ballots since the last tally, in log order: a tally counts
	// the latest one logged up to its upto, whatever came after.
	type voter struct {
		history []*auditedBallot
		ballots int
	}
	// The ballot a tally counts: the highest seq logged up to its upto
	// (the entries of one checkpoint are ordered by hash, not by seq).
	latestUpto := func(v *voter, upto int64) *auditedBallot {
		var found *auditedBallot
		for _, b := range v.history {
			if b.seq <= upto && (found == nil || b.body.Seq > found.body.Seq) {
				found = b
			}
		}
		return found
	}
	voters := map[string]*voter{}
	piSeen := map[string]bool{}
	rootFirst := map[string]time.Time{} // tree root → when first in a checkpoint
	for _, cp := range cps {
		if _, ok := rootFirst[cp.TreeRoot]; !ok {
			rootFirst[cp.TreeRoot] = cpAt(cp)
		}
	}
	// Which checkpoint an entry belongs to: the first whose LogSize ≥ seq.
	cpOf := func(seq int64) Checkpoint {
		for _, cp := range cps {
			if cp.LogSize >= seq {
				return cp
			}
		}
		return last
	}
	expectedRoots := map[int64]string{} // checkpoint index → root after its entries
	var closedAt *time.Time
	var snapshot *LoggedSnapshot
	tallySeqs := map[int64]int64{} // tally seq → its upto
	// What the rejects are re-checked against once the replay is done
	// (§12.5: every reject deserves its reason): the first checkpoint
	// that logged each pi_hash, passport, leaf, and each pseudonym's seqs.
	mirrorEntries := map[int64][]MirrorEntry{} // checkpoint → what its entries file must list
	piFirst := map[string]int64{}
	uidFirst := map[string]int64{}
	idFirst := map[string]int64{}
	seqsOf := map[string]map[uint32]int64{} // nullifier → seq → checkpoint
	type loggedReject struct {
		seq int64
		cp  Checkpoint
		rj  Reject
	}
	var rejects []loggedReject
	var prevTallyUpto int64
	var prevTallyAt time.Time
	lg := &logged{ballots: map[string]int64{}, registers: map[string]int64{}, rejects: map[string]rejectAt{}, seen: map[string]int64{}, cps: cpByIndex}
	other := 0
	for _, e := range entries {
		cp := cpOf(e.Seq)
		// Only the bodies of this protocol version; a log that also holds
		// older entries (a trial) is not this audit's.
		var ver struct {
			V int `json:"v"`
		}
		if json.Unmarshal([]byte(e.Body), &ver) != nil || ver.V != Version {
			other++
			continue
		}
		switch e.Kind {
		case KindSetup:
			var s Setup
			if err := json.Unmarshal([]byte(e.Body), &s); err != nil || s.Event != opt.EventID {
				if err != nil {
					rep.problem("setup %d: %v", e.Seq, err)
				}
				continue
			}
			if err := s.ValidateLogged(); err != nil {
				rep.problem("setup %d: %v", e.Seq, err)
			}
			if s.NostrPubkey == "" || s.TallyPubkey == "" {
				rep.warn("setup %d names no %s: a trial log", e.Seq, map[bool]string{true: "nostr_pubkey", false: "tally_pubkey"}[s.NostrPubkey == ""])
			}
			if len(setups) > 0 {
				if err := checkLaterSetup(setups[0].Setup, s); err != nil {
					rep.problem("setup %d: %v", e.Seq, err)
				}
			}
			setups = append(setups, LoggedSetup{Seq: e.Seq, LoggedAt: cpAt(cp), Setup: s})
		case KindRegister:
			var r Register
			if err := json.Unmarshal([]byte(e.Body), &r); err != nil || r.Event != opt.EventID {
				continue
			}
			if regUID[r.UID] {
				rep.problem("registration %d: passport %s registered twice", e.Seq, r.UID)
			}
			if regID[r.ID] {
				rep.problem("registration %d: leaf %s registered twice", e.Seq, r.ID)
			}
			regUID[r.UID], regID[r.ID] = true, true
			if _, ok := uidFirst[r.UID]; !ok {
				uidFirst[r.UID] = cp.Index
			}
			if _, ok := idFirst[r.ID]; !ok {
				idFirst[r.ID] = cp.Index
			}
			lg.registers[BodyHash([]byte(e.Body))] = cp.Index
			mirrorEntries[cp.Index] = append(mirrorEntries[cp.Index], MirrorEntry{Checkpoint: cp.Index, Kind: KindRegister, Hash: BodyHash([]byte(e.Body)), LogSeq: e.Seq})
			if snapshot != nil {
				rep.problem("registration %d after the close snapshot", e.Seq)
			}
			leaf, err := parseField(r.ID)
			if err != nil {
				rep.problem("registration %d: %v", e.Seq, err)
				continue
			}
			if len(setups) > 0 {
				validity := time.Duration(setups[0].Setup.Validity) * time.Second
				if d := cpAt(cp).Sub(r.ProofDate); d > validity+10*time.Minute || d < -(validity+10*time.Minute) {
					rep.problem("registration %d: proof date %s, logged %s", e.Seq, r.ProofDate.Format(time.RFC3339), cpAt(cp).Format(time.RFC3339))
				}
			}
			regKey, err := VerifyRegisterSig([]byte(e.Body))
			if err != nil {
				rep.problem("registration %d: %v", e.Seq, err)
			} else if opt.Passport != nil {
				uid, err := opt.Passport.VerifyRegistration(ctx, r.Proof, RegistrationBinding(opt.EventID, regKey), r.ProofDate)
				if err != nil || uid != r.UID {
					rep.problem("registration %d: passport proof: %v", e.Seq, err)
				}
			}
			if _, err := tree.Append(leaf); err != nil {
				rep.problem("registration %d: %v", e.Seq, err)
			}
			rep.Registrations++
		case KindBallot:
			p, err := ParseBallot([]byte(e.Body))
			if err != nil || p.Event != opt.EventID {
				if err != nil {
					rep.problem("ballot %d: %v", e.Seq, err)
				}
				continue
			}
			rep.Ballots++
			var setup *LoggedSetup
			for i := range setups {
				if setups[i].Seq == p.Setup {
					setup = &setups[i]
				}
			}
			if setup == nil {
				rep.problem("ballot %d names setup %d, not logged", e.Seq, p.Setup)
				continue
			}
			if closedAt != nil {
				rep.problem("ballot %d after the close", e.Seq)
			} else if snapshot != nil {
				rep.problem("ballot %d after the close snapshot", e.Seq)
			}
			at, known := rootFirst[p.Root]
			if !known {
				rep.problem("ballot %d: root %s is no checkpoint's", e.Seq, p.Root[:12])
			} else if cpAt(cp).Sub(at) > time.Duration(setup.Setup.AnchorAge)*time.Second {
				rep.problem("ballot %d: root older than anchor_age", e.Seq)
			}
			pi := p.PIHash(setup.Setup.Mask)
			if piSeen[pi] {
				rep.problem("ballot %d: the same public inputs as an earlier ballot", e.Seq)
			}
			piSeen[pi] = true
			if _, ok := piFirst[pi]; !ok {
				piFirst[pi] = cp.Index
			}
			lg.ballots[pi] = cp.Index
			mirrorEntries[cp.Index] = append(mirrorEntries[cp.Index], MirrorEntry{Checkpoint: cp.Index, Kind: KindBallot, Hash: pi, LogSeq: e.Seq})
			v := voters[p.Nullifier]
			if v == nil {
				v = &voter{}
				voters[p.Nullifier] = v
			}
			// §6.3: a seq above every seq logged at an earlier checkpoint,
			// and (nullifier, seq) once; two ballots of one pseudonym in
			// one checkpoint come in hash order, either first.
			if seqsOf[p.Nullifier] == nil {
				seqsOf[p.Nullifier] = map[uint32]int64{}
			}
			if at, dup := seqsOf[p.Nullifier][p.Seq]; dup {
				rep.problem("ballot %d: seq %d of the pseudonym logged twice (checkpoint %d)", e.Seq, p.Seq, at)
			}
			for sq, at := range seqsOf[p.Nullifier] {
				if at < cp.Index && sq >= p.Seq {
					rep.problem("ballot %d: seq %d not above the pseudonym's %d of checkpoint %d", e.Seq, p.Seq, sq, at)
					break
				}
			}
			seqsOf[p.Nullifier][p.Seq] = cp.Index
			if opt.Verifier != nil {
				if err := opt.Verifier.Verify(ctx, p.ProofBytes, p.PublicInputs32(setup.Setup.Mask)); err != nil {
					rep.problem("ballot %d: %v", e.Seq, err)
				}
			}
			v.history = append(v.history, &auditedBallot{seq: e.Seq, body: p, logged: cpAt(cp)})
			v.ballots++
		case KindReject:
			var rj Reject
			if err := json.Unmarshal([]byte(e.Body), &rj); err != nil {
				rep.problem("reject %d: %v", e.Seq, err)
				continue
			}
			rep.Rejects++
			lg.rejects[rj.NostrID] = rejectAt{cp: cp.Index, reason: rj.Reason}
			mirrorEntries[cp.Index] = append(mirrorEntries[cp.Index], MirrorEntry{Checkpoint: cp.Index, Kind: KindReject, Hash: BodyHash([]byte(e.Body)), LogSeq: e.Seq})
			if !knownReason(rj.Reason) {
				rep.problem("reject %d: unknown reason %q", e.Seq, rj.Reason)
			}
			rejects = append(rejects, loggedReject{seq: e.Seq, cp: cp, rj: rj})
		case KindTally:
			var t TallyEntry
			if err := json.Unmarshal([]byte(e.Body), &t); err != nil || t.Event != opt.EventID {
				if err != nil {
					rep.problem("tally %d: %v", e.Seq, err)
				}
				continue
			}
			if len(setups) > 0 && setups[0].Setup.TallyPubkey != "" {
				if err := VerifyEntry(setups[0].Setup.TallyPubkey, []byte(e.Body)); err != nil {
					rep.problem("tally %d: %v", e.Seq, err)
				}
			}
			rep.Tallies++
			if len(setups) == 0 {
				rep.problem("tally %d before any setup", e.Seq)
				continue
			}
			pk, err := (PublicKeys{Event: opt.EventID, PK: setups[0].Setup.PK, PoK: setups[0].Setup.PKPoK}).ParsedKeys()
			if err != nil {
				rep.problem("tally %d: keys: %v", e.Seq, err)
				continue
			}
			var cts []ballot.Ciphertext
			changed := map[string]bool{} // pseudonyms with a ballot since the last tally
			for n, v := range voters {
				for _, b := range v.history {
					if b.seq > prevTallyUpto && b.seq <= t.Upto {
						changed[n] = true
					}
				}
				counted := latestUpto(v, t.Upto)
				if counted == nil {
					continue
				}
				cts = append(cts, counted.body.Ciphertext)
				// The ballots before the counted one will never count again.
				kept := v.history[:0]
				for _, b := range v.history {
					if b.seq >= counted.seq {
						kept = append(kept, b)
					}
				}
				v.history = kept
			}
			// §9.1 in public: at least K_REAL pseudonyms with a new ballot
			// since the last tally (the final tally excepted), the gap
			// between tallies, the count over the pseudonyms with a ballot.
			final := snapshot != nil && t.Upto == snapshot.Seq
			if !final && len(changed) < setups[0].Setup.KReal {
				rep.problem("tally %d: %d pseudonyms voted since the last tally, K_REAL is %d", e.Seq, len(changed), setups[0].Setup.KReal)
			}
			if gap := setups[0].Setup.TallyMinGap; gap > 0 && !final && !prevTallyAt.IsZero() && cpAt(cp).Sub(prevTallyAt) < time.Duration(gap)*time.Second {
				rep.problem("tally %d: %s after the previous one, tally_min_gap is %d s", e.Seq, cpAt(cp).Sub(prevTallyAt), gap)
			}
			if t.Count != int64(len(cts)) {
				rep.problem("tally %d: counts %d ballots, %d pseudonyms had one", e.Seq, t.Count, len(cts))
			}
			prevTallyUpto, prevTallyAt = t.Upto, cpAt(cp)
			vals, err := VerifyTally(t, pk, cts)
			if err != nil {
				rep.problem("tally %d: %v", e.Seq, err)
				continue
			}
			cur := setups[0]
			for _, s := range setups[1:] {
				if !cpAt(cp).Before(s.LoggedAt.Add(24 * time.Hour)) {
					cur = s
				}
			}
			for _, b := range cur.Setup.Blackouts {
				if !cpAt(cp).Before(b.From) && cpAt(cp).Before(b.To) {
					rep.problem("tally %d inside a blackout", e.Seq)
				}
			}
			res := ComputeResults(vals, &cur.Setup)
			rep.Results = &res
			rep.Voters = res.Voters
			tallySeqs[e.Seq] = t.Upto
		case KindSnapshot:
			var s Snapshot
			if err := json.Unmarshal([]byte(e.Body), &s); err != nil || s.Event != opt.EventID {
				if err != nil {
					rep.problem("snapshot %d: %v", e.Seq, err)
				}
				continue
			}
			if snapshot != nil {
				rep.problem("snapshot %d: a second snapshot (the first at %d)", e.Seq, snapshot.Seq)
			}
			if err := parseSnapshotRoot(s.Root); err != nil {
				rep.problem("snapshot %d: %v", e.Seq, err)
			}
			if len(setups) > 0 && s.At.Before(setups[0].Setup.Closes) {
				rep.problem("snapshot %d taken at %s, before the close %s", e.Seq, s.At.Format(time.RFC3339), setups[0].Setup.Closes.Format(time.RFC3339))
			}
			snapshot = &LoggedSnapshot{Seq: e.Seq, Body: s}
			lg.snapshot = snapshot
		case KindClosed:
			var c Closed
			if err := json.Unmarshal([]byte(e.Body), &c); err != nil || c.Event != opt.EventID {
				if err != nil {
					rep.problem("closed %d: %v", e.Seq, err)
				}
				continue
			}
			if len(setups) > 0 && setups[0].Setup.TallyPubkey != "" {
				if err := VerifyEntry(setups[0].Setup.TallyPubkey, []byte(e.Body)); err != nil {
					rep.problem("closed %d: %v", e.Seq, err)
				}
			}
			closedAt = &c.At
			// 8. The final tally covers exactly the snapshot: every ballot
			// logged before the snapshot entry, none after.
			switch {
			case snapshot == nil:
				rep.problem("closed %d without a close snapshot", e.Seq)
			case c.SnapshotRoot != snapshot.Body.Root:
				rep.problem("closed %d names snapshot root %s, the snapshot's is %s", e.Seq, c.SnapshotRoot, snapshot.Body.Root)
			case c.Final == 0 && rep.Ballots > 0:
				rep.problem("closed %d names no final tally over %d ballots", e.Seq, rep.Ballots)
			case c.Final != 0:
				upto, ok := tallySeqs[c.Final]
				if !ok || c.Final > e.Seq {
					rep.problem("closed %d names final tally %d, not a tally before it", e.Seq, c.Final)
				} else if upto != snapshot.Seq {
					rep.problem("closed %d: the final tally covers the log up to %d, the snapshot is entry %d", e.Seq, upto, snapshot.Seq)
				}
			}
		case KindSeen:
			var sn Seen
			if json.Unmarshal([]byte(e.Body), &sn) == nil && sn.NostrID != "" {
				lg.seen[sn.NostrID] = cp.Index
				mirrorEntries[cp.Index] = append(mirrorEntries[cp.Index], MirrorEntry{Checkpoint: cp.Index, Kind: KindSeen, Hash: BodyHash([]byte(e.Body)), LogSeq: e.Seq})
			}
		case KindDestroyed, KindAbort:
		default:
			rep.warn("entry %d: unknown kind %q", e.Seq, e.Kind)
		}
		expectedRoots[cp.Index] = "0x" + hexOf(frBytes(tree.Root()))
	}
	// 4. The tree: each checkpoint's root is the root of the leaves logged so far.
	for _, cp := range cps {
		if want, ok := expectedRoots[cp.Index]; ok && want != cp.TreeRoot && cp.LogSize > 0 {
			// A checkpoint without new registrations keeps the previous root.
			if prev, ok := expectedRoots[cp.Index-1]; !ok || prev != cp.TreeRoot {
				rep.problem("checkpoint %d: tree root %s, the registrations give %s", cp.Index, cp.TreeRoot[:12], want[:12])
			}
		}
	}
	// 5. Every reject deserves its reason (§12.5), from its stored body
	// and the log as it was at its checkpoint.
	if len(setups) > 0 {
		rc := rejectChecker{opt: opt, setups: setups, cpAt: cpAt, cpByIndex: cpByIndex, rootFirst: rootFirst, piFirst: piFirst, uidFirst: uidFirst, idFirst: idFirst, seqsOf: seqsOf, closedAt: closedAt}
		for _, r := range rejects {
			if why := rc.check(ctx, r.cp, r.rj); why != "" {
				rep.problem("reject %d (%s): %s", r.seq, r.rj.Reason, why)
			}
		}
		if rc.unchecked > 0 {
			rep.warn("%d rejects for bad_proof not re-checked (no verifier or passport)", rc.unchecked)
		}
	}
	if other > 0 {
		rep.warn("%d entries of another protocol version skipped", other)
	}
	// 9. The stamps: every checkpoint's note in Bitcoin (§12.1).
	rep.checkStamps(ctx, src, cps, noteTime, opt)
	// 10. The mirror: the files browsers read at each checkpoint's commit
	// are the log's (§8.3).
	rep.checkMirror(ctx, cps, noteTime, mirrorEntries, opt)
	if len(setups) == 0 {
		rep.problem("no setup for event %d", opt.EventID)
	} else if setups[0].Setup.TallyPubkey == "" {
		rep.warn("the setup names no tally_pubkey: the tallies and the close are not signed (a trial log)")
	}
	if opt.Passport == nil {
		rep.warn("passport proofs not checked")
	}
	if opt.Verifier == nil {
		rep.warn("ballot proofs not checked (no verification key)")
	}
	// 6. The relays.
	lg.setups = setups
	if opt.Relays == nil {
		rep.warn("the relays not read: withholding not checked (--relays)")
	} else {
		rep.checkRelays(opt.Relays, lg, opt)
	}
	return rep, nil
}

func frBytes(e fr.Element) []byte {
	b := e.Bytes()
	return b[:]
}

func noteText(note string) string {
	if i := strings.Index(note, "\n\n"); i >= 0 {
		return note[:i+1]
	}
	return note
}

func knownReason(r string) bool {
	switch r {
	case ReasonMalformed, ReasonUnknownSetup, ReasonOldRoot, ReasonBadProof, ReasonBadSig, ReasonDuplicate, ReasonStaleSeq, ReasonStaleDate, ReasonUIDUsed, ReasonIDUsed, ReasonClosed:
		return true
	}
	return false
}

// checkLaterSetup applies the rules of SPEC §11 to a setup after the first.
func checkLaterSetup(first, later Setup) error {
	if later.NCap != first.NCap || later.Blank != first.Blank || later.VKHash != first.VKHash || later.KReal != first.KReal ||
		later.AnchorAge != first.AnchorAge || !later.Closes.Equal(first.Closes) || len(later.PK) != len(first.PK) {
		return errors.New("a fixed field changed")
	}
	for j := range first.PK {
		if later.PK[j] != first.PK[j] {
			return errors.New("the keys changed")
		}
	}
	for _, r := range first.Relays {
		found := false
		for _, l := range later.Relays {
			if l.URL == r.URL {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("relay %s removed (retire it instead)", r.URL)
		}
	}
	return nil
}

// storedHashes serves tlog's stored-hash indexes from the record hashes
// (tlog.StoredHashIndex numbering), recomputing the interior nodes.
func storedHashes(records []tlog.Hash, idx []int64) ([]tlog.Hash, error) {
	out := make([]tlog.Hash, len(idx))
	for i, x := range idx {
		level, n := tlog.SplitStoredHashIndex(x)
		h, err := subtree(records, level, n)
		if err != nil {
			return nil, err
		}
		out[i] = h
	}
	return out, nil
}

func subtree(records []tlog.Hash, level int, n int64) (tlog.Hash, error) {
	if level == 0 {
		if n >= int64(len(records)) {
			return tlog.Hash{}, fmt.Errorf("record %d beyond the log", n)
		}
		return records[n], nil
	}
	l, err := subtree(records, level-1, 2*n)
	if err != nil {
		return tlog.Hash{}, err
	}
	r, err := subtree(records, level-1, 2*n+1)
	if err != nil {
		return tlog.Hash{}, err
	}
	return tlog.NodeHash(l, r), nil
}

// SetupsOf reads the setups of an event from a log source, in log order.
func SetupsOf(ctx context.Context, src LogSource, eventID int64) ([]LoggedSetup, error) {
	var out []LoggedSetup
	var after int64
	for {
		page, err := src.Entries(ctx, after, 1000)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return out, nil
		}
		for _, e := range page {
			after = e.Seq
			if e.Kind != KindSetup {
				continue
			}
			var s Setup
			if json.Unmarshal([]byte(e.Body), &s) == nil && s.Event == eventID {
				out = append(out, LoggedSetup{Seq: e.Seq, LoggedAt: e.CreatedAt, Setup: s})
			}
		}
	}
}

// StoreSource reads the log from the store (tests, local audits).
type StoreSource struct{ Store *Store }

func (s StoreSource) Entries(ctx context.Context, after int64, limit int) ([]polls.LogEntry, error) {
	return s.Store.Log.Log(ctx, after, limit)
}

func (s StoreSource) Checkpoints(ctx context.Context) ([]Checkpoint, error) {
	return s.Store.AllCheckpoints(ctx)
}

func (s StoreSource) Stamps(ctx context.Context) ([]polls.Stamp, error) {
	return s.Store.Log.Stamps(ctx)
}

// AllCheckpoints lists every checkpoint in order.
func (s *Store) AllCheckpoints(ctx context.Context) ([]Checkpoint, error) {
	rows, err := s.db.Query(ctx, `SELECT idx, log_size, tree_root, tree_size, mirror_commit, note, created_at FROM vote2_checkpoints ORDER BY idx`)
	if err != nil {
		return nil, fmt.Errorf("checkpoints: %w", err)
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		if err := rows.Scan(&c.Index, &c.LogSize, &c.TreeRoot, &c.TreeSize, &c.Commit, &c.Note, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// rejectChecker re-checks a reject's reason against the stored body and
// the log at its checkpoint.
type rejectChecker struct {
	opt       Options
	setups    []LoggedSetup
	cpAt      func(Checkpoint) time.Time
	cpByIndex map[int64]Checkpoint
	rootFirst map[string]time.Time
	piFirst   map[string]int64
	uidFirst  map[string]int64
	idFirst   map[string]int64
	seqsOf    map[string]map[uint32]int64
	closedAt  *time.Time
	unchecked int
}

// check returns "" when the reason holds, else why it does not.
func (c *rejectChecker) check(ctx context.Context, cp Checkpoint, rj Reject) string {
	at := c.cpAt(cp)
	body := []byte(rj.Body)
	cur := c.setups[0]
	for _, s := range c.setups[1:] {
		if !at.Before(s.LoggedAt.Add(24 * time.Hour)) {
			cur = s
		}
	}
	p, perr := ParseBallot(body)
	if perr == nil && p.Event != c.opt.EventID {
		perr = errors.New("another event")
	}
	var r Register
	rerr := json.Unmarshal(body, &r)
	if rerr == nil && (r.V != Version || r.Event != c.opt.EventID || r.UID == "" || len(r.Proof) == 0) {
		rerr = errors.New("not a registration of this event")
	}
	if rerr == nil {
		if _, rerr = parseField(r.ID); rerr != nil {
			rerr = fmt.Errorf("id: %w", rerr)
		}
	}
	isBallot, isReg := perr == nil, rerr == nil
	switch rj.Reason {
	case ReasonMalformed:
		if isBallot {
			return "the body parses"
		}
		if isReg {
			if _, _, err := registerKey(r); err == nil {
				return "the body parses"
			}
		}
	case ReasonClosed:
		if at.Before(cur.Setup.Closes) && (c.closedAt == nil || at.Before(*c.closedAt)) {
			return "the vote was open at the checkpoint"
		}
	case ReasonUnknownSetup:
		if !isBallot {
			return "not a ballot"
		}
		for i, s := range c.setups {
			if s.Seq == p.Setup && (i == 0 || !at.Before(s.LoggedAt.Add(24*time.Hour))) {
				return fmt.Sprintf("setup %d was in force", p.Setup)
			}
		}
	case ReasonOldRoot:
		if !isBallot {
			return "not a ballot"
		}
		first, known := c.rootFirst[p.Root]
		if known && at.Sub(first) <= time.Duration(cur.Setup.AnchorAge)*time.Second {
			return "the root was within anchor_age"
		}
	case ReasonDuplicate:
		if isBallot {
			if first, ok := c.piFirst[p.PIHash(cur.Setup.Mask)]; !ok || first > cp.Index {
				return "no ballot with these public inputs was logged by then"
			}
		} else if isReg {
			if first, ok := c.uidFirst[r.UID]; !ok || first > cp.Index {
				return "the passport was not registered by then"
			}
		} else {
			return "the body does not parse"
		}
	case ReasonStaleSeq:
		if !isBallot {
			return "not a ballot"
		}
		for sq, idx := range c.seqsOf[p.Nullifier] {
			if idx <= cp.Index && sq >= p.Seq {
				return ""
			}
		}
		return fmt.Sprintf("no ballot of the pseudonym with seq ≥ %d was logged by then", p.Seq)
	case ReasonBadProof:
		if isBallot {
			if c.opt.Verifier == nil {
				c.unchecked++
				return ""
			}
			var setup *LoggedSetup
			for i := range c.setups {
				if c.setups[i].Seq == p.Setup {
					setup = &c.setups[i]
				}
			}
			if setup == nil {
				return ""
			}
			if err := c.opt.Verifier.Verify(ctx, p.ProofBytes, p.PublicInputs32(setup.Setup.Mask)); err == nil {
				return "the proof verifies"
			}
		} else if isReg {
			if c.opt.Passport == nil {
				c.unchecked++
				return ""
			}
			regKey, err := VerifyRegisterSig(body)
			if err != nil {
				return ""
			}
			if uid, err := c.opt.Passport.VerifyRegistration(ctx, r.Proof, RegistrationBinding(c.opt.EventID, regKey), r.ProofDate); err == nil && uid == r.UID {
				return "the passport proof verifies"
			}
		} else {
			return "the body does not parse"
		}
	case ReasonBadSig:
		if !isReg {
			return "not a registration"
		}
		if _, err := VerifyRegisterSig(body); err == nil {
			return "the registration's signature verifies"
		}
	case ReasonStaleDate:
		if !isReg {
			return "not a registration"
		}
		validity := time.Duration(cur.Setup.Validity) * time.Second
		if d := at.Sub(r.ProofDate); d <= validity+10*time.Minute && d >= -(validity+10*time.Minute) {
			return "the proof date was within validity"
		}
	case ReasonUIDUsed:
		if !isReg {
			return "not a registration"
		}
		if first, ok := c.uidFirst[r.UID]; !ok || first > cp.Index {
			return "the passport had no registration by then"
		}
	case ReasonIDUsed:
		if !isReg {
			return "not a registration"
		}
		if first, ok := c.idFirst[r.ID]; !ok || first > cp.Index {
			return "the leaf had no registration by then"
		}
	}
	return ""
}

// checkStamps verifies the OpenTimestamps proof of every checkpoint: the
// stamp's note is the log's, the proof is of its digest, and its Bitcoin
// attestations match the block headers. A checkpoint older than a day
// without a Bitcoin attestation is a warning (the calendars confirm within
// hours; the upgrade may lag).
func (rep *Report) checkStamps(ctx context.Context, src LogSource, cps []Checkpoint, noteTime map[int64]time.Time, opt Options) {
	stamps, err := src.Stamps(ctx)
	if err != nil {
		rep.warn("stamps not read: %v", err)
		return
	}
	bySize := map[int64]polls.Stamp{}
	for _, st := range stamps {
		bySize[st.Size] = st
	}
	if opt.BlockRoots == nil {
		rep.warn("the stamps' Bitcoin attestations not checked against the block headers (no block source)")
	}
	unstamped := 0
	for _, cp := range cps {
		st, ok := bySize[cp.LogSize]
		if !ok {
			if opt.Now.Sub(noteTime[cp.Index]) > time.Hour {
				unstamped++
			}
			continue
		}
		if st.Checkpoint != cp.Note {
			rep.problem("stamp of size %d: its note is not checkpoint %d's", st.Size, cp.Index)
			continue
		}
		heights, pending, err := polls.VerifyOTS(ctx, st.OTS, polls.CheckpointDigest([]byte(cp.Note)), opt.BlockRoots)
		if err != nil {
			rep.problem("stamp of checkpoint %d: %v", cp.Index, err)
			continue
		}
		if len(heights) > 0 {
			rep.Stamped++
		} else {
			rep.StampsPending++
			if opt.Now.Sub(noteTime[cp.Index]) > 24*time.Hour {
				rep.warn("checkpoint %d: stamped %s ago, still pending at %s (no Bitcoin attestation yet)", cp.Index, opt.Now.Sub(noteTime[cp.Index]).Round(time.Hour), strings.Join(pending, ", "))
			}
		}
	}
	if unstamped > 0 {
		rep.warn("%d checkpoints older than an hour have no stamp", unstamped)
	}
}

// checkMirror reads, at the commit each note names, the note's own file
// and the hour's entries file, and compares them with the log.
func (rep *Report) checkMirror(ctx context.Context, cps []Checkpoint, noteTime map[int64]time.Time, entries map[int64][]MirrorEntry, opt Options) {
	if opt.Mirror == nil {
		rep.warn("the mirror not checked against the log (--mirror-git or --mirror-cdn)")
		return
	}
	for _, cp := range cps {
		if cp.Commit == "" {
			continue
		}
		note, err := opt.Mirror.ReadAt(ctx, cp.Commit, fmt.Sprintf("checkpoints/%d.txt", cp.Index))
		if err != nil {
			rep.warn("mirror at %s: %v", short(cp.Commit), err)
			continue
		}
		if note == nil {
			rep.problem("mirror at %s: no checkpoints/%d.txt", short(cp.Commit), cp.Index)
			continue
		}
		if strings.TrimSpace(string(note)) != strings.TrimSpace(cp.Note) {
			rep.problem("mirror at %s: checkpoints/%d.txt is not the log's note", short(cp.Commit), cp.Index)
		}
		hour := noteTime[cp.Index].UTC().Format("2006-01-02T15")
		raw, err := opt.Mirror.ReadAt(ctx, cp.Commit, "entries/"+hour+".json")
		if err != nil {
			rep.warn("mirror at %s: %v", short(cp.Commit), err)
			continue
		}
		var listed []MirrorEntry
		if raw != nil {
			if err := json.Unmarshal(raw, &listed); err != nil {
				rep.problem("mirror at %s: entries/%s.json: %v", short(cp.Commit), hour, err)
				continue
			}
		}
		key := func(e MirrorEntry) string { return fmt.Sprintf("%s/%s/%d", e.Kind, e.Hash, e.LogSeq) }
		want := map[string]bool{}
		for _, e := range entries[cp.Index] {
			want[key(e)] = true
		}
		got := map[string]bool{}
		for _, e := range listed {
			if e.Checkpoint == cp.Index {
				got[key(e)] = true
			}
		}
		for k := range want {
			if !got[k] {
				rep.problem("mirror at %s: entries/%s.json does not list %s of checkpoint %d", short(cp.Commit), hour, k, cp.Index)
			}
		}
		for k := range got {
			if !want[k] {
				rep.problem("mirror at %s: entries/%s.json lists %s, not an entry of checkpoint %d", short(cp.Commit), hour, k, cp.Index)
			}
		}
		rep.MirrorChecked++
	}
}
