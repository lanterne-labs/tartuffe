package frpolitics

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend"
	"digital-mirror/backend/core"
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
	require.Equal(t, "RUANR5L17S2026IDS30722", cr.SeanceRef, "seanceRef is the pairing key with scrutins")
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
// The processing order handed to the document engine: newest séance first
// when asked, oldest otherwise.
func TestDebateDocumentOrder(t *testing.T) {
	day := func(d int) *time.Time { t := time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC); return &t }
	docs := []core.SourceDocument{
		{URL: "A", PublishedAt: day(1)}, {URL: "B", PublishedAt: day(3)}, {URL: "C", PublishedAt: day(2)}, {URL: "D", PublishedAt: day(4)},
	}
	order := func(opts DebatsOptions, priority map[int64]int) []string {
		sorted := append([]core.SourceDocument(nil), docs...)
		less := debateDocumentOrder(opts, priority)
		sort.SliceStable(sorted, func(i, j int) bool { return less(sorted[i], sorted[j]) })
		out := make([]string, len(sorted))
		for i, d := range sorted {
			out[i] = d.URL
		}
		return out
	}
	require.Equal(t, []string{"D", "B", "C", "A"}, order(DebatsOptions{NewestFirst: true}, nil))
	require.Equal(t, []string{"A", "C", "B", "D"}, order(DebatsOptions{}, nil))
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
	require.True(t, subjectMatches(fakeFact("Mathilde Panot"), "Mathilde Panot", "mathilde panot"))
	require.True(t, subjectMatches(fakeFact("mathilde PANOT"), "Mathilde Panot", "mathilde panot"))
	require.False(t, subjectMatches(fakeFact("Sébastien Lecornu"), "Mathilde Panot", "mathilde panot"), "facts about a third party are dropped")
	require.False(t, subjectMatches(fakeFact(""), "Mathilde Panot", "mathilde panot"))
}

// Backfill priority: speakers with press facts first (most first), then the
// date order; speakers without press facts come last.
func TestDebateDocumentOrder_PrioritizePress(t *testing.T) {
	day := func(d int) *time.Time { t := time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC); return &t }
	docs := []core.SourceDocument{
		{URL: "A", PublishedAt: day(5), PersonID: 1}, {URL: "B", PublishedAt: day(9), PersonID: 2}, {URL: "C", PublishedAt: day(3), PersonID: 3},
		{URL: "D", PublishedAt: day(7), PersonID: 2}, {URL: "E", PublishedAt: day(8), PersonID: 1},
	}
	priority := map[int64]int{1: 40, 2: 300} // person 3 has no press facts
	less := debateDocumentOrder(DebatsOptions{NewestFirst: true, PrioritizePress: true}, priority)
	sort.SliceStable(docs, func(i, j int) bool { return less(docs[i], docs[j]) })
	seen := make([]string, len(docs))
	for i, d := range docs {
		seen[i] = d.URL
	}
	require.Equal(t, []string{"B", "D", "E", "A", "C"}, seen, "press-heavy speaker first, newest first within a speaker, no-press speaker last")
}

// A (speaker, séance) document round-trips through its stored form: the
// provenance the facts need comes back from the metadata.
func TestDebateDocumentStoredForm(t *testing.T) {
	cr := &compteRendu{UID: "CRSANR5L17S2026O1N001", SeanceRef: "RUANR5L17S2026IDS28001", Legislature: 17,
		SeanceDate: time.Date(2026, 1, 26, 0, 0, 0, 0, time.UTC), DateSeanceJour: "lundi 26 janvier 2026", NumSeanceJour: "1",
		Points: map[string]odjPoint{"p1": {Title: "Budget", TexteNum: "2341"}}}
	doc := &debateDocument{
		CR: cr, ActorID: "PA1", Person: storage.PersonInfo{ID: 42, Name: "mathilde panot"}, Speaker: "Mathilde Panot",
		Paragraphs: []crParagraph{{IDSyceron: "39", PtsODJ: "p1", Article: "1er", Amendement: "109", Text: "Nous demandons le retrait."}},
		SourceURL:  "https://www.assemblee-nationale.fr/dyn/17/comptes-rendus/seance/x/y#39", Chars: 27,
	}
	stored := doc.toSourceDocument()
	require.Equal(t, debatsSourceType, stored.SourceType)
	require.Equal(t, "debats:CRSANR5L17S2026O1N001/PA1", stored.URL, "identity, not the permalink: stable when a republished séance moves the anchor")
	require.EqualValues(t, 42, stored.PersonID)
	require.Equal(t, cr.SeanceDate, *stored.PublishedAt)
	require.Contains(t, stored.Content, "Nous demandons le retrait.")

	// Metadata survives a JSON round-trip (what the database hands back).
	raw, err := json.Marshal(stored.Metadata)
	require.NoError(t, err)
	var back map[string]any
	require.NoError(t, json.Unmarshal(raw, &back))
	stored.Metadata = back
	meta := debateMeta(stored)
	require.Equal(t, doc.SourceURL, meta.sourceURL, "the permalink is the facts' source")
	require.Equal(t, "CRSANR5L17S2026O1N001", meta.crUID)
	require.Equal(t, "PA1", meta.actorID)
	require.Equal(t, "Mathilde Panot", meta.speaker)
	require.Equal(t, "mathilde panot", meta.personName)
	require.Equal(t, []string{"39"}, meta.paragraphIDs)
	require.Equal(t, []string{"2341"}, meta.textes)
	require.Equal(t, []string{"1er"}, meta.articles)
	require.Equal(t, []string{"109"}, meta.amendements)
}
