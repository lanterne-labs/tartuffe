package xbot

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// StandingsFrom is how many counted ballots the vote needs before the bot
// may tweet its results. Below it the tweets only call to vote: a ranking of
// a few dozen ballots says nothing and reads as a poll.
const StandingsFrom = 300

// VoteState is the vote the bot tweets about: the open event and how many
// ballots its latest tally counted.
type VoteState struct {
	Slug       string
	MaxChoices int
	MinVoters  int
	Counted    int
}

// voteTexts are the calls to vote, taken in turn. Every line must stay true
// to the vote as /election/<slug>/fonctionnement tells it. %d is how many
// candidates a ballot ranks.
var voteTexts = []string{
	"Présidentielle 2027 : et si vous votiez dès maintenant ? Classez jusqu'à %d candidats, ou votez blanc. Votre passeport prouve que vous êtes électeur, sans dire qui vous êtes.",
	"Un vote où personne, ni nous ni l'État, ne peut savoir qui a voté quoi. Où chacun peut recompter. Et où vous pouvez changer d'avis jusqu'à la clôture. Il est ouvert, pour 2027.",
	"Avant de choisir votre candidat pour 2027, regardez son Tartuffe-Score : il s'affiche à côté de chaque nom au moment de voter.",
	"Pour voter ici, il suffit de votre passeport et de votre téléphone. Aucun compte, aucun e-mail, et votre vote reste secret.",
	"Ni scrutin officiel ni sondage : un vote d'opinion, ouvert à tout électeur français muni d'un passeport, anonyme, vérifiable par tous. Qui choisiriez-vous pour 2027 ?",
	"Aucun candidat ne vous convient pour 2027 ? Dites-le : le vote blanc est compté à part. Sinon, classez-en jusqu'à %d.",
}

// VoteLink is the page of the vote on the site.
func VoteLink(site, slug string) string {
	return fmt.Sprintf("%s/election/%s", strings.TrimRight(site, "/"), slug)
}

// VoteTweet is the n-th call to vote, with the ballots counted so far once
// the event shows its results (its minimum of voters) and the link.
func VoteTweet(v *VoteState, n int, site string) string {
	text := voteTexts[n%len(voteTexts)]
	if strings.Contains(text, "%d") {
		text = fmt.Sprintf(text, v.MaxChoices)
	}
	if v.MinVoters > 0 && v.Counted >= v.MinVoters {
		if with := text + fmt.Sprintf(" Déjà %d bulletins.", v.Counted); Length(with)+1+LinkLength <= MaxLength {
			text = with
		}
	}
	return text + "\n" + VoteLink(site, v.Slug)
}

// DraftVote sends the vote tweet of a slot to Telegram. No open vote: the
// slot is left.
func (b *Bot) DraftVote(ctx context.Context, slot time.Time) error {
	if b.Vote == nil {
		return nil
	}
	v, err := b.Vote(ctx, slot)
	if err != nil {
		return fmt.Errorf("vote state: %w", err)
	}
	if v == nil {
		log.Info().Time("slot", slot).Msg("xbot: no open vote; no vote tweet for this slot")
		return nil
	}
	n, err := b.Store.CountXPosts(ctx, storage.XPostVote)
	if err != nil {
		return err
	}
	text := VoteTweet(v, n, b.Site)
	id, err := b.Store.InsertXPost(ctx, storage.XPost{Kind: storage.XPostVote, Slot: slot, Variants: []string{text}})
	if err != nil {
		return err
	}
	var m strings.Builder
	fmt.Fprintf(&m, "🗳 <b>Tweet du vote de %s</b>\n%d bulletins comptés\n", slot.In(b.Zone).Format("02/01 15:04"), v.Counted)
	if v.Counted >= StandingsFrom {
		fmt.Fprintf(&m, "⚠️ Le seuil de %d bulletins est passé : les résultats peuvent être tweetés.\n", StandingsFrom)
	}
	fmt.Fprintf(&m, "\n<pre>%s</pre>\n\nPour publier votre propre texte : répondez à ce message avec le tweet.", html.EscapeString(text))
	msg, err := b.Telegram.Send(ctx, m.String(), [][]Button{{
		{Text: "✅ Publier", Data: fmt.Sprintf("p:%d:1", id)},
		{Text: "⏭ Passer", Data: fmt.Sprintf("s:%d", id)},
	}})
	if err != nil {
		return fmt.Errorf("send vote draft %d: %w", id, err)
	}
	log.Info().Int64("post", id).Time("slot", slot).Int("counted", v.Counted).Msg("xbot: vote draft sent")
	return b.Store.SetXPostTelegram(ctx, id, msg)
}

// CountedBallots reads how many ballots the latest tally of a poll counted,
// from the vote's public mirror (polls/<id>/tallies/latest.json, isoloir SPEC
// §15). The proofs are not checked here: the number only decides what the bot
// proposes, and a person approves every tweet. No tally yet: 0.
func CountedBallots(ctx context.Context, mirror string, poll int) (int, error) {
	url := fmt.Sprintf("%s/polls/%d/tallies/latest.json?t=%d", strings.TrimRight(mirror, "/"), poll, time.Now().Unix())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return 0, fmt.Errorf("read the tally: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, fmt.Errorf("read the tally: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("read the tally: status %d", resp.StatusCode)
	}
	var entry struct {
		Kind string `json:"kind"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil || entry.Kind != "tally" {
		return 0, fmt.Errorf("read the tally: not a tally entry")
	}
	var body struct {
		Counted int `json:"counted"`
	}
	if err := json.Unmarshal([]byte(entry.Body), &body); err != nil {
		return 0, fmt.Errorf("read the tally body: %w", err)
	}
	return body.Counted, nil
}
