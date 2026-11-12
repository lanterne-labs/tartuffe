package xbot

import (
	"errors"
	"regexp"
	"strings"

	"digital-mirror/backend/storage"
)

var statusURL = regexp.MustCompile(`^https?://(?:www\.|mobile\.)?(?:x|twitter)\.com/([A-Za-z0-9_]{1,15})/status/(\d+)`)

// QuoteTarget is the tweet a tweet on the contradiction quotes: a source of
// one of its two facts that is a tweet posted by the person's own account
// (handles), the later fact first. A tweet of anyone else is never quoted: it
// would answer the wrong person. "" when neither fact is such a tweet.
func QuoteTarget(c *storage.BotContradiction, handles []string) (id, link string) {
	own := map[string]bool{}
	for _, h := range handles {
		own[strings.ToLower(h)] = true
	}
	for _, f := range []storage.BotFact{c.B, c.A} {
		for _, u := range f.SourceURLs {
			m := statusURL.FindStringSubmatch(u)
			if m != nil && own[strings.ToLower(m[1])] {
				return m[2], "https://x.com/" + m[1] + "/status/" + m[2]
			}
		}
	}
	return "", ""
}

// ErrQuoteRefused is the error of a Poster when X refuses the quote and not
// the tweet: since late 2026 the API lets an app quote only the posts that
// mention its account or are its own ("You can only reply to or quote posts
// where you are mentioned or are the author", 2026-11-12, the first quote
// the bot tried). The bot then posts the tweet without the quote.
var ErrQuoteRefused = errors.New("X refused the quote")

// quoteRefused reports whether the body of a 403 refuses the quote.
func quoteRefused(body string) bool {
	return strings.Contains(body, "reply to or quote posts")
}
