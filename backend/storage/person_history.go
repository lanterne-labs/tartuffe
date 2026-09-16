package storage

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// PersonHistoryWindowDays is the trailing window used for HistoryPoint.RecentScore.
const PersonHistoryWindowDays = 365

// personHistoryMaxSpan caps how far back the series starts. Events older than this
// (mis-parsed publication dates, very old votes) fold into the first point instead of
// stretching the x-axis over decades of empty buckets.
const personHistoryMaxSpan = 10 * 365 * 24 * time.Hour

// HistoryPoint is one weekly bucket of a person's honesty timeline. Date is the bucket
// start (Monday, UTC); every other field is the state at the END of that week.
type HistoryPoint struct {
	Date       time.Time `json:"date"`
	FactsAdded int       `json:"facts_added"`
	FactsTotal int       `json:"facts_total"`
	// Contradictions is the cumulative number of contradiction pairs, the same count as
	// persons.contradiction_count on the profile. The score itself uses stance bundles.
	Contradictions int `json:"contradictions"`
	// Score is the honesty score over the whole record up to this week — the same
	// formula as persons.own_score, so the last point equals the live score.
	Score float64 `json:"score"`
	// RecentScore is the same formula restricted to the trailing PersonHistoryWindowDays:
	// facts stated in the window and contradictions whose later fact falls in it.
	RecentScore float64 `json:"recent_score"`
}

// historyEvent is a dated fact (bundle == "") or contradiction (bundle key) on the
// statement-time axis: a fact is dated by the earliest publication among its own sources
// and its folded members' sources (a meta fact carries a dateless 'meta' source, so its
// members hold the real dates), ingestion time as fallback; a contradiction by the later
// of its two facts.
type historyEvent struct {
	at     time.Time
	bundle string
}

// historyCacheMaxAge bounds how long a cached series may be served on an unchanged
// fingerprint. It covers the rare edits the fingerprint cannot see: a fold or repoint
// that leaves every count equal, or a corrected published_at.
const historyCacheMaxAge = time.Hour

// historyFingerprint is the cheap per-person state read on every request to validate a
// cached series. Any fact-set change moves own_score (refreshPersonScores) and any
// contradiction insert or eviction moves the count, so the common write paths all miss.
type historyFingerprint struct {
	factCount      int
	ownScore       float64
	contradictions int
	// week pins the series to its last bucket so a cached entry never serves yesterday's
	// week boundary after midnight Monday.
	week time.Time
}

type historyCacheEntry struct {
	fp     historyFingerprint
	at     time.Time
	points []HistoryPoint
}

// historyCache is an in-process memo of GetPersonHistory. The daily pipeline writes from
// another container, so it cannot rely on in-process invalidation: every hit is validated
// against a fresh fingerprint (one indexed query) instead of an event.
type historyCache struct {
	mu      sync.Mutex
	entries map[int64]historyCacheEntry
}

func (c *historyCache) get(personID int64, fp historyFingerprint, now time.Time) ([]HistoryPoint, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[personID]
	if !ok || e.fp != fp || now.Sub(e.at) > historyCacheMaxAge {
		return nil, false
	}
	return e.points, true
}

func (c *historyCache) put(personID int64, fp historyFingerprint, now time.Time, points []HistoryPoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[int64]historyCacheEntry{}
	}
	c.entries[personID] = historyCacheEntry{fp: fp, at: now, points: points}
}

// GetPersonHistory returns the weekly honesty timeline of a person on the statement-time
// axis, ending at the current week. Deleted (evicted) contradictions leave no trace, so the
// series always reflects the current record. Returns an empty slice for a person with no facts.
// The replay is memoized per person and revalidated against a cheap fingerprint on every call.
func (s *PostgresStorer) GetPersonHistory(ctx context.Context, personID int64) ([]HistoryPoint, error) {
	now := time.Now()
	fp, err := s.personHistoryFingerprint(ctx, personID)
	if err != nil {
		return nil, err
	}
	fp.week = weekStart(now)
	if points, ok := s.historyCache.get(personID, fp, now); ok {
		return points, nil
	}
	events, err := s.personHistoryEvents(ctx, personID)
	if err != nil {
		return nil, err
	}
	points := buildPersonHistory(events, now)
	s.historyCache.put(personID, fp, now, points)
	log.Debug().Int64("person_id", personID).Int("events", len(events)).Msg("person history recomputed")
	return points, nil
}

func (s *PostgresStorer) personHistoryFingerprint(ctx context.Context, personID int64) (historyFingerprint, error) {
	var fp historyFingerprint
	err := s.pool.QueryRow(ctx, `
		SELECT p.fact_count, COALESCE(p.own_score, 1.0),
		       (SELECT COUNT(*) FROM contradictions c WHERE c.person_id = p.id)
		FROM persons p WHERE p.id = $1`, personID).Scan(&fp.factCount, &fp.ownScore, &fp.contradictions)
	if err != nil {
		return historyFingerprint{}, fmt.Errorf("failed to read history fingerprint for person %d: %w", personID, err)
	}
	return fp, nil
}

func (s *PostgresStorer) personHistoryEvents(ctx context.Context, personID int64) ([]historyEvent, error) {
	// person_facts mirrors getPersonFactsCount (mentioning the person, duplicate members
	// excluded). dated covers those plus the facts referenced by the person's contradictions,
	// so a contradiction is always dated even if a side is not in person_facts.
	query := `
		WITH person_facts AS (
			SELECT DISTINCT f.id
			FROM facts f
			JOIN fact_sources fs ON fs.fact_id = f.id
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			WHERE fm.person_id = $1
			  AND NOT EXISTS (
				SELECT 1 FROM fact_relations fr
				WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
			  )
		),
		dated AS (
			SELECT f.id, COALESCE(MIN(fs.published_at), f.created_at) AS at
			FROM facts f
			LEFT JOIN fact_relations fr ON fr.general_fact_id = f.id AND fr.relation_type = 'duplicate'
			LEFT JOIN fact_sources fs ON fs.fact_id = f.id OR fs.fact_id = fr.specific_fact_id
			WHERE f.id IN (
				SELECT id FROM person_facts
				UNION SELECT fact_a_id FROM contradictions WHERE person_id = $1
				UNION SELECT fact_b_id FROM contradictions WHERE person_id = $1
			)
			GROUP BY f.id
		)
		SELECT d.at, '' AS bundle FROM dated d JOIN person_facts pf ON pf.id = d.id
		UNION ALL
		SELECT GREATEST(da.at, db.at), COALESCE(c.bundle_stance_id::text, 'solo:' || c.id::text)
		FROM contradictions c
		JOIN dated da ON da.id = c.fact_a_id
		JOIN dated db ON db.id = c.fact_b_id
		WHERE c.person_id = $1`

	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to query history events for person %d: %w", personID, err)
	}
	defer rows.Close()

	var events []historyEvent
	for rows.Next() {
		var ev historyEvent
		if err := rows.Scan(&ev.at, &ev.bundle); err != nil {
			return nil, fmt.Errorf("failed to scan history event for person %d: %w", personID, err)
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read history events for person %d: %w", personID, err)
	}
	return events, nil
}

// weekStart truncates t to the Monday 00:00 UTC that starts its ISO week.
func weekStart(t time.Time) time.Time {
	t = t.UTC()
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	back := (int(d.Weekday()) + 6) % 7
	return d.AddDate(0, 0, -back)
}

// buildPersonHistory replays events (any order) into weekly points from the first event's
// week (bounded by personHistoryMaxSpan) to now's week. Future-dated events are clamped
// to now; events before the span start fold into the first point.
func buildPersonHistory(events []historyEvent, now time.Time) []HistoryPoint {
	if len(events) == 0 {
		return []HistoryPoint{}
	}
	now = now.UTC()
	for i := range events {
		if events[i].at.After(now) {
			events[i].at = now
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })

	start := weekStart(events[0].at)
	if floor := weekStart(now.Add(-personHistoryMaxSpan)); start.Before(floor) {
		start = floor
	}
	end := weekStart(now)
	window := time.Duration(PersonHistoryWindowDays) * 24 * time.Hour

	var (
		points        []HistoryPoint
		next, expire  int // event cursors: next to add; next to leave the trailing window
		factsTotal    int
		cumulBundles  = map[string]int{}
		cumulTotal    int
		windowFacts   int
		windowBundles = map[string]int{}
		windowTotal   int
	)
	for wk := start; !wk.After(end); wk = wk.AddDate(0, 0, 7) {
		bucketEnd := wk.AddDate(0, 0, 7)
		if wk.Equal(end) {
			bucketEnd = now.Add(time.Nanosecond) // include events stamped exactly now
		}
		added := 0
		for ; next < len(events) && events[next].at.Before(bucketEnd); next++ {
			ev := events[next]
			if ev.bundle == "" {
				factsTotal++
				windowFacts++
				added++
				continue
			}
			cumulBundles[ev.bundle]++
			cumulTotal++
			windowBundles[ev.bundle]++
			windowTotal++
		}
		windowStart := bucketEnd.Add(-window)
		for ; expire < next && events[expire].at.Before(windowStart); expire++ {
			ev := events[expire]
			if ev.bundle == "" {
				windowFacts--
				continue
			}
			windowTotal--
			if windowBundles[ev.bundle]--; windowBundles[ev.bundle] == 0 {
				delete(windowBundles, ev.bundle)
			}
		}
		points = append(points, HistoryPoint{
			Date:           wk,
			FactsAdded:     added,
			FactsTotal:     factsTotal,
			Contradictions: cumulTotal,
			Score:          ComputeHonestyScore(factsTotal, ContradictionStats{Bundles: len(cumulBundles), Total: cumulTotal}),
			RecentScore:    ComputeHonestyScore(windowFacts, ContradictionStats{Bundles: len(windowBundles), Total: windowTotal}),
		})
	}
	return points
}
