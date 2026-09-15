package frpolitics

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/require"
)

func fakeFact(subject string) backend.Fact { return backend.Fact{SubjectEntity: subject} }

// The fixture is a real QAG séance (2026-06-17, CRSANR5L17S2026O1N274)
// structurally trimmed to its first 60 paragraphs: the chair, three tracked
// deputies (Magnier, Cazeneuve, Panot) and three government members who are
// not tracked (Nuñez, Farandou, Lecornu).
func loadDebatsFixture(t *testing.T) *compteRendu {
	t.Helper()
	f, err := os.Open("testdata/debats/CRSANR5L17S2026O1N274.trimmed.xml")
	require.NoError(t, err)
	defer f.Close()
	cr, err := parseCompteRendu(f)
	require.NoError(t, err)
	return cr
}

func TestParseCompteRendu_MetadataPointsParagraphs(t *testing.T) {
	cr := loadDebatsFixture(t)
	require.Equal(t, "CRSANR5L17S2026O1N274", cr.UID)
	require.Equal(t, 17, cr.Legislature)
	require.Equal(t, time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC), cr.SeanceDate)
	require.Equal(t, "mercredi 17 juin 2026", cr.DateSeanceJour)
	require.Equal(t, "1", cr.NumSeanceJour)

	// Ordre-du-jour points: QAG has no texte number; the next ones do.
	require.Equal(t, "Questions au gouvernement", cr.Points["1"].Title)
	require.Equal(t, "", cr.Points["1"].TexteNum)
	require.Equal(t, "2700", cr.Points["2"].TexteNum)
	require.Contains(t, cr.Points["2"].Title, "Accès au logement")

	require.Len(t, cr.Paragraphs, 60)
	var panot *crParagraph
	for i := range cr.Paragraphs {
		if cr.Paragraphs[i].IDSyceron == "4124064" {
			panot = &cr.Paragraphs[i]
		}
	}
	require.NotNil(t, panot, "Panot's QAG paragraph must be parsed")
	require.Equal(t, "PA720892", panot.ActorID)
	require.Equal(t, "Mme Mathilde Panot", panot.SpeakerName)
	require.Equal(t, "PAROLE_GENERIQUE", panot.CodeGram)
	require.Equal(t, "1", panot.PtsODJ)
	require.Greater(t, len(panot.Text), 2000)
	require.NotContains(t, panot.Text, "\n", "inline markup and line breaks are flattened")
	require.Contains(t, panot.Text, "pas une de plus")

	// The chair is tagged with roledebat and must be recognizable.
	var chair int
	for _, p := range cr.Paragraphs {
		if p.RoleDebat == "president" {
			chair++
		}
	}
	require.Greater(t, chair, 0)
}

func TestGroupDocuments_TrackedSpeakersOnly(t *testing.T) {
	cr := loadDebatsFixture(t)
	persons := map[string]storage.PersonInfo{
		"PA720892": {ID: 1, Name: "mathilde panot"},
		"PA795864": {ID: 2, Name: "pierre cazeneuve"},
		"PA841587": {ID: 3, Name: "david magnier"},
		// Lecornu is deliberately NOT tracked here.
	}
	docs := groupDocuments(cr, persons, debatsMinInterventionChars)
	byActor := map[string]debateDocument{}
	for _, d := range docs {
		byActor[d.ActorID] = d
	}
	require.Len(t, docs, 3, "one document per tracked speaker with substantive paragraphs")
	require.NotContains(t, byActor, "PA643210", "untracked government member is skipped")

	panot := byActor["PA720892"]
	require.Equal(t, "Mathilde Panot", panot.Speaker, "civility stripped")
	require.Equal(t, int64(1), panot.Person.ID)
	require.Len(t, panot.Paragraphs, 1)
	require.Equal(t,
		"https://www.assemblee-nationale.fr/dyn/17/comptes-rendus/seance/session-ordinaire-de-2025-2026/premiere-seance-du-mercredi-17-juin-2026#4124064",
		panot.SourceURL)

	// Cazeneuve intervened four times (all >= 300 chars): one document, four
	// paragraphs, anchored on the first one; the document text carries them all.
	caz := byActor["PA795864"]
	require.Len(t, caz.Paragraphs, 4)
	require.True(t, strings.HasSuffix(caz.SourceURL, "#4124088"), caz.SourceURL)
	body := renderDebateDocument(&caz)
	require.True(t, strings.HasPrefix(body, "Intervention de Pierre Cazeneuve en séance publique à l'Assemblée nationale, mercredi 17 juin 2026 (première séance)."), body[:160])
	require.Contains(t, body, "Débat : Questions au gouvernement.")
	for _, p := range caz.Paragraphs {
		require.Contains(t, body, p.Text[:60])
	}

	// Raising the threshold above Cazeneuve's longest paragraph drops him.
	docs = groupDocuments(cr, persons, 1000)
	for _, d := range docs {
		require.NotEqual(t, "PA795864", d.ActorID)
	}
}

func TestRenderDebateDocument_TexteAndAmendementHeader(t *testing.T) {
	cr := &compteRendu{
		UID: "CRSANR5L17S2026O1N126", Legislature: 17,
		SeanceDate: time.Date(2026, 1, 26, 0, 0, 0, 0, time.UTC), DateSeanceJour: "lundi 26 janvier 2026", NumSeanceJour: "2",
		Points: map[string]odjPoint{"1": {Title: "Protéger les mineurs des risques auxquels les expose l'utilisation des réseaux sociaux", TexteNum: "2341"}},
	}
	doc := &debateDocument{CR: cr, Speaker: "Laure Miller", Paragraphs: []crParagraph{
		{IDSyceron: "1", PtsODJ: "1", Article: " 1er ", Amendement: " 109", Qualite: "rapporteure", Text: "Avis défavorable : l'amendement vide l'article de sa portée."},
		{IDSyceron: "2", PtsODJ: "1", Article: " 1er ", Amendement: " 110", Text: "Même avis."},
	}}
	body := renderDebateDocument(doc)
	require.Contains(t, body, "Intervention de Laure Miller (rapporteure) en séance publique à l'Assemblée nationale, lundi 26 janvier 2026 (deuxième séance).")
	require.Contains(t, body, "Débat : Protéger les mineurs des risques auxquels les expose l'utilisation des réseaux sociaux (texte n° 2341).")
	// Each change of article/amendement is a marker line BEFORE its paragraph,
	// so the second opinion is attributed to amendment 110, not 109.
	i109 := strings.Index(body, "[article 1er, amendement n° 109]")
	iAvis := strings.Index(body, "Avis défavorable")
	i110 := strings.Index(body, "[article 1er, amendement n° 110]")
	iMeme := strings.Index(body, "Même avis.")
	require.True(t, i109 >= 0 && iAvis > i109 && i110 > iAvis && iMeme > i110, body)
}

// The scheduling loop is exercised with a fake ingester: ordering, the
// MaxDocs cap (attempts, not successes), the deadline, failed documents
// staying pending, the consecutive-failure breaker and the all-failed error.
func TestRunDebateDocuments_Scheduling(t *testing.T) {
	mk := func(uid string, day int) *debateDocument {
		return &debateDocument{CR: &compteRendu{UID: uid, SeanceDate: time.Date(2026, 6, day, 0, 0, 0, 0, time.UTC)}, ActorID: "PA1"}
	}
	docs := func() []*debateDocument {
		return []*debateDocument{mk("A", 1), mk("B", 3), mk("C", 2), mk("D", 4)}
	}

	// Newest first, MaxDocs=2 attempts, one failure: order D,B; B fails and stays pending.
	var seen []string
	stats := DebatsStats{Pending: 4}
	err := runDebateDocuments(context.Background(), docs(), DebatsOptions{NewestFirst: true, MaxDocs: 2},
		func(_ context.Context, d *debateDocument) (docResult, error) {
			seen = append(seen, d.CR.UID)
			if d.CR.UID == "B" {
				return docResult{Inserted: 1}, errors.New("boom")
			}
			return docResult{Inserted: 2, Skipped: 1, DroppedSubject: 1}, nil
		}, &stats)
	require.NoError(t, err)
	require.Equal(t, []string{"D", "B"}, seen)
	require.Equal(t, 2, stats.Attempted)
	require.Equal(t, 1, stats.Processed)
	require.Equal(t, 1, stats.Errors)
	require.Equal(t, 2, stats.Facts)
	require.Equal(t, 1, stats.FactsSkipped)
	require.Equal(t, 1, stats.FactsDroppedSubject)
	require.Equal(t, 3, stats.PendingAfter, "the failed document is still pending")

	// Oldest first with a deadline already passed: nothing attempted, flagged.
	stats = DebatsStats{Pending: 4}
	seen = nil
	err = runDebateDocuments(context.Background(), docs(), DebatsOptions{Deadline: time.Now().Add(-time.Second)},
		func(_ context.Context, d *debateDocument) (docResult, error) {
			seen = append(seen, d.CR.UID)
			return docResult{}, nil
		}, &stats)
	require.NoError(t, err)
	require.Empty(t, seen)
	require.True(t, stats.DeadlineHit)
	require.Equal(t, 4, stats.PendingAfter)

	// Oldest first, no caps: A, C, B, D; a zero-fact document counts as NoFacts.
	stats = DebatsStats{Pending: 4}
	seen = nil
	err = runDebateDocuments(context.Background(), docs(), DebatsOptions{},
		func(_ context.Context, d *debateDocument) (docResult, error) {
			seen = append(seen, d.CR.UID)
			return docResult{}, nil
		}, &stats)
	require.NoError(t, err)
	require.Equal(t, []string{"A", "C", "B", "D"}, seen)
	require.Equal(t, 4, stats.NoFacts)
	require.Equal(t, 0, stats.PendingAfter)

	// Every document fails: run reported as an error (breaker after 10, or
	// all-failed on a short list).
	stats = DebatsStats{Pending: 4}
	err = runDebateDocuments(context.Background(), docs(), DebatsOptions{},
		func(_ context.Context, d *debateDocument) (docResult, error) {
			return docResult{}, errors.New("outage")
		}, &stats)
	require.Error(t, err)
	require.Equal(t, 4, stats.Errors)
	require.Equal(t, 4, stats.PendingAfter)

	// Outage on a long backlog: stops after debatsMaxConsecutiveFailures.
	long := make([]*debateDocument, 0, 30)
	for i := 1; i <= 30; i++ {
		long = append(long, mk(fmt.Sprintf("L%02d", i), 1))
	}
	stats = DebatsStats{Pending: 30}
	err = runDebateDocuments(context.Background(), long, DebatsOptions{},
		func(_ context.Context, d *debateDocument) (docResult, error) {
			return docResult{}, errors.New("outage")
		}, &stats)
	require.Error(t, err)
	require.Equal(t, debatsMaxConsecutiveFailures, stats.Attempted)
}

// Permalink slugs, verified against the live site for every session kind of
// the 17th legislature (2026-09-14).
func TestCompteRenduPermalink(t *testing.T) {
	cases := []struct {
		uid, num, day, want string
	}{
		{"CRSANR5L17S2026O1N126", "2", "lundi 26 janvier 2026", "session-ordinaire-de-2025-2026/deuxieme-seance-du-lundi-26-janvier-2026"},
		{"CRSANR5L17S2025O1N037", "1", "mercredi 06 novembre 2024", "session-ordinaire-de-2024-2025/premiere-seance-du-mercredi-06-novembre-2024"},
		{"CRSANR5L17S2026O1N168", "1", "mercredi 25 février 2026", "session-ordinaire-de-2025-2026/premiere-seance-du-mercredi-25-fevrier-2026"},
		{"CRSANR5L17S2026E1N024", "2", "mardi 21 juillet 2026", "session-extraordinaire-de-2025-2026/deuxieme-seance-du-mardi-21-juillet-2026"},
		{"CRSANR5L17S2025E2N001", "Unique", "lundi 08 septembre 2025", "2e-session-extraordinaire-de-2024-2025/seance-du-lundi-08-septembre-2025"},
		{"CRSANR5L17S2024D1N001", "Unique", "jeudi 18 juillet 2024", "session-de-droit-de-2024/seance-du-jeudi-18-juillet-2024"},
		{"CRSANR5L17S2024D1N002", "3", "vendredi 19 juillet 2024", "session-de-droit-de-2024/troisieme-seance-du-vendredi-19-juillet-2024"},
	}
	for _, c := range cases {
		cr := &compteRendu{UID: c.uid, Legislature: 17, NumSeanceJour: c.num, DateSeanceJour: c.day}
		got := compteRenduPermalink(cr, "42")
		require.Equal(t, "https://www.assemblee-nationale.fr/dyn/17/comptes-rendus/seance/"+c.want+"#42", got, c.uid)
	}
}

func TestStripCivilityAndSubjectMatch(t *testing.T) {
	require.Equal(t, "Mathilde Panot", stripCivility("Mme Mathilde Panot"))
	require.Equal(t, "Thibault Bazin", stripCivility("M. Thibault Bazin"))
	require.Equal(t, "la présidente", stripCivility("Mme la présidente"))
	doc := &debateDocument{Speaker: "Mathilde Panot", Person: storage.PersonInfo{Name: "mathilde panot"}}
	require.True(t, subjectMatches(fakeFact("Mathilde Panot"), doc))
	require.True(t, subjectMatches(fakeFact("mathilde PANOT"), doc))
	require.False(t, subjectMatches(fakeFact("Sébastien Lecornu"), doc), "facts about a third party are dropped")
	require.False(t, subjectMatches(fakeFact(""), doc))
}
