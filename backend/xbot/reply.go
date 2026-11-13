package xbot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"sort"
	"strings"
	"time"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// The replies. A tweet of the bot's account reaches its followers; a reply
// under a candidate's tweet that many read reaches that audience. Before
// each reply slot the bot reads the candidates' most seen tweets of the last
// day, asks the LLM whether one of the candidate's contradictions answers
// the tweet (same subject: a reply beside the point is spam), and writes two
// versions.
//
// X lets an app reply only where it is mentioned (ErrQuoteRefused), and
// counts automatic replies as spam: the bot never posts a reply. The
// Telegram draft carries a link per version that opens X on the reply,
// already written, and the person posts it by hand from the bot's account.

// FoundTweet is a tweet read on X.
type FoundTweet struct {
	ID, Text, Author, URL string
	Views, Likes          int
	At                    *time.Time
}

// The limits of the search for a slot.
const (
	// ReplyWindow is how old a tweet may be: later, the readers are gone.
	ReplyWindow = 24 * time.Hour
	// replyMinLikes keeps the tweets few read out of the search (the search
	// bills per tweet returned).
	replyMinLikes = 150
	// replySearchMax is the most tweets one search returns.
	replySearchMax = 40
	// replyTries is how many tweets a slot asks the LLM about, the most seen
	// first, before it gives up.
	replyTries = 3
	// replyChoices is how many contradictions of the person the chooser
	// reads.
	replyChoices = 40
)

// ReplyQuery is the search for the candidates' own tweets worth a reply:
// theirs, not a reply or a retweet, liked enough.
func ReplyQuery(handles []storage.CandidateHandle) string {
	from := make([]string, 0, len(handles))
	for _, h := range handles {
		from = append(from, "from:"+h.Handle)
	}
	return fmt.Sprintf("(%s) -filter:replies -filter:retweets min_faves:%d", strings.Join(from, " OR "), replyMinLikes)
}

// ReplyLink opens X on a reply to the tweet with the text written; the
// person posts it by hand.
func ReplyLink(tweetID, text string) string {
	return "https://x.com/intent/post?in_reply_to=" + url.QueryEscape(tweetID) + "&text=" + url.QueryEscape(text)
}

// ChoicePrompt asks which contradiction of the person, if any, answers their
// tweet.
func ChoicePrompt(person string, t *storage.ReplyTarget, choices []storage.ReplyChoice) string {
	var b strings.Builder
	fmt.Fprintf(&b, `tartuffe.me sets what French presidential candidates said beside what they said or did later. %s just posted this tweet, and many people are reading it:
%q

Below are contradictions the site found in %s's record. Choose the one a reply under this tweet should point at, or none.
Choose one only when it is about the very subject of the tweet (the same measure, the same promise, the same question), so that a reader of the tweet sees at once why the reply is there. The same broad theme is not enough: a contradiction on pensions does not answer a tweet on wages. When in doubt, none: a reply beside the point reads as spam.

`, person, t.Text, person)
	for _, c := range choices {
		fmt.Fprintf(&b, "[%d] %s\n  - %s\n  - %s\n  Why: %s\n", c.ID, c.StanceLabel, c.A, c.B, c.Reason)
	}
	b.WriteString(`
Reply in JSON, the reasoning first and the choice last:
{"reasoning": "<what the tweet is about, and which contradiction is about that very subject, if any>", "pick": <the number in brackets of the contradiction, or 0 for none>}`)
	return b.String()
}

// ReplyPrompt is the request for the two versions of a reply under the
// person's tweet, about the contradiction; room is the characters left once
// the link is counted.
func ReplyPrompt(c *storage.BotContradiction, t *storage.ReplyTarget, room int) string {
	return Prompt(c, room) + fmt.Sprintf(`

This time the tweets are replies under a tweet %s just posted, which the reader sees right above yours:
%q
Write each as a reply to it: start from what the tweet says and set the contradiction beside it, short and direct. Speak of %s in the third person, to the readers of the tweet. The reader has the tweet: do not repeat it. One or two sentences beat a full account, the link under the reply tells the rest.`, c.PersonName, t.Text, c.PersonName)
}

// ComposeReply writes the versions of a reply about the contradiction, with
// its link, and keeps those that pass Check and were never proposed, the
// funny one first.
func ComposeReply(ctx context.Context, w Writer, c *storage.BotContradiction, t *storage.ReplyTarget, link string, rev *Revision, seen *Memory) (*Draft, error) {
	suffix := "\n" + link
	room := MaxLength - Length(suffix)
	prompt := ReplyPrompt(c, t, room)
	if rev != nil {
		prompt = rev.prompt(prompt)
	}
	reply, err := w.Write(ctx, prompt)
	if err != nil {
		return nil, err
	}
	d := &Draft{Contradiction: *c, Link: link, Reasoning: reply.Reasoning}
	for i := len(Styles) - 1; i >= 0; i-- {
		st := Styles[i]
		text := strings.TrimSpace(reply.tweet(st.Key))
		why := Check(c, text, room)
		if why == "" {
			why = seen.Seen(text)
		}
		if why != "" {
			d.Dropped = append(d.Dropped, st.Label+", "+why+": "+text)
			continue
		}
		d.Versions = append(d.Versions, text+suffix)
		d.Labels = append(d.Labels, st.Label)
	}
	return d, nil
}

// DraftReply proposes the reply of a slot: under the most seen tweet of a
// candidate, of the last day, that one of their contradictions answers. No
// such tweet: the slot is left.
func (b *Bot) DraftReply(ctx context.Context, slot time.Time) error {
	if b.FindTweets == nil {
		return nil
	}
	handles, err := b.Store.CandidateXHandles(ctx)
	if err != nil {
		return err
	}
	if len(handles) == 0 {
		return nil
	}
	owner := map[string]storage.CandidateHandle{}
	for _, h := range handles {
		owner[h.Handle] = h
	}
	found, err := b.FindTweets(ctx, ReplyQuery(handles), b.Now().Add(-ReplyWindow), replySearchMax)
	if err != nil {
		// The search is away: the slot is held, not tried every minute.
		log.Error().Err(err).Time("slot", slot).Msg("xbot: the search for tweets to reply to failed; no reply for this slot")
		return b.holdSlot(ctx, storage.XPostReply, slot)
	}
	ids := make([]string, 0, len(found))
	for _, t := range found {
		ids = append(ids, t.ID)
	}
	seenTargets, err := b.Store.SeenReplyTargets(ctx, ids)
	if err != nil {
		return err
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Views != found[j].Views {
			return found[i].Views > found[j].Views
		}
		return found[i].Likes > found[j].Likes
	})
	seen, err := Recall(ctx, b.Store, storage.XPostReply, 0)
	if err != nil {
		return err
	}
	tried := 0
	for _, f := range found {
		h, ok := owner[strings.ToLower(f.Author)]
		if !ok || seenTargets[f.ID] {
			continue
		}
		if tried == replyTries {
			break
		}
		tried++
		target := storage.ReplyTarget{TweetID: f.ID, PersonID: h.PersonID, Author: h.Handle, Text: f.Text, URL: f.URL, Views: f.Views, TweetedAt: f.At, Outcome: "no_match"}
		d, err := b.replyTo(ctx, h.Name, &target, seen)
		if err != nil {
			return err
		}
		if d == nil {
			if err := b.Store.RecordReplyTarget(ctx, target); err != nil {
				return err
			}
			continue
		}
		target.Outcome = "drafted"
		if err := b.Store.RecordReplyTarget(ctx, target); err != nil {
			return err
		}
		cid := d.Contradiction.ID
		id, err := b.Store.InsertXPost(ctx, storage.XPost{Kind: storage.XPostReply, ContradictionID: &cid, PersonID: h.PersonID, ReplyTo: &target.TweetID,
			Slot: slot, PostAt: slot, Variants: d.Versions})
		if err != nil {
			return err
		}
		msg, err := b.Telegram.Send(ctx, ReplyDraftMessage(d, &target, b.Site), replyButtons(id, &target, d))
		if err != nil {
			return fmt.Errorf("send reply draft %d: %w", id, err)
		}
		log.Info().Int64("post", id).Str("tweet", target.TweetID).Int("views", target.Views).Int64("contradiction", cid).Msg("xbot: reply draft sent")
		return b.Store.SetXPostTelegram(ctx, id, msg)
	}
	log.Info().Time("slot", slot).Int("found", len(found)).Int("asked", tried).Msg("xbot: no tweet to reply to for this slot")
	return b.holdSlot(ctx, storage.XPostReply, slot)
}

// replyTo chooses the contradiction that answers the tweet and writes the
// reply; nil when none answers it or no version passes the checks.
func (b *Bot) replyTo(ctx context.Context, person string, t *storage.ReplyTarget, seen *Memory) (*Draft, error) {
	choices, err := b.Store.ReplyChoices(ctx, t.PersonID, replyChoices)
	if err != nil || len(choices) == 0 {
		return nil, err
	}
	choice, err := b.Writer.Write(ctx, ChoicePrompt(person, t, choices))
	if err != nil {
		return nil, err
	}
	picked := false
	for _, c := range choices {
		picked = picked || c.ID == choice.Pick
	}
	if !picked {
		log.Info().Str("tweet", t.TweetID).Str("reasoning", choice.Reasoning).Msg("xbot: no contradiction answers this tweet")
		return nil, nil
	}
	c, err := b.Store.GetBotContradiction(ctx, choice.Pick)
	if err != nil {
		return nil, err
	}
	d, err := ComposeReply(ctx, b.Writer, c, t, ContradictionLink(b.Site, c), nil, seen)
	if err != nil {
		return nil, err
	}
	if len(d.Versions) == 0 {
		log.Warn().Str("tweet", t.TweetID).Int64("contradiction", c.ID).Strs("dropped", d.Dropped).Msg("xbot: no version of the reply passed the checks")
		return nil, nil
	}
	return d, nil
}

// holdSlot records a slot as left, so it is not tried again every minute.
func (b *Bot) holdSlot(ctx context.Context, kind string, slot time.Time) error {
	id, err := b.Store.InsertXPost(ctx, storage.XPost{Kind: kind, Slot: slot, PostAt: slot})
	if err != nil {
		return err
	}
	_, err = b.Store.DecideXPost(ctx, id, "skipped", nil)
	return err
}

// ReplyDraftMessage is the Telegram message of a reply draft: the tweet, the
// contradiction with its sources, the versions.
func ReplyDraftMessage(d *Draft, t *storage.ReplyTarget, site string) string {
	c := d.Contradiction
	esc := html.EscapeString
	var m strings.Builder
	fmt.Fprintf(&m, "↩️ <b>Réponse à proposer sous un tweet de %s</b>\n", esc(c.PersonName))
	reach := ""
	if t.Views > 0 {
		reach = fmt.Sprintf(" · %s vues", thousands(t.Views))
	}
	fmt.Fprintf(&m, "<a href=\"%s\">@%s</a>%s\n<blockquote>%s</blockquote>\n\n", esc(t.URL), esc(t.Author), reach, esc(clip(t.Text, 600)))
	side := func(label string, f storage.BotFact) {
		day := f.DateFR()
		if day == "" {
			day = "date ?"
		}
		fmt.Fprintf(&m, "<b>%s</b> (%s) : %s\n", label, day, esc(f.Text))
		if f.Quote != "" {
			fmt.Fprintf(&m, "<i>« %s »</i>\n", esc(clip(f.Quote, 300)))
		}
	}
	side("Fait 1", c.A)
	side("Fait 2", c.B)
	fmt.Fprintf(&m, "<b>Pourquoi</b> : %s\n<a href=\"%s\">sur le site</a>\n", esc(c.Reason), esc(ContradictionLink(site, &c)))
	for i, v := range d.Versions {
		fmt.Fprintf(&m, "\n<b>%s</b> (%d car.)\n<pre>%s</pre>\n", esc(label(d, i)), Length(v), esc(v))
	}
	m.WriteString("\nLe bouton ouvre X sur la réponse déjà écrite : relisez, puis envoyez depuis le compte. Rien n'est publié sans vous.\n\n" + replyHelp)
	return m.String()
}

// replyButtons open X on each version, then record the outcome.
func replyButtons(id int64, t *storage.ReplyTarget, d *Draft) [][]Button {
	var open []Button
	for i, v := range d.Versions {
		open = append(open, Button{Text: "↩️ Répondre : " + strings.ToLower(label(d, i)), URL: ReplyLink(t.TweetID, v)})
	}
	return [][]Button{open, {
		{Text: "✅ C'est envoyé", Data: fmt.Sprintf("d:%d", id)},
		{Text: "⏭ Passer", Data: fmt.Sprintf("s:%d", id)},
	}}
}

// thousands prints a count with spaces between the thousands.
func thousands(n int) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + " " + s[i:]
	}
	return s
}

// reviseReply writes the versions of a reply draft again as the chat asked.
func (b *Bot) reviseReply(ctx context.Context, p *storage.XPost, request string, tell func(string) error) error {
	if p.ContradictionID == nil || p.ReplyTo == nil {
		return tell("Ce brouillon n'a plus sa contradiction : il reste tel quel.")
	}
	c, err := b.Store.GetBotContradiction(ctx, *p.ContradictionID)
	if errors.Is(err, storage.ErrNoBotContradiction) {
		return tell("Cette contradiction n'est plus sur le site : le brouillon reste tel quel.")
	}
	if err != nil {
		return fmt.Errorf("revise reply %d: %w", p.ID, err)
	}
	t, err := b.Store.GetReplyTarget(ctx, *p.ReplyTo)
	if err != nil {
		return fmt.Errorf("revise reply %d: %w", p.ID, err)
	}
	seen, err := Recall(ctx, b.Store, storage.XPostReply, p.ID)
	if err != nil {
		return err
	}
	d, err := ComposeReply(ctx, b.Writer, c, t, ContradictionLink(b.Site, c), &Revision{Previous: p.Variants, Request: request}, seen)
	if errors.Is(err, llm.ErrUnavailable) {
		log.Warn().Err(err).Int64("post", p.ID).Msg("xbot: LLM unavailable for a revision")
		return tell("Le modèle n'est pas disponible pour l'instant : le brouillon reste tel quel. Répondez à nouveau plus tard.")
	}
	if err != nil {
		return fmt.Errorf("revise reply %d: %w", p.ID, err)
	}
	if len(d.Versions) == 0 {
		return tell("Aucune nouvelle version n'a passé les contrôles ; le brouillon reste tel quel.\n<pre>" + html.EscapeString(strings.Join(d.Dropped, "\n")) + "</pre>")
	}
	msg, err := b.Telegram.Send(ctx, ReplyDraftMessage(d, t, b.Site), replyButtons(p.ID, t, d))
	if err != nil {
		return fmt.Errorf("send revised reply draft %d: %w", p.ID, err)
	}
	ok, err := b.Store.ReviseXPost(ctx, p.ID, d.Versions, msg)
	if err != nil {
		return err
	}
	if !ok {
		return b.Telegram.Edit(ctx, msg, "Cette réponse a été décidée entre-temps : ces versions sont abandonnées.", nil)
	}
	b.closeMessage(ctx, p, "🔁 Réécrit à votre demande : les nouvelles versions sont dans le message plus bas.")
	return nil
}

// ownReply answers a reply draft's "= text": the text is the person's own,
// so the bot gives the link that opens X on it; nothing is posted.
func (b *Bot) ownReply(ctx context.Context, p *storage.XPost, text string) error {
	if p.ReplyTo == nil {
		return nil
	}
	_, err := b.Telegram.Send(ctx, "Votre texte, prêt à envoyer sous le tweet :\n<pre>"+html.EscapeString(text)+"</pre>",
		[][]Button{{{Text: "↩️ Répondre avec ce texte", URL: ReplyLink(*p.ReplyTo, text)}}})
	return err
}
