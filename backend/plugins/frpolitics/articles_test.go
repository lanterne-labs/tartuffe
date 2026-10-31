package frpolitics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The text of the commission (n° 1364) of the end-of-life bill, as pdftotext
// -layout gives it, around article 7.
const texte1364 = `
        « Il adresse cette prescription à l’une des pharmacies à usage intérieur
     désignées par l’arrêté du ministre chargé de la santé mentionné au second
     alinéa du 1° de l’article L. 5121-1 du présent code. »

                                         Article 7

         La sous-section 3 de la section 2 bis du chapitre Ier du titre Ier du livre Ier
     de la première partie du code de la santé publique, telle qu’elle résulte des
     articles 5 et 6 de la présente loi, est complétée par un article L. 1111-12-5
     ainsi rédigé :
                                           –8–



        « Art. L. 1111-12-5. – I. – Avec le médecin ou l’infirmier chargé de
    l’accompagner en application du second alinéa du V de l’article
    L. 1111-12-4, la personne convient de la date à laquelle elle souhaite
    procéder à l’administration de la substance létale.

        « II. – Dans des conditions convenues avec le médecin ou l’infirmier
    chargé de l’accompagner, l’administration de la substance létale peut être
    effectuée, à la demande de la personne, en dehors de son domicile.

                                         Article 8

         L’article L. 1111-12-6 est ainsi rédigé.
`

func TestExtractArticle(t *testing.T) {
	body := extractArticle(texte1364, "l'article 7")
	for _, want := range []string{"La sous-section 3", "article L. 1111-12-5", "en dehors de son domicile."} {
		if !strings.Contains(body, want) {
			t.Errorf("article 7 misses %q: %s", want, body)
		}
	}
	for _, not := range []string{"–8–", "Article 8", "L. 1111-12-6", "pharmacies"} {
		if strings.Contains(body, not) {
			t.Errorf("article 7 must not hold %q", not)
		}
	}
	if strings.Contains(body, "\n") || strings.Contains(body, "  ") {
		t.Error("the body is one paragraph with single spaces")
	}
	if got := extractArticle(texte1364, "l'article 9"); got != "" {
		t.Errorf("an absent article gives %q, want nothing", got)
	}
	text1er := "   Article 1er\n  Le code est modifié.\n   Article 2\n  Autre."
	if got := extractArticle(text1er, "l'article premier"); got != "Le code est modifié." {
		t.Errorf("article premier = %q", got)
	}
	if got := extractArticle("  Article 24\n Texte.\n Chapitre II\n Suite.", "l'article 24 (examen prioritaire)"); got != "Texte." {
		t.Errorf("article 24 (examen prioritaire) = %q", got)
	}
	// A new article of the commission text: "Article 4 (nouveau)"; the next
	// heading with a note ends it.
	if got := extractArticle("  Article 3\n Trois.\n  Article 4 (nouveau)\n Quatre.\n  Article 4 bis (nouveau)\n Suite.", "l'article 4"); got != "Quatre." {
		t.Errorf("article 4 (nouveau) = %q", got)
	}
	if got := extractArticle("  Article 4 (nouveau)\n Quatre.\n  Article 4 bis (nouveau)\n Suite.\n  Article 5\n Cinq.", "l'article 4 bis"); got != "Suite." {
		t.Errorf("article 4 bis (nouveau) = %q", got)
	}
}

func TestTextPage(t *testing.T) {
	cases := []struct {
		ref, slug string
		num       int
		ok        bool
	}{
		{"PIONANR5L17BTC1364", "texte-adopte-commission", 1364, true},
		{"PRJLANR5L17BTC0486", "texte-adopte-commission", 486, true},
		{"PIONANR5L17B1100", "proposition-loi", 1100, true},
		{"PRJLANR5L17B0486", "projet-loi", 486, true},
		{"PIONANR5L17BTA0323", "texte-adopte-seance", 323, true},
		{"RAPPANR5L17B1364", "", 0, false},
		{"PIONSNR5S459B0661", "", 0, false},
	}
	for _, c := range cases {
		num, slug, ok := textPage(c.ref)
		if ok != c.ok || num != c.num || slug != c.slug {
			t.Errorf("textPage(%s) = (%d, %q, %v), want (%d, %q, %v)", c.ref, num, slug, ok, c.num, c.slug, c.ok)
		}
	}
	if got := textPDFURL(17, 1364, "texte-adopte-commission"); got != "https://www.assemblee-nationale.fr/dyn/17/textes/l17b1364_texte-adopte-commission.pdf" {
		t.Errorf("pdf url = %s", got)
	}
	if got := textPDFURL(17, 122, "texte-adopte-seance"); got != "https://www.assemblee-nationale.fr/dyn/17/textes/l17t0122_texte-adopte-seance.pdf" {
		t.Errorf("pdf url of a texte adopté = %s", got)
	}
}

func TestBillTitleKey(t *testing.T) {
	bill := billTitleKey("la proposition de loi relative au droit à l'aide à mourir (première lecture).")
	if bill != "proposition de loi relative au droit à l aide à mourir" {
		t.Errorf("key = %q", bill)
	}
	amendment := billTitleKey("l'amendement n° 96 de M. Bernhardt après l'article 7 de la proposition de loi relative au droit à l'aide à mourir (première lecture).")
	if !strings.HasSuffix(amendment, bill) {
		t.Errorf("the amendment title %q must end with the bill key %q", amendment, bill)
	}
	longer := billTitleKey("l'amendement n° 3 de M. X à l'article 2 de la proposition de loi relative au droit à l'aide à mourir des mineurs (première lecture).")
	if strings.HasSuffix(longer, bill) {
		t.Error("a bill whose title extends the article's bill must not pass for it")
	}
}

// TestArticleResolver: the text of the séance comes from the amendments
// voted in it on the same bill; the statement comes from the text of the
// article and is kept; a séance whose amendments name two texts, or none,
// gates the scrutin; a statement under the confidence does too.
func TestArticleResolver(t *testing.T) {
	storer, store := setupStoreTest(t)
	ctx := context.Background()
	_, err := storer.Pool().Exec(ctx, "DELETE FROM vote_articles WHERE legislature = 99")
	if err != nil {
		t.Fatal(err)
	}
	date := time.Date(2025, 5, 22, 0, 0, 0, 0, time.UTC)
	seance := "RUANR5L99S2025IDS1"
	laws := []Law{
		{Key: "99-2003", Title: "l'article 7 de la proposition de loi relative au droit à l'aide à mourir (première lecture).", Legislature: 99, VoteDate: &date, SeanceRef: seance},
		{Key: "99-2001", Title: "l'amendement n° 96 de M. Bernhardt à l'article 7 de la proposition de loi relative au droit à l'aide à mourir (première lecture).", Legislature: 99, VoteDate: &date, SeanceRef: seance},
		{Key: "99-2002", Title: "l'amendement n° 12 de Mme Lebec à l'article 2 du projet de loi d'urgence pour Mayotte (première lecture).", Legislature: 99, VoteDate: &date, SeanceRef: seance},
		// Another bill whose title extends the article's bill, in the same séance: not a sibling.
		{Key: "99-2004", Title: "l'amendement n° 44 de M. Y à l'article 3 de la proposition de loi relative au droit à l'aide à mourir des mineurs (première lecture).", Legislature: 99, VoteDate: &date, SeanceRef: seance},
		{Key: "99-2010", Title: "l'article 3 du projet de loi d'urgence pour Mayotte (première lecture).", Legislature: 99, VoteDate: &date, SeanceRef: "RUANR5L99S2025IDS2"},
	}
	mayotte := laws[4]
	// Amendment 96 has the same number in two texts; 97 names the text alone:
	// the text every amendment of the séance names is the one.
	texts := map[int][]string{96: {"PIONANR5L99BTC1364", "PRJLANR5L99BTC0500"}, 97: {"PIONANR5L99BTC1364"}, 12: {"PRJLANR5L99BTC0500"}, 44: {"PIONANR5L99BTC0777"}}
	laws = append(laws, Law{Key: "99-2005", Title: "l'amendement n° 97 de M. Z à l'article 7 de la proposition de loi relative au droit à l'aide à mourir (première lecture).", Legislature: 99, VoteDate: &date, SeanceRef: seance})
	var statements, reads int
	r := newArticleResolver(store, laws, nil)
	r.lookup = func(_ context.Context, legislature, numero int, _ *time.Time) ([]string, error) {
		return texts[numero], nil
	}
	// The transcript names text 2222 for the séance without amendments.
	r.crTexts = func(_ context.Context, _ int, seanceRef string) ([]string, error) {
		if seanceRef == "RUANR5L99S2025IDS2" {
			return []string{"2222"}, nil
		}
		return nil, nil
	}
	r.readText = func(_ context.Context, _, url string) (string, error) {
		reads++
		if !strings.HasSuffix(url, "/dyn/99/textes/l99b1364_texte-adopte-commission.pdf") {
			return "", errors.New("unexpected url " + url)
		}
		return texte1364, nil
	}
	r.statement = func(_ context.Context, bill, article, text string) (LawSummary, error) {
		statements++
		if !strings.Contains(text, "en dehors de son domicile") || article != "l'article 7" {
			t.Errorf("statement asked for %q with text %.60q", article, text)
		}
		return LawSummary{SimpleStatement: "Permet l'administration de la substance létale en dehors du domicile.", Confidence: 0.9}, nil
	}

	object, ok, err := r.resolve(ctx, laws[0])
	if err != nil || !ok {
		t.Fatalf("resolve: (%q, %v, %v)", object, ok, err)
	}
	want := "l'article 7 (« Permet l'administration de la substance létale en dehors du domicile »)"
	if object != want {
		t.Errorf("object = %q, want %q", object, want)
	}
	phrase := votedObjectPhrase(object, "", LawSummary{SimpleStatement: "Crée un droit à l'aide à mourir."})
	if phrase != want+" du texte de loi : « Crée un droit à l'aide à mourir. »" {
		t.Errorf("phrase = %q", phrase)
	}
	// The statement is kept: a second scrutin on the same article reads it.
	object2, ok, err := r.resolve(ctx, laws[0])
	if err != nil || !ok || object2 != object || statements != 1 || reads != 1 {
		t.Errorf("second resolve: (%q, %v, %v) statements=%d reads=%d; want the stored statement", object2, ok, err, statements, reads)
	}

	// No amendment of the séance on this bill: the transcript names the
	// text; the page of the commission text does not exist, the deposited
	// text does.
	urls := []string{}
	r.readText = func(_ context.Context, _, url string) (string, error) {
		urls = append(urls, url)
		if !strings.Contains(url, "l99b2222_projet-loi") {
			return "", fmt.Errorf("%w: unexpected status 404", errNotFound)
		}
		return "   Article 3\n  Fixe la durée.\n   Article 4\n  Autre.", nil
	}
	r.statement = func(_ context.Context, bill, article, text string) (LawSummary, error) {
		return LawSummary{SimpleStatement: "Fixe la durée de l'état d'urgence.", Confidence: 0.9}, nil
	}
	object, ok, err = r.resolve(ctx, mayotte)
	if err != nil || !ok || object != "l'article 3 (« Fixe la durée de l'état d'urgence »)" {
		t.Errorf("an article whose text the transcript names: (%q, %v, %v)", object, ok, err)
	}
	if len(urls) != 3 || !strings.Contains(urls[0], "texte-adopte-commission") || !strings.Contains(urls[2], "l99b2222_projet-loi") {
		t.Errorf("urls tried = %v; want the commission text, then the deposited texts until one exists", urls)
	}
	// A séance with neither amendments nor a transcript: gated.
	r.crTexts = func(context.Context, int, string) ([]string, error) { return nil, nil }
	gated := mayotte
	gated.Key = "99-2011"
	if _, ok, err := r.resolve(ctx, gated); err != nil || ok {
		t.Errorf("an article without a text in its séance: ok=%v err=%v, want gated", ok, err)
	}
	// A statement under the confidence: gated, and kept so it is not asked again.
	r.statement = func(context.Context, string, string, string) (LawSummary, error) {
		return LawSummary{SimpleStatement: "Modifie des références.", Confidence: 0.3}, nil
	}
	low := laws[0]
	low.Title = "l'article 8 de la proposition de loi relative au droit à l'aide à mourir (première lecture)."
	_, ok, err = r.resolve(ctx, low)
	if err != nil || ok {
		t.Errorf("a low-confidence statement: ok=%v err=%v, want gated", ok, err)
	}
	// A failed source read is an error, not a gate; a PDF that does not
	// exist gates the scrutin and lets the run go on.
	r.readText = func(context.Context, string, string) (string, error) { return "", errors.New("pdf unavailable") }
	nine := laws[0]
	nine.Title = "l'article 9 de la proposition de loi relative au droit à l'aide à mourir (première lecture)."
	if _, _, err := r.resolve(ctx, nine); err == nil {
		t.Error("an unavailable text must be an error")
	}
	r.readText = func(context.Context, string, string) (string, error) {
		return "", fmt.Errorf("%w: unexpected status 404", errNotFound)
	}
	if _, ok, err := r.resolve(ctx, nine); err != nil || ok {
		t.Errorf("a missing PDF: ok=%v err=%v, want gated", ok, err)
	}
	part := laws[0]
	part.Title = "la première partie du projet de loi de finances pour 2027 (première lecture)."
	if _, ok, err := r.resolve(ctx, part); err != nil || ok {
		t.Errorf("a part of the text: ok=%v err=%v, want gated", ok, err)
	}
}
