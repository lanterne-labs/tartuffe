package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog/log"
)

// GroupSpokeInput is one contradicting fact handed to the summarizer callback.
type GroupSpokeInput struct {
	FactText string
	Reason   string
}

// GroupSummarizer synthesizes a merged "one inconsistency" sentence (native +
// English) for a group: one base claim contradicted by N facts. Injected by the
// caller (the regroup CLI wires the LLM) so storage stays free of llm deps. A
// non-nil error must leave any existing summary untouched.
type GroupSummarizer func(ctx context.Context, baseFact string, spokes []GroupSpokeInput) (summary, summaryEn string, err error)

// RegroupResult reports what a regroup pass changed, for logging/CLI output.
type RegroupResult struct {
	Rebased         int // rows whose base_fact_id pointer changed
	Groups          int // distinct multi-fact groups after regrouping
	SummariesWrote  int // groups whose merged summary was (re)generated
	SummariesFailed int // groups whose summary LLM call errored (left untouched)
}

// regroupRow is one contradiction row loaded for the regroup pass. a/b are the
// canonical (fact_a_id, fact_b_id); base is the current base_fact_id (0 if NULL).
type regroupRow struct {
	id       int64
	a, b     int64
	base     int64
	reason   string
	reasonEn string
}

// RegroupPersonContradictions layers N-ary groups onto a person's pairwise
// contradictions: it computes connected components by shared fact via a greedy
// "star peel" (repeatedly pick the highest-degree fact as the base and attach all
// its incident edges as spokes), sets each row's base_fact_id pointer to its
// group's base, and (re)generates the merged summary for every multi-fact group.
// Single-fact groups get a NULL summary (the row's own reason suffices). The
// canonical (fact_a_id, fact_b_id) pair, bundle_stance_id, and the honesty score
// are all left untouched — grouping is a presentation-layer pointer, so insert /
// dedup / repoint keep working unchanged.
//
// summarize may be nil (set bases only, skip LLM). A summarizer error for one
// group is logged and that group keeps its existing summary; the pass continues.
func (s *PostgresStorer) RegroupPersonContradictions(ctx context.Context, personID int64, summarize GroupSummarizer) (RegroupResult, error) {
	var res RegroupResult

	rows, err := s.loadRegroupRows(ctx, personID)
	if err != nil {
		return res, err
	}
	if len(rows) == 0 {
		return res, nil
	}

	// Greedy star-peel over all of the person's edges (shared-fact grouping is
	// bundle-agnostic; the base fact's own stance later places the group's card).
	assignBase, _ := starPeel(rows)

	factIDs := make(map[int64]struct{})
	for _, r := range rows {
		factIDs[r.a] = struct{}{}
		factIDs[r.b] = struct{}{}
	}
	texts, err := s.factTexts(ctx, factIDs)
	if err != nil {
		return res, err
	}

	// Group rows by their assigned base; the contradicting fact is the non-base end.
	groups := make(map[int64][]regroupRow)
	for _, r := range rows {
		groups[assignBase[r.id]] = append(groups[assignBase[r.id]], r)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin regroup tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Point each row's base_fact_id at its group's base (only if changed).
	for _, r := range rows {
		nb := assignBase[r.id]
		if nb == r.base {
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE contradictions SET base_fact_id=$2 WHERE id=$1`, r.id, nb); err != nil {
			return res, fmt.Errorf("set base for contradiction %d: %w", r.id, err)
		}
		res.Rebased++
	}

	// 2. Write merged summaries. Deterministic base order for stable logs/tests.
	bases := make([]int64, 0, len(groups))
	for base := range groups {
		bases = append(bases, base)
	}
	sort.Slice(bases, func(i, j int) bool { return bases[i] < bases[j] })

	for _, base := range bases {
		grp := groups[base]
		if len(grp) < 2 {
			// Single-fact group: clear any stale merged summary.
			if _, err := tx.Exec(ctx,
				`UPDATE contradictions SET group_summary=NULL, group_summary_en=NULL WHERE person_id=$1 AND base_fact_id=$2`,
				personID, base); err != nil {
				return res, fmt.Errorf("clear summary for base %d: %w", base, err)
			}
			continue
		}
		res.Groups++
		if summarize == nil {
			continue
		}

		spokes := make([]GroupSpokeInput, 0, len(grp))
		for _, r := range grp {
			spoke := r.a
			if r.a == base {
				spoke = r.b
			}
			spokes = append(spokes, GroupSpokeInput{FactText: texts[spoke], Reason: r.reason})
		}
		summary, summaryEn, sErr := summarize(ctx, texts[base], spokes)
		if sErr != nil {
			log.Warn().Err(sErr).Int64("person_id", personID).Int64("base_fact_id", base).
				Msg("regroup: group summary failed; keeping existing summary")
			res.SummariesFailed++
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE contradictions SET group_summary=$3, group_summary_en=NULLIF($4,'') WHERE person_id=$1 AND base_fact_id=$2`,
			personID, base, summary, summaryEn); err != nil {
			return res, fmt.Errorf("write summary for base %d: %w", base, err)
		}
		res.SummariesWrote++
	}

	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit regroup tx: %w", err)
	}
	return res, nil
}

// GetStanceGroupCounts returns, per stance, the number of distinct-inconsistency
// GROUPS whose base fact belongs to that stance (= the number of cards the stance
// view renders). Keyed by stance_documents.id; stances with none are omitted. This
// is the count the UI badge should show — distinct from the bundle-based total used
// for the per-stance honesty score.
func (s *PostgresStorer) GetStanceGroupCounts(ctx context.Context, personID int64) (map[int64]int, error) {
	// The membership join is scoped to the person's own stances: a fact
	// mentioned by several persons belongs to one stance per person, so without
	// the scope the map would gain keys for other persons' stance ids.
	query := `
		SELECT sdf.stance_document_id, COUNT(DISTINCT COALESCE(c.base_fact_id, c.fact_a_id))
		FROM contradictions c
		JOIN stance_document_facts sdf ON sdf.fact_id = COALESCE(c.base_fact_id, c.fact_a_id)
		JOIN stance_documents sd ON sd.id = sdf.stance_document_id AND sd.person_id = $1
		WHERE c.person_id = $1
		GROUP BY sdf.stance_document_id`
	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("query stance group counts for person %d: %w", personID, err)
	}
	defer rows.Close()
	counts := make(map[int64]int)
	for rows.Next() {
		var stanceID int64
		var n int
		if err := rows.Scan(&stanceID, &n); err != nil {
			return nil, err
		}
		counts[stanceID] = n
	}
	return counts, rows.Err()
}

// PersonIDsWithContradictions returns the distinct person ids that currently have
// at least one contradiction — the work list for a full regroup pass.
func (s *PostgresStorer) PersonIDsWithContradictions(ctx context.Context) ([]int64, error) {
	rs, err := s.pool.Query(ctx, `SELECT DISTINCT person_id FROM contradictions ORDER BY person_id`)
	if err != nil {
		return nil, fmt.Errorf("list persons with contradictions: %w", err)
	}
	defer rs.Close()
	var out []int64
	for rs.Next() {
		var id int64
		if err := rs.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rs.Err()
}

// loadRegroupRows fetches a person's contradictions (skipping any degenerate self-pairs).
func (s *PostgresStorer) loadRegroupRows(ctx context.Context, personID int64) ([]regroupRow, error) {
	q := `SELECT id, fact_a_id, fact_b_id, COALESCE(base_fact_id, 0), COALESCE(reason,''), COALESCE(reason_en,'')
	      FROM contradictions WHERE person_id=$1`
	rs, err := s.pool.Query(ctx, q, personID)
	if err != nil {
		return nil, fmt.Errorf("load contradictions for person %d: %w", personID, err)
	}
	defer rs.Close()
	var out []regroupRow
	for rs.Next() {
		var r regroupRow
		if err := rs.Scan(&r.id, &r.a, &r.b, &r.base, &r.reason, &r.reasonEn); err != nil {
			return nil, err
		}
		if r.a == r.b {
			continue // defensive: a fact cannot contradict itself
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// factTexts returns id -> fact_text for the given fact ids.
func (s *PostgresStorer) factTexts(ctx context.Context, ids map[int64]struct{}) (map[int64]string, error) {
	list := make([]int64, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	out := make(map[int64]string, len(list))
	if len(list) == 0 {
		return out, nil
	}
	rs, err := s.pool.Query(ctx, `SELECT id, fact_text FROM facts WHERE id = ANY($1)`, list)
	if err != nil {
		return nil, fmt.Errorf("load fact texts: %w", err)
	}
	defer rs.Close()
	for rs.Next() {
		var id int64
		var text string
		if err := rs.Scan(&id, &text); err != nil {
			return nil, err
		}
		out[id] = text
	}
	return out, rs.Err()
}

// starPeel assigns every edge (row) to a base via greedy highest-degree peeling.
// Returns row id -> base fact and row id -> contradicting (spoke) fact. The result
// is deterministic regardless of input order: at each step the highest-degree fact
// (ties broken by lowest id) becomes a base and absorbs all its incident edges.
func starPeel(rows []regroupRow) (base map[int64]int64, other map[int64]int64) {
	base = make(map[int64]int64, len(rows))
	other = make(map[int64]int64, len(rows))

	remaining := make(map[int64]regroupRow, len(rows))
	for _, r := range rows {
		remaining[r.id] = r
	}

	for len(remaining) > 0 {
		deg := make(map[int64]int)
		for _, r := range remaining {
			deg[r.a]++
			deg[r.b]++
		}
		// Pick base: max degree, tie-break lowest fact id (deterministic).
		var chosen int64
		bestDeg := -1
		for f, d := range deg {
			if d > bestDeg || (d == bestDeg && f < chosen) {
				bestDeg = d
				chosen = f
			}
		}
		// Absorb all remaining edges incident to the chosen base.
		for id, r := range remaining {
			if r.a == chosen || r.b == chosen {
				spoke := r.a
				if r.a == chosen {
					spoke = r.b
				}
				base[id] = chosen
				other[id] = spoke
				delete(remaining, id)
			}
		}
	}
	return base, other
}

// --- Read path: grouped contradictions for a stance card ---

// ContradictionGroup is one distinct inconsistency: a base fact contradicted by N
// facts. It replaces the wall of pairwise cards on the person/stance view.
type ContradictionGroup struct {
	BaseFactID         int64      `json:"base_fact_id"`
	BaseFactText       string     `json:"base_fact_text"`
	BaseFactTextEn     string     `json:"base_fact_text_en,omitempty"`
	BaseFactDate       *time.Time `json:"base_fact_date"`
	BaseFactSourceURLs []string   `json:"base_fact_source_urls"`
	// BaseLikes counts the users' +1 on the base fact; BaseLiked says the
	// viewer gave one.
	BaseLikes int  `json:"base_likes"`
	BaseLiked bool `json:"base_liked"`
	// BaseRestatements are the base fact's other wordings (its claim group).
	BaseRestatements   []Restatement       `json:"base_restatements"`
	Summary            *string             `json:"summary"`              // merged reason; nil for single-spoke
	SummaryEn          *string             `json:"summary_en,omitempty"` // English merged reason
	ContradictingFacts []ContradictingFact `json:"contradicting_facts"`
}

// ContradictingFact is one spoke of a group: a fact that conflicts with the base,
// with the reason it conflicts.
type ContradictingFact struct {
	ContradictionID int64      `json:"contradiction_id"`
	FactID          int64      `json:"fact_id"`
	FactText        string     `json:"fact_text"`
	FactTextEn      string     `json:"fact_text_en,omitempty"`
	FactDate        *time.Time `json:"fact_date"`
	SourceURLs      []string   `json:"source_urls"`
	// Restatements are the fact's other wordings (its claim group).
	Restatements []Restatement `json:"restatements"`
	Reason       string        `json:"reason"`
	ReasonEn     string        `json:"reason_en,omitempty"`
	// Explanation is the text shown with the contradiction (feed.go,
	// FeedItem.Explanation); empty until it is written.
	Explanation   string `json:"explanation,omitempty"`
	ExplanationEn string `json:"explanation_en,omitempty"`
	// Likes counts the users' +1 on the contradiction; Liked says the viewer
	// gave one.
	Likes int  `json:"likes"`
	Liked bool `json:"liked"`
}

// GetStanceContradictionGroups returns the distinct-inconsistency groups whose BASE
// fact belongs to the given stance (via stance_document_facts), each as one base
// fact + its contradicting facts. Grouping is by base fact (fact_a_id); the regroup
// pass must have run for the orientation to be meaningful. The likes of the
// viewer (viewerID, 0: none) are marked.
func (s *PostgresStorer) GetStanceContradictionGroups(ctx context.Context, stanceID, viewerID int64) ([]ContradictionGroup, error) {
	// base = base_fact_id once regrouped, else fact_a_id (graceful pre-regroup fallback:
	// each un-grouped row renders as its own base + single spoke). spoke = the other fact.
	query := `
		WITH g AS (
			SELECT c.id, c.person_id, c.group_summary, c.group_summary_en,
			       COALESCE(c.reason,'') AS reason, COALESCE(c.reason_en,'') AS reason_en,
			       COALESCE(c.explanation,'') AS explanation, COALESCE(c.explanation_en,'') AS explanation_en,
			       COALESCE(c.base_fact_id, c.fact_a_id) AS base_id,
			       CASE WHEN COALESCE(c.base_fact_id, c.fact_a_id) = c.fact_a_id
			            THEN c.fact_b_id ELSE c.fact_a_id END AS spoke_id
			FROM contradictions c
		)
		SELECT g.id, g.base_id, g.spoke_id, g.reason, g.reason_en, g.explanation, g.explanation_en,
		       g.group_summary, g.group_summary_en,
		       fb.fact_text, COALESCE(fb.fact_text_en,''),
		       sp.fact_text, COALESCE(sp.fact_text_en,''),
		       COALESCE(fsb.urls, '{}'), fsb.earliest,
		       COALESCE(fss.urls, '{}'), fss.earliest,
		       ` + restatementsJSON("g.base_id") + `, ` + restatementsJSON("g.spoke_id") + `,
		       blk.n, blk.mine, clk.n, clk.mine
		FROM g
		JOIN stance_document_facts sdf ON sdf.fact_id = g.base_id AND sdf.stance_document_id = $1
		-- Only this stance owner's contradictions: a fact mentioned by several
		-- persons sits in each one's stances, and without this filter person Q's
		-- contradictions on the fact would render inside person P's stance card.
		JOIN stance_documents sd ON sd.id = $1 AND sd.person_id = g.person_id
		JOIN facts fb ON fb.id = g.base_id
		JOIN facts sp ON sp.id = g.spoke_id
		LEFT JOIN LATERAL (
			SELECT array_agg(source_url ORDER BY published_at ASC NULLS LAST) AS urls, MIN(published_at) AS earliest
			FROM fact_sources WHERE fact_id = g.base_id
		) fsb ON true
		LEFT JOIN LATERAL (
			SELECT array_agg(source_url ORDER BY published_at ASC NULLS LAST) AS urls, MIN(published_at) AS earliest
			FROM fact_sources WHERE fact_id = g.spoke_id
		) fss ON true
		` + likesJoin("fact", "g.base_id", "blk", "$2") + `
		` + likesJoin("contradiction", "g.id", "clk", "$2") + `
		ORDER BY g.base_id, g.id`

	rows, err := s.pool.Query(ctx, query, stanceID, viewerID)
	if err != nil {
		return nil, fmt.Errorf("query contradiction groups for stance %d: %w", stanceID, err)
	}
	defer rows.Close()

	groups := make([]ContradictionGroup, 0)
	idx := make(map[int64]int) // base fact id -> index in groups

	for rows.Next() {
		var (
			cid, baseID, spokeID                         int64
			reason, reasonEn, explanation, explanationEn string
			summary, summaryEn                           *string
			baseText, baseTextEn, spokeText, spokeTextEn string
			baseURLs, spokeURLs                          []string
			baseDate, spokeDate                          *time.Time
			baseRestatements, spokeRestatements          []byte
			baseLikes, spokeLikes                        float64
			baseLiked, spokeLiked                        bool
		)
		if err := rows.Scan(&cid, &baseID, &spokeID, &reason, &reasonEn, &explanation, &explanationEn,
			&summary, &summaryEn,
			&baseText, &baseTextEn, &spokeText, &spokeTextEn,
			&baseURLs, &baseDate, &spokeURLs, &spokeDate,
			&baseRestatements, &spokeRestatements,
			&baseLikes, &baseLiked, &spokeLikes, &spokeLiked); err != nil {
			return nil, fmt.Errorf("scan contradiction group row: %w", err)
		}
		var spokeAlso []Restatement
		if err := json.Unmarshal(spokeRestatements, &spokeAlso); err != nil {
			return nil, fmt.Errorf("decoding restatements of fact %d: %w", spokeID, err)
		}

		i, ok := idx[baseID]
		if !ok {
			var baseAlso []Restatement
			if err := json.Unmarshal(baseRestatements, &baseAlso); err != nil {
				return nil, fmt.Errorf("decoding restatements of fact %d: %w", baseID, err)
			}
			groups = append(groups, ContradictionGroup{
				BaseRestatements:   baseAlso,
				BaseFactID:         baseID,
				BaseFactText:       baseText,
				BaseFactTextEn:     baseTextEn,
				BaseFactDate:       baseDate,
				BaseFactSourceURLs: baseURLs,
				BaseLikes:          int(baseLikes),
				BaseLiked:          baseLiked,
				Summary:            summary,
				SummaryEn:          summaryEn,
			})
			i = len(groups) - 1
			idx[baseID] = i
		}
		groups[i].ContradictingFacts = append(groups[i].ContradictingFacts, ContradictingFact{
			ContradictionID: cid,
			FactID:          spokeID,
			FactText:        spokeText,
			FactTextEn:      spokeTextEn,
			FactDate:        spokeDate,
			SourceURLs:      spokeURLs,
			Restatements:    spokeAlso,
			Reason:          reason,
			ReasonEn:        reasonEn,
			Explanation:     explanation,
			ExplanationEn:   explanationEn,
			Likes:           int(spokeLikes),
			Liked:           spokeLiked,
		})
	}
	return groups, rows.Err()
}
