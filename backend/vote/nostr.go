package vote

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/rs/zerolog/log"
)

// The Nostr mailbox: one box per relay. Voters' events are regular events
// of these kinds, tagged t = tartuffe-vote/2/<slug>, signed by throwaway
// keys; the server's events are signed by its Nostr key (SPEC §7.1).
const (
	KindNostrBallot     = 17020
	KindNostrRegister   = 17021
	KindNostrAck        = 17022
	KindNostrCheckpoint = 17023
	KindNostrComplaint  = 17024
	KindNostrSnapshot   = 17025
)

// EventTag is the tag of every event of a vote.
func EventTag(slug string) string { return "tartuffe-vote/2/" + slug }

// NostrKey is our server's Nostr key (VOTE_NOSTR_KEY, 32 bytes of hex).
type NostrKey struct {
	secret string
	Public string
}

// NostrKeyFromEnv reads VOTE_NOSTR_KEY.
func NostrKeyFromEnv() (*NostrKey, error) { return NewNostrKey(os.Getenv("VOTE_NOSTR_KEY")) }

// NewNostrKey parses a secret key.
func NewNostrKey(secret string) (*NostrKey, error) {
	b, err := hex.DecodeString(secret)
	if err != nil || len(b) != 32 {
		return nil, errors.New("VOTE_NOSTR_KEY: 32 bytes of hex (openssl rand -hex 32)")
	}
	pub, err := nostr.GetPublicKey(secret)
	if err != nil {
		return nil, fmt.Errorf("VOTE_NOSTR_KEY: %w", err)
	}
	return &NostrKey{secret: secret, Public: pub}, nil
}

// NostrMailbox implements Mailbox over Nostr relays.
type NostrMailbox struct {
	slug  string
	key   *NostrKey
	urls  []string
	mu    sync.Mutex
	conns map[string]*nostr.Relay
}

// NewNostrMailbox prepares the relays of a vote (connections open lazily).
func NewNostrMailbox(slug string, key *NostrKey, urls []string) *NostrMailbox {
	return &NostrMailbox{slug: slug, key: key, urls: urls, conns: map[string]*nostr.Relay{}}
}

// Boxes implements Mailbox.
func (m *NostrMailbox) Boxes() []string { return m.urls }

func (m *NostrMailbox) conn(ctx context.Context, url string) (*nostr.Relay, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.conns[url]; ok && c.IsConnected() {
		return c, nil
	}
	dial, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := nostr.RelayConnect(dial, url)
	if err != nil {
		return nil, fmt.Errorf("relay %s: %w", url, err)
	}
	m.conns[url] = c
	return c, nil
}

func outgoingKind(k OutgoingKind) int {
	switch k {
	case OutAck:
		return KindNostrAck
	case OutCheckpoint:
		return KindNostrCheckpoint
	default:
		return KindNostrSnapshot
	}
}

func (m *NostrMailbox) sign(msg Outgoing) (nostr.Event, error) {
	ev := nostr.Event{
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Kind:      outgoingKind(msg.Kind),
		Tags:      nostr.Tags{{"t", EventTag(m.slug)}},
		Content:   msg.Body,
	}
	if err := ev.Sign(m.key.secret); err != nil {
		return ev, fmt.Errorf("sign nostr event: %w", err)
	}
	return ev, nil
}

// Post implements Mailbox.
func (m *NostrMailbox) Post(ctx context.Context, url string, msg Outgoing) error {
	ev, err := m.sign(msg)
	if err != nil {
		return err
	}
	c, err := m.conn(ctx, url)
	if err != nil {
		return err
	}
	pub, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := c.Publish(pub, ev); err != nil {
		m.mu.Lock()
		delete(m.conns, url)
		m.mu.Unlock()
		return fmt.Errorf("publish to %s: %w", url, err)
	}
	return nil
}

// PostAll implements Mailbox.
func (m *NostrMailbox) PostAll(ctx context.Context, msg Outgoing) int {
	ok := 0
	for _, u := range m.urls {
		if err := m.Post(ctx, u, msg); err != nil {
			log.Warn().Err(err).Str("relay", u).Msg("vote: post failed")
			continue
		}
		ok++
	}
	return ok
}

// Read implements Mailbox: a live subscription from since on. The event's
// id and signature are checked; the kinds map to message kinds.
func (m *NostrMailbox) Read(ctx context.Context, url string, since int64, out chan<- Message) error {
	dial, cancel := context.WithTimeout(ctx, 15*time.Second)
	c, err := nostr.RelayConnect(dial, url)
	cancel()
	if err != nil {
		return fmt.Errorf("relay %s: %w", url, err)
	}
	defer c.Close()
	ts := nostr.Timestamp(since)
	filter := nostr.Filter{
		Kinds: []int{KindNostrBallot, KindNostrRegister, KindNostrComplaint},
		Tags:  nostr.TagMap{"t": {EventTag(m.slug)}},
		Since: &ts,
	}
	sub, err := c.Subscribe(ctx, nostr.Filters{filter})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", url, err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-sub.Events:
			if !ok {
				return fmt.Errorf("relay %s: subscription closed", url)
			}
			if ev == nil || ev.GetID() != ev.ID {
				continue
			}
			if ok, _ := ev.CheckSignature(); !ok {
				continue
			}
			var kind MessageKind
			switch ev.Kind {
			case KindNostrBallot:
				kind = MsgBallot
			case KindNostrRegister:
				kind = MsgRegister
			case KindNostrComplaint:
				kind = MsgComplaint
			default:
				continue
			}
			select {
			case out <- Message{ID: ev.ID, Box: url, Kind: kind, Body: []byte(ev.Content), Time: int64(ev.CreatedAt)}:
			case <-ctx.Done():
				return ctx.Err()
			}
		case reason := <-sub.ClosedReason:
			return fmt.Errorf("relay %s closed the subscription: %s", url, reason)
		}
	}
}
