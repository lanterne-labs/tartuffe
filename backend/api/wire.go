package api

import (
	"context"
	"fmt"
	"github.com/rs/zerolog/log"
	"os"
	"strconv"
	"time"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/accounts/providers"
	"digital-mirror/backend/mirror"
	"digital-mirror/backend/polls"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/vote"
)

// Wire builds the three parts on the database of storer: the polls check
// the voters' documents through the accounts (never their accounts: no
// ballot names one), and get the candidates' cards from the mirror.
func Wire(storer *storage.PostgresStorer, reg *providers.Registry, cfg polls.Config) (*accounts.Service, *mirror.Service, *polls.Service) {
	acc := accounts.NewService(accounts.NewPGStore(storer.Pool()), reg)
	acc.SubjectKey = []byte(os.Getenv("AUTH_SUBJECT_KEY"))
	store := polls.NewPGStore(storer.Pool())
	p := polls.NewService(store, store, PersonCards{storer}, acc, cfg)
	p.Extra = vote.Routes(vote.NewStore(storer.Pool()))
	return acc, mirror.NewService(storer, acc), p
}

// PollsFromEnv reads the polls' config: the batches (BatchesFromEnv), the
// key of the log's checkpoints (VOTE_LOG_KEY, from `dm poll log-key`; a key
// made at start when unset, with a warning: the checkpoints of a restart
// then fail to verify) and the Nostr relays (NOSTR_RELAYS, comma separated;
// polls.DefaultRelays when unset, "none" for none).
func PollsFromEnv() (polls.Config, error) {
	batches, err := BatchesFromEnv()
	if err != nil {
		return polls.Config{}, err
	}
	cfg := polls.Config{Batches: batches, Relays: polls.DefaultRelays}
	skey := os.Getenv("VOTE_LOG_KEY")
	if skey == "" {
		log.Warn().Msg("VOTE_LOG_KEY is not set: the vote log's checkpoints are signed by a key made now, that the next start replaces")
		if skey, _, err = polls.GenerateLogKey(); err != nil {
			return polls.Config{}, err
		}
	}
	if cfg.Signer, err = polls.NewLogSigner(skey); err != nil {
		return polls.Config{}, fmt.Errorf("VOTE_LOG_KEY: %w", err)
	}
	switch r := os.Getenv("NOSTR_RELAYS"); r {
	case "":
	case "none":
		cfg.Relays = nil
	default:
		if cfg.Relays, err = polls.ParseRelays(r); err != nil {
			return polls.Config{}, fmt.Errorf("NOSTR_RELAYS: %w", err)
		}
	}
	return cfg, nil
}

// BatchesFromEnv reads VOTE_BATCH_INTERVAL, VOTE_BATCH_MIN and
// VOTE_BATCH_MAX_WAIT (polls.DefaultBatches when unset).
func BatchesFromEnv() (polls.BatchConfig, error) {
	c := polls.DefaultBatches
	for _, d := range []struct {
		key string
		v   *time.Duration
	}{{"VOTE_BATCH_INTERVAL", &c.Interval}, {"VOTE_BATCH_MAX_WAIT", &c.MaxWait}} {
		if s := os.Getenv(d.key); s != "" {
			v, err := time.ParseDuration(s)
			if err != nil || v <= 0 {
				return c, fmt.Errorf("%s: %q is not a duration", d.key, s)
			}
			*d.v = v
		}
	}
	if s := os.Getenv("VOTE_BATCH_MIN"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return c, fmt.Errorf("VOTE_BATCH_MIN: %q is not a count", s)
		}
		c.Min = n
	}
	return c, nil
}

// PersonCards gives the polls the cards of the mirror's persons.
type PersonCards struct{ Storer *storage.PostgresStorer }

func (c PersonCards) Cards(ctx context.Context, ids []int64) (map[int64]polls.Card, error) {
	cards, err := c.Storer.GetPersonCards(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]polls.Card, len(cards))
	for id, p := range cards {
		out[id] = polls.Card(p)
	}
	return out, nil
}
