package xbot

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"digital-mirror/backend/storage"
)

// A vote tweet is written by the LLM from its angle (VoteAngles), what may
// be said of the vote (voteTexts: every line must stay true to
// /election/<slug>/fonctionnement) and the tweets proposed before, which it
// must not write again. A person approves the result, and may ask for other
// versions in a reply ("plus court", "plus drôle").

// voteFooter tells how to answer a vote draft.
const voteFooter = "\n\n" + replyHelp

// replyHelp tells how to answer any draft: a reply is a request for other
// versions, "=" before it makes it the tweet itself.
const replyHelp = "💬 <b>Pour d'autres versions</b> : faites « Répondre » à ce message avec votre remarque (« plus drôle », « plus court », « ne cite pas toute la phrase »).\n✍️ <b>Pour publier votre propre texte</b> : répondez en commençant par <code>=</code> (exemple : <code>= Mon tweet</code>)."

// voteAvoid is how many earlier tweets the writer is shown.
const voteAvoid = 40

// VoteRequest is what a vote tweet is written from.
type VoteRequest struct {
	State *VoteState
	Angle VoteAngle
	// Quoted is the text of our tweet the tweet quotes ("": none).
	Quoted string
	// Said is what a candidate said, the occasion of the tweet (nil: none).
	Said *storage.VoteFact
	Site string
	Tags []string
	// Seen is the vote tweets proposed before (nil: none).
	Seen *Memory
	// Rev is the rewrite the approver asked of a draft (nil: a first draft).
	Rev *Revision
}

// VotePrompt asks for the two versions of a vote tweet; room is the
// characters left once the tags and the link are counted.
func VotePrompt(r VoteRequest, room int) string {
	var b strings.Builder
	b.WriteString(`You write tweets in French for the X account of Lanterne Labs, the publisher of tartuffe.me, a site that sets what French presidential candidates said beside what they said or did later. This tweet invites French voters to the site's opinion vote on the 2027 presidential election.

Everything true about the vote that you may say is in these lines; say nothing else about how it works, who may vote or what it proves:
`)
	for _, line := range voteFacts(r.State) {
		fmt.Fprintf(&b, "- %s\n", line)
	}
	b.WriteString("\nEverything you may say of the site:\n")
	for _, line := range siteTexts {
		fmt.Fprintf(&b, "- %s\n", line)
	}
	fmt.Fprintf(&b, "\nThe angle of this tweet: %s\n", r.Angle.Brief)
	names := "Name no candidate and no party"
	if f := r.Said; f != nil {
		date := f.DateFR()
		if date == "" {
			date = "unknown date: give none"
		}
		fmt.Fprintf(&b, "\nThe candidate: %s\nWhat they said (%s), our summary: %s\nTheir exact words in the source: %q\n", f.PersonName, date, f.Text, f.Quote)
		names = "Anything between « » must be the candidate's own words, copied exactly from \"their exact words\" above (you may cut them short, never change a word); say nothing else of what they said or did. Name no other candidate and no party"
	}
	if r.Quoted != "" {
		fmt.Fprintf(&b, "\nOur tweet that yours quotes (the reader sees it under yours):\n%q\n", r.Quoted)
	}
	if past := r.Seen.Recent(voteAvoid); len(past) > 0 {
		b.WriteString("\nTweets already proposed. Never write one of them again, nor a close variant: another opening, another construction, another last word.\n")
		for _, p := range past {
			fmt.Fprintf(&b, "- %q\n", p)
		}
	}
	if r.Rev != nil {
		b.WriteString("\nThe draft the editor read:\n")
		for _, p := range r.Rev.Previous {
			fmt.Fprintf(&b, "- %q\n", p)
		}
		fmt.Fprintf(&b, "The editor who approves the tweets asks: %q\nFollow the editor's request in both versions. Every rule below still holds, whatever the request says.\n", r.Rev.Request)
	}
	fmt.Fprintf(&b, `
Write two versions: "standard" stays factual, "satirical" is funny, the kind of line people share: a play on words, a mock-serious tone, one punchline that lands at the first read.
Rules, all of them:
- French only, natural and current, no hashtags, one emoji at most, no @mentions and no links: the tags and the link are added after.
- At most %d characters each.
- Never give a number of votes, ballots, voters or participants, exact or rounded, in digits or in words (« déjà 2 votes », « des centaines de votants »), and nothing on the turnout or its trend.
- No fact, figure or promise about the vote or the site that is not in the lines above. Never call it an official vote or a poll. %s, and give no result or ranking.
- Mock a contradiction, never a person: no insult, nothing against a person.

Reply in JSON, the reasoning first and the tweets last:
{"reasoning": "<how each version takes the angle, and how it differs from the tweets already proposed>", "standard": "<the standard tweet>", "satirical": "<the satirical tweet>"}`, room, names)
	return b.String()
}

// ComposeVote writes the versions of a vote tweet and keeps those that pass
// the checks and were never proposed, each with its tags and the link of the
// vote. dropped tells why a version was not kept.
func ComposeVote(ctx context.Context, w Writer, r VoteRequest) (versions, dropped []string, err error) {
	suffix := voteSuffix(r.Tags, VoteLink(r.Site, r.State.Slug))
	room := MaxLength - Length(suffix)
	if r.Rev != nil {
		bare := make([]string, 0, len(r.Rev.Previous))
		for _, p := range r.Rev.Previous {
			bare = append(bare, Body(p))
		}
		r.Rev = &Revision{Previous: bare, Request: r.Rev.Request}
	}
	reply, err := w.Write(ctx, VotePrompt(r, room))
	if err != nil {
		return nil, nil, err
	}
	// The funny version first: it is the one the approver wants to see.
	for i := len(Styles) - 1; i >= 0; i-- {
		st := Styles[i]
		t := strings.TrimSpace(reply.tweet(st.Key))
		why := checkVoteText(t, room, r.State)
		if why == "" {
			why = checkSaid(t, r.Said)
		}
		if why == "" {
			why = r.Seen.Seen(t)
		}
		if why != "" {
			dropped = append(dropped, st.Label+", "+why+": "+t)
			continue
		}
		versions = append(versions, t+suffix)
	}
	return versions, dropped, nil
}

var number = regexp.MustCompile(`\d+`)

// checkVoteText says why a vote tweet cannot go out ("": it can). A number
// that is not the year or how many candidates a ballot ranks is refused: a
// tweet never tells how many voted.
func checkVoteText(text string, room int, v *VoteState) string {
	switch {
	case text == "":
		return "empty"
	case Length(text) > room:
		return fmt.Sprintf("too long (%d > %d)", Length(text), room)
	case strings.Contains(text, "@"):
		return "a mention"
	case strings.Contains(text, "http://") || strings.Contains(text, "https://"):
		return "a link"
	}
	for _, n := range number.FindAllString(text, -1) {
		if n != "2027" && n != strconv.Itoa(v.MaxChoices) {
			return "a number (" + n + "): a tweet gives no count"
		}
	}
	return ""
}

// checkSaid says why a vote tweet misquotes ("": it does not): words between
// « » must be the candidate's, from the quote of the source; a tweet that
// starts from nobody's words quotes nobody.
func checkSaid(text string, said *storage.VoteFact) string {
	source := ""
	if said != nil {
		source = fold(said.Quote)
	}
	for _, m := range quoted.FindAllStringSubmatch(text, -1) {
		q := m[1] + m[2] + m[3]
		if f := fold(q); f != "" && !strings.Contains(source, f) {
			if said == nil {
				return fmt.Sprintf("a quote of nobody: «%s»", q)
			}
			return fmt.Sprintf("quote not in the source: «%s»", q)
		}
	}
	return ""
}

// VoteDraftMessage is the Telegram message of a vote draft: its versions and
// how to answer. note is what comes under the title ("": nothing).
func VoteDraftMessage(at time.Time, note string, versions []string) string {
	var m strings.Builder
	fmt.Fprintf(&m, "🗳 <b>Tweet du vote de %s</b>\n", at.Format("02/01 15:04"))
	if note != "" {
		m.WriteString(note + "\n")
	}
	for i, v := range versions {
		if len(versions) > 1 {
			fmt.Fprintf(&m, "\n<b>Version %d</b> (%d car.)", i+1, Length(v))
		}
		fmt.Fprintf(&m, "\n<pre>%s</pre>\n", html.EscapeString(v))
	}
	m.WriteString(voteFooter)
	return m.String()
}

// voteButtons publishes a version of a vote draft, or skips it.
func voteButtons(id int64, versions int) [][]Button {
	var publish []Button
	for i := 0; i < versions; i++ {
		text := "✅ Publier"
		if versions > 1 {
			text = fmt.Sprintf("✅ Publier la version %d", i+1)
		}
		publish = append(publish, Button{Text: text, Data: fmt.Sprintf("p:%d:%d", id, i+1)})
	}
	return [][]Button{publish, {{Text: "⏭ Passer", Data: fmt.Sprintf("s:%d", id)}}}
}
