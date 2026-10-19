package frpolitics

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestRearticleBallots: the ballots of an article scrutin that name the
// article alone are rewritten with what the article does; a gated scrutin
// keeps its ballots and is listed; with rejudge, the contradictions on the
// rewritten ballots and the verdicts of their pairs are removed and the
// person's score is refreshed.
func TestRearticleBallots(t *testing.T) {
	storer, store := setupStoreTest(t)
	ctx := context.Background()
	pool := storer.Pool()
	for _, q := range []string{"DELETE FROM vote_articles WHERE legislature = 99", "DELETE FROM fact_pair_checks WHERE person_id IN (SELECT id FROM persons WHERE name = 'rearticle person')",
		"DELETE FROM contradictions WHERE person_id IN (SELECT id FROM persons WHERE name = 'rearticle person')"} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	person, err := storer.FindOrInsertPersonWithType(ctx, "rearticle person", "human")
	if err != nil {
		t.Fatal(err)
	}
	insertBallot := func(text, url, lawKey string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, "INSERT INTO facts (fact_text, fact_text_en, embedding_text) VALUES ($1, 'old english', $2) RETURNING id", text, "le texte de loi").Scan(&id); err != nil {
			t.Fatal(err)
		}
		var sid int64
		if err := pool.QueryRow(ctx, "INSERT INTO fact_sources (fact_id, source_url, source_type, metadata) VALUES ($1, $2, 'assemblee_law_page', jsonb_build_object('law_key', $3::text)) RETURNING id", id, url, lawKey).Scan(&sid); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)", sid, person); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Every scrutin of the bill shares the dossier URL: the scrutin is its law_key.
	dossier := "https://www.assemblee-nationale.fr/dyn/99/dossiers/fin_de_vie"
	a := insertBallot("Rearticle Person a voté POUR l'article 7 du texte de loi : « Crée un droit à l'aide à mourir »", dossier, "99-2003")
	b := insertBallot("Rearticle Person a voté CONTRE l'article 9 du texte de loi : « Crée un droit à l'aide à mourir »", dossier, "99-2009")
	// Article 7 voted again in a later reading: another scrutin, another text.
	c := insertBallot("Rearticle Person a voté POUR l'article 7 du texte de loi : « Crée un droit à l'aide à mourir »", dossier, "99-3003")
	if _, err := pool.Exec(ctx, "INSERT INTO contradictions (person_id, fact_a_id, fact_b_id, reason) VALUES ($1, $2, $3, 'test')", person, a, b); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO fact_pair_checks (person_id, fact_a_id, fact_b_id, is_contradiction) VALUES ($1, $2, $3, true)", person, a, b); err != nil {
		t.Fatal(err)
	}

	date := time.Date(2025, 5, 22, 0, 0, 0, 0, time.UTC)
	laws := []Law{
		{Key: "99-2003", URL: dossier, Title: "l'article 7 de la proposition de loi relative au droit à l'aide à mourir (première lecture).", Legislature: 99, VoteDate: &date, SeanceRef: "S1"},
		{Key: "99-2001", URL: dossier, Title: "l'amendement n° 96 de M. X à l'article 7 de la proposition de loi relative au droit à l'aide à mourir (première lecture).", Legislature: 99, VoteDate: &date, SeanceRef: "S1"},
		{Key: "99-2009", URL: dossier, Title: "l'article 9 de la proposition de loi relative au droit à l'aide à mourir (première lecture).", Legislature: 99, VoteDate: &date, SeanceRef: "S9"}, // no amendment: gated
		{Key: "99-3003", URL: dossier, Title: "l'article 7 de la proposition de loi relative au droit à l'aide à mourir (deuxième lecture).", Legislature: 99, VoteDate: &date, SeanceRef: "S3"}, // no amendment: gated
		{Key: "99-0001", URL: "u2", Title: "l'ensemble de la proposition de loi X", Legislature: 99, VoteDate: &date, SeanceRef: "S1"},
	}
	r := newArticleResolver(store, laws, nil)
	r.lookup = func(_ context.Context, _, numero int, _ *time.Time) ([]string, error) {
		return []string{"PIONANR5L99BTC1364"}, nil
	}
	r.readText = func(context.Context, string, string) (string, error) { return texte1364, nil }
	r.crTexts = func(context.Context, int, string) ([]string, error) { return nil, nil }
	r.statement = func(context.Context, string, string, string) (LawSummary, error) {
		return LawSummary{SimpleStatement: "Permet l'administration de la substance létale en dehors du domicile.", Confidence: 0.9}, nil
	}

	res, err := RearticleBallots(ctx, storer, store, r, laws, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Scrutins != 1 || res.Ballots != 1 || len(res.Gated) != 2 || res.Reopened != 1 {
		t.Fatalf("result = %+v; want 1 scrutin, 1 ballot, 2 gated, 1 contradiction reopened", res)
	}
	var text string
	var textEn *string
	if err := pool.QueryRow(ctx, "SELECT fact_text, fact_text_en FROM facts WHERE id = $1", a).Scan(&text, &textEn); err != nil {
		t.Fatal(err)
	}
	want := "Rearticle Person a voté POUR l'article 7 (« Permet l'administration de la substance létale en dehors du domicile ») du texte de loi : « Crée un droit à l'aide à mourir »"
	if text != want {
		t.Errorf("text = %q\nwant   %q", text, want)
	}
	if textEn != nil {
		t.Errorf("fact_text_en = %q, want reset for the translation repair", *textEn)
	}
	if err := pool.QueryRow(ctx, "SELECT fact_text FROM facts WHERE id = $1", c).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "(«") {
		t.Errorf("the ballot of the other scrutin on article 7 changed: %q", text)
	}
	if err := pool.QueryRow(ctx, "SELECT fact_text FROM facts WHERE id = $1", b).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "Rearticle Person a voté CONTRE l'article 9 du texte de loi : « Crée un droit à l'aide à mourir »" {
		t.Errorf("the gated ballot changed: %q", text)
	}
	var contradictions, checks int
	if err := pool.QueryRow(ctx, "SELECT (SELECT COUNT(*) FROM contradictions WHERE person_id = $1), (SELECT COUNT(*) FROM fact_pair_checks WHERE person_id = $1)", person).Scan(&contradictions, &checks); err != nil {
		t.Fatal(err)
	}
	if contradictions != 0 || checks != 0 {
		t.Errorf("contradictions=%d checks=%d after rejudge, want 0 and 0", contradictions, checks)
	}
	// A second run finds nothing to rewrite.
	res, err = RearticleBallots(ctx, storer, store, r, laws, true)
	if err != nil || res.Scrutins != 0 || res.Ballots != 0 {
		t.Errorf("second run: %+v, %v; want nothing rewritten", res, err)
	}
}

// TestRemotionBallots: the ballots of the motions that carry the bill phrase
// are rewritten with the motion. A censure motion keeps its purpose and its
// verdicts; a motion to reject loses the bill phrase, and its contradictions
// and pair verdicts are removed. Other scrutins and rewritten ballots are
// left alone.
func TestRemotionBallots(t *testing.T) {
	storer, store := setupStoreTest(t)
	ctx := context.Background()
	pool := storer.Pool()
	person, err := storer.FindOrInsertPersonWithType(ctx, "remotion person", "human")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"DELETE FROM fact_pair_checks WHERE person_id = $1", "DELETE FROM contradictions WHERE person_id = $1"} {
		if _, err := pool.Exec(ctx, q, person); err != nil {
			t.Fatal(err)
		}
	}
	insertBallot := func(text, lawKey string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, "INSERT INTO facts (fact_text, fact_text_en, embedding_text) VALUES ($1, 'old english', 'motion') RETURNING id", text).Scan(&id); err != nil {
			t.Fatal(err)
		}
		var sid int64
		if err := pool.QueryRow(ctx, "INSERT INTO fact_sources (fact_id, source_url, source_type, metadata) VALUES ($1, 'https://www.assemblee-nationale.fr/dyn/99/scrutins/' || $2::text, 'assemblee_law_page', jsonb_build_object('law_key', $2::text)) RETURNING id", id, lawKey).Scan(&sid); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)", sid, person); err != nil {
			t.Fatal(err)
		}
		return id
	}
	censure := insertBallot("Remotion Person a voté POUR le texte de loi : « Vise à renverser le gouvernement. »", "99-9001")
	party := insertBallot("Groupe X a collectivement voté CONTRE le texte de loi : « Vise à renverser le gouvernement. »", "99-9001")
	rejet := insertBallot("Remotion Person a voté CONTRE le texte de loi : « Offre des réponses à l'ordre public. »", "99-9003")
	final := insertBallot("Remotion Person a voté POUR le texte de loi : « Offre des réponses à l'ordre public. »", "99-9002")
	if _, err := pool.Exec(ctx, "INSERT INTO contradictions (person_id, fact_a_id, fact_b_id, reason) VALUES ($1, $2, $3, 'contre puis pour')", person, rejet, final); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO fact_pair_checks (person_id, fact_a_id, fact_b_id, is_contradiction) VALUES ($1, $2, $3, true), ($1, $4, $3, false)", person, rejet, final, censure); err != nil {
		t.Fatal(err)
	}
	d := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	laws := []Law{
		{Key: "99-9001", Title: "la motion de censure déposée en application de l'article 49, alinéa 3, de la Constitution par M. Y et 110 députés.", Legislature: 99, VoteDate: &d},
		{Key: "99-9003", Title: "la motion de rejet préalable, déposée par Mme Z, du projet de loi visant à répondre à l'ordre public (première lecture).", Legislature: 99, VoteDate: &d},
		{Key: "99-9002", Title: "l'ensemble du projet de loi visant à répondre à l'ordre public (première lecture).", Legislature: 99, VoteDate: &d},
	}

	res, err := RemotionBallots(ctx, storer, store, laws)
	if err != nil {
		t.Fatal(err)
	}
	if res.Scrutins != 2 || res.Ballots != 3 || res.Reopened != 1 {
		t.Fatalf("result = %+v; want 2 motions, 3 ballots, 1 contradiction reopened", res)
	}
	text := func(id int64) string {
		var s string
		if err := pool.QueryRow(ctx, "SELECT fact_text FROM facts WHERE id = $1", id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got, want := text(censure), "Remotion Person a voté POUR la motion de censure du 2 février 2026 (article 49, alinéa 3, de la Constitution) : « Vise à renverser le gouvernement. »"; got != want {
		t.Errorf("censure = %q\nwant      %q", got, want)
	}
	if got := text(party); !strings.Contains(got, "collectivement voté CONTRE la motion de censure du 2 février 2026") {
		t.Errorf("party ballot = %q", got)
	}
	if got, want := text(rejet), "Remotion Person a voté CONTRE la motion de rejet préalable, déposée par Mme Z, du projet de loi visant à répondre à l'ordre public (première lecture)"; got != want {
		t.Errorf("rejet = %q\nwant    %q", got, want)
	}
	if got := text(final); got != "Remotion Person a voté POUR le texte de loi : « Offre des réponses à l'ordre public. »" {
		t.Errorf("the ballot of another scrutin changed: %q", got)
	}
	var textEn *string
	if err := pool.QueryRow(ctx, "SELECT fact_text_en FROM facts WHERE id = $1", rejet).Scan(&textEn); err != nil {
		t.Fatal(err)
	}
	if textEn != nil {
		t.Errorf("fact_text_en = %q, want reset for the translation repair", *textEn)
	}
	var contradictions, checks int
	if err := pool.QueryRow(ctx, "SELECT (SELECT COUNT(*) FROM contradictions WHERE person_id = $1), (SELECT COUNT(*) FROM fact_pair_checks WHERE person_id = $1)", person).Scan(&contradictions, &checks); err != nil {
		t.Fatal(err)
	}
	if contradictions != 0 || checks != 1 {
		t.Errorf("contradictions=%d checks=%d, want 0 and the censure verdict kept", contradictions, checks)
	}
	// A second run finds nothing to rewrite.
	if res, err = RemotionBallots(ctx, storer, store, laws); err != nil || res.Scrutins != 0 || res.Ballots != 0 {
		t.Errorf("second run: %+v, %v; want nothing rewritten", res, err)
	}
}
