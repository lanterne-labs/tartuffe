package core

import (
	"strings"
	"testing"
)

func TestNormalizeForMatch(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  Hello   World ", "hello world"},
		// Smart quotes / guillemets / ellipsis unification.
		{"« Il faut agir », a déclaré…", `" il faut agir ", a declare...`},
		// NFD-fold of accented French.
		{"déclare", "declare"},
		{"épouvantable", "epouvantable"},
		{"présidente", "presidente"},
		// Mixed whitespace + accents.
		{"Le\nprésident\ta parlé.", "le president a parle."},
	}
	for _, c := range cases {
		got := normalizeForMatch(c.in)
		if got != c.want {
			t.Errorf("normalizeForMatch(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestQuoteInContent(t *testing.T) {
	// Authentic stored quote (fact about Macron on leprogres.fr One Health Summit).
	storedQuote := "Il s’agit aussi de bannir l’usage des antimicrobiens, encore trop souvent utilisés comme facteurs de croissance dans certaines pratiques d’élevage."
	// Simulated scraped content: same sentence with ASCII quotes (as Apify/webcrawlerapi often delivers).
	cleanedContent := strings.Join([]string{
		"# Sommet One Health",
		"",
		"Le président de la République s'est exprimé mardi matin devant l'auditoire.",
		"Il s'agit aussi de bannir l'usage des antimicrobiens, encore trop souvent utilisés comme facteurs de croissance dans certaines pratiques d'élevage.",
		"Plusieurs autres thèmes ont été abordés au cours du sommet.",
	}, "\n")

	if !quoteInContent(storedQuote, cleanedContent) {
		t.Fatalf("expected quote to be found in cleaned content after normalization")
	}

	// Negative: BFMTV tile-headline quote vs Le Pen article body (the bug pattern).
	tileQuote := `Municipales 2026: "Les extrêmes, où qu'ils soient, demeurent dangereux pour la République", déclare Emmanuel Macron`
	unrelatedBody := "La présidente du RN à l'Assemblée nationale Marine Le Pen est en visite à Courcelles-lès-Lens (Pas-de-Calais) pour soutenir le candidat du Rassemblement national dans le second tour des élections municipales, mercredi 18 mars."
	if quoteInContent(tileQuote, unrelatedBody) {
		t.Fatalf("expected sidebar-tile quote NOT to be found in unrelated body")
	}

	// Negative: very short quote rejected (avoids false positives on noise).
	if quoteInContent("oui", cleanedContent) {
		t.Fatalf("expected short quote 'oui' to be rejected by 10-char floor")
	}

	// Empty inputs both ways.
	if quoteInContent("", cleanedContent) {
		t.Fatalf("expected empty quote to be rejected")
	}
	if quoteInContent(storedQuote, "") {
		t.Fatalf("expected empty content to reject any quote")
	}
}
