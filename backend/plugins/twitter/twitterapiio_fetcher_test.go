package twitter

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestTwitterAPIIOFetcher_Integration(t *testing.T) {
	apiKey := os.Getenv("TWITTER_API_IO_KEY")
	if apiKey == "" {
		t.Skip("TWITTER_API_IO_KEY not set, skipping integration test")
	}

	fetcher, err := NewTwitterAPIIOFetcherFromEnv()
	if err != nil {
		t.Fatalf("failed to create fetcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tweets, err := fetcher.FetchTweets(ctx, "from:MLP_officiel", FetchOptions{
		MaxTweets: 5,
	})
	if err != nil {
		t.Fatalf("FetchTweets failed: %v", err)
	}

	if len(tweets) == 0 {
		t.Fatal("expected at least 1 tweet")
	}

	for _, tw := range tweets {
		if tw.ID == "" {
			t.Error("tweet ID is empty")
		}
		if tw.Text == "" {
			t.Error("tweet text is empty")
		}
		if tw.URL == "" {
			t.Error("tweet URL is empty")
		}
		t.Logf("Tweet %s: %s (retweet=%v)", tw.ID, tw.Text[:min(80, len(tw.Text))], tw.IsRetweet)
	}
}
