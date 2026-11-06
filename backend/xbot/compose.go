// Package xbot drafts the tweets of the X bot: a contradiction of a
// candidate, told by the LLM in a few lively versions, with the accounts it
// tags. A person approves each draft before it is posted; the quotes of a
// version must be the person's own words, word for word, or the version is
// dropped.
package xbot

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"
)

// The limits of a tweet. X counts 280 characters; a link counts LinkLength
// whatever its length.
const (
	MaxLength  = 280
	LinkLength = 23
	// MaxTags is the most accounts a tweet tags: beyond, X's spam filter
	// cuts its reach even when a person posts it.
	MaxTags = 4
	// Versions is how many versions the LLM writes.
	Versions = 3
)

// Draft is a tweet on a contradiction: the versions to choose from (tags and
// link included) and why the other versions were dropped.
type Draft struct {
	Contradiction storage.BotContradiction
	Tags          []string
	Link          string
	Versions      []string
	Dropped       []string
	Reasoning     string
}

// Domains returns the sites of the facts' sources, without "www.", once each.
func Domains(c *storage.BotContradiction) []string {
	var out []string
	seen := map[string]bool{}
	for _, u := range append(append([]string{}, c.A.SourceURLs...), c.B.SourceURLs...) {
		p, err := url.Parse(u)
		if err != nil || p.Hostname() == "" {
			continue
		}
		d := strings.TrimPrefix(strings.ToLower(p.Hostname()), "www.")
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// ChooseTags picks the accounts a tweet tags, at most max: the person, one
// of their parties, up to two outlets that published the facts, then the
// others (fact-checkers, associations, independent media), the least
// recently tagged first.
func ChooseTags(pool *storage.BotTagPool, max int) []string {
	var tags []string
	seen := map[string]bool{}
	add := func(h string) bool {
		if len(tags) >= max || h == "" || seen[h] {
			return false
		}
		seen[h] = true
		tags = append(tags, h)
		return true
	}
	if len(pool.Person) > 0 {
		add(pool.Person[0])
	}
	for _, a := range pool.Parties {
		if add(a.Handle) {
			break
		}
	}
	outlets := 0
	for _, a := range pool.Outlets {
		if outlets < 2 && add(a.Handle) {
			outlets++
		}
	}
	for _, a := range pool.Others {
		add(a.Handle)
	}
	return tags
}

// Writer is the LLM call that writes the versions; the tests fake it.
type Writer interface {
	Write(ctx context.Context, prompt string) (Reply, error)
}

// Reply is the LLM's answer: its reasoning first, the tweets last
// (CLAUDE.md, "reasoning first, verdict last").
type Reply struct {
	Reasoning string   `json:"reasoning"`
	Tweets    []string `json:"tweets"`
}

// LLMWriter writes the versions with the chat LLM.
type LLMWriter struct {
	Client *llm.Client
}

// NewLLMWriterFromEnv opens the chat LLM of the environment.
func NewLLMWriterFromEnv() (*LLMWriter, error) {
	client, err := llm.NewClientFromEnv("xbot")
	if err != nil {
		return nil, err
	}
	return &LLMWriter{Client: client}, nil
}

func (w *LLMWriter) Write(ctx context.Context, prompt string) (Reply, error) {
	reply, err := llm.ChatJSON[Reply](ctx, w.Client, llm.ChatRequest{
		Messages:       []llm.ChatMessage{{Role: "user", Content: prompt}},
		Temperature:    0.9,
		MaxTokens:      1500,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Task:           llm.TaskTweet,
	}, "X bot tweet")
	if err != nil {
		return Reply{}, fmt.Errorf("tweet call: %w", err)
	}
	return reply, nil
}

// Prompt is the request for the versions of a tweet on the contradiction;
// room is the characters left for the text once the tags and link are
// counted.
func Prompt(c *storage.BotContradiction, room int) string {
	side := func(f storage.BotFact) string {
		date := "unknown date"
		if f.Date != nil {
			date = f.Date.Format("2 January 2006")
		}
		s := fmt.Sprintf("- Statement (%s): %s", date, f.Text)
		if f.Quote != "" {
			s += fmt.Sprintf("\n  Their exact words in the source: %q", f.Quote)
		}
		return s
	}
	subject := c.StanceLabel
	if subject == "" {
		subject = "(no label)"
	}
	return fmt.Sprintf(`You write tweets in French for an anonymous X account that exposes the contradictions of French presidential candidates. Each tweet points at one contradiction that our site found between two things the same person said or did.

Person: %s
Subject: %s
%s
%s
Why it is a contradiction: %s

Write %d different tweets about this contradiction. They must read like a sharp, witty human, not a bot: ironic, punchy, sometimes provocative, the kind of tweet people quote and share. Vary the angle between the versions (a deadpan juxtaposition, a rhetorical question, a mock-serious headline, a punchline).

Rules, all of them:
- French only, natural and current, no hashtags, no emoji spam (one emoji at most), no @mentions and no links: the tags and the link are added after.
- At most %d characters each.
- Anything between « » must be the person's own words, copied exactly from "their exact words" above (you may cut a quote short, never change a word). Never invent, extend or paraphrase inside « ».
- Say only what the two statements and the reason above say: no fact, figure or date of your own.
- Mock the contradiction, never the person: no insult, no slur, nothing about their body, family, origin or private life (French law punishes insult and defamation).
- Name the person so the tweet stands alone.

Reply in JSON, the reasoning first and the tweets last:
{"reasoning": "<what is striking in this contradiction and the angle of each version>", "tweets": ["<version 1>", "<version 2>", "<version 3>"]}`,
		c.PersonName, subject, side(c.A), side(c.B), c.Reason, Versions, room)
}

// Length counts a tweet as X does, closely enough: characters, a link as
// LinkLength.
func Length(text string) int {
	return utf8.RuneCountInString(urlPattern.ReplaceAllString(text, "")) + LinkLength*len(urlPattern.FindAllString(text, -1))
}

var urlPattern = regexp.MustCompile(`https?://\S+`)

var quoted = regexp.MustCompile(`«\s*([^»]+?)\s*»|“([^”]+)”|"([^"]+)"`)

// fold lowercases and keeps letters and digits, a space between words, so a
// quote matches its source whatever the quotation marks and apostrophes.
func fold(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		} else {
			space = true
		}
	}
	return b.String()
}

// Check returns why a version must be dropped, or "": a quote that is not
// the person's words, a mention or a link of its own, or too long for room.
func Check(c *storage.BotContradiction, text string, room int) string {
	if strings.TrimSpace(text) == "" {
		return "empty"
	}
	if strings.Contains(text, "@") || strings.Contains(text, "http") {
		return "a mention or a link of its own"
	}
	if n := Length(text); n > room {
		return fmt.Sprintf("too long (%d > %d)", n, room)
	}
	sources := fold(strings.Join([]string{c.A.Quote, c.A.Text, c.B.Quote, c.B.Text}, " \n "))
	for _, m := range quoted.FindAllStringSubmatch(text, -1) {
		q := m[1] + m[2] + m[3]
		if f := strings.TrimSpace(fold(q)); f != "" && !strings.Contains(sources, f) {
			return fmt.Sprintf("quote not in the sources: «%s»", q)
		}
	}
	return ""
}

// Compose writes the versions of a tweet on the contradiction with its tags
// and link ("": none), and keeps the versions that pass Check.
func Compose(ctx context.Context, w Writer, c *storage.BotContradiction, tags []string, link string) (*Draft, error) {
	suffix := ""
	if len(tags) > 0 {
		suffix = "\n\n@" + strings.Join(tags, " @")
	}
	if link != "" {
		suffix += "\n" + link
	}
	room := MaxLength - Length(suffix)
	reply, err := w.Write(ctx, Prompt(c, room))
	if err != nil {
		return nil, err
	}
	d := &Draft{Contradiction: *c, Tags: tags, Link: link, Reasoning: reply.Reasoning}
	for _, t := range reply.Tweets {
		t = strings.TrimSpace(t)
		if why := Check(c, t, room); why != "" {
			d.Dropped = append(d.Dropped, why+": "+t)
			continue
		}
		d.Versions = append(d.Versions, t+suffix)
	}
	return d, nil
}

// ContradictionLink is the page of a contradiction on the site.
func ContradictionLink(site string, c *storage.BotContradiction) string {
	return fmt.Sprintf("%s/person/%d?c=%d", strings.TrimRight(site, "/"), c.PersonID, c.ID)
}
