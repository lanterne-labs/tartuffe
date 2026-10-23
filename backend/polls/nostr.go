package polls

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"digital-mirror/backend/metrics"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// Ballots come through public Nostr relays (NIP-01): the browser publishes
// each ballot as an event of NostrKind, signed by a key of its own made for
// that event, to several relays run by others; the API reads them from the
// relays at each batch. So the API never sees the voter's address, and a
// ballot it would drop stays on the relays, where anyone finds it (dm poll
// verify --relays). The event's created_at is the hour it was sent in, and
// the API reads once an hour: neither tells the API when a page sent it.
// The relays see the address, as any site the browser opens does.

// NostrKind is the kind of the ballot events: a regular event (1000-9999),
// which relays keep.
const NostrKind = 7464

// NostrTag is the t tag of the ballot events, what a relay is asked for.
const NostrTag = "tartuffe-ballot-v1"

// DefaultRelays are the relays the browser publishes to and the API reads.
var DefaultRelays = []string{
	"wss://nos.lol",
	"wss://relay.primal.net",
	"wss://offchain.pub",
	"wss://nostr.mom",
	"wss://relay.snort.social",
}

// nostrEvent is a Nostr event (NIP-01). The API does not check its
// signature: the ballot inside carries its own proof.
type nostrEvent struct {
	ID        string     `json:"id"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
}

// relayReadTimeout bounds one read of a relay.
const relayReadTimeout = 45 * time.Second

// relaySlack reads again the events of the hour before the last one seen:
// an event's created_at is the hour it was sent in, so events of that hour
// may reach a relay after the last read.
const relaySlack = 2 * time.Hour

// ReadRelays reads the new ballot events of each relay and casts their
// ballots, as if posted (Service.accept).
func (s *Service) ReadRelays(ctx context.Context) {
	for _, relay := range s.relays {
		n, err := s.readRelay(ctx, relay)
		if err != nil {
			metrics.NostrRelayReadsTotal.WithLabelValues(relay, "error").Inc()
			log.Warn().Err(err).Str("relay", relay).Msg("read ballots from Nostr relay")
			continue
		}
		metrics.NostrRelayReadsTotal.WithLabelValues(relay, "ok").Inc()
		if n > 0 {
			log.Info().Str("relay", relay).Int("events", n).Msg("ballots read from Nostr relay")
		}
	}
}

// readRelay reads the relay's ballot events since its cursor, and returns
// how many it read.
func (s *Service) readRelay(ctx context.Context, relay string) (int, error) {
	since, err := s.box.relayCursor(ctx, relay)
	if err != nil {
		return 0, err
	}
	if since > 0 {
		since -= int64(relaySlack / time.Second)
	}
	latest, n := since, 0
	err = relayEvents(ctx, relay, since, func(ev nostrEvent) {
		n++
		latest = max(latest, ev.CreatedAt)
		s.acceptEvent(ctx, relay, ev)
	})
	if err != nil {
		return n, err
	}
	return n, s.box.setRelayCursor(ctx, relay, latest)
}

// relayEvents calls each on every ballot event of the relay dated since
// (0: all) and returns at the relay's end of stored events.
func relayEvents(ctx context.Context, relay string, since int64, each func(nostrEvent)) error {
	ctx, cancel := context.WithTimeout(ctx, relayReadTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, relay, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	filter := map[string]any{"kinds": []int{NostrKind}, "#t": []string{NostrTag}}
	if since > 0 {
		filter["since"] = since
	}
	req, _ := json.Marshal([]any{"REQ", "ballots", filter})
	if err := conn.Write(ctx, websocket.MessageText, req); err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		var msg []json.RawMessage
		if err := json.Unmarshal(raw, &msg); err != nil || len(msg) == 0 {
			continue
		}
		var label string
		json.Unmarshal(msg[0], &label)
		switch label {
		case "EVENT":
			if len(msg) < 3 {
				continue
			}
			var ev nostrEvent
			if err := json.Unmarshal(msg[2], &ev); err != nil {
				continue
			}
			each(ev)
		case "EOSE":
			conn.Write(ctx, websocket.MessageText, []byte(`["CLOSE","ballots"]`))
			conn.Close(websocket.StatusNormalClosure, "")
			return nil
		case "CLOSED":
			return fmt.Errorf("the relay closed the request: %s", raw)
		case "NOTICE":
			log.Debug().Str("relay", relay).RawJSON("notice", raw).Msg("Nostr relay notice")
		}
	}
}

// RelayBallots returns the ballots of every ballot event the relay holds
// (events without a readable ballot are skipped): what a verifier compares
// with the log.
func RelayBallots(ctx context.Context, relay string) ([]RelayBallot, error) {
	var out []RelayBallot
	err := relayEvents(ctx, relay, 0, func(ev nostrEvent) {
		var b AnonBallot
		if ev.Kind != NostrKind || len(ev.Content) > maxBallotBody || json.Unmarshal([]byte(ev.Content), &b) != nil {
			return
		}
		out = append(out, RelayBallot{Ballot: b, At: time.Unix(ev.CreatedAt, 0), Relay: relay})
	})
	return out, err
}

// acceptEvent casts the ballot of an event. Anyone can publish events of
// this kind: one that holds no valid ballot is dropped. An event seen
// already (on another relay, or in the slack of the last read) is skipped
// before its proof is checked again.
func (s *Service) acceptEvent(ctx context.Context, relay string, ev nostrEvent) {
	if ev.Kind != NostrKind || len(ev.Content) > maxBallotBody || s.seenEvent(ev) {
		return
	}
	var b AnonBallot
	if err := json.Unmarshal([]byte(ev.Content), &b); err != nil {
		metrics.BallotChangesTotal.WithLabelValues("nostr", codeInvalid).Inc()
		log.Debug().Str("relay", relay).Str("event", ev.ID).Err(err).Msg("Nostr event holds no ballot")
		return
	}
	e, err := s.eventByID(ctx, b.EventID)
	if err != nil {
		log.Error().Err(err).Msg("polls of a Nostr ballot")
		return
	}
	if e == nil {
		metrics.BallotChangesTotal.WithLabelValues("nostr", codeInvalid).Inc()
		return
	}
	code, err := s.accept(ctx, e, b, time.Now())
	metrics.BallotChangesTotal.WithLabelValues("nostr", code).Inc()
	switch {
	case code == "ok":
	case code == codeStaleSeq:
		// The same ballot from another relay, or one a newer replaced.
	case err != nil && code == "":
		log.Error().Err(err).Str("relay", relay).Str("event", ev.ID).Msg("cast a Nostr ballot")
	default:
		log.Debug().Err(err).Str("relay", relay).Str("event", ev.ID).Str("code", code).Msg("Nostr ballot refused")
	}
}

// seenEvent tells whether the event was read before, and remembers it; it
// forgets the events older than the slack.
func (s *Service) seenEvent(ev nostrEvent) bool {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	if s.seen == nil {
		s.seen = map[string]int64{}
	}
	if _, ok := s.seen[ev.ID]; ok {
		return true
	}
	old := time.Now().Add(-2 * relaySlack).Unix()
	for id, at := range s.seen {
		if at < old {
			delete(s.seen, id)
		}
	}
	s.seen[ev.ID] = ev.CreatedAt
	return false
}

// eventByID returns the poll of the id, nil if there is none.
func (s *Service) eventByID(ctx context.Context, id int64) (*Event, error) {
	events, err := s.store.ListEvents(ctx)
	if err != nil {
		return nil, err
	}
	for i := range events {
		if events[i].ID == id {
			return &events[i], nil
		}
	}
	return nil, nil
}

// relayCursor is the created_at of the last ballot event read from the
// relay (0 before the first read).
func (s *PGStore) relayCursor(ctx context.Context, relay string) (int64, error) {
	var since int64
	err := s.pool.QueryRow(ctx, `SELECT since FROM nostr_cursors WHERE relay = $1`, relay).Scan(&since)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("nostr cursor: %w", err)
	}
	return since, nil
}

func (s *PGStore) setRelayCursor(ctx context.Context, relay string, since int64) error {
	if _, err := s.pool.Exec(ctx, `INSERT INTO nostr_cursors (relay, since) VALUES ($1, $2)
		ON CONFLICT (relay) DO UPDATE SET since = GREATEST(nostr_cursors.since, EXCLUDED.since)`, relay, since); err != nil {
		return fmt.Errorf("nostr cursor: %w", err)
	}
	return nil
}

// ParseRelays reads a list of relay URLs, separated by commas.
func ParseRelays(s string) ([]string, error) {
	var out []string
	for _, r := range strings.Split(s, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if !strings.HasPrefix(r, "wss://") && !strings.HasPrefix(r, "ws://") {
			return nil, fmt.Errorf("relay %q is not a websocket URL", r)
		}
		out = append(out, r)
	}
	return out, nil
}
