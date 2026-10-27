package vote

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

// The relay probe (SPEC §7.1 requirements, measured): what a relay says of
// itself (NIP-11), whether it takes a ballot-sized voter event and a
// server event of our kinds, and whether it still serves the ones posted
// by earlier runs: the retention the post-hoc check of §12.6 rests on.

// ProbeTag is the tag of the probe's events: a vote of its own.
const ProbeTag = "tartuffe-vote/2/probe"

// ProbeState is what a probe file keeps between runs: the events posted.
type ProbeState struct {
	Posted []ProbePost `json:"posted"`
}

// ProbePost is one event posted by a run.
type ProbePost struct {
	Relay string    `json:"relay"`
	ID    string    `json:"id"`
	Kind  int       `json:"kind"`
	Size  int       `json:"size"`
	At    time.Time `json:"at"`
}

// ProbeResult is what one run found of a relay.
type ProbeResult struct {
	Relay string
	// NIP-11.
	Name, Software            string
	MaxMessage                int
	Auth, Payment, Restricted bool
	MinPow                    int
	Retention                 string
	InfoErr                   string
	// This run's posts: a ballot-sized voter event, a server-kind event.
	BallotPost, ServerPost string // "" when taken, else the refusal
	// Earlier posts: how many this run read back, how old the oldest
	// still served and the youngest missing are.
	Kept, Lost   int
	OldestKept   time.Duration
	YoungestLost time.Duration
}

// LoadProbeState reads a probe file; empty when it does not exist.
func LoadProbeState(path string) (*ProbeState, error) {
	st := &ProbeState{}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	return st, json.Unmarshal(raw, st)
}

// Save writes the probe file.
func (st *ProbeState) Save(path string) error {
	raw, _ := json.MarshalIndent(st, "", " ")
	return os.WriteFile(path, raw, 0o644)
}

// ProbeRelay measures one relay: NIP-11, a post of each kind, and what
// it still serves of the state's earlier posts there. The new posts are
// appended to the state.
func ProbeRelay(ctx context.Context, relay string, st *ProbeState, serverKey *NostrKey, now time.Time) ProbeResult {
	res := ProbeResult{Relay: relay}
	ictx, cancel := context.WithTimeout(ctx, 15*time.Second)
	info, err := nip11.Fetch(ictx, relay)
	cancel()
	if err != nil {
		res.InfoErr = err.Error()
	} else {
		res.Name, res.Software = info.Name, info.Software
		if l := info.Limitation; l != nil {
			res.MaxMessage, res.Auth, res.Payment, res.Restricted, res.MinPow = l.MaxMessageLength, l.AuthRequired, l.PaymentRequired, l.RestrictedWrites, l.MinPowDifficulty
		}
		for _, r := range info.Retention {
			if r == nil {
				continue
			}
			if res.Retention != "" {
				res.Retention += "; "
			}
			switch {
			case r.Time == 0 && r.Count == 0:
				res.Retention += fmt.Sprintf("kinds %v: unlimited", r.Kinds)
			case r.Time == 0:
				res.Retention += fmt.Sprintf("kinds %v: %d events", r.Kinds, r.Count)
			default:
				res.Retention += fmt.Sprintf("kinds %v: %dd", r.Kinds, r.Time/86400)
			}
		}
	}
	dial, cancel := context.WithTimeout(ctx, 15*time.Second)
	c, err := nostr.RelayConnect(dial, relay)
	cancel()
	if err != nil {
		res.BallotPost, res.ServerPost = err.Error(), err.Error()
		return res
	}
	defer c.Close()

	// What earlier runs posted here.
	var ids []string
	byID := map[string]ProbePost{}
	for _, p := range st.Posted {
		if p.Relay == relay {
			ids = append(ids, p.ID)
			byID[p.ID] = p
		}
	}
	if len(ids) > 0 {
		qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		got, err := c.QuerySync(qctx, nostr.Filter{IDs: ids})
		cancel()
		found := map[string]bool{}
		if err == nil {
			for _, ev := range got {
				found[ev.ID] = true
			}
		}
		for _, id := range ids {
			age := now.Sub(byID[id].At)
			if found[id] {
				res.Kept++
				if age > res.OldestKept {
					res.OldestKept = age
				}
			} else {
				res.Lost++
				if res.YoungestLost == 0 || age < res.YoungestLost {
					res.YoungestLost = age
				}
			}
		}
	}

	// A ballot-sized voter event (a throwaway key) and a server event.
	body := make([]byte, 15*1024)
	_, _ = rand.Read(body)
	content := base64.StdEncoding.EncodeToString(body) // about 20 KB, a ballot's size
	post := func(kind int, content, secret string) (string, int, error) {
		ev := nostr.Event{CreatedAt: nostr.Timestamp(now.Unix()), Kind: kind, Tags: nostr.Tags{{"t", ProbeTag}}, Content: content}
		if err := ev.Sign(secret); err != nil {
			return "", 0, err
		}
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		raw, _ := json.Marshal(ev)
		return ev.ID, len(raw), c.Publish(pctx, ev)
	}
	if id, size, err := post(KindNostrBallot, content, nostr.GeneratePrivateKey()); err != nil {
		res.BallotPost = err.Error()
	} else {
		st.Posted = append(st.Posted, ProbePost{Relay: relay, ID: id, Kind: KindNostrBallot, Size: size, At: now})
	}
	ack, _ := Canonical(Ack{V: Version, NostrID: "probe", BodyHash: BodyHash(body), Checkpoint: 1, Outcome: "logged"})
	if id, size, err := post(KindNostrAck, string(ack), serverKey.secret); err != nil {
		res.ServerPost = err.Error()
	} else {
		st.Posted = append(st.Posted, ProbePost{Relay: relay, ID: id, Kind: KindNostrAck, Size: size, At: now})
	}
	return res
}

// ProbeRelays measures every relay, one after the other, and returns the
// results in the relays' order.
func ProbeRelays(ctx context.Context, relays []string, st *ProbeState, serverKey *NostrKey) []ProbeResult {
	now := time.Now().UTC()
	var out []ProbeResult
	for _, r := range relays {
		out = append(out, ProbeRelay(ctx, r, st, serverKey, now))
	}
	sort.SliceStable(st.Posted, func(i, j int) bool { return st.Posted[i].At.Before(st.Posted[j].At) })
	return out
}
