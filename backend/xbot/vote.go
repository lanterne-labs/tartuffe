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

// voteTexts are what the bot may say of the vote: the writer of a vote tweet
// gets these lines and nothing else. Every line must stay true to the vote
// as /election/<slug>/fonctionnement tells it. %d is how many candidates a
// ballot ranks.
var voteTexts = []string{
	"Présidentielle 2027 : et si vous votiez dès maintenant ? Classez jusqu'à %d candidats, ou votez blanc. Votre passeport prouve que vous êtes électeur, sans dire qui vous êtes.",
	"Un vote où personne, ni nous ni l'État, ne peut savoir qui a voté quoi. Où chacun peut recompter. Et où vous pouvez changer d'avis jusqu'à la clôture. Il est ouvert, pour 2027.",
	"Avant de choisir votre candidat pour 2027, regardez son Tartuffe-Score : il s'affiche à côté de chaque nom au moment de voter.",
	"Pour voter ici, il suffit de votre passeport et de votre téléphone. Aucun compte, aucun e-mail, et votre vote reste secret.",
	"Ni scrutin officiel ni sondage : un vote d'opinion, ouvert à tout électeur français muni d'un passeport, anonyme, vérifiable par tous. Qui choisiriez-vous pour 2027 ?",
	"Aucun candidat ne vous convient pour 2027 ? Dites-le : le vote blanc est compté à part. Sinon, classez-en jusqu'à %d.",
}

// siteTexts are what the bot may say of the site in a vote tweet. Every line
// must stay true to /about.
var siteTexts = []string{
	"tartuffe.me met côte à côte ce qu'un candidat à la présidentielle a dit et ce qu'il a dit ou fait ensuite.",
	"Chaque contradiction montre les deux déclarations, leurs dates et leurs sources : chacun peut vérifier.",
	"Le Tartuffe-Score résume la cohérence de chaque candidat, de « cohérent » à « tartuffe ».",
	"La même méthode s'applique à tous les candidats.",
	"Une erreur peut être signalée ; les corrections sont publiques.",
	"Tartuffe est l'hypocrite de la pièce de Molière.",
}

// voteFacts are the lines of voteTexts for this vote.
func voteFacts(v *VoteState) []string {
	out := make([]string, 0, len(voteTexts))
	for _, line := range voteTexts {
		if strings.Contains(line, "%d") {
			line = fmt.Sprintf(line, v.MaxChoices)
		}
		out = append(out, line)
	}
	return out
}

// VoteLink is the page of the vote on the site.
func VoteLink(site, slug string) string {
	return fmt.Sprintf("%s/election/%s", strings.TrimRight(site, "/"), slug)
}

// voteAngleTries is how many angles a slot tries before it gives up: an
// angle whose versions all fail the checks gives way to the next.
const voteAngleTries = 3

// voteTextRoom is the least room the text of a vote tweet keeps: tags that
// leave less are left out, the last first.
const voteTextRoom = 170

// DraftVote writes the vote tweet of a slot and sends it to Telegram: the
// angle least recently used, written by the LLM in two versions that were
// never proposed before. A tweet of an angle that quotes is posted as a quote
// of our latest tweet on a contradiction not quoted yet. No open vote: the
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
	used, err := b.Store.VoteAngleUses(ctx)
	if err != nil {
		return err
	}
	target, err := b.Store.VoteQuoteTarget(ctx, slot.Add(-VoteQuoteWindow))
	if err != nil {
		return err
	}
	said, err := b.Store.PickVoteFact(ctx, slot.Add(-VoteSaidWindow))
	if err != nil {
		return err
	}
	seen, err := Recall(ctx, b.Store, storage.XPostVote, 0)
	if err != nil {
		return err
	}
	tags, err := VoteTags(ctx, b.Store)
	if err != nil {
		return err
	}
	for len(tags) > 0 && MaxLength-Length(voteSuffix(tags, VoteLink(b.Site, v.Slug))) < voteTextRoom {
		tags = tags[:len(tags)-1]
	}
	at := b.postAt(slot)
	var dropped []string
	for i, angle := range PickVoteAngles(used, v, slot.In(b.Zone), target != nil, said != nil) {
		if i == voteAngleTries {
			break
		}
		req := VoteRequest{State: v, Angle: angle, Site: b.Site, Tags: tags, Seen: seen}
		var quote *string
		if angle.Quote {
			req.Quoted, quote = Body(*target.Chosen), target.TweetID
		}
		post := storage.XPost{Kind: storage.XPostVote, QuoteTweetID: quote, Slot: slot, PostAt: at, Tags: tags, Angle: angle.Key}
		if angle.Said {
			req.Said, post.FactID, post.PersonID = said, &said.ID, said.PersonID
		}
		versions, why, err := ComposeVote(ctx, b.Writer, req)
		if err != nil {
			return err
		}
		if len(versions) == 0 {
			log.Warn().Str("angle", angle.Key).Strs("dropped", why).Msg("xbot: no version of the vote tweet passed the checks; trying another angle")
			dropped = append(dropped, why...)
			continue
		}
		post.Variants = versions
		id, err := b.Store.InsertXPost(ctx, post)
		if err != nil {
			return err
		}
		msg, err := b.Telegram.Send(ctx, VoteDraftMessage(at.In(b.Zone), voteNote(v, req, quote), versions), voteButtons(id, len(versions)))
		if err != nil {
			return fmt.Errorf("send vote draft %d: %w", id, err)
		}
		log.Info().Int64("post", id).Time("slot", slot).Str("angle", angle.Key).Msg("xbot: vote draft sent")
		return b.Store.SetXPostTelegram(ctx, id, msg)
	}
	// Nothing new to say for this slot: it is held, so the writer is not
	// asked again every minute until it passes.
	log.Warn().Time("slot", slot).Strs("dropped", dropped).Msg("xbot: no vote tweet for this slot")
	id, err := b.Store.InsertXPost(ctx, storage.XPost{Kind: storage.XPostVote, Slot: slot, PostAt: at})
	if err != nil {
		return err
	}
	if _, err := b.Store.DecideXPost(ctx, id, "skipped", nil); err != nil {
		return err
	}
	_, err = b.Telegram.Send(ctx, fmt.Sprintf("🗳 Pas de tweet du vote pour %s : aucune version nouvelle n'a passé les contrôles.\n<pre>%s</pre>",
		slot.In(b.Zone).Format("02/01 15:04"), html.EscapeString(strings.Join(dropped, "\n"))), nil)
	return err
}

// voteNote is what the Telegram draft of a vote tweet tells the approver
// above the versions: the angle, the tweet it quotes or the words it starts
// from, and the ballots counted, which are for them alone (a tweet never
// gives the count).
func voteNote(v *VoteState, r VoteRequest, quote *string) string {
	esc := html.EscapeString
	note := "Angle : " + esc(r.Angle.Label)
	if quote != nil {
		note += fmt.Sprintf("\n🔁 Il citera <a href=\"https://x.com/i/status/%s\">notre tweet</a> : <i>%s</i>", *quote, esc(clip(r.Quoted, 200)))
	}
	if f := r.Said; f != nil {
		note += fmt.Sprintf("\n🗣 <b>%s</b> (%s) : <i>« %s »</i>", esc(f.PersonName), f.DateFR(), esc(clip(f.Quote, 400)))
		if len(f.SourceURLs) > 0 {
			note += fmt.Sprintf(" <a href=\"%s\">source</a>", esc(f.SourceURLs[0]))
		}
	}
	note += fmt.Sprintf("\nℹ️ %d bulletins comptés (pour vous : un tweet ne donne jamais ce nombre)", v.Counted)
	if v.Counted >= StandingsFrom {
		note += fmt.Sprintf("\n⚠️ Le seuil de %d bulletins est passé : les résultats peuvent être tweetés.", StandingsFrom)
	}
	return note
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
