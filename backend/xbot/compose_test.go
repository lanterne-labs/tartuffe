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
