package vote

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// Complaint is what a browser posts when its acknowledged ballot is not in
// the log (SPEC §7.2): the server's signed ack event, which the auditor
// judges against the log.
type Complaint struct {
	V       int         `json:"v"`
	Event   int64       `json:"event"`
	NostrID string      `json:"nostr_id"`
	PIHash  string      `json:"pi_hash,omitempty"`
	Ack     nostr.Event `json:"ack"`
}

// logged is where the log lists the protocol events: by pi_hash (ballots),
// by the hash of the canonical body (registrations), by Nostr id (rejects
// and seen), each with the checkpoint that holds the entry.
type logged struct {
	ballots   map[string]int64 // pi_hash → checkpoint index
	registers map[string]int64 // canonical body hash → checkpoint index
	rejects   map[string]rejectAt
	seen      map[string]int64 // nostr id → checkpoint index
	setups    []LoggedSetup
	cps       map[int64]Checkpoint
	snapshot  *LoggedSnapshot
}

type rejectAt struct {
	cp     int64
	reason string
}

// listed tells whether an event is in the log, and in which checkpoint:
// a ballot by its public inputs, a registration by its body, anything
// else by its id (a reject, a seen).
func (l *logged) listed(e RelayEvent) (cp int64, outcome, reason string, ok bool) {
	if r, has := l.rejects[e.ID]; has {
		return r.cp, "rejected", r.reason, true
	}
	if cp, has := l.seen[e.ID]; has {
		return cp, "logged", "", true
	}
	switch e.Kind {
	case KindNostrBallot:
		if p, err := ParseBallot([]byte(e.Content)); err == nil {
			for _, s := range l.setups {
				if s.Seq == p.Setup {
					if cp, has := l.ballots[p.PIHash(s.Setup.Mask)]; has {
						return cp, "logged", "", true
					}
				}
			}
		}
	case KindNostrRegister:
		var r Register
		if json.Unmarshal([]byte(e.Content), &r) == nil {
			if body, err := Canonical(r); err == nil {
				if cp, has := l.registers[BodyHash(body)]; has {
					return cp, "logged", "", true
				}
			}
		}
	}
	return 0, "", "", false
}

// honoured tells whether the log honours an acknowledgement: its
// checkpoint lists what it names, with its outcome.
func (l *logged) honoured(a Ack) error {
	if _, ok := l.cps[a.Checkpoint]; !ok {
		return fmt.Errorf("names checkpoint %d, which the log has not", a.Checkpoint)
	}
	switch a.Outcome {
	case "logged":
		if a.PIHash != "" {
			if cp, ok := l.ballots[a.PIHash]; ok && cp == a.Checkpoint {
				return nil
			}
			return fmt.Errorf("checkpoint %d does not list ballot %s", a.Checkpoint, a.PIHash)
		}
		if cp, ok := l.seen[a.NostrID]; ok && cp == a.Checkpoint {
			return nil
		}
		// A registration: the ack names the posted body's hash, the log the
		// canonical one; the relay event itself settles it (checkRelays).
		return fmt.Errorf("checkpoint %d does not list event %s", a.Checkpoint, a.NostrID)
	case "rejected":
		if r, ok := l.rejects[a.NostrID]; ok && r.cp == a.Checkpoint && r.reason == a.Reason {
			return nil
		}
		return fmt.Errorf("checkpoint %d does not list the reject of %s (%s)", a.Checkpoint, a.NostrID, a.Reason)
	}
	return fmt.Errorf("outcome %q", a.Outcome)
}

// checkRelays runs SPEC §12.6 and the relay parts of §12.1 and §12.8 over
// the events read from the relays (and an archive, with first sightings).
func (rep *Report) checkRelays(events []RelayEvent, l *logged, opt Options) {
	var setup *Setup
	if len(l.setups) > 0 {
		setup = &l.setups[len(l.setups)-1].Setup
	}
	byID := map[string]RelayEvent{}
	for _, e := range events {
		if _, ok := byID[e.ID]; !ok || (e.Live && !byID[e.ID].Live) {
			byID[e.ID] = e
		}
	}
	server := func(e RelayEvent) bool {
		if setup == nil || setup.NostrPubkey == "" {
			return true
		}
		return e.Pubkey == setup.NostrPubkey
	}
	if setup == nil || setup.NostrPubkey == "" {
		rep.warn("the setup names no nostr_pubkey: every ack and checkpoint on the relays is taken as our server's")
	}
	// The checkpoints on the relays: the log's, by index (no split view),
	// and when each was first seen (the archive's attribution clock).
	cpSeen := map[int64]time.Time{}
	acks := map[string]Ack{} // nostr id → the server's ack
	for _, e := range events {
		switch e.Kind {
		case KindNostrCheckpoint:
			if !server(e) {
				continue
			}
			_, _, index, _, _, err := ParseCheckpoint(noteText(e.Content))
			if err != nil {
				rep.problem("relay %s: checkpoint event %s is not a checkpoint note", e.Relay, short(e.ID))
				continue
			}
			if cp, ok := l.cps[index]; !ok {
				rep.problem("relay %s: checkpoint %d is on the relay, not in the log", e.Relay, index)
			} else if cp.Note != e.Content {
				rep.problem("relay %s: checkpoint %d differs from the log's (a split view)", e.Relay, index)
			}
			if e.Live {
				if t, ok := cpSeen[index]; !ok || e.SeenAt.Before(t) {
					cpSeen[index] = e.SeenAt
				}
			}
		case KindNostrAck:
			if !server(e) {
				continue
			}
			var a Ack
			if json.Unmarshal([]byte(e.Content), &a) != nil || a.V != Version {
				rep.problem("relay %s: ack event %s is not an ack", e.Relay, short(e.ID))
				continue
			}
			rep.Acks++
			if prev, dup := acks[a.NostrID]; dup && prev != a {
				rep.problem("relay %s: two different acks for event %s", e.Relay, short(a.NostrID))
			}
			acks[a.NostrID] = a
			if err := l.honoured(a); err != nil {
				// A registration's ack names the posted body's hash; the
				// event itself, when the relay still holds it, settles it.
				if ev, ok := byID[a.NostrID]; ok && a.Outcome == "logged" && a.PIHash == "" {
					if cp, _, _, listed := l.listed(ev); listed && cp == a.Checkpoint {
						continue
					}
				}
				rep.Withheld++
				rep.problem("withholding, proven: the ack of %s on %s %v", short(a.NostrID), e.Relay, err)
			}
		}
	}
	// The checkpoint an archived event had to be listed by: the first one
	// first seen 10 min after the event.
	var cpOrder []int64
	for i := range cpSeen {
		cpOrder = append(cpOrder, i)
	}
	sort.Slice(cpOrder, func(i, j int) bool { return cpOrder[i] < cpOrder[j] })
	dueBy := func(t time.Time) (int64, bool) {
		for _, i := range cpOrder {
			if !cpSeen[i].Before(t.Add(10 * time.Minute)) {
				return i, true
			}
		}
		return 0, false
	}
	// Every protocol event, by its acknowledgement.
	var snapshotIDs map[string]bool
	if opt.Snapshot != nil {
		snapshotIDs = map[string]bool{}
		for _, id := range opt.Snapshot.IDs {
			snapshotIDs[id] = true
		}
	}
	judged := map[string]bool{}
	for _, e := range events {
		switch e.Kind {
		case KindNostrBallot, KindNostrRegister, KindNostrComplaint:
		default:
			continue
		}
		if judged[e.ID] {
			continue
		}
		judged[e.ID] = true
		rep.RelayEvents++
		cp, _, _, listed := l.listed(e)
		_, acked := acks[e.ID]
		switch {
		case acked:
			// Judged with the ack above.
		case listed:
			rep.warn("relay %s: event %s is logged but its copy there was not acknowledged (a relay-copy discrepancy)", e.Relay, short(e.ID))
		default:
			rep.Unacknowledged++
			if due, ok := dueBy(e.SeenAt); ok && attributable(e) {
				rep.Withheld++
				rep.problem("withholding: %s %s seen on %s at %s, acknowledged by no one and not listed by checkpoint %d", kindName(e.Kind), short(e.ID), e.Relay, e.SeenAt.Format(time.RFC3339), due)
			} else {
				rep.warn("unacknowledged: %s %s on %s (created %s), never acknowledged nor logged", kindName(e.Kind), short(e.ID), e.Relay, time.Unix(e.CreatedAt, 0).UTC().Format(time.RFC3339))
			}
		}
		if listed && attributable(e) {
			if due, ok := dueBy(e.SeenAt); ok && cp > due {
				rep.Withheld++
				rep.problem("withholding: %s %s seen on %s at %s was listed by checkpoint %d, due by %d", kindName(e.Kind), short(e.ID), e.Relay, e.SeenAt.Format(time.RFC3339), cp, due)
			}
		}
		if e.Kind == KindNostrComplaint {
			rep.Complaints++
			rep.judgeComplaint(e, l, setup, byID)
		}
		if snapshotIDs != nil && setup != nil && time.Unix(e.CreatedAt, 0).Before(setup.Closes.Add(-5*time.Minute)) && !snapshotIDs[e.ID] {
			rep.problem("close: %s %s on %s, created %s, is not in the snapshot", kindName(e.Kind), short(e.ID), e.Relay, time.Unix(e.CreatedAt, 0).UTC().Format(time.RFC3339))
		}
	}
	// The snapshot on the relays: the log's root, in time.
	for _, e := range events {
		if e.Kind != KindNostrSnapshot || !server(e) {
			continue
		}
		var s Snapshot
		if json.Unmarshal([]byte(e.Content), &s) != nil || s.V != Version {
			rep.problem("relay %s: snapshot event %s is not a snapshot", e.Relay, short(e.ID))
			continue
		}
		if l.snapshot == nil {
			rep.problem("relay %s: a snapshot on the relay, none in the log", e.Relay)
		} else if l.snapshot.Body.Root != s.Root {
			rep.problem("relay %s: the snapshot there has root %s, the log's %s", e.Relay, short(s.Root), short(l.snapshot.Body.Root))
		}
		if setup != nil && e.Live && e.SeenAt.After(setup.Closes.Add(2*time.Minute)) {
			rep.problem("relay %s: the snapshot was first seen at %s, after T + 2 min", e.Relay, e.SeenAt.Format(time.RFC3339))
		}
		if opt.Snapshot != nil {
			ids := append([]string(nil), opt.Snapshot.IDs...)
			if SnapshotRoot(ids) != s.Root {
				rep.problem("the snapshot list is not the snapshot's root")
			}
		}
	}
}

// attributable tells whether an unacknowledged event seen live is the
// server's fault (SPEC §12.6): its created_at must be within an hour of
// its first sighting. The server reads a relay from the newest time it
// delivered minus two hours; an event dated earlier is never delivered to
// any live reader, and a browser's clock set that far back is the
// browser's fault (it corrects its clock from the checkpoints).
func attributable(e RelayEvent) bool {
	return e.Live && e.SeenAt.Sub(time.Unix(e.CreatedAt, 0)) <= time.Hour
}

// judgeComplaint checks a complaint's ack: signed by our server, and
// honoured by the log, else withholding proven. A registration's ack is
// settled by the event itself when a relay still holds it: the ack names
// the posted body's hash, the log the canonical one.
func (rep *Report) judgeComplaint(e RelayEvent, l *logged, setup *Setup, byID map[string]RelayEvent) {
	var c Complaint
	if json.Unmarshal([]byte(e.Content), &c) != nil || c.V != Version {
		rep.warn("relay %s: complaint %s is not a complaint", e.Relay, short(e.ID))
		return
	}
	if c.Ack.GetID() != c.Ack.ID {
		rep.warn("relay %s: complaint %s carries an ack with a wrong id", e.Relay, short(e.ID))
		return
	}
	if ok, _ := c.Ack.CheckSignature(); !ok {
		rep.warn("relay %s: complaint %s carries an ack that is not signed", e.Relay, short(e.ID))
		return
	}
	if setup != nil && setup.NostrPubkey != "" && c.Ack.PubKey != setup.NostrPubkey {
		rep.warn("relay %s: complaint %s carries an ack not signed by our server", e.Relay, short(e.ID))
		return
	}
	var a Ack
	if json.Unmarshal([]byte(c.Ack.Content), &a) != nil || a.NostrID != c.NostrID {
		rep.warn("relay %s: complaint %s carries an ack of another event", e.Relay, short(e.ID))
		return
	}
	if err := l.honoured(a); err != nil {
		if a.Outcome == "logged" && a.PIHash == "" {
			if ev, ok := byID[a.NostrID]; ok {
				if cp, _, _, listed := l.listed(ev); listed && cp == a.Checkpoint {
					return
				}
			} else {
				rep.warn("relay %s: complaint %s about registration %s, which no relay holds any more: not judged", e.Relay, short(e.ID), short(a.NostrID))
				return
			}
		}
		rep.Withheld++
		rep.problem("withholding, proven by the complaint %s on %s: the ack of %s %v", short(e.ID), e.Relay, short(a.NostrID), err)
	}
}

// short abbreviates an id or a hash for a finding.
func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func kindName(k int) string {
	switch k {
	case KindNostrBallot:
		return "ballot"
	case KindNostrRegister:
		return "registration"
	case KindNostrComplaint:
		return "complaint"
	}
	return fmt.Sprintf("kind %d", k)
}
