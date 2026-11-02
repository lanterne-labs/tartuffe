package frpolitics

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// The summary of an amendment scrutin made before 1298d12 (2026-10-31) was
// made from the whole title ("l'amendement de suppression n° 120 de Mme
// Youssouffa à l'article 3 du projet de loi d'urgence pour Mayotte"), so it
// often describes the AMENDMENT ("Supprime l'article 3 du projet de loi
// d'urgence pour Mayotte"), and every ballot presents that as the bill:
// « …, déposé sur le texte de loi : « Supprime l'article 3… » » (review of
// 2026-11-01: 12 of 100 audited votes, about 63 of 160 amendment scrutins).
// StoredLawSummaries keeps reusing it, so a rerun copies it again. The
// resummarize repair finds these summaries, makes the bill's summary with the
// current summaryTitle, and rewrites the ballots and the stored summary.

// staleAmendmentSummaryRe matches a bill summary that speaks of a part of the
// text or of an amendment instead of what the bill does: an article, an
// alinéa, an amendment, the deletion, restoration or rewriting of provisions,
// a change to "the bill" itself, or the scrutin title kept as the summary.
var staleAmendmentSummaryRe = regexp.MustCompile(`(?i)\bl['’](article|alinéa)\b|\bamendements?\b|\barticles? (premier|unique|liminaire|\d+)|\b(supprime|rétablit|réinstaure|réécrit|modifie)\s+(certaines |des |les )?(dispositions|mesures)\b|\bmodifie (le|la) (projet|proposition|texte)\b|\bvise à supprimer\b`)

// staleAmendmentSummary reports whether the stored summary of an amendment
// scrutin describes the amendment, not the bill.
func staleAmendmentSummary(statement string) bool {
	return staleAmendmentSummaryRe.MatchString(statement)
}

// summaryQuote is how a bill summary stands in a ballot: the quote that
// follows "le texte de loi :" (prepareLawPhrase). The repair replaces the
// quote, not the whole phrase: a ballot stored before textNoun may name the
// text otherwise ("du texte de loi" for an organic law).
func summaryQuote(statement string) string {
	return "« " + strings.TrimSpace(statement) + " »"
}

// ResummarizeResult is what ResummarizeBallots did.
type ResummarizeResult struct {
	// Scrutins and Ballots count the amendment scrutins whose summary was
	// stale and the ballots rewritten (with dryRun: that would be).
	Scrutins int
	Ballots  int
	// Gated lists the scrutins whose new summary is not usable (low
	// confidence, a remark on the title, or still about the amendment):
	// their ballots keep the old text.
	Gated []string
	// Reopened is the number of contradictions on the rewritten ballots
	// removed with their pair verdicts (rejudge).
	Reopened int
}

// ResummarizeBallots rewrites the ballots of the amendment scrutins among
// laws whose stored bill summary describes the amendment
// (staleAmendmentSummary) with the bill's summary, made now from the bill's
// title (summaryTitle), and stores that summary in the scrutin's sources so
// a later run reuses it. With dryRun nothing is summarized or written: it
// counts the scrutins and their ballots. With rejudge the contradictions on
// the rewritten ballots and their pair verdicts are removed: they were judged
// on a text that misstated the bill.
func ResummarizeBallots(ctx context.Context, storer *storage.PostgresStorer, store *Store, laws []Law, dryRun, rejudge bool) (ResummarizeResult, error) {
	var res ResummarizeResult
	var amendments []Law
	for _, law := range laws {
		if t := ClassifyScrutinObject(law.Title); t == ObjectAmendement || t == ObjectSousAmendement {
			amendments = append(amendments, law)
		}
	}
	keys := make([]string, len(amendments))
	for i, law := range amendments {
		keys[i] = law.Key
	}
	variants, err := store.StoredSummaryVariants(ctx, keys)
	if err != nil {
		return res, err
	}
	var stale []Law
	staleQuotes := map[string][]string{} // law key → the quotes of its stale summaries
	counts := map[string]int{}
	for _, law := range amendments {
		for _, old := range variants[law.Key] {
			if !staleAmendmentSummary(old) {
				continue
			}
			n, err := store.BallotsQuoting(ctx, law.Key, summaryQuote(old))
			if err != nil {
				return res, err
			}
			if n == 0 {
				continue
			}
			counts[law.Key] += n
			staleQuotes[law.Key] = append(staleQuotes[law.Key], summaryQuote(old))
		}
		if counts[law.Key] > 0 {
			stale = append(stale, law)
		}
	}
	if dryRun {
		for _, law := range stale {
			res.Scrutins++
			res.Ballots += counts[law.Key]
			log.Info().Str("law_key", law.Key).Strs("summaries", staleQuotes[law.Key]).Str("bill", summaryTitle(law)).
				Int("ballots", counts[law.Key]).Msg("frpolitics: ballots of the stale summary would be rewritten")
		}
		return res, nil
	}

	fresh, err := summarizeConcurrently(ctx, stale)
	if err != nil {
		return res, err
	}
	for _, law := range stale {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		sum, ok := fresh[law.Key]
		if !ok || !usableBillSummary(sum) {
			res.Gated = append(res.Gated, law.Key)
			log.Warn().Str("law_key", law.Key).Str("summary", sum.SimpleStatement).Float64("confidence", sum.Confidence).
				Msg("frpolitics: the new bill summary is not usable; the ballots keep the old one")
			continue
		}
		ids, err := store.ResummarizeBallots(ctx, law.Key, staleQuotes[law.Key], summaryQuote(sum.SimpleStatement), sum)
		if err != nil {
			return res, err
		}
		res.Scrutins++
		res.Ballots += len(ids)
		if rejudge && len(ids) > 0 {
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
		log.Info().Str("law_key", law.Key).Strs("old", staleQuotes[law.Key]).Str("new", sum.SimpleStatement).
			Int("ballots", len(ids)).Msg("frpolitics: ballots of the stale summary rewritten")
	}
	return res, nil
}

// usableBillSummary reports whether a summary made by the repair can replace
// the stored one: confident, about the law, and no longer about a part of it.
func usableBillSummary(sum LawSummary) bool {
	s := strings.TrimSpace(sum.SimpleStatement)
	return s != "" && sum.Confidence >= 0.5 && !metaStatement(s) && !staleAmendmentSummary(s)
}

// summarizeConcurrently summarizes laws in batches of summarizeBatchSize,
// as many batches at once as the endpoint serves.
func summarizeConcurrently(ctx context.Context, laws []Law) (map[string]LawSummary, error) {
	var batches [][]Law
	for i := 0; i < len(laws); i += summarizeBatchSize {
		batches = append(batches, laws[i:min(i+summarizeBatchSize, len(laws))])
	}
	out := make(map[string]LawSummary, len(laws))
	var mu sync.Mutex
	err := llm.ForEach(ctx, batches, func(ctx context.Context, batch []Law) error {
		made, err := SummarizeLaws(ctx, batch)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		for k, v := range made {
			out[k] = v
		}
		return nil
	})
	return out, err
}

// BackfillSummaries discovers the scrutins of opts (Since, Legislatures)
// and rewrites the ballots of the amendment scrutins whose stored summary
// describes the amendment (ResummarizeBallots).
func BackfillSummaries(ctx context.Context, storer *storage.PostgresStorer, opts Options, dryRun, rejudge bool) (ResummarizeResult, error) {
	laws, _, _, _, _, err := discoverAndParse(ctx, opts)
	if err != nil {
		return ResummarizeResult{}, err
	}
	return ResummarizeBallots(ctx, storer, NewStore(storer), laws, dryRun, rejudge)
}
