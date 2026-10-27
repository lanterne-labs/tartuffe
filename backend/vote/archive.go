package vote

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/rs/zerolog/log"
)

// RelayEvent is one event of a relay as an auditor read it (its id and
// signature checked), with the time the auditor's own clock gave it. Read
// post hoc (DumpRelays) that time is only the reading's; read live
// (Archiver) it is the first sighting the withholding check of SPEC §12.6
// attributes with.
type RelayEvent struct {
	Relay     string    `json:"relay"`
	ID        string    `json:"id"`
	Pubkey    string    `json:"pubkey"`
	Kind      int       `json:"kind"`
	CreatedAt int64     `json:"created_at"`
	Content   string    `json:"content"`
	Sig       string    `json:"sig"`
	SeenAt    time.Time `json:"seen_at"`
	// Live: from the live subscription, not served from the relay's store
	// at a catch-up; only then is SeenAt a first sighting.
	Live bool `json:"live"`
}

// protocolKinds are every kind of the vote on the relays.
var protocolKinds = []int{KindNostrBallot, KindNostrRegister, KindNostrAck, KindNostrCheckpoint, KindNostrComplaint, KindNostrSnapshot}

// readRelay subscribes to every tagged event of a relay from since on and
// hands each to take, with live false until the relay's end of stored
// events; it returns at the end of stored events when once is set, else
// when ctx ends or the relay fails.
func readRelay(ctx context.Context, slug, relay string, since int64, once bool, take func(RelayEvent) error) error {
	dial, cancel := context.WithTimeout(ctx, 15*time.Second)
	c, err := nostr.RelayConnect(dial, relay)
	cancel()
	if err != nil {
		return fmt.Errorf("relay %s: %w", relay, err)
	}
	defer c.Close()
	ts := nostr.Timestamp(since)
	sub, err := c.Subscribe(ctx, nostr.Filters{{Kinds: protocolKinds, Tags: nostr.TagMap{"t": {EventTag(slug)}}, Since: &ts}})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", relay, err)
	}
	live := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sub.EndOfStoredEvents:
			if once {
				return nil
			}
			live = true
		case reason := <-sub.ClosedReason:
			return fmt.Errorf("relay %s closed the subscription: %s", relay, reason)
		case ev, ok := <-sub.Events:
			if !ok {
				return fmt.Errorf("relay %s: subscription closed", relay)
			}
			if ev == nil || ev.GetID() != ev.ID {
				continue
			}
			if ok, _ := ev.CheckSignature(); !ok {
				continue
			}
			e := RelayEvent{Relay: relay, ID: ev.ID, Pubkey: ev.PubKey, Kind: ev.Kind, CreatedAt: int64(ev.CreatedAt), Content: ev.Content, Sig: ev.Sig, SeenAt: time.Now().UTC(), Live: live}
			if err := take(e); err != nil {
				return err
			}
		}
	}
}

// DumpRelays reads every tagged event the relays hold, post hoc: what the
// verifier judges (SPEC §12.6). A relay that fails is reported, the others
// are read.
func DumpRelays(ctx context.Context, slug string, relays []string) ([]RelayEvent, []error) {
	var mu sync.Mutex
	var out []RelayEvent
	var errs []error
	var wg sync.WaitGroup
	for _, r := range relays {
		wg.Add(1)
		go func(relay string) {
			defer wg.Done()
			err := readRelay(ctx, slug, relay, 0, true, func(e RelayEvent) error {
				mu.Lock()
				out = append(out, e)
				mu.Unlock()
				return nil
			})
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(r)
	}
	wg.Wait()
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, errs
}

// Archiver keeps a continuous archive of the relays of a vote: every
// tagged event, one JSON line each, in <dir>/<relay host>/<day>.jsonl (the
// archiver's day). It never posts. Run it apart from our server, on a
// machine whose clock is right: its first sightings are what attributes
// an unacknowledged event (SPEC §12.6).
type Archiver struct {
	Dir    string
	Slug   string
	Relays []string

	mu   sync.Mutex
	seen map[string]bool // relay + id
}

// NewArchiver opens the archive of a directory, loading the ids it holds.
func NewArchiver(dir, slug string, relays []string) (*Archiver, error) {
	a := &Archiver{Dir: dir, Slug: slug, Relays: relays, seen: map[string]bool{}}
	events, err := ReadArchive(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		a.seen[e.Relay+" "+e.ID] = true
	}
	return a, nil
}

// Run reads every relay until ctx ends, reconnecting with a backoff; with
// once it reads what the relays hold and returns.
func (a *Archiver) Run(ctx context.Context, once bool) error {
	var wg sync.WaitGroup
	for _, r := range a.Relays {
		wg.Add(1)
		go func(relay string) {
			defer wg.Done()
			backoff := 2 * time.Second
			for ctx.Err() == nil {
				err := readRelay(ctx, a.Slug, relay, a.since(relay), once, a.Add)
				if err == nil || ctx.Err() != nil {
					return
				}
				log.Warn().Err(err).Str("relay", relay).Dur("retry", backoff).Msg("vote: archive read ended")
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return
				}
				if backoff < time.Minute {
					backoff *= 2
				}
			}
		}(r)
	}
	wg.Wait()
	return nil
}

// since is where a relay's catch-up starts: 2 h before the newest event
// archived from it, so a short outage loses nothing the relay kept.
func (a *Archiver) since(relay string) int64 {
	events, err := ReadArchive(filepath.Join(a.Dir, relayDir(relay)))
	if err != nil || len(events) == 0 {
		return 0
	}
	var newest int64
	for _, e := range events {
		if e.CreatedAt > newest {
			newest = e.CreatedAt
		}
	}
	return newest - 2*3600
}

// Add writes an event to the archive unless it holds it already.
func (a *Archiver) Add(e RelayEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := e.Relay + " " + e.ID
	if a.seen[key] {
		return nil
	}
	dir := filepath.Join(a.Dir, relayDir(e.Relay))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, e.SeenAt.UTC().Format("2006-01-02")+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, _ := json.Marshal(e)
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("archive %s: %w", e.Relay, err)
	}
	a.seen[key] = true
	return nil
}

// relayDir names a relay's directory: its host.
func relayDir(relay string) string {
	u, err := url.Parse(relay)
	if err != nil || u.Host == "" {
		return strings.NewReplacer("/", "_", ":", "_").Replace(relay)
	}
	return strings.ReplaceAll(u.Host, ":", "_")
}

// ReadArchive reads every event of an archive directory (or of one
// relay's), in the order they were seen; none for a missing directory.
func ReadArchive(dir string) ([]RelayEvent, error) {
	var out []RelayEvent
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == dir {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			var e RelayEvent
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			out = append(out, e)
		}
		return sc.Err()
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SeenAt.Before(out[j].SeenAt) })
	return out, nil
}
