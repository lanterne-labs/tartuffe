package xbot

import (
	"context"
	"encoding/json"
	"fmt"
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

type fakePoster struct {
	posted, quoted []string
	refuseQuotes   bool // as X does to an app the post does not mention
}

func (p *fakePoster) Post(_ context.Context, text, quote string) (string, error) {
	if p.refuseQuotes && quote != "" {
		return "", fmt.Errorf("post tweet: status 403: %w", ErrQuoteRefused)
	}
	p.posted = append(p.posted, text)
	p.quoted = append(p.quoted, quote)
	return "99", nil
}

// testBot builds the bot over the test database with one candidate, two
// contradictions that share no fact and a tag account. It returns the bot, the fake Telegram,
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
	for i, q := range []string{"Je me battrai pour la retraite à 60 ans.", "Je voterai le report à 64 ans.", "Je ne change jamais d'avis.", "Je voterai le report à 64 ans, finalement."} {
		var id int64
		emb := "[" + strings.TrimSuffix(strings.Repeat("0.1,", 4096), ",") + "]"
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO facts (fact_text, embedding, created_at) VALUES ($1, $2::halfvec, now()) RETURNING id`, "Il dit : "+q, emb).Scan(&id))
		_, err = pool.Exec(ctx, `INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type, published_at) VALUES ($1, 'https://www.lemonde.fr/x', $2, 'test', $3)`,
			id, q, time.Date(2020+i, 1, 1, 0, 0, 0, 0, time.UTC))
		require.NoError(t, err)
		facts = append(facts, id)
	}
	for _, pair := range [][2]int64{{facts[0], facts[1]}, {facts[2], facts[3]}} {
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
	// The sober version quotes words both contradictions hold (they share no
	// fact: a pair that shares one with a drafted pair is never drafted): the
	// draft has its two versions whichever contradiction is picked.
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
	b.Writer.(*fakeWriter).reply = Reply{Standard: "Jean Dupont : « Je voterai le report à 64 ans ». Tiens donc.", Satirical: "Jean Dupont, la suite au prochain épisode."}
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

// TestBotDraftWaits: without a grace a draft never expires, and a tap days
// later still approves it; the tweet then goes out at once.
func TestBotDraftWaits(t *testing.T) {
	b, _, poster, pool := testBot(t)
	b.Grace = 0
	ctx := context.Background()
	start := b.Now()
	require.NoError(t, b.Tick(ctx))
	b.Now = func() time.Time { return start.Add(72 * time.Hour) }
	require.NoError(t, b.Tick(ctx))
	assert.Equal(t, "draft", postStatus(t, pool, 1))
	require.NoError(t, b.Handle(ctx, Update{CallbackID: "c1", Data: "p:1:1", MessageID: 1}))
	require.NoError(t, b.Tick(ctx))
	assert.Equal(t, "posted", postStatus(t, pool, 1))
	assert.Len(t, poster.posted, 1)
}

// TestBotRevise: a reply to a draft asks for other versions: the writer gets
// the request and the versions before, the new versions come as a new
// message whose buttons publish them, and the draft is still a draft.
func TestBotRevise(t *testing.T) {
	b, tg, poster, pool := testBot(t)
	ctx := context.Background()
	require.NoError(t, b.Tick(ctx))
	w := b.Writer.(*fakeWriter)
	w.reply = Reply{Standard: "Jean Dupont, en bref.", Satirical: "Jean Dupont, encore plus drôle."}
	var msg int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT telegram_message_id FROM x_posts WHERE id = 1`).Scan(&msg))
	require.NoError(t, b.Handle(ctx, Update{ReplyTo: msg, Text: "plus drôle, sans citation"}))
	assert.Contains(t, w.prompt, "plus drôle, sans citation")
	assert.Contains(t, w.prompt, "Jean Dupont, toujours constant.")
	assert.Equal(t, "draft", postStatus(t, pool, 1))
	var variants []string
	var revised int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT variants, telegram_message_id FROM x_posts WHERE id = 1`).Scan(&variants, &revised))
	require.Len(t, variants, 2)
	assert.Contains(t, variants[1], "encore plus drôle")
	assert.NotEqual(t, msg, revised, "a new message carries the new versions")
	assert.Contains(t, tg.last("sendMessage")["text"], "encore plus drôle")
	require.NoError(t, b.Handle(ctx, Update{CallbackID: "c1", Data: "p:1:2", MessageID: revised}))
	b.Now = func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, b.Zone) }
	require.NoError(t, b.Tick(ctx))
	require.Len(t, poster.posted, 1)
	assert.Contains(t, poster.posted[0], "encore plus drôle")
}

// TestBotJitter: the draft is sent Lead before the earliest time the tweet
// may go out, the tweet gets a time within Jitter of its slot, and it goes
// out at that time, not at the slot.
func TestBotJitter(t *testing.T) {
	b, _, poster, pool := testBot(t)
	b.Jitter = 2 * time.Hour
	ctx := context.Background()
	slot := time.Date(2026, 11, 10, 12, 0, 0, 0, b.Zone)
	b.Now = func() time.Time { return slot.Add(-3*time.Hour - time.Minute) }
	require.NoError(t, b.Tick(ctx))
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM x_posts`).Scan(&n))
	assert.Equal(t, 0, n, "too early for a draft")
	b.Now = func() time.Time { return slot.Add(-3 * time.Hour).Add(time.Minute) }
	require.NoError(t, b.Tick(ctx))
	var at time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT post_at FROM x_posts WHERE id = 1`).Scan(&at))
	assert.False(t, at.Before(slot.Add(-2*time.Hour)) || at.After(slot.Add(2*time.Hour)), "post_at %s within 2 h of the slot", at)
	require.NoError(t, b.Handle(ctx, Update{CallbackID: "c1", Data: "p:1:1", MessageID: 1}))
	b.Now = func() time.Time { return at.Add(-time.Minute) }
	require.NoError(t, b.Tick(ctx))
	assert.Empty(t, poster.posted, "not before its time")
	b.Now = func() time.Time { return at }
	require.NoError(t, b.Tick(ctx))
	assert.Len(t, poster.posted, 1)
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
	b.Writer.(*fakeWriter).reply = Reply{Standard: "Jean Dupont, une autre histoire.", Satirical: "Jean Dupont, la suite au prochain épisode."}
	b.Now = func() time.Time { return start.Add(6*time.Hour + 45*time.Minute) }
	require.NoError(t, b.Tick(ctx))
	var msg int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT telegram_message_id FROM x_posts WHERE id = 2`).Scan(&msg))
	require.NoError(t, b.Handle(ctx, Update{ReplyTo: msg, Text: "= Mon propre texte."}))
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

// TestBotVoteTags: a vote tweet tags the approved accounts of the vote's
// categories, never a party, a suggested account or a person's own; a
// rewrite keeps them; posting marks them tagged, so the next tweet takes
// others.
func TestBotVoteTags(t *testing.T) {
	b, _, poster, pool := testBot(t)
	ctx := context.Background()
	b.Slots = nil
	b.VoteSlots = []Clock{{12, 0}}
	b.Vote = func(context.Context, time.Time) (*VoteState, error) {
		return &VoteState{Slug: "presidentielle-2027", MaxChoices: 3}, nil
	}
	_, err := pool.Exec(ctx, `TRUNCATE x_accounts; INSERT INTO x_accounts (handle, category, status, person_id) VALUES
		('anticor_org', 'association', 'approved', NULL), ('unepersonnalite', 'personality', 'approved', NULL),
		('rnational_off', 'party', 'approved', NULL), ('libe', 'outlet', 'suggested', NULL), ('jdupont', 'other', 'approved', 1)`)
	require.NoError(t, err)
	require.NoError(t, b.Tick(ctx))
	var tags, variants []string
	require.NoError(t, pool.QueryRow(ctx, `SELECT tags, variants FROM x_posts WHERE id = 1`).Scan(&tags, &variants))
	assert.ElementsMatch(t, []string{"anticor_org", "unepersonnalite"}, tags)
	assert.True(t, strings.HasSuffix(variants[0], "\n\n@"+strings.Join(tags, " @")+"\nhttps://tartuffe.me/election/presidentielle-2027"), variants[0])
	assert.LessOrEqual(t, Length(variants[0]), MaxLength)

	var msg int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT telegram_message_id FROM x_posts WHERE id = 1`).Scan(&msg))
	w := b.Writer.(*fakeWriter)
	w.reply = Reply{Standard: "Votez pour 2027.", Satirical: ""}
	require.NoError(t, b.Handle(ctx, Update{ReplyTo: msg, Text: "plus court"}))
	assert.NotContains(t, w.prompt, "@anticor_org", "the writer gets the text without the tags")
	require.NoError(t, pool.QueryRow(ctx, `SELECT variants FROM x_posts WHERE id = 1`).Scan(&variants))
	require.Len(t, variants, 1)
	assert.True(t, strings.HasPrefix(variants[0], "Votez pour 2027.\n\n@"), variants[0])

	require.NoError(t, b.Handle(ctx, Update{CallbackID: "c1", Data: "p:1:1"}))
	b.Now = func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, b.Zone) }
	require.NoError(t, b.Tick(ctx))
	require.Len(t, poster.posted, 1)
	var tagged int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM x_accounts WHERE last_tagged_at IS NOT NULL`).Scan(&tagged))
	assert.Equal(t, 2, tagged)
}

// TestBotQuoteRefused: when X refuses the quote, the tweet goes out without
// it; without XBOT_QUOTE the bot quotes nothing.
func TestBotQuoteRefused(t *testing.T) {
	for _, quote := range []bool{true, false} {
		b, _, poster, pool := testBot(t)
		ctx := context.Background()
		b.Quote = quote
		poster.refuseQuotes = true
		_, err := pool.Exec(ctx, `UPDATE fact_sources SET source_url = 'https://x.com/jdupont/status/555' WHERE fact_id IN (2, 4)`)
		require.NoError(t, err)
		require.NoError(t, b.Tick(ctx))
		require.NoError(t, b.Handle(ctx, Update{CallbackID: "c1", Data: "p:1:1", MessageID: 1}))
		b.Now = func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, b.Zone) }
		require.NoError(t, b.Tick(ctx))
		require.Len(t, poster.posted, 1, "quote %v", quote)
		assert.Empty(t, poster.quoted[0])
		assert.Equal(t, "posted", postStatus(t, pool, 1))
	}
}

// TestBotVoteRevise: a reply to a vote draft asks for other versions: the
// writer gets the request, the draft and what may be said of the vote; the
// new versions come with the link and buttons of their own; a version with a
// mention is dropped; "=" before a reply makes it the tweet itself.
func TestBotVoteRevise(t *testing.T) {
	b, tg, poster, pool := testBot(t)
	ctx := context.Background()
	b.Slots = nil
	b.VoteSlots = []Clock{{12, 0}}
	b.Vote = func(context.Context, time.Time) (*VoteState, error) {
		return &VoteState{Slug: "presidentielle-2027", MaxChoices: 3}, nil
	}
	require.NoError(t, b.Tick(ctx))
	assert.Contains(t, tg.last("sendMessage")["text"], "Pour d'autres versions")
	var msg int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT telegram_message_id FROM x_posts WHERE id = 1`).Scan(&msg))

	w := b.Writer.(*fakeWriter)
	w.reply = Reply{Standard: "2027 se prépare : votez, anonymement.", Satirical: "Votez, @toutlemonde !"}
	require.NoError(t, b.Handle(ctx, Update{ReplyTo: msg, Text: "plus court"}))
	assert.Contains(t, w.prompt, "plus court")
	assert.Contains(t, w.prompt, "Classez jusqu'à 3 candidats", "what may be said of the vote")
	assert.Contains(t, w.prompt, VoteAngles[0].Brief, "the rewrite keeps the angle")
	var variants []string
	var revised int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT variants, telegram_message_id FROM x_posts WHERE id = 1`).Scan(&variants, &revised))
	require.Equal(t, []string{"2027 se prépare : votez, anonymement.\n\n@lemondefr\nhttps://tartuffe.me/election/presidentielle-2027"}, variants, "the version with a mention is dropped")
	assert.NotEqual(t, msg, revised)
	assert.Equal(t, "draft", postStatus(t, pool, 1))

	require.NoError(t, b.Handle(ctx, Update{ReplyTo: revised, Text: "= Mon appel au vote."}))
	assert.Equal(t, "approved", postStatus(t, pool, 1))
	b.Now = func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, b.Zone) }
	require.NoError(t, b.Tick(ctx))
	require.Len(t, poster.posted, 1)
	assert.Equal(t, "Mon appel au vote.\n\n@lemondefr", poster.posted[0], "the tags are added to a text without any")
}

// TestBotVoteTweet: a vote slot drafts the call to vote, a tap publishes it
// at its slot; it quotes nothing and tags the approved outlet; without an open vote the
// slot is left. A contradiction whose fact is the person's own tweet is
// posted as a quote of it.
func TestBotVoteTweet(t *testing.T) {
	b, tg, poster, pool := testBot(t)
	ctx := context.Background()
	start := b.Now() // 11:30
	b.VoteSlots = []Clock{{9, 0}, {16, 0}}
	open := true
	b.Vote = func(context.Context, time.Time) (*VoteState, error) {
		if !open {
			return nil, nil
		}
		return &VoteState{Slug: "presidentielle-2027", MaxChoices: 3, MinVoters: 50, Counted: 301}, nil
	}
	b.Quote = true
	_, err := pool.Exec(ctx, `UPDATE fact_sources SET source_url = 'https://x.com/jdupont/status/555' WHERE fact_id IN (2, 4)`)
	require.NoError(t, err)

	// 11:30: the noon contradiction draft only.
	require.NoError(t, b.Tick(ctx))
	require.NoError(t, b.Handle(ctx, Update{CallbackID: "c1", Data: "p:1:1", MessageID: 1}))
	b.Now = func() time.Time { return start.Add(31 * time.Minute) }
	require.NoError(t, b.Tick(ctx))
	require.Len(t, poster.posted, 1)
	assert.Equal(t, "555", poster.quoted[0], "the person's own tweet is quoted")

	// 15:10: the 16:00 vote draft.
	b.Now = func() time.Time { return start.Add(3*time.Hour + 40*time.Minute) }
	require.NoError(t, b.Tick(ctx))
	msg := tg.last("sendMessage")
	assert.Contains(t, msg["text"], "Tweet du vote")
	assert.Contains(t, msg["text"], "301 bulletins")
	assert.Contains(t, msg["text"], "seuil de 300")
	var kind string
	require.NoError(t, pool.QueryRow(ctx, `SELECT kind FROM x_posts WHERE id = 2`).Scan(&kind))
	assert.Equal(t, "vote", kind)
	require.NoError(t, b.Handle(ctx, Update{CallbackID: "c2", Data: "p:2:1", MessageID: 2}))
	b.Now = func() time.Time { return start.Add(4*time.Hour + 31*time.Minute) }
	require.NoError(t, b.Tick(ctx))
	require.Len(t, poster.posted, 2)
	assert.Contains(t, poster.posted[1], "https://tartuffe.me/election/presidentielle-2027")
	assert.NotContains(t, poster.posted[1], "301", "a tweet never tells the count")
	assert.Contains(t, poster.posted[1], "\n\n@lemondefr\n", "the approved outlet is tagged, nobody else")
	assert.NotContains(t, poster.posted[1], "@jdupont")
	assert.Empty(t, poster.quoted[1])
	assert.Equal(t, "posted", postStatus(t, pool, 2))

	// The next morning, no open vote: no draft for 09:00.
	open = false
	b.Now = func() time.Time { return start.Add(20*time.Hour + 40*time.Minute) }
	require.NoError(t, b.Tick(ctx))
	var votes int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM x_posts WHERE kind = 'vote'`).Scan(&votes))
	assert.Equal(t, 1, votes)
}

// TestBotVoteNeverRepeats: a vote draft skipped never comes back: the next
// slot takes another angle, a version already proposed is dropped, and when
// the writer has nothing new the slot is left with a word to the chat. A
// tweet of ours on a contradiction gives the next vote tweet a quote, once;
// what a candidate said of the vote gives another, once.
func TestBotVoteNeverRepeats(t *testing.T) {
	b, tg, poster, pool := testBot(t)
	ctx := context.Background()
	b.Slots = nil
	b.VoteSlots = []Clock{{12, 0}, {19, 0}}
	b.Vote = func(context.Context, time.Time) (*VoteState, error) {
		return &VoteState{Slug: "presidentielle-2027", MaxChoices: 3}, nil
	}
	w := b.Writer.(*fakeWriter)
	w.reply = Reply{Standard: "Le vote pour 2027 est ouvert.", Satirical: "Et vous, c'est qui votre tartuffe préféré ?"}
	start := b.Now()
	angleOf := func(id int64) (angle, status string) {
		require.NoError(t, pool.QueryRow(ctx, `SELECT angle, status FROM x_posts WHERE id = $1`, id).Scan(&angle, &status))
		return
	}

	require.NoError(t, b.Tick(ctx))
	angle, _ := angleOf(1)
	assert.Equal(t, "tartuffe", angle)
	msg := tg.last("sendMessage")["text"].(string)
	assert.Less(t, strings.Index(msg, "tartuffe préféré ?"), strings.Index(msg, "est ouvert."), "the funny version first")
	require.NoError(t, b.Handle(ctx, Update{CallbackID: "c1", Data: "s:1"}))

	// 18:15: the writer says the same again: nothing is proposed.
	b.Now = func() time.Time { return start.Add(6*time.Hour + 45*time.Minute) }
	require.NoError(t, b.Tick(ctx))
	angle, status := angleOf(2)
	assert.Equal(t, "", angle)
	assert.Equal(t, "skipped", status)
	assert.Contains(t, tg.last("sendMessage")["text"], "Pas de tweet du vote")
	assert.Contains(t, tg.last("sendMessage")["text"], "already proposed")
	require.NoError(t, b.Tick(ctx))
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM x_posts`).Scan(&n))
	assert.Equal(t, 2, n, "the slot is held")

	// The next day: a tweet of ours to quote and a statement on the vote.
	_, err := pool.Exec(ctx, `INSERT INTO x_posts (kind, contradiction_id, person_id, slot, status, chosen, tweet_id, posted_at)
		VALUES ('contradiction', 1, 1, now(), 'posted', 'Jean Dupont, 60 puis 64.' || E'\n\n@jdupont', '777', $1)`, start)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `WITH src AS (INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type, published_at)
		VALUES (3, 'https://www.lemonde.fr/y', 'Il faut rendre la parole au peuple par référendum, sans attendre.', 'test', $1) RETURNING id)
		INSERT INTO fact_mentions (fact_source_id, person_id) SELECT id, 1 FROM src`, start.AddDate(0, 0, -10))
	require.NoError(t, err)
	w.reply = Reply{Standard: "Un de plus pour le Tartuffe-Score.", Satirical: "Votez pour votre tartuffe préféré."}
	b.Now = func() time.Time { return start.Add(24 * time.Hour) }
	require.NoError(t, b.Tick(ctx))
	angle, _ = angleOf(4)
	assert.Equal(t, "quote-tartuffe", angle)
	assert.Contains(t, w.prompt, "Jean Dupont, 60 puis 64.")
	assert.NotContains(t, w.prompt, "@jdupont")
	assert.Contains(t, w.prompt, "Et vous, c'est qui votre tartuffe préféré ?", "the writer is shown what was proposed")
	require.NoError(t, b.Handle(ctx, Update{CallbackID: "c2", Data: "p:4:1"}))
	b.Now = func() time.Time { return start.Add(24*time.Hour + 31*time.Minute) }
	require.NoError(t, b.Tick(ctx))
	require.Len(t, poster.posted, 1)
	assert.Equal(t, "777", poster.quoted[0], "posted as a quote of our tweet")

	w.reply = Reply{Standard: "Jean Dupont : « rendre la parole au peuple ». Ici, le vote est ouvert.", Satirical: "Jean Dupont : « rendre la parole au peuple ». Chiche."}
	b.Now = func() time.Time { return start.Add(30*time.Hour + 45*time.Minute) }
	require.NoError(t, b.Tick(ctx))
	angle, _ = angleOf(5)
	assert.Equal(t, "said", angle, "our tweet is quoted once")
	assert.Contains(t, tg.last("sendMessage")["text"], "jean dupont")
	var fact int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT fact_id FROM x_posts WHERE id = 5`).Scan(&fact))
	assert.Equal(t, int64(3), fact)

	w.reply = Reply{Standard: "Molière en aurait fait cinq actes.", Satirical: "Une campagne, toute une distribution."}
	b.Now = func() time.Time { return start.Add(48 * time.Hour) }
	require.NoError(t, b.Tick(ctx))
	angle, _ = angleOf(6)
	assert.Equal(t, "moliere", angle, "the statement is used once")
}
