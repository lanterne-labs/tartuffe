package news

import (
	"testing"
	"time"
)

// TestDateFromURL checks the URL shapes of the undated pages of 2026-10-28
// to 2026-11-01 (mirror-rxd), and that an id or a URL without a date gives
// nothing.
func TestDateFromURL(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	d := func(y, m, day int) *time.Time {
		v := time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC)
		return &v
	}
	cases := []struct {
		url  string
		want *time.Time
	}{
		{"https://www.leparisien.fr/politique/jordan-bardella-face-a-nos-lecteurs-le-pouvoir-je-my-prepare-jy-travaille-29-10-2025-FUGOI5VLDVBJPF3XJMTIOFCJ5M.php", d(2025, 10, 29)},
		{"https://www.lemonde.fr/politique/article/2025/10/15/marine-le-pen-subit-un-revers_6646.html", d(2025, 10, 15)},
		{"https://www.20minutes.fr/politique/4181338-20251025-avoir-point-commun-marine-pen", d(2025, 10, 25)},
		{"https://www.bfmtv.com/politique/elections/presidentielle/retailleau-hold-up_AV-202602260332.html", d(2026, 2, 26)},
		{"https://www.lefigaro.fr/politique/dans-les-vosges-marine-le-pen-20251025", d(2025, 10, 25)},
		{"https://example.fr/actu/2026-10-28/le-budget", d(2026, 10, 28)},
		{"https://www.lejdd.fr/politique/eric-zemmour-lislam-nest-pas-une-religion-comme-une-autre-158520", nil},
		{"https://www.ouest-france.fr/politique/marine-le-pen/marine-le-pen-a-rennes-5b2d0e7a-1234-11f0", nil},
		{"https://example.fr/article-20301025", nil},    // after now: an id
		{"https://example.fr/article-20251399", nil},    // no such month
		{"https://example.fr/article/12345678901", nil}, // a plain id
	}
	for _, c := range cases {
		got := DateFromURL(c.url, now)
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%s: got %s, want none", c.url, got.Format("2006-01-02"))
		case c.want != nil && (got == nil || !got.Equal(*c.want)):
			t.Errorf("%s: got %v, want %s", c.url, got, c.want.Format("2006-01-02"))
		}
	}
}

func TestDocumentDatePrefersURLOverSearch(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	page := time.Date(2022, 3, 15, 8, 0, 0, 0, time.UTC)
	search := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	u := "https://www.lemonde.fr/en/opinion/article/2022/03/14/eric-zemmour-s-rollercoaster-candidacy_5978632_23.html"
	if got := documentDate(&page, &search, u, now); !got.Equal(page) {
		t.Fatalf("page metadata first: got %v", got)
	}
	if got := documentDate(nil, &search, u, now); !got.Equal(time.Date(2022, 3, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("URL before search: got %v", got)
	}
	if got := documentDate(nil, &search, "https://example.org/article-sans-date", now); !got.Equal(search) {
		t.Fatalf("search last: got %v", got)
	}
	if got := documentDate(nil, nil, "https://example.org/a", now); got != nil {
		t.Fatalf("no date: got %v", got)
	}
}
