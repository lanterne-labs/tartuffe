package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsGoogleRedirect(t *testing.T) {
	cases := map[string]bool{
		"https://www.google.com/goto?url=CAES4AEB6zswF":              true,
		"https://www.google.com/url?q=https://lemonde.fr/a":          true,
		"https://www.lemonde.fr/politique/article/2026/10/30/x.html": false,
		"https://news.google.com/articles/CBMi":                      false,
	}
	for link, want := range cases {
		if got := isGoogleRedirect(link); got != want {
			t.Errorf("%s: got %v, want %v", link, got, want)
		}
	}
}

// A Google redirect link is replaced by the URL it points at; a link that
// does not redirect is kept.
func TestResolveGoogleLinks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/goto":
			http.Redirect(w, r, "https://www.lefigaro.fr/politique/article-"+r.URL.Query().Get("url"), http.StatusFound)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	// The test server is not google.com: resolve it directly, and check the
	// host filter on real links separately (TestIsGoogleRedirect).
	final, err := googleResolver.resolve(context.Background(), srv.URL+"/goto?url=abc")
	if err != nil || final != "https://www.lefigaro.fr/politique/article-abc" {
		t.Fatalf("resolve: got %q, %v", final, err)
	}
	if _, err := googleResolver.resolve(context.Background(), srv.URL+"/plain"); err == nil {
		t.Error("a page without a redirect must not resolve")
	}

	keep := "https://www.lemonde.fr/x"
	links := []*string{&keep}
	resolveGoogleLinks(context.Background(), links)
	if keep != "https://www.lemonde.fr/x" {
		t.Errorf("a plain link is kept, got %q", keep)
	}
}
