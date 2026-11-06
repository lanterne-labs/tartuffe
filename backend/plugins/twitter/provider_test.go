package twitter

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend"
)

func TestFormatTweetBatch(t *testing.T) {
	now := time.Date(2026, 1, 15, 14, 30, 0, 0, time.UTC)
	tweets := []Tweet{
		{
			ID:           "123",
			Text:         "Hello world",
			AuthorHandle: "test_user",
			AuthorName:   "Test User",
			URL:          "https://x.com/test_user/status/123",
			Lang:         "en",
			CreatedAt:    &now,
		},
		{
			ID:           "456",
			Text:         "Another tweet",
			AuthorHandle: "test_user",
			AuthorName:   "Test User",
			URL:          "https://x.com/test_user/status/456",
		},
	}

	result := formatTweetBatch(tweets)

	if result == "" {
		t.Fatal("expected non-empty result")
	}

	// Check that it contains key elements
	checks := []string{
		"https://x.com/test_user/status/123",
		"@test_user",
		"Test User",
		"Hello world",
		"2026-01-15",
		"---",
		"https://x.com/test_user/status/456",
		"Another tweet",
	}

	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("expected output to contain %q", check)
		}
	}
}

func TestDetermineDateRange(t *testing.T) {
	now := time.Now()

	t.Run("explicit dates", func(t *testing.T) {
		from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		to := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

		handle := newTestHandle()
		opts := Options{DateFrom: &from, DateTo: &to}

		gotFrom, gotTo := determineDateRange(handle, opts)
		if !gotFrom.Equal(from) {
			t.Errorf("expected from=%v, got %v", from, gotFrom)
		}
		if !gotTo.Equal(to) {
			t.Errorf("expected to=%v, got %v", to, gotTo)
		}
	})

	t.Run("incremental from last_fetched_at", func(t *testing.T) {
		lastFetched := now.Add(-48 * time.Hour)
		handle := newTestHandle()
		handle.LastFetchedAt = &lastFetched
		opts := Options{}

		gotFrom, _ := determineDateRange(handle, opts)
		if !gotFrom.Equal(lastFetched) {
			t.Errorf("expected from=lastFetched, got %v", gotFrom)
		}
	})

	t.Run("default 30 days back", func(t *testing.T) {
		handle := newTestHandle()
		opts := Options{}

		gotFrom, _ := determineDateRange(handle, opts)
		expected := now.AddDate(0, 0, -30)
		diff := gotFrom.Sub(expected).Abs()
		if diff > 2*time.Second {
			t.Errorf("expected from ~30 days ago, got %v (diff=%v)", gotFrom, diff)
		}
	})
}

func TestAggregateResult(t *testing.T) {
	dst := &Result{
		TweetsFetched:   10,
		FactsExtracted:  3,
		SkippedRetweets: 2,
	}
	src := &Result{
		TweetsFetched:           5,
		FactsExtracted:          2,
		SkippedRetweets:         1,
		SkippedAlreadyProcessed: 4,
		Errors:                  []error{fmt.Errorf("test error")},
	}

	aggregateResult(dst, src)

	if dst.TweetsFetched != 15 {
		t.Errorf("expected TweetsFetched=15, got %d", dst.TweetsFetched)
	}
	if dst.FactsExtracted != 5 {
		t.Errorf("expected FactsExtracted=5, got %d", dst.FactsExtracted)
	}
	if dst.SkippedRetweets != 3 {
		t.Errorf("expected SkippedRetweets=3, got %d", dst.SkippedRetweets)
	}
	if dst.SkippedAlreadyProcessed != 4 {
		t.Errorf("expected SkippedAlreadyProcessed=4, got %d", dst.SkippedAlreadyProcessed)
	}
	if len(dst.Errors) != 1 {
		t.Errorf("expected 1 error, got %d", len(dst.Errors))
	}
}

func TestMapTweet(t *testing.T) {
	t.Run("regular tweet", func(t *testing.T) {
		apiTweet := twitterAPIIOTweet{
			ID:        "123",
			Text:      "Hello",
			URL:       "https://x.com/user/status/123",
			CreatedAt: "2026-01-15T14:30:00.000Z",
			Lang:      "en",
			Author: twitterAPIIOAuthor{
				UserName: "test_user",
				Name:     "Test User",
			},
			RetweetedTweet: nil,
		}

		tweet := mapTweet(apiTweet)
		if tweet.IsRetweet {
			t.Error("expected IsRetweet=false")
		}
		if tweet.CreatedAt == nil {
			t.Error("expected CreatedAt to be parsed")
		}
	})
}

func newTestHandle() Handle {
	return Handle{
		ID:         1,
		PersonID:   42,
		PersonName: "Test Person",
		Handle:     "test_handle",
		Active:     true,
	}
}

// A fact's language is the extractor's; Twitter's codes that name no
// language (qme, zxx, und) never become one.
func TestFactLang(t *testing.T) {
	batch := []Tweet{{URL: "https://x.com/a/status/1", Lang: "qme"}}
	cases := []struct {
		name string
		fact backend.Fact
		want string
	}{
		{"extractor wins", backend.Fact{Lang: "fr", SourceURL: "https://x.com/a/status/1"}, "fr"},
		{"no language code", backend.Fact{SourceURL: "https://x.com/a/status/1"}, ""},
	}
	for _, c := range cases {
		if got := factLang(c.fact, batch); got != c.want {
			t.Errorf("%s: factLang = %q, want %q", c.name, got, c.want)
		}
	}
	if got := factLang(backend.Fact{OriginalQuote: "hola amigos"}, []Tweet{{Lang: "es", Text: "¡Hola, amigos!"}}); got != "es" {
		t.Errorf("tweet fallback: factLang = %q, want es", got)
	}
}

// A fact is matched to the tweet that holds its quote, not to the first tweet
// of the batch: the extractor's SourceURL is the profile's.
func TestFindMatchingTweet(t *testing.T) {
	batch := []Tweet{
		{URL: "https://x.com/a/status/1", Text: "Bonne fête à tous les agriculteurs !"},
		{URL: "https://x.com/a/status/2", Text: "Il est temps d’organiser une grande primaire. Pour proposer un nouvel espoir https://t.co/x"},
		{URL: "https://x.com/a/status/3", Text: "Nous voterons contre ce budget, qui matraque les classes moyennes."},
	}
	cases := []struct {
		name, quote, url, want string
	}{
		{"quote held whole", "Il est temps d'organiser une grande primaire.", "https://x.com/a", "https://x.com/a/status/2"},
		{"quote trimmed and retyped", "nous voterons contre ce budget qui matraque", "https://x.com/a", "https://x.com/a/status/3"},
		{"URL named", "", "https://x.com/a/status/1", "https://x.com/a/status/1"},
		{"no tweet holds it", "La dissuasion nucléaire n'est pas partageable.", "https://x.com/a", ""},
	}
	for _, c := range cases {
		got := ""
		if tw := findMatchingTweet(backend.Fact{OriginalQuote: c.quote, SourceURL: c.url}, batch); tw != nil {
			got = tw.URL
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestIsAuthor(t *testing.T) {
	cases := []struct {
		subject, author string
		want            bool
	}{
		{"Jean-Luc Mélenchon", "jean-luc melenchon", true},
		{"Éric Zemmour", "eric zemmour", true},
		{"jean luc melenchon", "jean-luc melenchon", true},
		{"La France insoumise", "jean-luc melenchon", false},
		{"Laurent Nuñez", "eric zemmour", false},
		{"", "eric zemmour", false},
	}
	for _, c := range cases {
		if got := isAuthor(c.subject, c.author); got != c.want {
			t.Errorf("isAuthor(%q, %q) = %v, want %v", c.subject, c.author, got, c.want)
		}
	}
}
