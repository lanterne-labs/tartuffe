package api

import (
	"context"
	"fmt"
	"github.com/rs/zerolog/log"
	"os"

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
	p := polls.NewService(store, store, PersonCards{storer}, cfg)
	vs := vote.NewStore(storer.Pool())
	p.Extra = vote.Routes(vs)
	p.ResultsOf = vote.ResultsOf(vs, PersonCards{storer})
	return acc, mirror.NewService(storer, acc), p
}

// PollsFromEnv reads the polls' config: the key of the log's checkpoints
// (VOTE_LOG_KEY, from `dm vote log-key`; a key made at start when unset,
// with a warning: the checkpoints of a restart then fail to verify).
func PollsFromEnv() (polls.Config, error) {
	cfg := polls.Config{}
	skey := os.Getenv("VOTE_LOG_KEY")
	var err error
	if skey == "" {
		log.Warn().Msg("VOTE_LOG_KEY is not set: the vote log's checkpoints are signed by a key made now, that the next start replaces")
		if skey, _, err = polls.GenerateLogKey(); err != nil {
			return polls.Config{}, err
		}
	}
	if cfg.Signer, err = polls.NewLogSigner(skey); err != nil {
		return polls.Config{}, fmt.Errorf("VOTE_LOG_KEY: %w", err)
	}
	return cfg, nil
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
