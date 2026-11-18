package storage

import (
	"context"
	"testing"
)

// RemoveFactsForGood remembers what it deletes: the same fact read again
// from the same source is recognised, give or take a few words; another fact
// of that source, or the same fact from another source, is not.
func TestRemoveFactsForGoodIsRemembered(t *testing.T) {
	s, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	const url = "https://example.test/removed-facts/programme"
	var factID int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO facts (fact_text) VALUES ($1) RETURNING id`,
		"Nicolas Test est favorable au rétablissement de la retraite à taux plein à 60 ans avec 37,5 annuités.").Scan(&factID); err != nil {
		t.Fatalf("insert fact: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type) VALUES ($1, $2, $3, 'news')`,
		factID, url, "Nicolas Test est favorable au rétablissement de la retraite à taux plein à 60 ans avec 37,5 annuités."); err != nil {
		t.Fatalf("insert source: %v", err)
	}
	t.Cleanup(func() { s.pool.Exec(ctx, `DELETE FROM removed_facts WHERE source_url = $1`, url) })

	if _, err := s.RemoveFactsForGood(ctx, []int64{factID}, "test"); err != nil {
		t.Fatalf("RemoveFactsForGood: %v", err)
	}
	var left int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM facts WHERE id = $1`, factID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("fact still there (%d, %v)", left, err)
	}
	for _, tc := range []struct {
		name, url, text, quote string
		want                   bool
	}{
		{"the same fact, reworded a little", url, "Nicolas Test se dit favorable au rétablissement de la retraite à taux plein à 60 ans, avec 37,5 annuités.", "", true},
		{"another text on the same quote", url, "Il veut revenir à soixante ans.", "Nicolas Test est favorable au rétablissement de la retraite à taux plein à 60 ans avec 37,5 annuités.", true},
		{"another fact of the page", url, "Nicolas Test propose de rétablir l'impôt sur la fortune.", "La principale mesure porte sur le rétablissement de l'impôt sur la fortune.", false},
		{"the same fact from another page", "https://example.test/removed-facts/other", "Nicolas Test est favorable au rétablissement de la retraite à taux plein à 60 ans avec 37,5 annuités.", "", false},
	} {
		got, err := s.WasRemoved(ctx, tc.url, tc.text, tc.quote)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: removed=%v, want %v", tc.name, got, tc.want)
		}
	}
}
