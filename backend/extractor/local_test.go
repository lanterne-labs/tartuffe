package extractor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleArticleHTML = `<!DOCTYPE html>
<html lang="fr">
<head>
  <meta charset="UTF-8">
  <title>Annonces du gouvernement sur les aides au carburant</title>
  <meta property="og:title" content="Annonces du gouvernement sur les aides au carburant">
  <meta property="article:published_time" content="2026-05-21T14:30:00+02:00">
</head>
<body>
  <header><nav>Menu Politique Économie International</nav></header>
  <aside class="related"><h3>À lire aussi</h3><ul><li>Article 1</li><li>Article 2</li></ul></aside>
  <article>
    <h1>Annonces du gouvernement sur les aides au carburant</h1>
    <p class="byline">Par la rédaction · 21 mai 2026</p>
    <p>Le Premier ministre Sébastien Lecornu a annoncé jeudi 21 mai 2026 une enveloppe de 710 millions d'euros d'aides nouvelles destinées à compenser la hausse des prix du carburant pour les Français qui se rendent au travail en voiture.</p>
    <p>Le montant maximum de la prime carburant employeur sera doublé, passant de 300 à 600 euros par an. Cette mesure devrait bénéficier à plus de deux millions de salariés selon l'estimation du ministère du Travail.</p>
    <p>David Amiel, porte-parole adjoint, a précisé que les indemnités kilométriques des fonctionnaires utilisant leur véhicule personnel pour leurs déplacements professionnels seraient également revalorisées de quinze pour cent.</p>
    <p>Philippe Tabarot, ministre des Transports, a indiqué que les chauffeurs de taxi bénéficieraient à partir du 1er octobre d'une aide à l'achat de véhicules à faibles émissions pouvant atteindre 5 000 euros.</p>
  </article>
  <footer><p>Cookies | Mentions légales | Contact</p></footer>
</body>
</html>`

const tinyPageHTML = `<!DOCTYPE html>
<html><head><title>Tiny</title></head><body><p>Only this.</p></body></html>`

// TestLocalExtractor_HappyPath verifies that LocalExtractor returns a clean
// ExtractedContent from a well-formed article — stripping nav/aside/footer
// and surfacing the title from <meta og:title>.
func TestLocalExtractor_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, sampleArticleHTML)
	}))
	defer srv.Close()

	ext := NewLocalExtractor()
	out, err := ext.ExtractContent(srv.URL + "/article")
	require.NoError(t, err)
	require.NotNil(t, out)

	assert.Equal(t, "Annonces du gouvernement sur les aides au carburant", out.Title)
	// Body content shows up, navigation/footer stripped
	assert.Contains(t, out.Content, "710 millions d'euros")
	assert.Contains(t, out.Content, "Sébastien Lecornu")
	assert.Contains(t, out.Content, "Philippe Tabarot")
	assert.NotContains(t, out.Content, "Menu Politique Économie")
	assert.NotContains(t, out.Content, "Mentions légales")

	require.NotNil(t, out.DatePublished, "DatePublished should be populated from <meta article:published_time>")
	assert.Equal(t, 2026, out.DatePublished.Year())
	assert.Equal(t, 5, int(out.DatePublished.Month()))
	assert.Equal(t, 21, out.DatePublished.Day())
}

// TestLocalExtractor_ShortContentTriggersFailure verifies that pages with too
// little extractable content (paywall preview, cookie banner, video stub)
// return an error so the failover can try another extractor.
func TestLocalExtractor_ShortContentTriggersFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, tinyPageHTML)
	}))
	defer srv.Close()

	ext := NewLocalExtractor()
	_, err := ext.ExtractContent(srv.URL + "/stub")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "below")
}

// TestLocalExtractor_HTTPError surfaces non-2xx responses as errors so the
// failover layer can route around them.
func TestLocalExtractor_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	ext := NewLocalExtractor()
	_, err := ext.ExtractContent(srv.URL + "/blocked")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 403")
}

// TestLocalExtractor_InvalidURL — bad URLs fail fast before any HTTP.
func TestLocalExtractor_InvalidURL(t *testing.T) {
	ext := NewLocalExtractor()
	_, err := ext.ExtractContent("not-a-valid-url")
	require.Error(t, err)
}
