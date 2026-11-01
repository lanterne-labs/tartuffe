package frpolitics

import (
	"context"
	"fmt"
	"strings"

	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// RearticleResult is what RearticleBallots did.
type RearticleResult struct {
	// Scrutins is the number of article scrutins whose ballots were rewritten.
	Scrutins int
	// Ballots is the number of ballots rewritten.
	Ballots int
	// Gated lists the article scrutins whose article could not be resolved:
	// their ballots keep the article alone.
	Gated []string
	// Reopened is the number of contradictions on the rewritten ballots that
	// were removed with their pair verdicts, so the detector judges the pairs
	// again on the new text (rejudge).
	Reopened int
}

// RearticleBallots rewrites the ballots of the article scrutins among laws
// that still name the article alone ("l'article 7 du texte de loi : « ... »")
// with what the article does, as the resolver gives it. A scrutin the
// resolver gates keeps its ballots and is listed. With rejudge, the
// contradictions on the rewritten ballots and the verdicts of their pairs are
// removed: they were judged on a text that did not say what was voted.
func RearticleBallots(ctx context.Context, storer *storage.PostgresStorer, store *Store, r *articleResolver, laws []Law, rejudge bool) (RearticleResult, error) {
	var res RearticleResult
	for _, law := range laws {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if ClassifyScrutinObject(law.Title) != ObjectArticle {
			continue
		}
		article, _, ok := splitArticle(law.Title)
		if !ok {
			continue
		}
		n, err := store.BallotsNamingArticle(ctx, law.Key, article)
		if err != nil {
			return res, err
		}
		if n == 0 {
			continue
		}
		object, ok, err := r.resolve(ctx, law)
		if err != nil {
			return res, fmt.Errorf("article of %s: %w", law.Key, err)
		}
		if !ok {
			res.Gated = append(res.Gated, law.Key)
			continue
		}
		ids, err := store.RearticleBallots(ctx, law.Key, article, object)
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
		log.Info().Str("law_key", law.Key).Str("object", object).Int("ballots", len(ids)).Msg("frpolitics: ballots of the article rewritten")
	}
	return res, nil
}

// BackfillArticles discovers the scrutins of opts (Since, Legislatures) and
// rewrites the ballots of their article scrutins (RearticleBallots).
func BackfillArticles(ctx context.Context, storer *storage.PostgresStorer, opts Options, rejudge bool) (RearticleResult, error) {
	laws, _, _, orgCache, _, err := discoverAndParse(ctx, opts)
	if err != nil {
		return RearticleResult{}, err
	}
	store := NewStore(storer)
	return RearticleBallots(ctx, storer, store, newArticleResolver(store, laws, orgCache), laws, rejudge)
}

// ReamendResult is what ReamendBallots did.
type ReamendResult struct {
	// Scrutins and Ballots count the amendment scrutins and the ballots
	// rewritten (with dryRun: that would be).
	Scrutins int
	Ballots  int
	// Gated lists the scrutins whose amendment the archive does not give:
	// their ballots keep the cut quote.
	Gated []string
}

// ReamendBallots rewrites the ballots of the amendment scrutins among laws
// whose quoted object was cut inside the guillemets (purposePhrase capped
// the whole phrase before 2026-11-01: 19,223 ballots) with the object as
// the archive gives it now. The pair verdicts stay: the quote is completed,
// the vote is the same. With dryRun nothing is written.
func ReamendBallots(ctx context.Context, store *Store, laws []Law, orgCache map[string]OrganeInfo, dryRun bool) (ReamendResult, error) {
	var res ReamendResult
	for _, law := range laws {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if t := ClassifyScrutinObject(law.Title); t != ObjectAmendement && t != ObjectSousAmendement {
			continue
		}
		n, err := store.TruncatedAmendmentBallots(ctx, law.Key)
		if err != nil {
			return res, err
		}
		if n == 0 {
			continue
		}
		object, ok, err := resolveVotedObject(ctx, law, orgCache)
		if err != nil {
			return res, fmt.Errorf("amendment of %s: %w", law.Key, err)
		}
		if !ok || object == "" {
			res.Gated = append(res.Gated, law.Key)
			continue
		}
		res.Scrutins++
		if dryRun {
			res.Ballots += n
			log.Info().Str("law_key", law.Key).Str("object", object).Int("ballots", n).Msg("frpolitics: ballots of the amendment would be rewritten")
			continue
		}
		ids, err := store.ReamendBallots(ctx, law.Key, object)
		if err != nil {
			return res, err
		}
		res.Ballots += len(ids)
		log.Info().Str("law_key", law.Key).Str("object", object).Int("ballots", len(ids)).Msg("frpolitics: ballots of the amendment rewritten")
	}
	return res, nil
}

// BackfillAmendments discovers the scrutins of opts (Since, Legislatures)
// and rewrites the truncated ballots of their amendment scrutins.
func BackfillAmendments(ctx context.Context, storer *storage.PostgresStorer, opts Options, dryRun bool) (ReamendResult, error) {
	laws, _, _, orgCache, _, err := discoverAndParse(ctx, opts)
	if err != nil {
		return ReamendResult{}, err
	}
	return ReamendBallots(ctx, NewStore(storer), laws, orgCache, dryRun)
}

// RemotionResult is what RemotionBallots did.
type RemotionResult struct {
	// Scrutins and Ballots count the motions and the ballots rewritten.
	Scrutins int
	Ballots  int
	// Reopened is the number of contradictions on the rewritten ballots of
	// the motions other than censure that were removed with the verdicts of
	// their pairs: they were judged on a text that said the bill was voted.
	Reopened int
}

// RemotionBallots rewrites the ballots of the motions among laws that still
// carry the bill phrase with the motion (motionObject). A censure motion
// keeps its purpose and its pair verdicts: the new text says the same vote,
// it only tells two motions apart. Any other motion loses the bill phrase,
// which read as the object of the vote, and its pairs are reopened.
func RemotionBallots(ctx context.Context, storer *storage.PostgresStorer, store *Store, laws []Law) (RemotionResult, error) {
	var res RemotionResult
	for _, law := range laws {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if ClassifyScrutinObject(law.Title) != ObjectMotion {
			continue
		}
		object, ok := motionObject(law)
		if !ok {
			continue
		}
		censure := strings.HasPrefix(object, censurePrefix)
		ids, err := store.RemotionBallots(ctx, law.Key, object, censure)
		if err != nil {
			return res, err
		}
		if len(ids) == 0 {
			continue
		}
		res.Scrutins++
		res.Ballots += len(ids)
		if !censure {
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
		log.Info().Str("law_key", law.Key).Str("object", object).Int("ballots", len(ids)).Msg("frpolitics: ballots of the motion rewritten")
	}
	return res, nil
}

// BackfillMotions discovers the scrutins of opts (Since, Legislatures) and
// rewrites the ballots of their motions (RemotionBallots).
func BackfillMotions(ctx context.Context, storer *storage.PostgresStorer, opts Options) (RemotionResult, error) {
	laws, _, _, _, _, err := discoverAndParse(ctx, opts)
	if err != nil {
		return RemotionResult{}, err
	}
	return RemotionBallots(ctx, storer, NewStore(storer), laws)
}
