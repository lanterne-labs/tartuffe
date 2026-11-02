package api

import (
	"context"
	"os"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/accounts/providers"
	"digital-mirror/backend/mirror"
	"digital-mirror/backend/polls"
	"digital-mirror/backend/storage"
)

// Wire builds the three parts on the database of storer: the polls get
// the candidates' cards from the mirror.
func Wire(storer *storage.PostgresStorer, reg *providers.Registry) (*accounts.Service, *mirror.Service, *polls.Service) {
	acc := accounts.NewService(accounts.NewPGStore(storer.Pool()), reg)
	acc.SubjectKey = []byte(os.Getenv("AUTH_SUBJECT_KEY"))
	p := polls.NewService(polls.NewPGStore(storer.Pool()), PersonCards{storer})
	return acc, mirror.NewService(storer, acc), p
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
