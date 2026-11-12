package xbot

import (
	"context"
	"errors"
	"fmt"
	"html"
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
// approved tweet is posted at its slot, a draft still undecided an hour after
// its slot expires.
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
	// its slot a draft may still be approved.
	Lead, Grace time.Duration
	// Site is the site of the links; Link adds the contradiction's link to
	// the tweet (X bills a tweet with a link more).
	Site string
	Link bool
	Now  func() time.Time
}

// Config is the bot's settings from the environment: XBOT_SLOTS (default
// "12:00,19:00", Europe/Paris), XBOT_VOTE_SLOTS (default "09:00,16:00";
// "none": no vote tweet), XBOT_LINK (default false), XBOT_SITE.
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
	if b.Lead == 0 {
		b.Lead = time.Hour
	}
	if b.Grace == 0 {
		b.Grace = time.Hour
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

// Tick drafts the slots due within Lead, posts the approved tweets whose slot
// came, and expires the drafts past their grace.
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
	expired, err := b.Store.ExpireXPosts(ctx, now.Add(-b.Grace))
	if err != nil {
		return err
	}
	for _, p := range expired {
		b.closeMessage(ctx, &p, "⏱ Expiré : pas de décision à temps, rien n'a été publié.")
	}
	return nil
}

// draftSlots drafts the slots due within Lead that hold no tweet yet and are
// not under the electoral silence.
func (b *Bot) draftSlots(ctx context.Context, slots []Clock, now time.Time, draft func(context.Context, time.Time) error) error {
	for _, slot := range b.nextSlots(slots, now, now.Add(b.Lead)) {
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
		id, err := b.Store.InsertXPost(ctx, storage.XPost{ContradictionID: &cid, PersonID: c.PersonID, QuoteTweetID: quote, Slot: slot, Variants: d.Versions, Tags: d.Tags})
		if err != nil {
			return err
		}
		msg, err := b.Telegram.Send(ctx, DraftMessage(d, slot.In(b.Zone), b.Site), draftButtons(id, d.Labels))
		if err != nil {
			return fmt.Errorf("send draft %d: %w", id, err)
		}
		log.Info().Int64("post", id).Int64("contradiction", c.ID).Time("slot", slot).Msg("xbot: draft sent")
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
// sources, the reason, the versions, how to answer.
func DraftMessage(d *Draft, slot time.Time, site string) string {
	c := d.Contradiction
	esc := html.EscapeString
	var b strings.Builder
	reviewed := "non revue"
	if c.Reviewed {
		reviewed = "revue : réelle"
	}
	fmt.Fprintf(&b, "🐦 <b>Tweet de %s</b>\n<b>%s</b> · %s · %s\n\n", slot.Format("02/01 15:04"), esc(c.PersonName), esc(c.StanceLabel), reviewed)
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
	b.WriteString("\nPour publier votre propre texte : répondez à ce message avec le tweet (les tags sont ajoutés s'il n'en a pas).")
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
	if !strings.Contains(text, "@") && len(p.Tags) > 0 {
		text += "\n\n@" + strings.Join(p.Tags, " @")
	}
	if n := Length(text); n > MaxLength {
		_, err := b.Telegram.Send(ctx, fmt.Sprintf("Trop long : %d caractères sur %d. Rien n'a changé.", n, MaxLength), nil)
		return err
	}
	return b.approve(ctx, p, text)
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
	when := "sera publié à " + p.Slot.In(b.Zone).Format("15:04")
	if !b.Now().Before(p.Slot) {
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
