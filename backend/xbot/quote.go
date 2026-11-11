package xbot

import (
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
