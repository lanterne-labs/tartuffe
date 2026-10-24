package polls

import (
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"time"

	"digital-mirror/backend/polls/semaphore"
)

// Audit replays the public log from its first entry, as anyone can, and
// reports every entry that does not follow from the ones before:
//   - a registration: one document holds one commitment at a time, a
//     commitment belongs to one document, a new one replaces the last one;
//   - a root: the tree of the commitments the registrations admitted to the
//     window, in order, as they stood at that entry;
//   - a ballot: its proof is valid, for its poll, window and text, against
//     a root the window's tree had before, and its seq is new;
//   - a tally: the count (Tally) of the last ballot of each voter of the
//     window, published before it.
//
// It checks what the log says, not that the log holds every ballot cast:
// AuditRelays compares it with the ballots on the relays.
func Audit(entries []LogEntry) AuditReport {
	a := &auditor{
		docs:       map[string]*auditKey{},
		owner:      map[string]string{},
		roots:      map[Window]map[string]bool{},
		trees:      map[Window]*semaphore.Tree{},
		inserted:   map[Window]int{},
		ballots:    map[ballotKey]AnonBallot{},
		lastSeq:    map[voterKey]int{},
		Published:  map[string]bool{},
		Superseded: map[voterKey]int{},
	}
	for _, e := range entries {
		a.entry(e)
	}
	a.report.Entries = len(entries)
	a.report.Published, a.report.LastSeq, a.report.WindowRoots = a.Published, a.Superseded, a.roots
	return a.report
}

// AuditReport is what Audit found.
type AuditReport struct {
	Entries, Registrations, Roots, Ballots, Tallies int
	// Problems are the entries that do not follow, by seq.
	Problems []string
	// Published holds the ballots of the log (BallotID), LastSeq the
	// highest seq of each voter of a window: AuditRelays reads them.
	Published   map[string]bool
	LastSeq     map[voterKey]int
	WindowRoots map[Window]map[string]bool
}

// RelayBallot is a ballot found on a relay, with the hour its event is
// dated.
type RelayBallot struct {
	Ballot AnonBallot
	At     time.Time
	Relay  string
}

// AuditRelays finds the ballots of the relays the log should hold and does
// not: valid, proved against a root of the log, of a window still open an
// hour after they were sent, sent more than maxDelay before end (the log's
// last entry), and with no ballot of the same voter and a seq as high in
// the log.
func AuditRelays(r AuditReport, found []RelayBallot, end time.Time, maxDelay time.Duration) []string {
	var missing []string
	seen := map[string]bool{}
	for _, f := range found {
		b := f.Ballot
		id := BallotID(b)
		if seen[id] || r.Published[id] {
			continue
		}
		seen[id] = true
		if b.Series < 0 || b.Series >= Series || CheckBallotProof(b) != nil || !r.WindowRoots[b.Window][b.Proof.MerkleTreeRoot] {
			continue
		}
		if f.At.Add(maxDelay).After(end) || f.At.Add(time.Hour).After(b.Window.End()) {
			continue
		}
		if r.LastSeq[voterKey{event: b.EventID, w: b.Window, null: b.Proof.Nullifier}] >= b.Seq {
			continue
		}
		missing = append(missing, fmt.Sprintf("ballot %s, on %s since %s, is not in the log", id, f.Relay, f.At.UTC().Format(time.RFC3339)))
	}
	return missing
}

// BallotID names a ballot: its poll, window, nullifier and seq.
func BallotID(b AnonBallot) string {
	return fmt.Sprintf("%d/%d/%d/%s/%d", b.EventID, b.Series, b.Index, b.Proof.Nullifier, b.Seq)
}

type auditKey struct {
	commitment string
	from       [2]int
	// pos is the registration's place among the registrations.
	pos int
}

type ballotKey struct {
	event int64
	w     Window
	null  string
}

type voterKey = ballotKey

type auditor struct {
	report AuditReport
	// docs is the current key of each document; owner the document of each
	// commitment.
	docs  map[string]*auditKey
	owner map[string]string
	// keys are the commitments in registration order, with until (the
	// windows they stop at, by series: -1 none).
	keys []auditLeaf
	// roots are the roots each window's tree had; trees the windows' trees
	// so far, of the first inserted keys valid in them.
	roots    map[Window]map[string]bool
	trees    map[Window]*semaphore.Tree
	inserted map[Window]int
	// ballots is the last published ballot of each voter of a window.
	ballots    map[ballotKey]AnonBallot
	lastSeq    map[voterKey]int
	Published  map[string]bool
	Superseded map[voterKey]int
}

type auditLeaf struct {
	commitment *big.Int
	from       [2]int
	until      [2]int
}

func (a *auditor) problem(e LogEntry, format string, args ...any) {
	a.report.Problems = append(a.report.Problems, fmt.Sprintf("entry %d (%s): %s", e.Seq, e.Kind, fmt.Sprintf(format, args...)))
}

func (a *auditor) entry(e LogEntry) {
	switch e.Kind {
	case LogRegister:
		a.report.Registrations++
		var r registerEntry
		if err := json.Unmarshal([]byte(e.Body), &r); err != nil {
			a.problem(e, "unreadable: %v", err)
			return
		}
		a.register(e, r)
	case LogRoot:
		a.report.Roots++
		var r rootEntry
		if err := json.Unmarshal([]byte(e.Body), &r); err != nil {
			a.problem(e, "unreadable: %v", err)
			return
		}
		a.root(e, r)
	case LogBallot:
		a.report.Ballots++
		var b AnonBallot
		if err := json.Unmarshal([]byte(e.Body), &b); err != nil {
			a.problem(e, "unreadable: %v", err)
			return
		}
		a.ballot(e, b)
	case LogTally:
		a.report.Tallies++
		var t tallyEntry
		if err := json.Unmarshal([]byte(e.Body), &t); err != nil {
			a.problem(e, "unreadable: %v", err)
			return
		}
		a.tally(e, t)
	default:
		a.problem(e, "unknown kind")
	}
}

func (a *auditor) register(e LogEntry, r registerEntry) {
	c, err := semaphore.ParseField(r.Commitment)
	if err != nil || c.Sign() == 0 {
		a.problem(e, "the commitment is not a field element")
		return
	}
	if doc, ok := a.owner[r.Commitment]; ok && doc != r.Document {
		a.problem(e, "the commitment belongs to another document")
		return
	}
	old := a.docs[r.Document]
	switch {
	case old == nil && r.Replaces != "":
		a.problem(e, "replaces a commitment the document never had")
	case old != nil && r.Replaces != old.commitment:
		a.problem(e, "the document's key is %s, not the one it replaces", old.commitment)
	}
	if old != nil {
		// The old key stops where the new one starts (it never voted if it
		// was itself waiting: then both start at the same windows).
		leaf := &a.keys[old.pos]
		for s := range 2 {
			if r.From[s] < leaf.from[s] {
				a.problem(e, "the new key starts before the old one")
			}
			leaf.until[s] = r.From[s]
		}
	}
	a.owner[r.Commitment] = r.Document
	a.docs[r.Document] = &auditKey{commitment: r.Commitment, from: r.From, pos: len(a.keys)}
	a.keys = append(a.keys, auditLeaf{commitment: c, from: r.From, until: [2]int{-1, -1}})
}

// validIn tells whether the key votes in the window.
func (l auditLeaf) validIn(w Window) bool {
	return l.from[w.Series] <= w.Index && (l.until[w.Series] < 0 || w.Index < l.until[w.Series])
}

func (a *auditor) root(e LogEntry, r rootEntry) {
	if r.Series < 0 || r.Series >= Series {
		a.problem(e, "no series %d", r.Series)
		return
	}
	t := a.trees[r.Window]
	if t == nil {
		t, _ = semaphore.NewTree(nil)
		a.trees[r.Window] = t
	}
	// The window's tree holds, in registration order, every key valid in it
	// registered so far. A key never leaves a window that has a root: a
	// replacement starts at the next window.
	for i := a.inserted[r.Window]; i < len(a.keys); i++ {
		if a.keys[i].validIn(r.Window) {
			if err := t.Insert(a.keys[i].commitment); err != nil {
				a.problem(e, "tree: %v", err)
				return
			}
		}
	}
	a.inserted[r.Window] = len(a.keys)
	if got := t.Root().String(); got != r.Root || t.Size() != r.Size {
		a.problem(e, "the window's tree of %d keys has root %s, the log says %s of %d", t.Size(), got, r.Root, r.Size)
		return
	}
	if a.roots[r.Window] == nil {
		a.roots[r.Window] = map[string]bool{}
	}
	a.roots[r.Window][r.Root] = true
}

func (a *auditor) ballot(e LogEntry, b AnonBallot) {
	switch b.Kind {
	case KindChoices:
		seen := map[int64]bool{}
		for i, id := range b.Choices {
			if seen[id] {
				a.problem(e, "ranks %d twice", id)
			}
			if id == BlankID && (i == 0 || i != len(b.Choices)-1) {
				a.problem(e, "the blank vote is not last after a candidate")
			}
			seen[id] = true
		}
		if len(b.Choices) == 0 {
			a.problem(e, "a ranking of no candidate")
		}
	case KindBlank, KindWithdraw:
		if len(b.Choices) != 0 {
			a.problem(e, "a %s ballot names candidates", b.Kind)
		}
	default:
		a.problem(e, "kind %q", b.Kind)
	}
	if err := CheckBallotProof(b); err != nil {
		a.problem(e, "%v", err)
		return
	}
	if !a.roots[b.Window][b.Proof.MerkleTreeRoot] {
		a.problem(e, "proves against a root window %v never had", b.Window)
		return
	}
	id := BallotID(b)
	if a.Published[id] {
		a.problem(e, "published twice")
		return
	}
	a.Published[id] = true
	k := ballotKey{event: b.EventID, w: b.Window, null: b.Proof.Nullifier}
	if b.Seq > a.lastSeq[k] {
		a.lastSeq[k] = b.Seq
		a.ballots[k] = b
	}
	a.Superseded[k] = a.lastSeq[k]
}

func (a *auditor) tally(e LogEntry, t tallyEntry) {
	candidates := slices.Sorted(maps.Keys(t.First))
	count := NewTally(candidates)
	keys := slices.SortedFunc(maps.Keys(a.ballots), func(x, y ballotKey) int {
		if x.null < y.null {
			return -1
		}
		if x.null > y.null {
			return 1
		}
		return 0
	})
	for _, k := range keys {
		if k.event == t.EventID && k.w == t.Window {
			b := a.ballots[k]
			count.Add(b.Kind, b.Choices)
		}
	}
	got, turnout := count.Result()
	if turnout.Voters != t.Voters || turnout.Blank != t.Blank || turnout.BlankRanked != t.BlankRanked {
		a.problem(e, "counts %d voters, %d blank and %d placing the blank vote, the log says %d, %d and %d", turnout.Voters, turnout.Blank, turnout.BlankRanked, t.Voters, t.Blank, t.BlankRanked)
	}
	for _, d := range candidates {
		if turnout.BlankBeats[d] != t.BlankBeats[d] {
			a.problem(e, "the blank vote above %d on %d ballots, the log says %d", d, turnout.BlankBeats[d], t.BlankBeats[d])
		}
	}
	for _, c := range got {
		if c.FirstChoices != t.First[c.PersonID] || c.Ranked != t.Ranked[c.PersonID] {
			a.problem(e, "candidate %d: %d first choices and %d ranked, the log says %d and %d", c.PersonID, c.FirstChoices, c.Ranked, t.First[c.PersonID], t.Ranked[c.PersonID])
		}
		for _, d := range append(slices.Clone(candidates), BlankID) {
			if c.Beats[d] != t.Beats[c.PersonID][d] {
				a.problem(e, "candidate %d above %d on %d ballots, the log says %d", c.PersonID, d, c.Beats[d], t.Beats[c.PersonID][d])
			}
		}
	}
}
