package frpolitics

import (
	"context"
	"fmt"

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

// RecensureBallots rewrites the ballots of the censure motions among laws
// that still carry the bill phrase alone with the motion's name (date and
// paragraph of article 49, censureObject), and returns the motions and the
// ballots rewritten. The pair verdicts stay: the new text says the same vote,
// it only tells two motions apart.
func RecensureBallots(ctx context.Context, store *Store, laws []Law) (scrutins, ballots int, err error) {
	for _, law := range laws {
		if err := ctx.Err(); err != nil {
			return scrutins, ballots, err
		}
		if ClassifyScrutinObject(law.Title) != ObjectMotion {
			continue
		}
		object, ok := censureObject(law)
		if !ok {
			continue
		}
		ids, err := store.RecensureBallots(ctx, law.Key, object)
		if err != nil {
			return scrutins, ballots, err
		}
		if len(ids) == 0 {
			continue
		}
		scrutins++
		ballots += len(ids)
		log.Info().Str("law_key", law.Key).Str("object", object).Int("ballots", len(ids)).Msg("frpolitics: ballots of the censure motion rewritten")
	}
	return scrutins, ballots, nil
}

// BackfillCensures discovers the scrutins of opts (Since, Legislatures) and
// rewrites the ballots of their censure motions (RecensureBallots).
func BackfillCensures(ctx context.Context, storer *storage.PostgresStorer, opts Options) (scrutins, ballots int, err error) {
	laws, _, _, _, _, err := discoverAndParse(ctx, opts)
	if err != nil {
		return 0, 0, err
	}
	return RecensureBallots(ctx, NewStore(storer), laws)
}
