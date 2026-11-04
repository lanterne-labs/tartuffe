package frpolitics

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// RewordResult is what RewordAmendmentBallots did.
type RewordResult struct {
	// Scrutins and Ballots count the amendment scrutins and the ballots
	// rewritten (with dryRun: that would be).
	Scrutins int
	Ballots  int
	// Kept lists the scrutins whose amendment got no confident statement (or
	// none from the archive): their ballots keep the quoted exposé.
	Kept []string
	// Reopened counts the contradictions on the rewritten ballots removed
	// with their pair verdicts (rejudge).
	Reopened int
}

// oldAmendmentPurposeRe matches, in a ballot, the amendment object as
// purposePhrase wrote it: "l'amendement n° N (groupe) dont l'objet est : « … »",
// "… dont le dispositif est : « … »" or "… visant à …", up to the ", déposé
// sur" that follows. Group 1 is the amendment and its group, kept.
var oldAmendmentPurposeRe = regexp.MustCompile(`((?:l'amendement|le sous-amendement) n° [0-9]+(?: \([^)]*\))?) (?:dont (?:l'objet|le dispositif) est : « .*? »|visant à .*?), déposé sur`)

// rewordBallot replaces the quoted exposé of the amendment in text with
// purpose. ok is false when text carries no such object.
func rewordBallot(text, purpose string) (string, bool) {
	loc := oldAmendmentPurposeRe.FindStringSubmatchIndex(text)
	if loc == nil {
		return text, false
	}
	head := text[loc[2]:loc[3]]
	return text[:loc[0]] + head + " " + purpose + ", déposé sur" + text[loc[1]:], true
}

// RewordAmendmentBallots rewrites the ballots of the amendment scrutins among
// laws whose object is the quoted exposé with the amendment's statement
// (amendmentStatements). With rejudge, the contradictions on the rewritten
// ballots and their pair verdicts are removed: they were judged on a text
// that did not say what was voted. With dryRun the statements are still
// asked and cached, but no ballot is written.
func RewordAmendmentBallots(ctx context.Context, storer *storage.PostgresStorer, store *Store, laws []Law, orgCache map[string]OrganeInfo, rejudge, dryRun bool) (RewordResult, error) {
	var res RewordResult
	amendments := newAmendmentStatements(store)
	for _, law := range laws {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if t := ClassifyScrutinObject(law.Title); t != ObjectAmendement && t != ObjectSousAmendement {
			continue
		}
		ballots, err := store.AmendmentBallots(ctx, law.Key)
		if err != nil {
			return res, err
		}
		if len(ballots) == 0 {
			continue
		}
		object, ok, stated, err := resolveVotedObjectWith(ctx, law, orgCache, amendments)
		if err != nil {
			return res, fmt.Errorf("amendment of %s: %w", law.Key, err)
		}
		if !ok || !stated {
			res.Kept = append(res.Kept, law.Key)
			continue
		}
		purpose := object[strings.LastIndex(object, " (« ")+1:]
		var changed []BallotText
		for _, b := range ballots {
			if t, ok := rewordBallot(b.Text, purpose); ok && t != b.Text {
				changed = append(changed, BallotText{ID: b.ID, Text: t})
			}
		}
		if len(changed) == 0 {
			continue
		}
		res.Scrutins++
		res.Ballots += len(changed)
		log.Info().Str("law_key", law.Key).Str("object", object).Int("ballots", len(changed)).Bool("dry_run", dryRun).
			Msg("frpolitics: amendment ballots reworded")
		if dryRun {
			continue
		}
		if err := store.RewriteBallotTexts(ctx, changed); err != nil {
			return res, err
		}
		if rejudge {
			ids := make([]int64, len(changed))
			for i, b := range changed {
				ids[i] = b.ID
			}
			persons, reopened, err := store.ReopenPairsOf(ctx, ids)
			if err != nil {
				return res, err
			}
			res.Reopened += reopened
			for _, pid := range persons {
				if err := storer.RefreshPersonScore(ctx, pid); err != nil {
					return res, fmt.Errorf("refreshing the score of person %d: %w", pid, err)
				}
			}
		}
	}
	return res, nil
}

// BackfillAmendmentStatements discovers the scrutins of opts and rewords
// their amendment ballots (RewordAmendmentBallots).
func BackfillAmendmentStatements(ctx context.Context, storer *storage.PostgresStorer, opts Options, rejudge, dryRun bool) (RewordResult, error) {
	laws, _, _, orgCache, _, err := discoverAndParse(ctx, opts)
	if err != nil {
		return RewordResult{}, err
	}
	return RewordAmendmentBallots(ctx, storer, NewStore(storer), laws, orgCache, rejudge, dryRun)
}
