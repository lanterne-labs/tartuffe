package xbot

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"
)

// The vote tweets are fixed lines (voteTexts), written without the LLM. When
// the person who approves them asks for something else in a reply ("plus
// court", "plus drôle"), the writer rewrites the draft here: it may only say
// of the vote what voteTexts say, since every line must stay true to
// /election/<slug>/fonctionnement, and a person still approves the result.

// voteFooter tells how to answer a vote draft.
const voteFooter = "\n\n" + replyHelp

// replyHelp tells how to answer any draft: a reply is a request for other
// versions, "=" before it makes it the tweet itself.
const replyHelp = "💬 <b>Pour d'autres versions</b> : faites « Répondre » à ce message avec votre remarque (« plus drôle », « plus court », « ne cite pas toute la phrase »).\n✍️ <b>Pour publier votre propre texte</b> : répondez en commençant par <code>=</code> (exemple : <code>= Mon tweet</code>)."

// VoteRevisionPrompt asks for the vote tweet again as the request says; room
// is the characters left once the link is counted.
func VoteRevisionPrompt(v *VoteState, previous []string, request string, room int) string {
	var facts strings.Builder
	for _, line := range voteTexts {
		if strings.Contains(line, "%d") {
			line = fmt.Sprintf(line, v.MaxChoices)
		}
		fmt.Fprintf(&facts, "- %s\n", line)
	}
	var before strings.Builder
	for _, p := range previous {
		fmt.Fprintf(&before, "- %q\n", p)
	}
	return fmt.Sprintf(`You write tweets in French for the X account of Lanterne Labs, the publisher of tartuffe.me. This tweet invites French voters to the site's opinion vote on the 2027 presidential election.

Everything true about the vote that you may say is in these lines; say nothing else about how it works, who may vote or what it proves:
%s
The draft the editor read:
%s
The editor who approves the tweets asks: %q

Write two new versions that follow the editor's request: "standard" stays factual, "satirical" may be lighter and playful.
Rules, all of them:
- French only, natural and current, no hashtags, one emoji at most, no @mentions and no links: the link is added after.
- At most %d characters each.
- No fact, figure or promise about the vote that is not in the lines above. Never call it an official vote or a poll. Name no candidate and no party, and give no result.
- No insult, nothing against a person.

Reply in JSON, the reasoning first and the tweets last:
{"reasoning": "<how each version follows the request>", "standard": "<the standard tweet>", "satirical": "<the satirical tweet>"}`,
		facts.String(), before.String(), request, room)
}

// ReviseVote writes the vote tweet again as the request says, and keeps the
// versions that pass the checks, each with the link of the vote. dropped
// tells why a version was not kept.
func ReviseVote(ctx context.Context, w Writer, v *VoteState, site string, previous []string, request string) (versions, dropped []string, err error) {
	link := VoteLink(site, v.Slug)
	room := MaxLength - 1 - LinkLength
	var bare []string
	for _, p := range previous {
		bare = append(bare, strings.TrimSpace(strings.TrimSuffix(p, link)))
	}
	reply, err := w.Write(ctx, VoteRevisionPrompt(v, bare, request, room))
	if err != nil {
		return nil, nil, err
	}
	for _, st := range Styles {
		t := strings.TrimSpace(reply.tweet(st.Key))
		if why := checkVoteText(t, room); why != "" {
			dropped = append(dropped, st.Label+", "+why+": "+t)
			continue
		}
		versions = append(versions, t+"\n"+link)
	}
	return versions, dropped, nil
}

// checkVoteText says why a vote tweet cannot go out ("": it can).
func checkVoteText(text string, room int) string {
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
	return ""
}

// VoteDraftMessage is the Telegram message of a vote draft: its versions and
// how to answer. note is a line under the title ("": none).
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
