package contradiction

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"digital-mirror/backend/pairfilter"
)

// screenChunk is how many pairs one call of the filter gets: a filter that
// fails loses one chunk to its fallback, not the pairs of a person.
const screenChunk = 200

// screenPairs asks the pair filter which pairs need the judge. cleared are
// the pairs the filter says cannot conflict: they are recorded as judged "no"
// without a call to the judge. A pair a producer queued and a near-identical
// pair (the grouping check reads the judge's "no" on it) always go to the
// judge, and so does every pair when the filter does not answer. asked is how
// many pairs the filter was asked about: asked pairs with none cleared is a
// filter that is down.
func screenPairs(ctx context.Context, filter pairfilter.Filter, pairs []pairInfo) (toJudge, cleared []pairInfo, asked int) {
	if _, off := filter.(pairfilter.All); off {
		return pairs, nil, 0
	}
	keep := make([]bool, len(pairs))
	var ask []int
	for i, p := range pairs {
		if p.queued || p.similarity >= groupingSimFloor {
			keep[i] = true
			continue
		}
		ask = append(ask, i)
	}
	for at := 0; at < len(ask); at += screenChunk {
		chunk := ask[at:min(at+screenChunk, len(ask))]
		in := make([]pairfilter.Pair, len(chunk))
		for j, i := range chunk {
			p := pairs[i]
			in[j] = pairfilter.Pair{A: p.factAText, B: p.factBText, DateA: filterDate(p.factADate), DateB: filterDate(p.factBDate)}
		}
		answers, err := filter.Keep(ctx, in)
		if err != nil || len(answers) != len(chunk) {
			if ctx.Err() == nil {
				log.Warn().Err(err).Str("filter", filter.Name()).Int("pairs", len(chunk)).Msg("detection: the pair filter did not answer, the judge reads these pairs")
			}
			for _, i := range chunk {
				keep[i] = true
			}
			continue
		}
		for j, i := range chunk {
			keep[i] = answers[j]
		}
	}
	for i, p := range pairs {
		if keep[i] {
			toJudge = append(toJudge, p)
		} else {
			cleared = append(cleared, p)
		}
	}
	return toJudge, cleared, len(ask)
}

func filterDate(date *time.Time) string {
	if date == nil {
		return ""
	}
	return date.Format("2006-01-02")
}
