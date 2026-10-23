package api

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/accounts/providers"
	"digital-mirror/backend/mirror"
	"digital-mirror/backend/polls"
	"digital-mirror/backend/storage"
)

// Wire builds the three parts on the database of storer: the polls check
// the voters' documents through the accounts (never their accounts: no
// ballot names one), and get the candidates' cards from the mirror.
func Wire(storer *storage.PostgresStorer, reg *providers.Registry, batches polls.BatchConfig) (*accounts.Service, *mirror.Service, *polls.Service) {
	acc := accounts.NewService(accounts.NewPGStore(storer.Pool(), accounts.Hooks{}), reg)
	acc.SubjectKey = []byte(os.Getenv("AUTH_SUBJECT_KEY"))
	store := polls.NewPGStore(storer.Pool())
	p := polls.NewService(store, store, PersonCards{storer}, acc, batches)
	return acc, mirror.NewService(storer, acc), p
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
