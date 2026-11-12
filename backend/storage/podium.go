package storage

import (
	"context"
	"fmt"
	"time"
)

// The podium: the candidates with the lowest honesty score among those whose
// score is reliable. The site shows the live one, and the one of each past
// week from podium_snapshots, which the maintenance pass writes at its end.
const (
	// PodiumSize is the number of steps.
	PodiumSize = 3
	// podiumMinFacts is the public floor of facts (mirror.publicMinFacts).
	podiumMinFacts = 4
)

// PodiumEntry is one step of a podium. The score and the facts are those of
// the podium's time; the name, the slug and the photo are the person's now.
type PodiumEntry struct {
	Rank         int     `json:"rank"`
	ID           int64   `json:"id"`
	Slug         string  `json:"slug"`
	Name         string  `json:"name"`
	ImageURL     *string `json:"image_url,omitempty"`
	HonestyScore float64 `json:"honesty_score"`
	FactCount    int     `json:"fact_count"`
}

// WeekStart is the Monday 00:00 UTC that starts the week of t: the key of a
// podium snapshot.
func WeekStart(t time.Time) time.Time { return weekStart(t) }

// LivePodium returns the podium as it stands now.
func (s *PostgresStorer) LivePodium(ctx context.Context) ([]PodiumEntry, error) {
	persons, err := s.GetPersonsRankedByScore(ctx, 100, 0, "human", "", "honesty_asc", podiumMinFacts, "true")
	if err != nil {
		return nil, fmt.Errorf("podium: %w", err)
	}
	base, err := s.ScoreBaseline(ctx)
	if err != nil {
		return nil, fmt.Errorf("podium: %w", err)
	}
	entries := []PodiumEntry{}
	for _, p := range persons {
		if len(entries) == PodiumSize {
			break
		}
		if !base.ScoreReliability(p.FactCount, p.HonestyScore) {
			continue
		}
		entries = append(entries, PodiumEntry{
			Rank: len(entries) + 1, ID: p.ID, Slug: p.Slug, Name: p.Name, ImageURL: p.ImageURL,
			HonestyScore: p.HonestyScore, FactCount: p.FactCount,
		})
	}
	return entries, nil
}

// RecordPodiumSnapshot writes the live podium as the snapshot of the week of
// now, in place of the one recorded earlier in the week. An empty podium
// records nothing. It returns the number of steps written.
func (s *PostgresStorer) RecordPodiumSnapshot(ctx context.Context, now time.Time) (int, error) {
	entries, err := s.LivePodium(ctx)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, nil
	}
	week := weekStart(now)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("podium snapshot: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM podium_snapshots WHERE week = $1`, week); err != nil {
		return 0, fmt.Errorf("podium snapshot: clearing the week: %w", err)
	}
	for _, e := range entries {
		if _, err := tx.Exec(ctx, `
			INSERT INTO podium_snapshots (week, rank, person_id, honesty_score, fact_count)
			VALUES ($1, $2, $3, $4, $5)`, week, e.Rank, e.ID, e.HonestyScore, e.FactCount); err != nil {
			return 0, fmt.Errorf("podium snapshot: step %d: %w", e.Rank, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("podium snapshot: %w", err)
	}
	return len(entries), nil
}

// PodiumSnapshot returns the podium recorded for the week that starts at
// week, empty when none was.
func (s *PostgresStorer) PodiumSnapshot(ctx context.Context, week time.Time) ([]PodiumEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ps.rank, p.id, p.slug, p.name, p.image_url, ps.honesty_score, ps.fact_count
		FROM podium_snapshots ps
		JOIN persons p ON p.id = ps.person_id
		WHERE ps.week = $1
		ORDER BY ps.rank`, week)
	if err != nil {
		return nil, fmt.Errorf("podium of the week %s: %w", week.Format(time.DateOnly), err)
	}
	defer rows.Close()
	entries := []PodiumEntry{}
	for rows.Next() {
		var e PodiumEntry
		if err := rows.Scan(&e.Rank, &e.ID, &e.Slug, &e.Name, &e.ImageURL, &e.HonestyScore, &e.FactCount); err != nil {
			return nil, fmt.Errorf("podium of the week %s: %w", week.Format(time.DateOnly), err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("podium of the week %s: %w", week.Format(time.DateOnly), err)
	}
	return entries, nil
}

// PodiumWeeks lists the weeks before the given one that have a snapshot,
// the latest first.
func (s *PostgresStorer) PodiumWeeks(ctx context.Context, before time.Time) ([]time.Time, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT week FROM podium_snapshots WHERE week < $1 ORDER BY week DESC`, before)
	if err != nil {
		return nil, fmt.Errorf("podium weeks: %w", err)
	}
	defer rows.Close()
	weeks := []time.Time{}
	for rows.Next() {
		var w time.Time
		if err := rows.Scan(&w); err != nil {
			return nil, fmt.Errorf("podium weeks: %w", err)
		}
		weeks = append(weeks, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("podium weeks: %w", err)
	}
	return weeks, nil
}
