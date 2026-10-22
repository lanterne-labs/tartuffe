package api

import (
	"context"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/accounts/providers"
	"digital-mirror/backend/mirror"
	"digital-mirror/backend/polls"
	"digital-mirror/backend/storage"
)

// Wire builds the three parts on the database of storer: the polls follow
// the accounts through their hooks (a document moving takes its ballots, a
// deleted account refreshes the results), and get the candidates' cards
// from the mirror.
func Wire(storer *storage.PostgresStorer, reg *providers.Registry) (*accounts.Service, *mirror.Service, *polls.Service) {
	pollStore := polls.NewPGStore(storer.Pool(), accounts.LockVerified)
	acc := accounts.NewService(NewAccountStore(storer, pollStore), reg)
	p := polls.NewService(pollStore, PersonCards{storer}, acc)
	acc.Changed = append(acc.Changed, p.Changed)
	return acc, mirror.NewService(storer, acc), p
}

// NewAccountStore keeps the accounts on the database of storer, with the
// polls' hooks.
func NewAccountStore(storer *storage.PostgresStorer, pollStore *polls.PGStore) *accounts.PGStore {
	return accounts.NewPGStore(storer.Pool(), accounts.Hooks{
		Moved:    []accounts.MovedHook{pollStore.MoveBallots},
		Deleting: []accounts.DeletingHook{pollStore.ForgetUser},
	})
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
