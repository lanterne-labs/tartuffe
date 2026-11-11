package xbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/storage"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTelegram records the Bot API calls.
type fakeTelegram struct {
	mu    sync.Mutex
	calls []map[string]any
	next  int64
}

func (f *fakeTelegram) server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		f.mu.Lock()
		defer f.mu.Unlock()
		body["method"] = r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		f.calls = append(f.calls, body)
		f.next++
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":` + strconv.FormatInt(f.next, 10) + `}}`))
	}))
}

func (f *fakeTelegram) last(method string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i]["method"] == method {
			return f.calls[i]
		}
	}
	return nil
}

type fakePoster struct{ posted []string }

func (p *fakePoster) Post(_ context.Context, text string) (string, error) {
	p.posted = append(p.posted, text)
	return "99", nil
}

// testBot builds the bot over the test database with one candidate, two
// contradictions and a tag account. It returns the bot, the fake Telegram,
// the fake poster and the pool.
func testBot(t *testing.T) (*Bot, *fakeTelegram, *fakePoster, *pgxpool.Pool) {
	_ = godotenv.Overload("../../.env")
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	store, err := storage.NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err)
	t.Cleanup(store.Close)
	pool := store.Pool()
	_, err = pool.Exec(ctx, `TRUNCATE persons, facts, fact_sources, fact_mentions, contradictions, stance_documents, twitter_handles,
		x_posts, x_accounts, contradiction_reviews, user_reports RESTART IDENTITY CASCADE`)
	require.NoError(t, err)
	var person int64
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO persons (name, entity_type, attributes, fact_count) VALUES ('jean dupont', 'human', '{"candidate":"true"}', 3) RETURNING id`).Scan(&person))
	_, err = pool.Exec(ctx, `INSERT INTO twitter_handles (person_id, handle) VALUES ($1, 'JDupont')`, person)
	require.NoError(t, err)
	var facts []int64
	for i, q := range []string{"Je me battrai pour la retraite à 60 ans.", "Je voterai le report à 64 ans.", "Je ne change jamais d'avis."} {
		var id int64
		emb := "[" + strings.TrimSuffix(strings.Repeat("0.1,", 4096), ",") + "]"
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO facts (fact_text, embedding, created_at) VALUES ($1, $2::halfvec, now()) RETURNING id`, "Il dit : "+q, emb).Scan(&id))
		_, err = pool.Exec(ctx, `INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type, published_at) VALUES ($1, 'https://www.lemonde.fr/x', $2, 'test', $3)`,
			id, q, time.Date(2020+i, 1, 1, 0, 0, 0, 0, time.UTC))
		require.NoError(t, err)
		facts = append(facts, id)
	}
	for _, pair := range [][2]int64{{facts[0], facts[1]}, {facts[1], facts[2]}} {
		_, err = pool.Exec(ctx, `INSERT INTO contradictions (person_id, fact_a_id, fact_b_id, reason, detected_at) VALUES ($1, $2, $3, '60 puis 64.', now())`, person, pair[0], pair[1])
		require.NoError(t, err)
	}
	dom := "lemonde.fr"
	_, err = store.UpsertXAccount(ctx, storage.XAccount{Handle: "lemondefr", Category: "outlet", Domain: &dom, Status: "approved"})
	require.NoError(t, err)

	tg := &fakeTelegram{}
	srv := tg.server(t)
	t.Cleanup(srv.Close)
	poster := &fakePoster{}
	zone, _ := time.LoadLocation("Europe/Paris")
	now := time.Date(2026, 11, 10, 11, 30, 0, 0, zone)
	// The sober version quotes the fact both contradictions share: the draft
	// has its two versions whichever contradiction is picked.
	b := &Bot{Store: store, Telegram: NewTelegram("tok", 42, srv.URL), Poster: poster,
		Writer: &fakeWriter{reply: Reply{Standard: "Jean Dupont : « Je voterai le report à 64 ans ». Logique.", Satirical: "Jean Dupont, toujours constant."}},
		Slots:  []Clock{{12, 0}, {19, 0}}, Zone: zone, Lead: time.Hour, Grace: time.Hour, Site: "https://tartuffe.me",
		Now: func() time.Time { return now }}
	return b, tg, poster, pool
}

func postStatus(t *testing.T, pool *pgxpool.Pool, id int64) string {
	var s string
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT status FROM x_posts WHERE id = $1`, id).Scan(&s))
	return s
}

// TestBotFlow: a draft before the noon slot, approved by a tap before the
// slot, posted at the slot with its tags marked; the next slot's draft is
// marked false, and its contradiction leaves the site.
func TestBotFlow(t *testing.T) {
	b, tg, poster, pool := testBot(t)
	ctx := context.Background()
	start := b.Now()

	require.NoError(t, b.Tick(ctx))
	msg := tg.last("sendMessage")
	require.NotNil(t, msg, "a draft is sent an hour before noon")
	assert.Contains(t, msg["text"], "<b>Satirique</b>")
	assert.Contains(t, msg["text"], "@jdupont @lemondefr")
	require.NoError(t, b.Tick(ctx))
	var drafts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM x_posts`).Scan(&drafts))
	assert.Equal(t, 1, drafts, "one draft per slot")

	// Approved before the slot: it waits, then goes out at noon.
	require.NoError(t, b.Handle(ctx, Update{CallbackID: "c1", Data: "p:1:2", MessageID: 1}))
	assert.Equal(t, "approved", postStatus(t, pool, 1))
	assert.Empty(t, poster.posted)
	b.Now = func() time.Time { return start.Add(31 * time.Minute) }
	require.NoError(t, b.Tick(ctx))
	require.Len(t, poster.posted, 1)
	assert.Equal(t, "Jean Dupont, toujours constant.\n\n@jdupont @lemondefr", poster.posted[0])
	assert.Equal(t, "posted", postStatus(t, pool, 1))
	var tagged int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM x_accounts WHERE last_tagged_at IS NOT NULL`).Scan(&tagged))
	assert.Equal(t, 1, tagged)
	var reviewed string
	require.NoError(t, pool.QueryRow(ctx, `SELECT verdict FROM contradiction_reviews WHERE reviewer = 'xbot'`).Scan(&reviewed))
	assert.Equal(t, "real", reviewed)
	assert.Contains(t, tg.last("editMessageText")["text"], "Publié")

	// A second tap on a decided draft changes nothing.
	require.NoError(t, b.Handle(ctx, Update{CallbackID: "c2", Data: "p:1:1", MessageID: 1}))
	assert.Len(t, poster.posted, 1)

	// The evening slot: a draft on the other contradiction, marked false.
	b.Now = func() time.Time { return start.Add(6*time.Hour + 45*time.Minute) }
	require.NoError(t, b.Tick(ctx))
	require.Equal(t, "draft", postStatus(t, pool, 2))
	var cid int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT contradiction_id FROM x_posts WHERE id = 2`).Scan(&cid))
	require.NoError(t, b.Handle(ctx, Update{CallbackID: "c3", Data: "f:2", MessageID: 3}))
	assert.Equal(t, "false", postStatus(t, pool, 2))
	var left int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM contradictions WHERE id = $1`, cid).Scan(&left))
	assert.Equal(t, 0, left, "dismissed from the site")
}

// TestBotReplyAndExpiry: a draft undecided past its grace expires; a reply
// gives the text (the tags added) and, without the X keys, it waits; the
// silence drafts nothing.
func TestBotReplyAndExpiry(t *testing.T) {
	b, tg, poster, pool := testBot(t)
	ctx := context.Background()
	start := b.Now()
	require.NoError(t, b.Tick(ctx))
	b.Now = func() time.Time { return start.Add(time.Hour + 31*time.Minute) }
	require.NoError(t, b.Tick(ctx))
	assert.Equal(t, "expired", postStatus(t, pool, 1))
	assert.Contains(t, tg.last("editMessageText")["text"], "Expiré")

	b.Poster = nil
	b.Now = func() time.Time { return start.Add(6*time.Hour + 45*time.Minute) }
	require.NoError(t, b.Tick(ctx))
	var msg int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT telegram_message_id FROM x_posts WHERE id = 2`).Scan(&msg))
	require.NoError(t, b.Handle(ctx, Update{ReplyTo: msg, Text: "Mon propre texte."}))
	assert.Equal(t, "approved", postStatus(t, pool, 2))
	var chosen string
	require.NoError(t, pool.QueryRow(ctx, `SELECT chosen FROM x_posts WHERE id = 2`).Scan(&chosen))
	assert.Equal(t, "Mon propre texte.\n\n@jdupont @lemondefr", chosen)
	b.Now = func() time.Time { return start.Add(8 * time.Hour) }
	require.NoError(t, b.Tick(ctx))
	assert.Empty(t, poster.posted, "no keys: nothing posted")
	assert.Equal(t, "approved", postStatus(t, pool, 2))

	b.Silence = func(context.Context, time.Time) (bool, error) { return true, nil }
	b.Now = func() time.Time { return start.Add(24 * time.Hour) }
	require.NoError(t, b.Tick(ctx))
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM x_posts`).Scan(&n))
	assert.Equal(t, 2, n, "no draft during the silence")
}
