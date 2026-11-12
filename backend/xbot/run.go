package xbot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// Reviewer is the reviewer name of the verdicts given from Telegram
// (contradiction_reviews): a tweet published is reviewed real, a draft
// marked false is reviewed false.
const Reviewer = "xbot"

// Clock is a time of day.
type Clock struct{ Hour, Min int }

// Bot runs the X bot: before each slot it drafts a tweet and sends it to
// Telegram; a tap approves a version, skips the draft or marks the
// contradiction false; a reply to the draft gives the text instead; an
// approved tweet is posted at its slot, or at once when approved later. A
// draft waits for its decision as long as it takes (XBOT_GRACE_HOURS sets an
// end).
type Bot struct {
	Store    *storage.PostgresStorer
	Writer   Writer
	Telegram *Telegram
	// waitingSlot is the slot whose draft waits for the LLM: its warning is
	// logged once, not every minute.
	waitingSlot time.Time
	// Poster is nil without the X keys: approved tweets then wait.
	Poster Poster
	// Silence reports whether the electoral silence holds at t (the eve and
	// the day of a round): no tweet then.
	Silence func(ctx context.Context, t time.Time) (bool, error)
	Slots   []Clock
	// VoteSlots are the slots of the tweets on the vote; Vote gives the open
	// vote at t (nil: none, no vote tweet).
	VoteSlots []Clock
	Vote      func(ctx context.Context, t time.Time) (*VoteState, error)
	Zone      *time.Location
	// Lead is how long before its slot a draft is sent; Grace how long after
	// its slot a draft may still be approved (0: it never expires).
	Lead, Grace time.Duration
	// Jitter moves each tweet from its slot by a random time within it,
	// earlier or later, so the hours of the bot are not predictable. The
	// draft is sent Lead before the earliest time the tweet may go out.
	Jitter time.Duration
	// Site is the site of the links; Link adds the contradiction's link to
	// the tweet (X bills a tweet with a link more).
	Site string
	Link bool
	Now  func() time.Time
}

// Config is the bot's settings from the environment: XBOT_SLOTS (default
// "12:00,19:00", Europe/Paris), XBOT_VOTE_SLOTS (default "09:00,16:00";
// "none": no vote tweet), XBOT_LINK (default false), XBOT_SITE,
// XBOT_GRACE_HOURS (default 0: a draft never expires), XBOT_JITTER_HOURS
// (default 2: a tweet goes out up to 2 h before or after its slot).
func (b *Bot) Config() error {
	zone, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		return fmt.Errorf("time zone: %w", err)
	}
	b.Zone = zone
	slots := os.Getenv("XBOT_SLOTS")
	if slots == "" {
		slots = "12:00,19:00"
	}
	if b.Slots, err = parseSlots("XBOT_SLOTS", slots); err != nil {
		return err
	}
	voteSlots := os.Getenv("XBOT_VOTE_SLOTS")
	if voteSlots == "" {
		voteSlots = "09:00,16:00"
	}
	if b.VoteSlots, err = parseSlots("XBOT_VOTE_SLOTS", voteSlots); err != nil {
		return err
	}
	for _, v := range b.VoteSlots {
		for _, c := range b.Slots {
			if v == c {
				return fmt.Errorf("XBOT_VOTE_SLOTS: %02d:%02d is also a slot of XBOT_SLOTS", v.Hour, v.Min)
			}
		}
	}
	b.Link, _ = strconv.ParseBool(os.Getenv("XBOT_LINK"))
	b.Site = os.Getenv("XBOT_SITE")
	if b.Site == "" {
		b.Site = "https://tartuffe.me"
	}
	b.Jitter = 2 * time.Hour
	if v := os.Getenv("XBOT_JITTER_HOURS"); v != "" {
		h, err := strconv.ParseFloat(v, 64)
		if err != nil || h < 0 {
			return fmt.Errorf("XBOT_JITTER_HOURS %q: a number of hours, 0 for the slot itself", v)
		}
		b.Jitter = time.Duration(h * float64(time.Hour))
	}
	if b.Lead == 0 {
		b.Lead = time.Hour
	}
	if v := os.Getenv("XBOT_GRACE_HOURS"); v != "" {
		h, err := strconv.ParseFloat(v, 64)
		if err != nil || h < 0 {
			return fmt.Errorf("XBOT_GRACE_HOURS %q: a number of hours, 0 for no expiry", v)
		}
		b.Grace = time.Duration(h * float64(time.Hour))
	}
	if b.Now == nil {
		b.Now = time.Now
	}
	return nil
}

// parseSlots reads times of day ("12:00,19:00"); "none" is no slot.
func parseSlots(name, list string) ([]Clock, error) {
	if strings.TrimSpace(list) == "none" {
		return nil, nil
	}
	var out []Clock
	for _, s := range strings.Split(list, ",") {
		t, err := time.Parse("15:04", strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("%s %q: %w", name, s, err)
		}
		out = append(out, Clock{t.Hour(), t.Minute()})
	}
	return out, nil
}

// Run serves the bot until ctx ends: the chat's taps and replies as they
// come, the slots every minute.
func (b *Bot) Run(ctx context.Context) {
	go func() {
		failures := 0
		for ctx.Err() == nil {
			updates, err := b.Telegram.Updates(ctx, 50*time.Second)
			if err != nil {
				if ctx.Err() == nil {
					failures++
					// One line at the first failure, then one every 30 (about 5 min):
					// on 2026-11-11 another reader of the same bot ("Conflict:
					// terminated by other getUpdates request") made this fail every
					// 13 s for 4 h 30, the taps on the drafts were lost and nobody knew.
					if failures == 1 || failures%30 == 0 {
						log.Warn().Err(err).Int("consecutive", failures).Msg("xbot: reading Telegram failed; retrying")
					}
					if failures == 30 {
						if _, sendErr := b.Telegram.Send(ctx, "⚠️ Le bot ne lit plus ce canal depuis 5 minutes : les boutons et les réponses sont peut-être perdus.\n<code>"+html.EscapeString(err.Error())+"</code>", nil); sendErr != nil {
							log.Error().Err(sendErr).Msg("xbot: could not send the Telegram reading alert")
						}
					}
					sleep(ctx, 10*time.Second)
				}
				continue
			}
			failures = 0
			for _, u := range updates {
				if err := b.Handle(ctx, u); err != nil {
					log.Error().Err(err).Str("data", u.Data).Msg("xbot: handling a Telegram update failed")
				}
			}
		}
	}()
	for ctx.Err() == nil {
		if err := b.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("xbot: tick failed")
		}
		sleep(ctx, time.Minute)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// nextSlots returns the times of the slots in [from, to).
func (b *Bot) nextSlots(slots []Clock, from, to time.Time) []time.Time {
	var out []time.Time
	day := from.In(b.Zone)
	for d := 0; d < 3; d++ {
		y, m, dd := day.AddDate(0, 0, d).Date()
		for _, c := range slots {
			t := time.Date(y, m, dd, c.Hour, c.Min, 0, 0, b.Zone)
			if !t.Before(from) && t.Before(to) {
				out = append(out, t)
			}
		}
	}
	return out
}

// postAt is when the tweet of a slot goes out: the slot moved by a random
// time within Jitter, to the minute.
func (b *Bot) postAt(slot time.Time) time.Time {
	minutes := int64(b.Jitter / time.Minute)
	if minutes <= 0 {
		return slot
	}
	return slot.Add(time.Duration(rand.Int63n(2*minutes+1)-minutes) * time.Minute)
}

// Tick drafts the slots due within Lead, posts the approved tweets whose slot
// came, and expires the drafts past their grace when there is one.
func (b *Bot) Tick(ctx context.Context) error {
	now := b.Now()
	if err := b.draftSlots(ctx, b.Slots, now, b.Draft); err != nil {
		return err
	}
	if err := b.draftSlots(ctx, b.VoteSlots, now, b.DraftVote); err != nil {
		return err
	}
	if err := b.PostDue(ctx); err != nil {
		return err
	}
	if b.Grace <= 0 {
		return nil
	}
	expired, err := b.Store.ExpireXPosts(ctx, now.Add(-b.Grace))
	if err != nil {
		return err
	}
	for _, p := range expired {
		b.closeMessage(ctx, &p, "⏱ Expiré : pas de décision à temps, rien n'a été publié.")
	}
	return nil
}

// draftSlots drafts the slots due within Lead of the earliest time their
// tweet may go out (Lead + Jitter before the slot) that hold no tweet yet and
// are not under the electoral silence.
func (b *Bot) draftSlots(ctx context.Context, slots []Clock, now time.Time, draft func(context.Context, time.Time) error) error {
	for _, slot := range b.nextSlots(slots, now, now.Add(b.Lead+b.Jitter)) {
		taken, err := b.Store.XPostSlotTaken(ctx, slot)
		if err != nil {
			return err
		}
		if taken {
			continue
		}
		if b.Silence != nil {
			silent, err := b.Silence(ctx, slot)
			if err != nil {
				return err
			}
			if silent {
				continue
			}
		}
		if err := draft(ctx, slot); err != nil {
			if errors.Is(err, llm.ErrUnavailable) {
				if !b.waitingSlot.Equal(slot) {
					b.waitingSlot = slot
					log.Warn().Err(err).Time("slot", slot).Msg("xbot: LLM unavailable; the draft is tried again every tick until its slot")
				}
				break
			}
			return fmt.Errorf("draft for %s: %w", slot, err)
		}
	}
	return nil
}

// Draft writes the tweet of a slot and sends it: the first contradiction
// with a version that passes the checks, among a few, of a person with no
// open draft. Nothing to tweet: the slot is left.
func (b *Bot) Draft(ctx context.Context, slot time.Time) error {
	open, err := b.Store.OpenXPostPersons(ctx)
	if err != nil {
		return err
	}
	picked, err := b.Store.PickBotContradictions(ctx, 3, open)
	if err != nil {
		return err
	}
	for i := range picked {
		c := &picked[i]
		pool, err := b.Store.BotTagPool(ctx, c, Domains(c))
		if err != nil {
			return err
		}
		link := ""
		if b.Link {
			link = ContradictionLink(b.Site, c)
		}
		d, err := Compose(ctx, b.Writer, c, ChooseTags(pool, MaxTags), link)
		if err != nil {
			return err
		}
		if len(d.Versions) == 0 {
			log.Warn().Int64("contradiction", c.ID).Strs("dropped", d.Dropped).Msg("xbot: no version passed the checks")
			continue
		}
		var quote *string
		if d.QuoteID, d.QuoteLink = QuoteTarget(c, pool.Person); d.QuoteID != "" {
			quote = &d.QuoteID
		}
		cid := c.ID
		at := b.postAt(slot)
		id, err := b.Store.InsertXPost(ctx, storage.XPost{ContradictionID: &cid, PersonID: c.PersonID, QuoteTweetID: quote, Slot: slot, PostAt: at, Variants: d.Versions, Tags: d.Tags})
		if err != nil {
			return err
		}
		msg, err := b.Telegram.Send(ctx, DraftMessage(d, at.In(b.Zone), b.Site), draftButtons(id, d.Labels))
		if err != nil {
			return fmt.Errorf("send draft %d: %w", id, err)
		}
		log.Info().Int64("post", id).Int64("contradiction", c.ID).Time("slot", slot).Time("post_at", at).Msg("xbot: draft sent")
		return b.Store.SetXPostTelegram(ctx, id, msg)
	}
	log.Warn().Time("slot", slot).Msg("xbot: nothing to tweet for this slot")
	return nil
}

// draftButtons publishes a version by its label, skips, or marks false.
func draftButtons(id int64, labels []string) [][]Button {
	var publish []Button
	for i, l := range labels {
		publish = append(publish, Button{Text: "✅ Publier " + strings.ToLower(l), Data: fmt.Sprintf("p:%d:%d", id, i+1)})
	}
	return [][]Button{publish, {
		{Text: "⏭ Passer", Data: fmt.Sprintf("s:%d", id)},
		{Text: "❌ Fausse contradiction", Data: fmt.Sprintf("f:%d", id)},
	}}
}

// DraftMessage is the Telegram message of a draft: the two facts with their
// sources, the reason, the versions, how to answer. at is when the tweet
// goes out once approved.
func DraftMessage(d *Draft, at time.Time, site string) string {
	c := d.Contradiction
	esc := html.EscapeString
	var b strings.Builder
	reviewed := "non revue"
	if c.Reviewed {
		reviewed = "revue : réelle"
	}
	fmt.Fprintf(&b, "🐦 <b>Tweet de %s</b>\n<b>%s</b> · %s · %s\n\n", at.Format("02/01 15:04"), esc(c.PersonName), esc(c.StanceLabel), reviewed)
	side := func(label string, f storage.BotFact) {
		day := f.DateFR()
		if day == "" {
			day = "date ?"
		}
		fmt.Fprintf(&b, "<b>%s</b> (%s) : %s\n", label, day, esc(f.Text))
		if f.Quote != "" {
			fmt.Fprintf(&b, "<i>« %s »</i>\n", esc(clip(f.Quote, 400)))
		}
		for i, u := range f.SourceURLs {
			if i == 2 {
				break
			}
			fmt.Fprintf(&b, "<a href=\"%s\">source %d</a> ", esc(u), i+1)
		}
		b.WriteString("\n\n")
	}
	side("Fait 1", c.A)
	side("Fait 2", c.B)
	fmt.Fprintf(&b, "<b>Pourquoi</b> : %s\n<a href=\"%s\">sur le site</a>\n", esc(c.Reason), esc(ContradictionLink(site, &c)))
	if d.QuoteLink != "" {
		fmt.Fprintf(&b, "🔁 Le tweet citera <a href=\"%s\">son propre tweet</a>.\n", esc(d.QuoteLink))
	}
	for i, v := range d.Versions {
		fmt.Fprintf(&b, "\n<b>%s</b> (%d car.)\n<pre>%s</pre>\n", esc(label(d, i)), Length(v), esc(v))
	}
	b.WriteString("\nPour d'autres versions : répondez à ce message avec votre demande (« plus drôle », « ne cite pas toute la phrase »).\nPour publier votre propre texte : répondez avec <code>=</code> suivi du tweet (les tags sont ajoutés s'il n'en a pas).")
	return b.String()
}

// label is the name of a version: its style, else its number.
func label(d *Draft, i int) string {
	if i < len(d.Labels) {
		return d.Labels[i]
	}
	return fmt.Sprintf("Version %d", i+1)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Handle acts on what the chat sent.
func (b *Bot) Handle(ctx context.Context, u Update) error {
	if u.CallbackID != "" {
		notice, err := b.tap(ctx, u)
		if aerr := b.Telegram.Answer(ctx, u.CallbackID, notice); aerr != nil {
			log.Warn().Err(aerr).Msg("xbot: answering a tap failed")
		}
		return err
	}
	if u.ReplyTo == 0 || strings.TrimSpace(u.Text) == "" {
		return nil
	}
	p, err := b.Store.XPostByTelegramMessage(ctx, u.ReplyTo)
	if errors.Is(err, storage.ErrNoXPost) {
		return nil
	}
	if err != nil {
		return err
	}
	text := strings.TrimSpace(u.Text)
	if p.Status != "draft" {
		_, err := b.Telegram.Send(ctx, "Ce tweet est déjà décidé ("+p.Status+") : rien n'a changé.", nil)
		return err
	}
	// A reply is a request for other versions; "=" before it makes it the
	// tweet itself. A vote tweet has no writer: its reply is the tweet.
	own := p.ContradictionID == nil
	if rest, ok := strings.CutPrefix(text, "="); ok {
		own, text = true, strings.TrimSpace(rest)
	}
	if text == "" {
		return nil
	}
	if !own {
		return b.revise(ctx, p, text)
	}
	if !strings.Contains(text, "@") && len(p.Tags) > 0 {
		text += "\n\n@" + strings.Join(p.Tags, " @")
	}
	if n := Length(text); n > MaxLength {
		_, err := b.Telegram.Send(ctx, fmt.Sprintf("Trop long : %d caractères sur %d. Rien n'a changé.", n, MaxLength), nil)
		return err
	}
	return b.approve(ctx, p, text)
}

// revise writes the versions of a draft again as the chat asked and sends
// them as a new message with the buttons; the message before loses its own.
// No version passes the checks, or the LLM is away: the draft stays as it is
// and the chat is told.
func (b *Bot) revise(ctx context.Context, p *storage.XPost, request string) error {
	tell := func(text string) error {
		_, err := b.Telegram.Send(ctx, text, nil)
		return err
	}
	if err := tell("✍️ Je réécris : les nouvelles versions arrivent dans une minute ou deux."); err != nil {
		return err
	}
	c, err := b.Store.GetBotContradiction(ctx, *p.ContradictionID)
	if err != nil {
		return fmt.Errorf("revise post %d: %w", p.ID, err)
	}
	link := ""
	if b.Link {
		link = ContradictionLink(b.Site, c)
	}
	d, err := Recompose(ctx, b.Writer, c, p.Tags, link, &Revision{Previous: p.Variants, Request: request})
	if errors.Is(err, llm.ErrUnavailable) {
		log.Warn().Err(err).Int64("post", p.ID).Msg("xbot: LLM unavailable for a revision")
		return tell("Le modèle n'est pas disponible pour l'instant : le brouillon reste tel quel. Répondez à nouveau plus tard.")
	}
	if err != nil {
		return fmt.Errorf("revise post %d: %w", p.ID, err)
	}
	if len(d.Versions) == 0 {
		log.Warn().Int64("post", p.ID).Strs("dropped", d.Dropped).Msg("xbot: no revised version passed the checks")
		return tell("Aucune nouvelle version n'a passé les contrôles ; le brouillon reste tel quel.\n<pre>" + html.EscapeString(strings.Join(d.Dropped, "\n")) + "</pre>")
	}
	pool, err := b.Store.BotTagPool(ctx, c, Domains(c))
	if err != nil {
		return err
	}
	if p.QuoteTweetID != nil {
		_, d.QuoteLink = QuoteTarget(c, pool.Person)
	}
	msg, err := b.Telegram.Send(ctx, DraftMessage(d, p.PostAt.In(b.Zone), b.Site), draftButtons(p.ID, d.Labels))
	if err != nil {
		return fmt.Errorf("send revised draft %d: %w", p.ID, err)
	}
	ok, err := b.Store.ReviseXPost(ctx, p.ID, d.Versions, msg)
	if err != nil {
		return err
	}
	if !ok {
		return b.Telegram.Edit(ctx, msg, "Ce tweet a été décidé entre-temps : ces versions sont abandonnées.", nil)
	}
	b.closeMessage(ctx, p, "🔁 Réécrit à votre demande : les nouvelles versions sont dans le message plus bas.")
	log.Info().Int64("post", p.ID).Str("request", request).Msg("xbot: draft revised")
	return nil
}

func (b *Bot) tap(ctx context.Context, u Update) (string, error) {
	parts := strings.Split(u.Data, ":")
	if len(parts) < 2 {
		return "", fmt.Errorf("tap data %q", u.Data)
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", fmt.Errorf("tap data %q: %w", u.Data, err)
	}
	p, err := b.Store.GetXPost(ctx, id)
	if err != nil {
		return "Brouillon introuvable", err
	}
	if p.Status != "draft" {
		return "Déjà décidé : " + p.Status, nil
	}
	switch parts[0] {
	case "p":
		if len(parts) != 3 {
			return "", fmt.Errorf("tap data %q", u.Data)
		}
		n, err := strconv.Atoi(parts[2])
		if err != nil || n < 1 || n > len(p.Variants) {
			return "", fmt.Errorf("tap data %q: no such version", u.Data)
		}
		return "Approuvé", b.approve(ctx, p, p.Variants[n-1])
	case "s":
		if _, err := b.Store.DecideXPost(ctx, p.ID, "skipped", nil); err != nil {
			return "", err
		}
		b.closeMessage(ctx, p, "⏭ Passé : la contradiction pourra revenir plus tard.")
		return "Passé", nil
	case "f":
		if _, err := b.Store.DecideXPost(ctx, p.ID, "false", nil); err != nil {
			return "", err
		}
		note := "❌ Fausse contradiction : retirée du site."
		if p.ContradictionID != nil {
			if err := b.Store.RecordContradictionReview(ctx, Reviewer, storage.ContradictionReview{ContradictionID: *p.ContradictionID, Verdict: "false", Note: "marked false from the X bot"}); err != nil {
				return "", err
			}
			if err := b.Store.DismissContradictionAsFalsePositive(ctx, *p.ContradictionID); err != nil {
				return "", fmt.Errorf("dismiss contradiction %d: %w", *p.ContradictionID, err)
			}
		}
		b.closeMessage(ctx, p, note)
		return "Retirée", nil
	}
	return "", fmt.Errorf("tap data %q: unknown action", u.Data)
}

// approve records the text chosen and the contradiction reviewed real, then
// posts at once when the slot has come.
func (b *Bot) approve(ctx context.Context, p *storage.XPost, text string) error {
	ok, err := b.Store.DecideXPost(ctx, p.ID, "approved", &text)
	if err != nil || !ok {
		return err
	}
	if p.ContradictionID != nil {
		if err := b.Store.RecordContradictionReview(ctx, Reviewer, storage.ContradictionReview{ContradictionID: *p.ContradictionID, Verdict: "real", Note: "approved for the X bot"}); err != nil {
			log.Error().Err(err).Int64("post", p.ID).Msg("xbot: recording the review failed")
		}
	}
	p.Chosen, p.Status = &text, "approved"
	when := "sera publié à " + p.PostAt.In(b.Zone).Format("15:04")
	if !b.Now().Before(p.PostAt) {
		when = "publication…"
	}
	if b.Poster == nil {
		when += " (pas encore de clés X : il attend)"
	}
	b.closeMessage(ctx, p, "✅ Approuvé, "+when+"\n<pre>"+html.EscapeString(text)+"</pre>")
	return b.PostDue(ctx)
}

// PostDue posts the approved tweets whose slot came. Without a poster they
// wait.
func (b *Bot) PostDue(ctx context.Context) error {
	if b.Poster == nil {
		return nil
	}
	due, err := b.Store.DueXPosts(ctx, b.Now())
	if err != nil {
		return err
	}
	for i := range due {
		p := &due[i]
		if p.Chosen == nil {
			continue
		}
		if b.Silence != nil {
			if silent, err := b.Silence(ctx, b.Now()); err != nil || silent {
				return err
			}
		}
		quote := ""
		if p.QuoteTweetID != nil {
			quote = *p.QuoteTweetID
		}
		id, err := b.Poster.Post(ctx, *p.Chosen, quote)
		if err != nil {
			log.Error().Err(err).Int64("post", p.ID).Msg("xbot: posting failed")
			if merr := b.Store.MarkXPostFailed(ctx, p.ID, err.Error()); merr != nil {
				return merr
			}
			b.closeMessage(ctx, p, "⚠️ Échec de la publication : "+html.EscapeString(err.Error()))
			continue
		}
		if err := b.Store.MarkXPostPosted(ctx, p.ID, id, p.Tags); err != nil {
			return err
		}
		log.Info().Int64("post", p.ID).Str("tweet", id).Msg("xbot: tweet posted")
		b.closeMessage(ctx, p, fmt.Sprintf("🚀 Publié : https://x.com/i/status/%s\n<pre>%s</pre>", id, html.EscapeString(*p.Chosen)))
	}
	return nil
}

// closeMessage replaces the buttons of a draft's message by its outcome.
func (b *Bot) closeMessage(ctx context.Context, p *storage.XPost, outcome string) {
	if p.TelegramMessageID == nil {
		return
	}
	if err := b.Telegram.Edit(ctx, *p.TelegramMessageID, outcome, nil); err != nil {
		log.Warn().Err(err).Int64("post", p.ID).Msg("xbot: updating the Telegram message failed")
	}
}
