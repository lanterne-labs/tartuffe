package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"digital-mirror/backend/core"

	"github.com/jackc/pgx/v5"
)

// Feed item kinds.
const (
	FeedContradiction = "contradiction"
	FeedFact          = "fact"
)

// FeedPerson is the person of a feed item.
type FeedPerson struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	ImageURL     *string `json:"image_url,omitempty"`
	HonestyScore float64 `json:"honesty_score"`
	FactCount    int     `json:"fact_count"`
	ScoreConfidence
}

// FeedFactSide is one fact of a feed item.
type FeedFactSide struct {
	ID         int64      `json:"id"`
	Text       string     `json:"text"`
	TextEn     *string    `json:"text_en,omitempty"`
	Date       *time.Time `json:"date,omitempty"`
	SourceURLs []string   `json:"source_urls"`
	Quote      string     `json:"quote,omitempty"`
}

// FeedItem is a new contradiction or a new fact. At orders the feed: the
// detection of a contradiction, the insertion of a fact.
type FeedItem struct {
	Kind          string        `json:"kind"`
	ID            int64         `json:"id"`
	At            time.Time     `json:"at"`
	Person        FeedPerson    `json:"person"`
	StanceID      *int64        `json:"stance_id,omitempty"`
	StanceLabel   *string       `json:"stance_label,omitempty"`
	StanceLabelEn *string       `json:"stance_label_en,omitempty"`
	Fact          FeedFactSide  `json:"fact"`
	Other         *FeedFactSide `json:"other,omitempty"`
	Reason        *string       `json:"reason,omitempty"`
	ReasonEn      *string       `json:"reason_en,omitempty"`
	// Likes counts the +1 of the users; Liked says the viewer gave one.
	Likes int  `json:"likes"`
	Liked bool `json:"liked"`
	// ScoreEffect is what the item does to the person's score today, in
	// percent of the score without it (a contradiction lowers it, a fact
	// without one may raise it); nil below MinScoreEffect.
	ScoreEffect *float64 `json:"score_effect,omitempty"`
	// rank orders the hot feed; bundleSize is the contradictions of the
	// contradiction's bundle (it alone: 1).
	rank       float64
	bundleSize int
}

// MinScoreEffect is the smallest effect on a score the feed shows, in percent.
const MinScoreEffect = 0.1

// FeedFilter narrows the feed: Kind to one kind ("": both), PersonID to one
// person (0: all), From and To to the statements made in that period (nil:
// open). A contradiction is in the period when its later fact is.
//
// Hot ranks the feed by likes and age instead of newest first, from the
// Offset-th item (the cursor is unused). ViewerID is the signed-in user whose
// likes are marked (0: none).
type FeedFilter struct {
	Kind     string
	PersonID int64
	From, To *time.Time
	Hot      bool
	Offset   int
	ViewerID int64
}

// The hot order is the gravity rank of Hacker News: (likes + 1) /
// (age in hours + 2)^HotGravity. The +1 lets an item without likes rank by
// its age, and a like weighs more on a young item. Over all persons it reads
// the items of the last HotWindow only; over one person, all of them.
const (
	HotGravity = 1.8
	HotWindow  = 14 * 24 * time.Hour
)

// hotRank is the SQL of the rank of an item liked lk.n times at the instant
// atCol, at the instant $9.
func hotRank(atCol string) string {
	return fmt.Sprintf(`(lk.n + 1) / power(GREATEST(EXTRACT(EPOCH FROM ($9::timestamptz - %s)) / 3600, 0) + 2, %v)`, atCol, HotGravity)
}

// likesLateral reads the likes of the target alias t of the kind into lk.n,
// and whether the user $11 is among them into lk.mine.
func likesLateral(kind, t string) string {
	return fmt.Sprintf(`LEFT JOIN LATERAL (
			SELECT COUNT(*)::float8 AS n, COALESCE(bool_or(user_id = $11), false) AS mine
			FROM feed_likes WHERE target_kind = '%s' AND target_id = %s.id
		) lk ON true`, kind, t)
}

// FeedCursor is the position after the last item of a page: the items
// strictly older than (At, Kind, ID).
type FeedCursor struct {
	At   time.Time
	Kind string
	ID   int64
}

// factSidesLateral reads the sources of fact alias f into the columns
// <p>urls, <p>quote and <p>earliest.
func factSidesLateral(f, p string) string {
	return fmt.Sprintf(`LEFT JOIN LATERAL (
			SELECT array_agg(source_url ORDER BY published_at ASC NULLS LAST) AS urls,
			       (array_agg(COALESCE(original_quote, '') ORDER BY published_at ASC NULLS LAST))[1] AS quote,
			       MIN(published_at) AS earliest
			FROM fact_sources WHERE fact_id = %[1]s.id
		) %[2]s ON true`, f, p)
}

// GetFeed returns the newest contradictions and facts, newest first, after
// the cursor (nil: from the newest), narrowed by the filter; or in the hot
// order (f.Hot), from f.Offset. A fact is listed
// with its person's first stance; a fact in no stance yet (being grouped) is
// not listed, nor are the items of a person with fewer than minFacts facts
// (the public floor).
func (s *PostgresStorer) GetFeed(ctx context.Context, f FeedFilter, after *FeedCursor, limit, minFacts int) ([]FeedItem, error) {
	kind := f.Kind
	// The cursor compares (at, kind, id) in that order: within one instant,
	// "fact" < "contradiction" is false, so the kinds interleave stably.
	at, ckind, cid := time.Now().Add(time.Hour), "~", int64(0)
	if after != nil {
		at, ckind, cid = after.At, after.Kind, after.ID
	}
	// The hot order reads the first Offset+limit of each kind, merges them
	// and keeps its page.
	q := feedQuery{at: at, ckind: ckind, cid: cid, limit: limit, minFacts: minFacts, now: time.Now()}
	if f.Hot {
		q.at, q.ckind, q.cid = time.Now().Add(time.Hour), "~", 0
		q.limit = f.Offset + limit
		if f.PersonID == 0 {
			w := q.now.Add(-HotWindow)
			q.since = &w
		}
	}
	var out []FeedItem
	if kind == "" || kind == FeedContradiction {
		items, err := s.feedContradictions(ctx, f, q)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	if kind == "" || kind == FeedFact {
		items, err := s.feedFacts(ctx, f, q)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	// Merge the two pages and keep the page.
	sortFeed(out, f.Hot)
	if f.Hot {
		out = out[min(f.Offset, len(out)):]
	}
	if len(out) > limit {
		out = out[:limit]
	}
	if err := s.feedScoreEffects(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// feedScoreEffects sets the effect of each item on its person's score as it
// is today: the score with the item against the score without it (the fact
// counted one less, or the contradiction taken out of its bundle). A party's
// score is half its own: the item moves that half.
func (s *PostgresStorer) feedScoreEffects(ctx context.Context, items []FeedItem) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return fmt.Errorf("begin feed score effects: %w", err)
	}
	defer tx.Rollback(ctx)
	type inputs struct {
		facts   int
		stats   core.ContradictionStats
		members *float64
	}
	byPerson := map[int64]*inputs{}
	for i := range items {
		id := items[i].Person.ID
		in, ok := byPerson[id]
		if !ok {
			in = &inputs{}
			if in.facts, err = s.getPersonFactsCount(ctx, tx, id); err != nil {
				return err
			}
			if in.stats, err = s.getPersonContradictionStats(ctx, tx, id); err != nil {
				return err
			}
			if in.members, err = s.computeMembersScore(ctx, tx, id); err != nil {
				return err
			}
			byPerson[id] = in
		}
		without := *in
		if items[i].Kind == FeedContradiction {
			if in.stats.Total == 0 {
				continue
			}
			without.stats.Total--
			if items[i].bundleSize <= 1 {
				without.stats.Bundles = max(0, without.stats.Bundles-1)
			}
		} else {
			if in.facts == 0 {
				continue
			}
			without.facts--
		}
		now := core.ComputeAggregateScore(core.ComputeHonestyScore(in.facts, in.stats), in.members)
		before := core.ComputeAggregateScore(core.ComputeHonestyScore(without.facts, without.stats), without.members)
		if before <= 0 {
			continue
		}
		if effect := 100 * (now - before) / before; math.Abs(effect) >= MinScoreEffect {
			items[i].ScoreEffect = &effect
		}
	}
	return nil
}

// feedQuery holds the bounds of the queries of one page: the cursor (at,
// ckind, cid), the rows to read, the public floor, the instant of the hot
// rank and the start of its window (nil: none).
type feedQuery struct {
	at              time.Time
	ckind           string
	cid             int64
	limit, minFacts int
	now             time.Time
	since           *time.Time
}

func sortFeed(items []FeedItem, hot bool) {
	less := func(a, b FeedItem) bool { // a before b in the feed
		if hot && a.rank != b.rank {
			return a.rank > b.rank
		}
		if !a.At.Equal(b.At) {
			return a.At.After(b.At)
		}
		if a.Kind != b.Kind {
			return a.Kind > b.Kind
		}
		return a.ID > b.ID
	}
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && less(items[j], items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

func (s *PostgresStorer) feedContradictions(ctx context.Context, f FeedFilter, q feedQuery) ([]FeedItem, error) {
	order := `c.detected_at DESC, c.id DESC`
	if f.Hot {
		order = `rank DESC, ` + order
	}
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.detected_at, p.id, p.name, p.image_url, p.honesty_score, p.fact_count,
		       lk.n::int, lk.mine, `+hotRank("c.detected_at")+` AS rank,
		       CASE WHEN c.bundle_stance_id IS NULL THEN 1 ELSE (
		         SELECT COUNT(*) FROM contradictions c2
		         WHERE c2.person_id = c.person_id AND c2.bundle_stance_id = c.bundle_stance_id) END,
		       sd.id, sd.cluster_label, sd.label_en,
		       c.reason, c.reason_en,
		       fa.id, fa.fact_text, fa.fact_text_en, sa.earliest, COALESCE(sa.urls, '{}'), COALESCE(sa.quote, ''),
		       fb.id, fb.fact_text, fb.fact_text_en, sb.earliest, COALESCE(sb.urls, '{}'), COALESCE(sb.quote, '')
		FROM contradictions c
		JOIN persons p ON p.id = c.person_id
		JOIN facts fa ON fa.id = c.fact_a_id
		JOIN facts fb ON fb.id = c.fact_b_id
		LEFT JOIN stance_documents sd ON sd.id = c.bundle_stance_id
		`+factSidesLateral("fa", "sa")+`
		`+factSidesLateral("fb", "sb")+`
		`+likesLateral(FeedContradiction, "c")+`
		WHERE c.detected_at IS NOT NULL AND p.fact_count >= $5
		  AND ($10::timestamptz IS NULL OR c.detected_at >= $10)
		  AND ($6::bigint = 0 OR c.person_id = $6)
		  AND ($7::timestamptz IS NULL OR GREATEST(sa.earliest, sb.earliest) >= $7)
		  AND ($8::timestamptz IS NULL OR GREATEST(sa.earliest, sb.earliest) < $8)
		  AND (c.detected_at < $1 OR (c.detected_at = $1 AND ('contradiction' < $2 OR ('contradiction' = $2 AND c.id < $3))))
		ORDER BY `+order+`
		LIMIT $4`, q.at, q.ckind, q.cid, q.limit, q.minFacts, f.PersonID, f.From, f.To, q.now, q.since, f.ViewerID)
	if err != nil {
		return nil, fmt.Errorf("feed contradictions: %w", err)
	}
	defer rows.Close()
	out := []FeedItem{}
	for rows.Next() {
		it := FeedItem{Kind: FeedContradiction, Other: &FeedFactSide{}}
		var reason, reasonEn *string
		if err := rows.Scan(&it.ID, &it.At, &it.Person.ID, &it.Person.Name, &it.Person.ImageURL, &it.Person.HonestyScore, &it.Person.FactCount,
			&it.Likes, &it.Liked, &it.rank, &it.bundleSize,
			&it.StanceID, &it.StanceLabel, &it.StanceLabelEn, &reason, &reasonEn,
			&it.Fact.ID, &it.Fact.Text, &it.Fact.TextEn, &it.Fact.Date, &it.Fact.SourceURLs, &it.Fact.Quote,
			&it.Other.ID, &it.Other.Text, &it.Other.TextEn, &it.Other.Date, &it.Other.SourceURLs, &it.Other.Quote); err != nil {
			return nil, fmt.Errorf("scan feed contradiction: %w", err)
		}
		it.Reason, it.ReasonEn = reason, reasonEn
		// The earlier fact first: what was said, then what contradicts it.
		if it.Other.Date != nil && it.Fact.Date != nil && it.Other.Date.Before(*it.Fact.Date) {
			it.Fact, *it.Other = *it.Other, it.Fact
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *PostgresStorer) feedFacts(ctx context.Context, f FeedFilter, q feedQuery) ([]FeedItem, error) {
	order := `f.created_at DESC, f.id DESC`
	if f.Hot {
		order = `rank DESC, ` + order
	}
	rows, err := s.pool.Query(ctx, `
		SELECT f.id, f.created_at, p.id, p.name, p.image_url, p.honesty_score, p.fact_count,
		       lk.n::int, lk.mine, `+hotRank("f.created_at")+` AS rank,
		       sd.id, sd.cluster_label, sd.label_en,
		       f.fact_text, f.fact_text_en, sf.earliest, COALESCE(sf.urls, '{}'), COALESCE(sf.quote, '')
		FROM facts f
		JOIN LATERAL (
			SELECT sd.id, sd.cluster_label, sd.label_en, sd.person_id
			FROM stance_document_facts sdf
			JOIN stance_documents sd ON sd.id = sdf.stance_document_id
			WHERE sdf.fact_id = f.id AND ($6::bigint = 0 OR sd.person_id = $6)
			ORDER BY sdf.added_at LIMIT 1
		) sd ON true
		JOIN persons p ON p.id = sd.person_id
		`+factSidesLateral("f", "sf")+`
		`+likesLateral(FeedFact, "f")+`
		WHERE f.created_at IS NOT NULL AND p.fact_count >= $5
		  AND ($10::timestamptz IS NULL OR f.created_at >= $10)
		  -- One person's facts are read from their stances, not found by
		  -- walking every fact.
		  AND ($6::bigint = 0 OR f.id IN (
			SELECT sdf.fact_id FROM stance_document_facts sdf
			JOIN stance_documents sd2 ON sd2.id = sdf.stance_document_id
			WHERE sd2.person_id = $6))
		  AND ($7::timestamptz IS NULL OR sf.earliest >= $7)
		  AND ($8::timestamptz IS NULL OR sf.earliest < $8)
		  AND (f.created_at < $1 OR (f.created_at = $1 AND ('fact' < $2 OR ('fact' = $2 AND f.id < $3))))
		  AND NOT EXISTS (SELECT 1 FROM fact_relations fr WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate')
		ORDER BY `+order+`
		LIMIT $4`, q.at, q.ckind, q.cid, q.limit, q.minFacts, f.PersonID, f.From, f.To, q.now, q.since, f.ViewerID)
	if err != nil {
		return nil, fmt.Errorf("feed facts: %w", err)
	}
	defer rows.Close()
	out := []FeedItem{}
	for rows.Next() {
		it := FeedItem{Kind: FeedFact}
		if err := rows.Scan(&it.ID, &it.At, &it.Person.ID, &it.Person.Name, &it.Person.ImageURL, &it.Person.HonestyScore, &it.Person.FactCount,
			&it.Likes, &it.Liked, &it.rank,
			&it.StanceID, &it.StanceLabel, &it.StanceLabelEn,
			&it.Fact.Text, &it.Fact.TextEn, &it.Fact.Date, &it.Fact.SourceURLs, &it.Fact.Quote); err != nil {
			return nil, fmt.Errorf("scan feed fact: %w", err)
		}
		it.Fact.ID = it.ID
		out = append(out, it)
	}
	return out, rows.Err()
}

// ShareTarget is what a shared link points at: the person, and the stance to
// open on their page, with the text of the preview.
type ShareTarget struct {
	Kind         string  `json:"kind"`
	ID           int64   `json:"id"`
	PersonID     int64   `json:"person_id"`
	PersonName   string  `json:"person_name"`
	ImageURL     *string `json:"image_url,omitempty"`
	HonestyScore float64 `json:"honesty_score"`
	FactCount    int     `json:"fact_count"`
	// Contradictions is the person's count.
	Contradictions int     `json:"contradiction_count"`
	StanceID       *int64  `json:"stance_id,omitempty"`
	StanceLabel    *string `json:"stance_label,omitempty"`
	StanceLabelEn  *string `json:"stance_label_en,omitempty"`
	// Text is the shared item: the stance summary, the fact, or the
	// contradiction's reason; Other is the second fact of a contradiction.
	Text   string  `json:"text"`
	TextEn *string `json:"text_en,omitempty"`
	Other  *string `json:"other,omitempty"`
	// Quote is the first source quote of a fact.
	Quote string `json:"quote,omitempty"`
}

// ErrNoShareTarget is returned when the shared item does not exist.
var ErrNoShareTarget = errors.New("no such item")

const sharePersonColumns = `p.id, p.name, p.image_url, p.honesty_score, p.fact_count,
	(SELECT COUNT(*) FROM contradictions cc WHERE cc.person_id = p.id)`

// GetShareTarget loads the item of a shared link: kind is person, stance,
// contradiction or fact. A fact opens its first stance of personID when
// given (the page it was shared from), else of its first person.
func (s *PostgresStorer) GetShareTarget(ctx context.Context, kind string, id, personID int64) (*ShareTarget, error) {
	t := ShareTarget{Kind: kind, ID: id}
	var err error
	switch kind {
	case "person":
		err = s.pool.QueryRow(ctx, `SELECT `+sharePersonColumns+` FROM persons p WHERE p.id = $1`, id).
			Scan(&t.PersonID, &t.PersonName, &t.ImageURL, &t.HonestyScore, &t.FactCount, &t.Contradictions)
	case "stance":
		err = s.pool.QueryRow(ctx, `
			SELECT `+sharePersonColumns+`, sd.id, sd.cluster_label, sd.label_en, sd.summary, sd.summary_en
			FROM stance_documents sd JOIN persons p ON p.id = sd.person_id WHERE sd.id = $1`, id).
			Scan(&t.PersonID, &t.PersonName, &t.ImageURL, &t.HonestyScore, &t.FactCount, &t.Contradictions,
				&t.StanceID, &t.StanceLabel, &t.StanceLabelEn, &t.Text, &t.TextEn)
	case "contradiction":
		var other string
		var reason *string
		err = s.pool.QueryRow(ctx, `
			SELECT `+sharePersonColumns+`, sd.id, sd.cluster_label, sd.label_en, c.reason, fa.fact_text, fb.fact_text
			FROM contradictions c
			JOIN persons p ON p.id = c.person_id
			JOIN facts fa ON fa.id = c.fact_a_id
			JOIN facts fb ON fb.id = c.fact_b_id
			LEFT JOIN stance_documents sd ON sd.id = c.bundle_stance_id
			WHERE c.id = $1`, id).
			Scan(&t.PersonID, &t.PersonName, &t.ImageURL, &t.HonestyScore, &t.FactCount, &t.Contradictions,
				&t.StanceID, &t.StanceLabel, &t.StanceLabelEn, &reason, &t.Text, &other)
		t.Other = &other
		if reason != nil && *reason != "" {
			// The reason says why the two facts cannot both hold: the
			// preview reads better with it than with the first fact.
			t.Quote = *reason
		}
	case "fact":
		err = s.pool.QueryRow(ctx, `
			SELECT `+sharePersonColumns+`, sd.id, sd.cluster_label, sd.label_en, f.fact_text, f.fact_text_en,
			       COALESCE((SELECT original_quote FROM fact_sources WHERE fact_id = f.id AND original_quote <> ''
			                 ORDER BY published_at ASC NULLS LAST LIMIT 1), '')
			FROM facts f
			JOIN LATERAL (
				SELECT sd.id, sd.cluster_label, sd.label_en, sd.person_id
				FROM stance_document_facts sdf
				JOIN stance_documents sd ON sd.id = sdf.stance_document_id
				WHERE sdf.fact_id = f.id AND ($2 = 0 OR sd.person_id = $2)
				ORDER BY sdf.added_at LIMIT 1
			) sd ON true
			JOIN persons p ON p.id = sd.person_id
			WHERE f.id = $1`, id, personID).
			Scan(&t.PersonID, &t.PersonName, &t.ImageURL, &t.HonestyScore, &t.FactCount, &t.Contradictions,
				&t.StanceID, &t.StanceLabel, &t.StanceLabelEn, &t.Text, &t.TextEn, &t.Quote)
		if errors.Is(err, pgx.ErrNoRows) && personID != 0 {
			return s.GetShareTarget(ctx, kind, id, 0)
		}
	default:
		return nil, ErrNoShareTarget
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoShareTarget
	}
	if err != nil {
		return nil, fmt.Errorf("share target %s %d: %w", kind, id, err)
	}
	return &t, nil
}

// ScoreExplanation is every input of a person's honesty score, so the page
// can show how the number is made (core.ComputeHonestyScore).
type ScoreExplanation struct {
	PersonID   int64  `json:"person_id"`
	EntityType string `json:"entity_type"`
	// Facts counts the person's facts, duplicates aside.
	Facts int `json:"facts"`
	// Bundles is the number of distinct contradictions (pairs on one topic
	// count once); Pairs is the raw count; Extras = Pairs − Bundles.
	Bundles int `json:"bundles"`
	Pairs   int `json:"pairs"`
	Extras  int `json:"extras"`
	// Denominator is max(Facts, MinDenominator).
	Denominator    int     `json:"denominator"`
	MinDenominator int     `json:"min_denominator"`
	RatioDecay     float64 `json:"ratio_decay"`
	AbsoluteDecay  float64 `json:"absolute_decay"`
	ExtraDecay     float64 `json:"extra_decay"`
	// OwnScore is the score of the person's own facts; MembersScore the
	// average of a party's members (nil for a person); Score the one shown.
	OwnScore     float64  `json:"own_score"`
	MembersScore *float64 `json:"members_score,omitempty"`
	Members      int      `json:"members"`
	Score        float64  `json:"score"`

	// Rate is the candidates' contradictions per fact; Reliable the
	// expected count at which a score is trusted.
	Rate     float64 `json:"rate"`
	Reliable float64 `json:"reliable_expected"`
	ScoreConfidence
}

// GetScoreExplanation computes the inputs of the person's score now. It
// returns pgx.ErrNoRows when the person does not exist.
func (s *PostgresStorer) GetScoreExplanation(ctx context.Context, personID int64) (*ScoreExplanation, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, fmt.Errorf("begin score explanation: %w", err)
	}
	defer tx.Rollback(ctx)
	e := ScoreExplanation{
		PersonID: personID, MinDenominator: core.HonestyScoreMinDenominator,
		RatioDecay: core.HonestyScoreRatioDecay, AbsoluteDecay: core.HonestyScoreAbsoluteDecay,
		ExtraDecay: core.HonestyScoreBundleExtraDecay,
	}
	var entityType *string
	if err := tx.QueryRow(ctx, `SELECT entity_type FROM persons WHERE id = $1`, personID).Scan(&entityType); err != nil {
		return nil, err
	}
	if entityType != nil {
		e.EntityType = *entityType
	}
	if e.Facts, err = s.getPersonFactsCount(ctx, tx, personID); err != nil {
		return nil, err
	}
	stats, err := s.getPersonContradictionStats(ctx, tx, personID)
	if err != nil {
		return nil, err
	}
	e.Bundles, e.Pairs, e.Extras = stats.Bundles, stats.Total, max(0, stats.Total-stats.Bundles)
	e.Denominator = max(e.Facts, core.HonestyScoreMinDenominator)
	e.OwnScore = core.ComputeHonestyScore(e.Facts, stats)
	if e.EntityType != "" && e.EntityType != "human" {
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(DISTINCT pl.src_person_id) FROM person_links pl
			WHERE pl.dst_person_id = $1 AND (pl.valid_to IS NULL OR pl.valid_to >= CURRENT_DATE)`, personID).Scan(&e.Members); err != nil {
			return nil, fmt.Errorf("count members of %d: %w", personID, err)
		}
		if e.MembersScore, err = s.computeMembersScore(ctx, tx, personID); err != nil {
			return nil, err
		}
	}
	e.Score = core.ComputeAggregateScore(e.OwnScore, e.MembersScore)
	return &e, nil
}
