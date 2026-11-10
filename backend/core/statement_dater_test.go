package core

import (
	"strings"
	"testing"
	"time"

	"digital-mirror/backend"
)

// A statement date that only repeats the source's date is not stored: the
// judges keep the source's full date.
func TestSameStatementDay(t *testing.T) {
	source := time.Date(2025, 11, 9, 10, 32, 0, 0, time.UTC)
	cases := []struct {
		date string
		same bool
	}{
		{"2025", true},
		{"2025-11", true},
		{"2025-11-09", true},
		{"2019", false},
		{"2025-10", false},
		{"2025-11-08", false},
	}
	for _, c := range cases {
		d, err := backend.ParseStatementDate(c.date)
		if err != nil {
			t.Fatal(err)
		}
		if got := SameStatementDay(&d, &source); got != c.same {
			t.Errorf("%s vs %v: same %v, want %v", c.date, source, got, c.same)
		}
	}
	if SameStatementDay(nil, &source) {
		t.Error("no statement date is not the source's")
	}
}

// The excerpt keeps the opening of the article and the text around the
// quote, found by its first words whatever the typography between them.
func TestArticleExcerpt(t *testing.T) {
	filler := strings.Repeat("Paragraphe sans rapport avec la citation. ", 200) // ~8 400 characters
	article := "Portrait : les années de formation.\n" + filler +
		"En 2017, devant la foule à Marseille, « l’immigration est un problème », lançait-il. " + filler
	got := ArticleExcerpt(article, "« L'immigration est un problème », lance-t-il à la foule")
	if !strings.HasPrefix(got, "Portrait : les années de formation.") {
		t.Errorf("the excerpt lost the opening: %q", got[:60])
	}
	if !strings.Contains(got, "En 2017, devant la foule à Marseille") {
		t.Error("the excerpt lost the sentence that dates the quote")
	}
	if n := len([]rune(got)); n > excerptHead+excerptBefore+excerptAfter+10 {
		t.Errorf("the excerpt is %d characters", n)
	}

	// A quote the article does not hold: the article from its start.
	got = ArticleExcerpt(article, "une phrase absente de la page")
	if n := len([]rune(got)); n != excerptNotFound || !strings.HasPrefix(got, "Portrait") {
		t.Errorf("an unfound quote gave %d characters", n)
	}
	// A short article is given whole.
	if got := ArticleExcerpt("Court. « Oui », dit-elle.", "Oui"); got != "Court. « Oui », dit-elle." {
		t.Errorf("a short article gave %q", got)
	}
}
