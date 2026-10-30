package twitter

import (
	"context"
	"testing"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
)

type redateFakes struct {
	docs    []core.SourceDocument
	sources []storage.FactSourceRow
	fetched map[string]Tweet
	asked   []string
	set     map[int64][2]any // fact source id -> url, date
}

func (f *redateFakes) ListDocuments(_ context.Context, _ string, afterID int64, limit int) ([]core.SourceDocument, error) {
	var out []core.SourceDocument
	for _, d := range f.docs {
		if d.ID > afterID && len(out) < limit {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *redateFakes) SetDocumentContent(_ context.Context, id int64, content string, publishedAt *time.Time) error {
	for i := range f.docs {
		if f.docs[i].ID == id {
			f.docs[i].Content = content
			if publishedAt != nil {
				f.docs[i].PublishedAt = publishedAt
			}
		}
	}
	return nil
}

func (f *redateFakes) ListFactSourcesByType(_ context.Context, _ string, afterID int64, limit int) ([]storage.FactSourceRow, error) {
	var out []storage.FactSourceRow
	for _, r := range f.sources {
		if r.ID > afterID && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *redateFakes) SetFactSourceURLAndDate(_ context.Context, id int64, url string, publishedAt *time.Time) error {
	if f.set == nil {
		f.set = map[int64][2]any{}
	}
	f.set[id] = [2]any{url, publishedAt}
	return nil
}

func (f *redateFakes) FetchTweetsByIDs(_ context.Context, ids []string) ([]Tweet, error) {
	f.asked = append(f.asked, ids...)
	var out []Tweet
	for _, id := range ids {
		if tw, ok := f.fetched[id]; ok {
			out = append(out, tw)
		}
	}
	return out, nil
}

// The facts of a batch all carried the first tweet's date; after the repair
// each carries its own tweet's URL and date, the tweets without text having
// been fetched again. A fact no tweet holds is reported, not changed.
func TestRedate(t *testing.T) {
	d1 := time.Date(2026, 2, 10, 9, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 2, 17, 9, 0, 0, 0, time.UTC)
	batchDate := d1
	f := &redateFakes{
		docs: []core.SourceDocument{
			{ID: 1, URL: "https://x.com/a/status/1", Content: "Bonne fête aux agriculteurs !", PublishedAt: &d1,
				Metadata: map[string]any{"tweet_id": "1", "author_handle": "A"}},
			{ID: 2, URL: "https://x.com/a/status/2", Content: "", PublishedAt: &d2,
				Metadata: map[string]any{"tweet_id": "2", "author_handle": "A"}},
			{ID: 3, URL: "https://x.com/a/status/3", Content: "",
				Metadata: map[string]any{"tweet_id": "3", "author_handle": "A"}},
		},
		fetched: map[string]Tweet{
			"2": {ID: "2", URL: "https://x.com/a/status/2", Text: "François Bayrou recule sur l'interdiction du voile dans le sport.", CreatedAt: &d2, AuthorHandle: "A"},
		},
		sources: []storage.FactSourceRow{
			{ID: 10, FactID: 100, SourceURL: "https://x.com/a", PublishedAt: &batchDate, OriginalQuote: "Bonne fête aux agriculteurs", Metadata: map[string]any{"twitter_handle": "a"}},
			{ID: 11, FactID: 101, SourceURL: "https://x.com/a", PublishedAt: &batchDate, OriginalQuote: "François Bayrou recule sur l'interdiction du voile dans le sport", Metadata: map[string]any{"twitter_handle": "a"}},
			{ID: 12, FactID: 102, SourceURL: "https://x.com/a", PublishedAt: &batchDate, OriginalQuote: "La dissuasion nucléaire n'est pas partageable.", Metadata: map[string]any{"twitter_handle": "a"}},
		},
	}

	rep, err := Redate(context.Background(), f, f, RedateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.asked) != 2 || rep.TweetsFetched != 1 || rep.TweetsMissing != 1 {
		t.Errorf("fetch: asked %v, fetched %d, missing %d", f.asked, rep.TweetsFetched, rep.TweetsMissing)
	}
	if f.docs[1].Content == "" {
		t.Error("the fetched text is stored even without --apply")
	}
	if rep.Sources != 3 || rep.Matched != 2 || rep.DateChanged != 1 || len(rep.Unmatched) != 1 || rep.Unmatched[0].FactID != 102 {
		t.Errorf("report: %+v", rep)
	}
	if len(f.set) != 0 {
		t.Errorf("without --apply nothing is written, got %v", f.set)
	}

	if _, err := Redate(context.Background(), f, f, RedateOptions{Apply: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.set[11]; got[0] != "https://x.com/a/status/2" || !got[1].(*time.Time).Equal(d2) {
		t.Errorf("source 11: got %v, want tweet 2 dated %v", got, d2)
	}
	if got := f.set[10]; got[0] != "https://x.com/a/status/1" {
		t.Errorf("source 10 moves to its tweet's URL, got %v", got)
	}
	if _, ok := f.set[12]; ok {
		t.Error("an unmatched source is not written")
	}
}
