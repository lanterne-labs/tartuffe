package xbot

import (
	"context"
	"strings"

	"digital-mirror/backend/storage"
)

// The tags of the tweets about the vote.
//
// A vote tweet is about no person and no article, so it has no tag of its
// own. To be seen, it tags accounts that have a reason to pass on a call to
// vote: the personalities and associations that defend democracy, the
// outlets, the independents, the fact-checkers. Only the accounts a person
// approved (`dm bot accounts approve`), the least recently tagged first, so
// the same account is not tagged twice a day. Never a party, never a
// candidate: the vote takes no side.

// VoteTagCategories are the categories of accounts a vote tweet may tag.
var VoteTagCategories = []string{"personality", "association", "outlet", "independent", "factcheck", "journalist"}

// MaxVoteTags is the most accounts a vote tweet tags: more reads as spam,
// to X and to the reader.
const MaxVoteTags = 3

// voteTagStore is what the tags need of the storage.
type voteTagStore interface {
	VoteTagAccounts(ctx context.Context, categories []string, n int) ([]storage.XAccount, error)
}

// VoteTags chooses the accounts the next vote tweet tags.
func VoteTags(ctx context.Context, store voteTagStore) ([]string, error) {
	accounts, err := store.VoteTagAccounts(ctx, VoteTagCategories, MaxVoteTags)
	if err != nil {
		return nil, err
	}
	tags := make([]string, 0, len(accounts))
	for _, a := range accounts {
		tags = append(tags, a.Handle)
	}
	return tags, nil
}

// voteSuffix is what follows the text of a vote tweet: its tags on a line of
// their own, then the link.
func voteSuffix(tags []string, link string) string {
	s := ""
	if len(tags) > 0 {
		s = "\n\n@" + strings.Join(tags, " @")
	}
	return s + "\n" + link
}
