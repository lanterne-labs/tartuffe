package xbot

import (
	"context"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func contradiction() *storage.BotContradiction {
	d1 := time.Date(2022, 3, 1, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	return &storage.BotContradiction{
		ID: 7, PersonID: 3, PersonName: "Jean Dupont", StanceLabel: "Retraite à 60 ans", Reason: "Il défendait la retraite à 60 ans, puis vote le report à 64.",
		A: storage.BotFact{ID: 1, Text: "Jean Dupont défend la retraite à 60 ans.", Quote: "Je me battrai pour la retraite à 60 ans, c'est une question de justice.", Date: &d1,
			SourceURLs: []string{"https://www.lemonde.fr/a", "https://twitter.com/x/status/1"}},
		B: storage.BotFact{ID: 2, Text: "Jean Dupont vote pour le report de l'âge légal à 64 ans.", Date: &d2,
			SourceURLs: []string{"https://www.assemblee-nationale.fr/b", "https://lemonde.fr/c"}},
	}
}

func TestDomains(t *testing.T) {
	assert.Equal(t, []string{"lemonde.fr", "twitter.com", "assemblee-nationale.fr"}, Domains(contradiction()))
}

func TestChooseTags(t *testing.T) {
	pool := &storage.BotTagPool{
		Person:  []string{"jdupont", "jdupont_bis"},
		Outlets: []storage.XAccount{{Handle: "lemondefr"}, {Handle: "lemondefr"}, {Handle: "libe"}, {Handle: "figaro"}},
	}
	// The person and two outlets: the cap is reached.
	assert.Equal(t, []string{"jdupont", "lemondefr", "libe"}, ChooseTags(pool, MaxTags))
	// Without outlets, the person alone: nobody fills in.
	pool.Outlets = nil
	assert.Equal(t, []string{"jdupont"}, ChooseTags(pool, MaxTags))
	assert.Empty(t, ChooseTags(&storage.BotTagPool{}, MaxTags))
}

func TestCheck(t *testing.T) {
	c := contradiction()
	for _, tc := range []struct {
		text, why string
	}{
		{"En 2022, Jean Dupont : « Je me battrai pour la retraite à 60 ans ». En 2025 il vote 64 ans. Cohérent.", ""},
		// The quote cut short and with another apostrophe still matches.
		{"« c’est une question de justice » disait Jean Dupont. Trois ans plus tard : 64 ans.", ""},
		{"Jean Dupont : « Je me battrai pour les retraites à 62 ans ».", "quote not in the sources"},
		// Our summary of the fact is not the person's words.
		{"Jean Dupont : « défend la retraite à 60 ans ».", "quote not in the sources"},
		{"Bravo @jdupont pour ce grand écart.", "a mention or a link"},
		{"Voir https://tartuffe.me", "a mention or a link"},
		{strings.Repeat("a", 300), "too long"},
		{"  ", "empty"},
	} {
		why := Check(c, tc.text, 250)
		if tc.why == "" {
			assert.Empty(t, why, tc.text)
		} else {
			assert.Contains(t, why, tc.why, tc.text)
		}
	}
}

func TestLength(t *testing.T) {
	assert.Equal(t, 5, Length("héhé!"))
	assert.Equal(t, len("voir ")+LinkLength, Length("voir https://tartuffe.me/person/1?c=2&very=long"))
	assert.Equal(t, 9, Length("abc\n\n@def"))
}

type fakeWriter struct {
	prompt string
	reply  Reply
}

func (f *fakeWriter) Write(_ context.Context, prompt string) (Reply, error) {
	f.prompt = prompt
	return f.reply, nil
}

func TestCompose(t *testing.T) {
	w := &fakeWriter{reply: Reply{Reasoning: "r",
		Standard:  "Jean Dupont, 2022 : « la retraite à 60 ans ». 2025 : vote pour 64. La justice a pris quatre ans de retard.",
		Satirical: "Jean Dupont : « Je défendrai toujours 60 ans ».",
	}}
	d, err := Compose(context.Background(), w, contradiction(), []string{"jdupont", "lemondefr"}, "https://tartuffe.me/person/3?c=7")
	require.NoError(t, err)
	require.Len(t, d.Versions, 1)
	assert.True(t, strings.HasSuffix(d.Versions[0], "\n\n@jdupont @lemondefr\nhttps://tartuffe.me/person/3?c=7"), d.Versions[0])
	assert.LessOrEqual(t, Length(d.Versions[0]), MaxLength)
	assert.Equal(t, []string{"Sobre"}, d.Labels)
	require.Len(t, d.Dropped, 1)
	assert.Contains(t, d.Dropped[0], "Satirique, quote not in the sources")
	// The room the prompt gives leaves space for the tags and the link.
	assert.Contains(t, w.prompt, "At most 235 characters")
	assert.Contains(t, w.prompt, "Je me battrai pour la retraite à 60 ans")
}

func TestCheckReadsEveryQuote(t *testing.T) {
	c := contradiction()
	c.B.Quotes = []string{"Un premier article.", "J'ai voté le report à 64 ans et je l'assume."}
	assert.Empty(t, Check(c, "Jean Dupont en 2025 : « je l'assume ».", 250))
	assert.Contains(t, Prompt(c, 250), "je l'assume")
}

func TestDatesAndLink(t *testing.T) {
	c := contradiction()
	c.A.DatePrecision = "year"
	c.B.DatePrecision = "month"
	assert.Equal(t, "2022", c.A.DateFR())
	assert.Equal(t, "juin 2025", c.B.DateFR())
	c.B.DatePrecision = ""
	assert.Equal(t, "1er juin 2025", c.B.DateFR())
	assert.Equal(t, "", storage.BotFact{}.DateFR())
	// The prompt gives the year alone, never the day of the article.
	c.B.Date = nil
	p := Prompt(c, 250)
	assert.Contains(t, p, "Statement (2022)")
	assert.Contains(t, p, "unknown date")
	assert.NotContains(t, p, "March")

	assert.Equal(t, "https://tartuffe.me/person/3?c=7", ContradictionLink("https://tartuffe.me/", c))
	c.PersonSlug = "jean-dupont"
	assert.Equal(t, "https://tartuffe.me/person/jean-dupont?c=7", ContradictionLink("https://tartuffe.me/", c))
}

func TestQuoteTarget(t *testing.T) {
	c := contradiction()
	// The source of fact A is a tweet, but of nobody we know as the person.
	id, _ := QuoteTarget(c, []string{"jdupont"})
	assert.Empty(t, id, "a tweet of another account is never quoted")
	c.A.SourceURLs = []string{"https://www.lemonde.fr/a", "https://twitter.com/JDupont/status/111?s=20"}
	id, link := QuoteTarget(c, []string{"jdupont"})
	assert.Equal(t, "111", id)
	assert.Equal(t, "https://x.com/JDupont/status/111", link)
	// The later fact goes first; a profile link is not a tweet.
	c.B.SourceURLs = []string{"https://x.com/jdupont", "https://x.com/jdupont/status/222"}
	id, _ = QuoteTarget(c, []string{"jdupont"})
	assert.Equal(t, "222", id)
	id, _ = QuoteTarget(c, nil)
	assert.Empty(t, id)
}

func TestMemory(t *testing.T) {
	m := Remember([]string{
		"Pour voter ici, il suffit de votre passeport et de votre téléphone.\n\n@lemondefr @libe\nhttps://tartuffe.me/election/presidentielle-2027",
		"Et vous, c'est qui votre tartuffe préféré ?",
	})
	// The same text, whatever its tags, link, case and punctuation.
	assert.Equal(t, "already proposed", m.Seen("pour voter ici il suffit de votre passeport, et de votre téléphone !\n\n@anticor_org"))
	// A close variant.
	assert.Contains(t, m.Seen("Pour voter ici, il suffit de votre passeport et de votre téléphone. Rien d'autre."), "too close")
	assert.Empty(t, m.Seen("Un vote où personne ne peut savoir qui a voté quoi."))
	assert.Equal(t, []string{"Pour voter ici, il suffit de votre passeport et de votre téléphone."}, m.Recent(1))
	var none *Memory
	assert.Empty(t, none.Seen("x"))
	assert.Equal(t, "Deux\nlignes", Body("Deux\nlignes\n\n@a @b\nhttps://x.y/z"))
}

func TestComposeDropsSeen(t *testing.T) {
	w := &fakeWriter{reply: Reply{Standard: "Jean Dupont, en 2022 puis en 2025 : deux avis.", Satirical: "Jean Dupont, toujours constant."}}
	d, err := Recompose(context.Background(), w, contradiction(), []string{"jdupont"}, "", nil, Remember([]string{"Jean Dupont, toujours constant.\n\n@jdupont @libe"}))
	require.NoError(t, err)
	assert.Equal(t, []string{"Sobre"}, d.Labels)
	require.Len(t, d.Dropped, 1)
	assert.Contains(t, d.Dropped[0], "already proposed")
}

func TestVoteAngles(t *testing.T) {
	v := &VoteState{Slug: "presidentielle-2027", MaxChoices: 3}
	monday := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	keys := func(as []VoteAngle) (out []string) {
		for _, a := range as {
			out = append(out, a.Key)
		}
		return
	}
	seen := map[string]bool{}
	for _, a := range VoteAngles {
		assert.False(t, seen[a.Key], a.Key)
		seen[a.Key] = true
	}
	plain := keys(PickVoteAngles(nil, v, monday, false, false))
	assert.Equal(t, "tartuffe", plain[0], "the playful ones first")
	for _, k := range plain {
		assert.NotContains(t, k, "quote-")
		assert.NotEqual(t, "said", k)
		assert.NotEqual(t, "sunday", k)
	}
	all := keys(PickVoteAngles(nil, v, monday.AddDate(0, 0, 6), true, true))
	assert.Len(t, all, len(VoteAngles))
	// The angle just used goes last.
	used := map[string]time.Time{"tartuffe": monday}
	next := keys(PickVoteAngles(used, v, monday, false, false))
	assert.Equal(t, "tartuffe", next[len(next)-1])
	assert.Equal(t, plain[1], next[0])
}

func TestComposeVote(t *testing.T) {
	v := &VoteState{Slug: "presidentielle-2027", MaxChoices: 3, MinVoters: 50, Counted: 412}
	w := &fakeWriter{reply: Reply{Standard: "Déjà 412 bulletins : votez pour 2027.", Satirical: "Classez jusqu'à 3 candidats pour 2027, du moins tartuffe au plus tartuffe."}}
	req := VoteRequest{State: v, Angle: VoteAngles[0], Site: "https://tartuffe.me/", Tags: []string{"libe"}, Seen: Remember([]string{"Un ancien tweet du vote."})}
	versions, dropped, err := ComposeVote(context.Background(), w, req)
	require.NoError(t, err)
	require.Equal(t, []string{"Classez jusqu'à 3 candidats pour 2027, du moins tartuffe au plus tartuffe.\n\n@libe\nhttps://tartuffe.me/election/presidentielle-2027"}, versions)
	require.Len(t, dropped, 1)
	assert.Contains(t, dropped[0], "a number (412)")
	assert.Contains(t, w.prompt, "Never give a number of votes")
	assert.Contains(t, w.prompt, "Un ancien tweet du vote.")
	assert.Contains(t, w.prompt, VoteAngles[0].Brief)
	assert.NotContains(t, w.prompt, "412", "the writer never learns the count")

	// A candidate taken at their word: only their own words between « ».
	d := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	req.Said = &storage.VoteFact{PersonName: "Jean Dupont", BotFact: storage.BotFact{Text: "Jean Dupont veut un référendum.", Quote: "Il faut rendre la parole au peuple par référendum.", Date: &d}}
	w.reply = Reply{Standard: "Jean Dupont : « rendre la parole au peuple ». Chiche : le vote est ouvert.", Satirical: "Jean Dupont : « le peuple doit voter chaque dimanche ». Ça tombe bien."}
	versions, dropped, err = ComposeVote(context.Background(), w, req)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	assert.Contains(t, versions[0], "Chiche")
	assert.Contains(t, dropped[0], "quote not in the source")
	assert.Contains(t, w.prompt, "Il faut rendre la parole au peuple par référendum.")
}
