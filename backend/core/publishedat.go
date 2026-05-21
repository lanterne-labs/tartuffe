package core

import "time"

// PickPublishedAt returns the first non-nil time in the supplied list,
// treating the argument order as a strict priority chain — most authoritative
// source first. Returns nil only when every source was nil.
//
// Each plugin orders its own sources because what counts as "most
// authoritative" is plugin-specific:
//
//	News        PickPublishedAt(llmFromBody, htmlMeta, serperSerpDate)
//	              — what the LLM extracted from the article body (e.g.
//	                "Posted on May 19" / "Publié il y a 2 jours") beats the
//	                article's <meta property="article:published_time"> meta
//	                tag, which in turn beats the Serper SERP date.
//
//	Twitter     PickPublishedAt(tweet.CreatedAt, llmFromBody)
//	              — the Twitter API's CreatedAt is deterministic and
//	                authoritative; an LLM-inferred body date is only a
//	                fallback if the tweet object somehow lacks a timestamp.
//
//	Frpolitics  PickPublishedAt(law.VoteDate)
//	              — single source today, but funneling through the helper
//	                means adding a second source later (e.g. promulgation
//	                date) is one new argument with no control-flow surgery.
//
// Keep it pure and trivially testable — no time.Now(), no I/O. Callers feed in
// whatever they have.
func PickPublishedAt(sources ...*time.Time) *time.Time {
	for _, s := range sources {
		if s != nil {
			return s
		}
	}
	return nil
}
