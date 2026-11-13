package xbot

import (
	"context"
	"strings"
)

// The bot never proposes the same tweet twice. Every text it sent to
// Telegram is kept (x_post_texts): the first versions of a draft, each
// rewrite, the text the approver typed, whatever became of the draft
// (published, skipped, refused, expired). A new version that is one of them,
// or close to one, is dropped before anyone sees it.

// textStore is what the memory needs of the storage.
type textStore interface {
	XPostTexts(ctx context.Context, kind string, exceptPost int64) ([]string, error)
}

// Memory is the texts the bot proposed before for a kind of tweet.
type Memory struct {
	bodies []string // newest first, once each
	folds  map[string]bool
	grams  []map[string]bool
}

// closeShare is the share of a text's word trigrams found in one earlier
// text from which it reads as the same tweet again.
const closeShare = 0.6

// Recall loads the texts proposed for the tweets of a kind. exceptPost
// leaves the texts of that tweet out (0: none): a rewrite asked of a draft
// may stay close to it.
func Recall(ctx context.Context, store textStore, kind string, exceptPost int64) (*Memory, error) {
	texts, err := store.XPostTexts(ctx, kind, exceptPost)
	if err != nil {
		return nil, err
	}
	return Remember(texts), nil
}

// Remember is the memory of these texts, the newest first.
func Remember(texts []string) *Memory {
	m := &Memory{folds: map[string]bool{}}
	for _, t := range texts {
		body := Body(t)
		f := fold(body)
		if f == "" || m.folds[f] {
			continue
		}
		m.folds[f] = true
		m.bodies = append(m.bodies, body)
		m.grams = append(m.grams, trigrams(f))
	}
	return m
}

// Seen says why a text must not be proposed ("": it is new): it was proposed
// before, whatever its tags, link, case and punctuation, or it is close to a
// text that was. A nil memory has seen nothing.
func (m *Memory) Seen(text string) string {
	if m == nil {
		return ""
	}
	f := fold(Body(text))
	if m.folds[f] {
		return "already proposed"
	}
	own := trigrams(f)
	if len(own) < 4 {
		return ""
	}
	for i, past := range m.grams {
		shared := 0
		for g := range own {
			if past[g] {
				shared++
			}
		}
		if float64(shared) >= closeShare*float64(len(own)) {
			return "too close to an earlier tweet (« " + clip(m.bodies[i], 80) + " »)"
		}
	}
	return ""
}

// Recent returns the n latest texts proposed, without their tags and link.
func (m *Memory) Recent(n int) []string {
	if m == nil {
		return nil
	}
	if len(m.bodies) < n {
		n = len(m.bodies)
	}
	return m.bodies[:n]
}

// Body is a tweet without what the bot adds after its text: the line of
// tags and the link.
func Body(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		if last != "" && !added(last) {
			break
		}
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// added reports whether a line is only tags or a link.
func added(line string) bool {
	for _, w := range strings.Fields(line) {
		if !strings.HasPrefix(w, "@") && !strings.HasPrefix(w, "http://") && !strings.HasPrefix(w, "https://") {
			return false
		}
	}
	return true
}

// trigrams is the set of the three-word sequences of a folded text.
func trigrams(folded string) map[string]bool {
	words := strings.Fields(folded)
	out := map[string]bool{}
	for i := 0; i+3 <= len(words); i++ {
		out[strings.Join(words[i:i+3], " ")] = true
	}
	return out
}
