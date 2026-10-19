package frpolitics

import (
	"context"
	"testing"
	"time"
)

// Titles are real prod scrutin titles (legislature 16/17).
func TestClassifyScrutinObject(t *testing.T) {
	cases := []struct {
		titre string
		want  ScrutinObjectType
	}{
		{"l'ensemble de la proposition de loi visant à la nationalisation d'ArcelorMittal France afin de préserver la souveraineté industrielle de la France (première lecture).", ObjectEnsemble},
		{"l'article premier de la proposition de loi visant à la nationalisation d'ArcelorMittal France", ObjectArticle},
		{"l'article 3 de la proposition de loi visant à la nationalisation d'ArcelorMittal France", ObjectArticle},
		{"le sous-amendement n° 372 de M. Verny à l'amendement n° 96 de M. Bernhardt après l'article premier bis de la proposition de loi visant à la nationalisation d'ArcelorMittal France", ObjectSousAmendement},
		{"l'amendement n° 96 de M. Bernhardt après l'article premier bis de la proposition de loi visant à la nationalisation d'ArcelorMittal France", ObjectAmendement},
		{"l'amendement n° 4 de M. Sitzenstuhl et les amendements identiques suivants à l'article premier de la proposition de loi", ObjectAmendement},
		{"la motion de censure déposée en application de l'article 49, alinéa 2, de la Constitution", ObjectMotion},
		{"la motion de rejet préalable, déposée par Mme Mathilde Panot, du projet de loi de finances pour 2026", ObjectMotion},
		{"la déclaration du Gouvernement relative à la politique générale", ObjectDeclaration},
	}
	for _, c := range cases {
		if got := ClassifyScrutinObject(c.titre); got != c.want {
			t.Errorf("ClassifyScrutinObject(%.60q) = %s, want %s", c.titre, got, c.want)
		}
	}

	if IsTextLevelVote(ObjectSousAmendement) || IsTextLevelVote(ObjectAmendement) {
		t.Error("amendment classes must not count as text-level votes")
	}
	if !IsTextLevelVote(ObjectEnsemble) || !IsTextLevelVote(ObjectArticle) || !IsTextLevelVote(ObjectMotion) {
		t.Error("ensemble/article/motion are text-level votes")
	}
}

// TestSplitArticle: a scrutin on an article of a bill is split into the
// article and the bill, so that the summarizer reads the bill and the fact
// names the article. Titles are real prod scrutin titles.
func TestSplitArticle(t *testing.T) {
	cases := []struct {
		titre, article, bill string
	}{
		{"l'article 7 de la proposition de loi relative au droit à l'aide à mourir (première lecture).", "l'article 7", "la proposition de loi relative au droit à l'aide à mourir (première lecture)."},
		{"l'article 21 bis de la proposition de loi visant à sortir la France du piège du narcotrafic (première lecture).", "l'article 21 bis", "la proposition de loi visant à sortir la France du piège du narcotrafic (première lecture)."},
		{"l'article 1er bis A de la proposition de loi visant à renforcer le parcours inclusif des enfants à besoins éducatifs particuliers (nouvelle lecture).", "l'article 1er bis A", "la proposition de loi visant à renforcer le parcours inclusif des enfants à besoins éducatifs particuliers (nouvelle lecture)."},
		{"l'article 24 (examen prioritaire) de la proposition de loi visant à sortir la France du piège du narcotrafic (première lecture).", "l'article 24 (examen prioritaire)", "la proposition de loi visant à sortir la France du piège du narcotrafic (première lecture)."},
		{"l'article 16 du projet de loi d'urgence pour Mayotte (première lecture).", "l'article 16", "projet de loi d'urgence pour Mayotte (première lecture)."},
		{"l'article premier de la proposition de loi visant à la nationalisation d'ArcelorMittal France", "l'article premier", "la proposition de loi visant à la nationalisation d'ArcelorMittal France"},
		{"la première partie du projet de loi de finances pour 2026 (première lecture).", "la première partie", "projet de loi de finances pour 2026 (première lecture)."},
	}
	for _, c := range cases {
		article, bill, ok := splitArticle(c.titre)
		if !ok || article != c.article || bill != c.bill {
			t.Errorf("splitArticle(%.50q) = (%q, %q, %v), want (%q, %q, true)", c.titre, article, bill, ok, c.article, c.bill)
		}
	}
	if _, _, ok := splitArticle("l'ensemble de la proposition de loi visant à la nationalisation d'ArcelorMittal France (première lecture)."); ok {
		t.Error("a vote on the whole text has no article")
	}
	law := Law{Title: "l'article 7 de la proposition de loi relative au droit à l'aide à mourir (première lecture)."}
	if got := summaryTitle(law); got != "la proposition de loi relative au droit à l'aide à mourir (première lecture)." {
		t.Errorf("summaryTitle = %q; want the bill", got)
	}
	if got := summaryTitle(Law{Title: "l'ensemble du projet de loi X"}); got != "l'ensemble du projet de loi X" {
		t.Errorf("summaryTitle of a whole-text vote = %q; want the title", got)
	}
}

// TestVotedObjectPhraseArticle: the fact of a ballot on an article names the
// article before the bill phrase; an amendment keeps its wording.
func TestVotedObjectPhraseArticle(t *testing.T) {
	summary := LawSummary{SimpleStatement: "Crée un droit à l'aide à mourir."}
	if got := votedObjectPhrase("l'article 7", summary); got != "l'article 7 du texte de loi : « Crée un droit à l'aide à mourir. »" {
		t.Errorf("article phrase = %q", got)
	}
	if got := votedObjectPhrase("", summary); got != "le texte de loi : « Crée un droit à l'aide à mourir. »" {
		t.Errorf("whole-text phrase = %q", got)
	}
	if got := votedObjectPhrase("l'amendement n° 3 (RN) visant à supprimer l'article", summary); got != "l'amendement n° 3 (RN) visant à supprimer l'article, déposé sur le texte de loi : « Crée un droit à l'aide à mourir. »" {
		t.Errorf("amendment phrase = %q", got)
	}
}

// TestResolveVotedObjectArticle: resolveVotedObject gates every article
// vote, with no archive read: the articleResolver, which reads the text, is
// the only path to a ballot on an article.
func TestResolveVotedObjectArticle(t *testing.T) {
	object, ok, err := resolveVotedObject(context.Background(), Law{Title: "l’article 7 de la proposition de loi relative au droit à l’aide à mourir (première lecture)."}, nil)
	if err != nil || ok || object != "" {
		t.Fatalf("article vote: (%q, %v, %v), want gated", object, ok, err)
	}
	if !isArticlePhrase("l’article 7") {
		t.Error("the curly apostrophe is an article phrase too")
	}
	if got := votedObjectPhrase("la première partie", LawSummary{}); got != "la première partie du texte de loi" {
		t.Errorf("part phrase without a statement = %q", got)
	}
	if got := summaryTitle(Law{Title: "la première partie du projet de loi de finances pour 2026 (première lecture)."}); got != "projet de loi de finances pour 2026 (première lecture)." {
		t.Errorf("summaryTitle of a budget part = %q", got)
	}
}

// TestCensureObject: a censure motion is named by its date and the paragraph
// of article 49; two motions with one purpose no longer share a ballot text.
func TestCensureObject(t *testing.T) {
	d2 := time.Date(2025, 10, 16, 0, 0, 0, 0, time.UTC)
	d3 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	m2 := Law{Title: "la motion de censure déposée en application de l'article 49, alinéa 2, de la Constitution par Mme Mathilde Panot et 86 députés.", VoteDate: &d2}
	m3 := Law{Title: "la motion de censure déposée en application de l'article 49, alinéa 3, de la Constitution par M. Stéphane Peu, Mme Mathilde Panot, Mme Cyrielle Chatelain et 110 députés.", VoteDate: &d3}
	summary := LawSummary{SimpleStatement: "Vise à renverser le gouvernement en engageant sa responsabilité par une motion de censure."}

	o2, ok, err := resolveVotedObject(context.Background(), m2, nil)
	if err != nil || !ok {
		t.Fatalf("censure motion: (%q, %v, %v), want resolved", o2, ok, err)
	}
	if o2 != "la motion de censure du 16 octobre 2025 (article 49, alinéa 2, de la Constitution)" {
		t.Errorf("object = %q", o2)
	}
	o3, _, _ := resolveVotedObject(context.Background(), m3, nil)
	p2, p3 := votedObjectPhrase(o2, summary), votedObjectPhrase(o3, summary)
	if p2 == p3 {
		t.Fatalf("two motions share the phrase %q", p2)
	}
	want := "la motion de censure du 1er février 2026 (article 49, alinéa 3, de la Constitution) : « Vise à renverser le gouvernement en engageant sa responsabilité par une motion de censure. »"
	if p3 != want {
		t.Errorf("phrase = %q\nwant     %q", p3, want)
	}
	if got := votedObjectPhrase(o2, LawSummary{}); got != o2 {
		t.Errorf("phrase without a statement = %q", got)
	}
	// Another motion keeps the bill phrase.
	if o, ok, err := resolveVotedObject(context.Background(), Law{Title: "la motion de rejet préalable, déposée par Mme Mathilde Panot, du projet de loi de finances pour 2026"}, nil); err != nil || !ok || o != "" {
		t.Errorf("rejet préalable: (%q, %v, %v), want the bill phrase", o, ok, err)
	}
}
