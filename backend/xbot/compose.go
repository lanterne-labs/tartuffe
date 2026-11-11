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
	// MaxTags is the most accounts a tweet tags: the person and two
	// outlets. X's rules count unsolicited mentions as spam.
	MaxTags = 3
)

// Style is a way to tell the contradiction; the LLM writes one version in
// each.
type Style struct {
	Key, Label, Brief string
}

// Styles are the versions of a draft, in the order of the buttons.
var Styles = []Style{
	{"standard", "Sobre", "plain and factual: what they said, then what they said or did, side by side, with a dry last word at most. Clear to someone who never heard of it."},
	{"satirical", "Satirique", "in the spirit of a French satirical weekly: a pun or a play on their own words, a mock-serious headline, biting irony, a bit provocative. Funny, never insulting or contemptuous. The joke must land at the first read, for someone scrolling fast who never heard of the subject: say plainly what they said and what they did, then one punchline. No allusion that needs a second read to be understood."},
}

// Draft is a tweet on a contradiction: the versions to choose from (tags and
// link included), the label of each one's style, and why the other versions
// were dropped.
type Draft struct {
	Contradiction storage.BotContradiction
	Tags          []string
	Link          string
	Versions      []string
	Labels        []string
	Dropped       []string
	Reasoning     string
	// QuoteID and QuoteLink are the person's own tweet the tweet quotes
	// ("": none).
	QuoteID, QuoteLink string
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

// ChooseTags picks the accounts a tweet tags, at most max: the person, then
// the outlets that published the facts, the least recently tagged first.
func ChooseTags(pool *storage.BotTagPool, max int) []string {
	var tags []string
	seen := map[string]bool{}
	add := func(h string) {
		if len(tags) < max && h != "" && !seen[h] {
			seen[h] = true
			tags = append(tags, h)
		}
	}
	if len(pool.Person) > 0 {
		add(pool.Person[0])
	}
	for _, a := range pool.Outlets {
		add(a.Handle)
	}
	return tags
}

// Writer is the LLM call that writes the versions; the tests fake it.
type Writer interface {
	Write(ctx context.Context, prompt string) (Reply, error)
}

// Reply is the LLM's answer: its reasoning first, the tweets last
// (CLAUDE.md, "reasoning first, verdict last"), one per style key.
type Reply struct {
	Reasoning string `json:"reasoning"`
	Standard  string `json:"standard"`
	Satirical string `json:"satirical"`
}

// tweet is the version of a style.
func (r Reply) tweet(key string) string {
	switch key {
	case "standard":
		return r.Standard
	case "satirical":
		return r.Satirical
	}
	return ""
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
		date := f.DateFR()
		if date == "" {
			date = "unknown date: give none"
		}
		s := fmt.Sprintf("- Statement (%s): %s", date, f.Text)
		for _, q := range quotesOf(f) {
			s += fmt.Sprintf("\n  Their exact words in a source: %q", q)
		}
		return s
	}
	subject := c.StanceLabel
	if subject == "" {
		subject = "(no label)"
	}
	var styles string
	var fields []string
	for _, st := range Styles {
		styles += fmt.Sprintf("- %s: %s\n", st.Key, st.Brief)
		fields = append(fields, fmt.Sprintf("%q: \"<the %s tweet>\"", st.Key, st.Key))
	}
	keys := strings.Join(fields, ", ")
	return fmt.Sprintf(`You write tweets in French for the X account of Lanterne Labs, the publisher of tartuffe.me, a site that sets what French presidential candidates said beside what they said or did later. Each tweet points at one contradiction the site found between two things the same person said or did.

Person: %s
Subject: %s
%s
%s
Why it is a contradiction: %s

Write two tweets about this contradiction, one in each style. Both must read like a sharp human, not a bot, the kind of tweet people quote and share.
%s
Rules, all of them:
- French only, natural and current, no hashtags, no emoji spam (one emoji at most), no @mentions and no links: the tags and the link are added after.
- At most %d characters each.
- Anything between « » must be the person's own words, copied exactly from "their exact words" above (you may cut a quote short, never change a word). Never invent, extend or paraphrase inside « ».
- Say only what the two statements and the reason above say: no fact, figure or date of your own. A date is given as above, never more precise ("2013" stays "en 2013").
- Mock the contradiction, never the person: no insult, no slur, no contempt, nothing about their body, family, origin or private life (French law punishes insult and defamation).
- Never name or imitate a newspaper: the style is ours.
- Name the person so the tweet stands alone.

Reply in JSON, the reasoning first and the tweets last:
{"reasoning": "<what is striking in this contradiction and the angle of each version>", %s}`,
		c.PersonName, subject, side(c.A), side(c.B), c.Reason, styles, room, keys)
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
	// Only the quotes of the sources: the fact's text is our summary, and a
	// summary between « » would put our words in the person's mouth.
	sources := fold(strings.Join(append(quotesOf(c.A), quotesOf(c.B)...), " \n "))
	for _, m := range quoted.FindAllStringSubmatch(text, -1) {
		q := m[1] + m[2] + m[3]
		if f := strings.TrimSpace(fold(q)); f != "" && !strings.Contains(sources, f) {
			return fmt.Sprintf("quote not in the sources: «%s»", q)
		}
	}
	return ""
}

// quotesOf returns the source quotes of a fact, at most 3 (the prompt and the
// check read the same ones).
func quotesOf(f storage.BotFact) []string {
	qs := f.Quotes
	if len(qs) == 0 && f.Quote != "" {
		qs = []string{f.Quote}
	}
	if len(qs) > 3 {
		qs = qs[:3]
	}
	return qs
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
	for _, st := range Styles {
		t := strings.TrimSpace(reply.tweet(st.Key))
		if why := Check(c, t, room); why != "" {
			d.Dropped = append(d.Dropped, st.Label+", "+why+": "+t)
			continue
		}
		d.Versions = append(d.Versions, t+suffix)
		d.Labels = append(d.Labels, st.Label)
	}
	return d, nil
}

// ContradictionLink is the page of a contradiction on the site, by the
// person's slug (by id for a person without one).
func ContradictionLink(site string, c *storage.BotContradiction) string {
	if c.PersonSlug == "" {
		return fmt.Sprintf("%s/person/%d?c=%d", strings.TrimRight(site, "/"), c.PersonID, c.ID)
	}
	return fmt.Sprintf("%s/person/%s?c=%d", strings.TrimRight(site, "/"), c.PersonSlug, c.ID)
}
