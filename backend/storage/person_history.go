package storage

import (
	"context"
	"fmt"
	"sort"
	"time"
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
	// Contradictions is the cumulative number of distinct contradiction bundles.
	Contradictions int `json:"contradictions"`
	// Score is the honesty score over the whole record up to this week — the same
	// formula as persons.own_score, so the last point equals the live score.
	Score float64 `json:"score"`
	// RecentScore is the same formula restricted to the trailing PersonHistoryWindowDays:
	// facts stated in the window and contradictions whose later fact falls in it.
	RecentScore float64 `json:"recent_score"`
}

// historyEvent is a dated fact (bundle == "") or contradiction (bundle key) on the
// statement-time axis: a fact is dated by its earliest source publication (ingestion
// time as fallback), a contradiction by the later of its two facts.
type historyEvent struct {
	at     time.Time
	bundle string
}

// GetPersonHistory returns the weekly honesty timeline of a person on the statement-time
// axis, ending at the current week. Deleted (evicted) contradictions leave no trace, so the
// series always reflects the current record. Returns an empty slice for a person with no facts.
func (s *PostgresStorer) GetPersonHistory(ctx context.Context, personID int64) ([]HistoryPoint, error) {
	events, err := s.personHistoryEvents(ctx, personID)
	if err != nil {
		return nil, err
	}
	return buildPersonHistory(events, time.Now()), nil
}

func (s *PostgresStorer) personHistoryEvents(ctx context.Context, personID int64) ([]historyEvent, error) {
	// person_facts mirrors getPersonFactsCount (mentioning the person, duplicate members
	// excluded). fact_dates covers the facts referenced by the person's contradictions
	// independently, so a contradiction is always dated even if a side is not in person_facts.
	query := `
		WITH person_facts AS (
			SELECT f.id, COALESCE(MIN(fs.published_at), f.created_at) AS at
			FROM facts f
			JOIN fact_sources fs ON fs.fact_id = f.id
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			WHERE fm.person_id = $1
			  AND NOT EXISTS (
				SELECT 1 FROM fact_relations fr
				WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
			  )
			GROUP BY f.id
		),
		fact_dates AS (
			SELECT f.id, COALESCE(MIN(fs.published_at), f.created_at) AS at
			FROM facts f
			LEFT JOIN fact_sources fs ON fs.fact_id = f.id
			WHERE f.id IN (
				SELECT fact_a_id FROM contradictions WHERE person_id = $1
				UNION
				SELECT fact_b_id FROM contradictions WHERE person_id = $1
			)
			GROUP BY f.id
		)
		SELECT at, '' AS bundle FROM person_facts
		UNION ALL
		SELECT GREATEST(da.at, db.at), COALESCE(c.bundle_stance_id::text, 'solo:' || c.id::text)
		FROM contradictions c
		JOIN fact_dates da ON da.id = c.fact_a_id
		JOIN fact_dates db ON db.id = c.fact_b_id
		WHERE c.person_id = $1
		ORDER BY at`

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
			Contradictions: len(cumulBundles),
			Score:          ComputeHonestyScore(factsTotal, ContradictionStats{Bundles: len(cumulBundles), Total: cumulTotal}),
			RecentScore:    ComputeHonestyScore(windowFacts, ContradictionStats{Bundles: len(windowBundles), Total: windowTotal}),
		})
	}
	return points
}
