package viepublique

import (
	"os"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend/plugins"
	"digital-mirror/backend/util"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

var nunezInterview = Speech{
	ID: "263000054", Title: "Interview de M. Laurent Nunez, ministre de l'intérieur, à France Inter le 30 décembre 2025",
	URL: "https://www.vie-publique.fr/discours/301563-laurent-nunez-30122025-france-inter", Type: "Interview", Media: "France Inter",
	Spoken: day("2025-12-30"), Online: day("2026-01-09"),
	Speakers: []Speaker{{Name: "Laurent Nunez", Role: "Ministre de l'intérieur"}, {Name: "Simon Le Baron", Role: "Journaliste"}},
}

// TestParsePage reads the turns of an interview page (archived copy of
// vie-publique.fr) and keeps the minister's answers with the questions.
func TestParsePage(t *testing.T) {
	f, err := os.Open("testdata/nunez_france_inter.html")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	turns, err := parsePage(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) < 20 {
		t.Fatalf("%d turns, want an interview", len(turns))
	}
	if turns[0].Label != "SIMON LE BARON" || turns[1].Label != "LAURENT NUNEZ" || turns[1].Text != "Bonjour," {
		t.Errorf("first turns: %+v %+v", turns[0], turns[1])
	}
	for _, tr := range turns {
		if tr.Label != "SIMON LE BARON" && tr.Label != "LAURENT NUNEZ" {
			t.Errorf("unexpected label %q (%s)", tr.Label, util.TruncateString(tr.Text, 60))
		}
	}

	ex := ownTurns(turns, nunezInterview, "Laurent Nuñez")
	if len(ex) == 0 || len(ex) != countLabel(turns, "LAURENT NUNEZ") {
		t.Fatalf("%d own turns, want %d", len(ex), countLabel(turns, "LAURENT NUNEZ"))
	}
	if ex[1].Question.Label != "SIMON LE BARON" || !strings.Contains(ex[1].Answer.Text, "Bamako") {
		t.Errorf("second exchange: %+v", ex[1])
	}
	out := render(nunezInterview, "Laurent Nuñez", ex)
	for _, want := range []string{"Interview de Laurent Nuñez (Ministre de l'intérieur), France Inter, le 2025-12-30.", attribution, "Question (SIMON LE BARON) :", "LAURENT NUNEZ : Je vais vous expliquer"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered text lacks %q", want)
		}
	}
}

func countLabel(turns []Turn, label string) int {
	n := 0
	for _, t := range turns {
		if t.Label == label {
			n++
		}
	}
	return n
}

// TestOwnTurns: a text without labels is the person's when the person is its
// only speaker besides journalists; an unknown label (LE PRÉSIDENT) too, but
// not in a text with another member of the executive.
func TestOwnTurns(t *testing.T) {
	solo := Speech{Speakers: []Speaker{{Name: "Emmanuel Macron", Role: "Président de la République"}, {Name: "Anne-Claire Coudray", Role: "Journaliste"}}}
	plain := []Turn{{Text: "Mes chers compatriotes, ..."}}
	if got := ownTurns(plain, solo, "Emmanuel Macron"); len(got) != 1 {
		t.Errorf("speech without labels: %d own turns, want 1", len(got))
	}
	labelled := []Turn{{Label: "ANNE-CLAIRE COUDRAY", Text: "Allez-vous dissoudre ?"}, {Label: "LE PRÉSIDENT", Text: "Non."}, {Label: "QUESTION", Text: "Et la retraite ?"}, {Label: "LE PRÉSIDENT", Text: "Nous tiendrons."}}
	got := ownTurns(labelled, solo, "Emmanuel Macron")
	if len(got) != 2 || got[0].Question.Text != "Allez-vous dissoudre ?" || got[1].Question.Label != "QUESTION" {
		t.Errorf("interview with unknown labels: %+v", got)
	}

	duo := Speech{Speakers: []Speaker{{Name: "Laurent Nunez", Role: "Ministre"}, {Name: "Gérald Darmanin", Role: "Ministre"}}}
	turns := []Turn{{Label: "M. GERALD DARMANIN", Text: "La justice suivra."}, {Label: "LAURENT NUÑEZ", Text: "La police aussi."}, {Label: "LE MODÉRATEUR", Text: "Merci."}}
	got = ownTurns(turns, duo, "Laurent Nunez")
	if len(got) != 1 || got[0].Answer.Text != "La police aussi." || got[0].Question.Text != "La justice suivra." {
		t.Errorf("press conference of two ministers: %+v", got)
	}
	if got := ownTurns(plain, duo, "Laurent Nunez"); len(got) != 0 {
		t.Errorf("unlabelled text of two ministers: %d own turns, want 0", len(got))
	}
}

// TestSpeechesOf keeps the speeches of the tracked persons given or put
// online in the window, without the tracking query of the URL, and never
// for a tracked journalist.
func TestSpeechesOf(t *testing.T) {
	index := `[
	{"id": "1", "titre": "Interview de M. Laurent Nunez", "url": "https://www.vie-publique.fr/discours/1-a?egn-publisher=dila_vp", "type_document": "Interview", "media": "France Inter", "prononciation": "2025-12-30", "mise_en_ligne": "2026-01-09",
	 "intervenants": [{"nom": "Laurent Nunez", "qualite": "Ministre de l'intérieur"}, {"nom": "Simon Le Baron", "qualite": "Journaliste"}], "resume": null},
	{"id": "2", "titre": "Déclaration de M. Laurent Nuñez", "url": "https://www.vie-publique.fr/discours/2-b", "type_document": "Déclaration", "media": null, "prononciation": "2025-06-01", "mise_en_ligne": "2025-06-03",
	 "intervenants": [{"nom": "Laurent Nuñez", "qualite": "Préfet de police"}]},
	{"id": "3", "titre": "Interview de Mme Rachida Dati", "url": "https://www.vie-publique.fr/discours/3-c", "type_document": "Interview", "prononciation": "2026-01-08", "mise_en_ligne": "2026-01-09",
	 "intervenants": [{"nom": "Rachida Dati", "qualite": "Ministre de la culture"}]}
	]`
	key := util.PersonNameKey("Laurent Nuñez")
	keys := map[string]bool{key: true, util.PersonNameKey("Simon Le Baron"): true}

	got, err := SpeechesOf(strings.NewReader(index), keys, day("2026-01-09"), day("2026-01-10"))
	if err != nil {
		t.Fatal(err)
	}
	// Simon Le Baron is tracked, but asks the questions.
	if len(got) != 1 || len(got[key]) != 1 {
		t.Fatalf("window of the day it went online: %+v", got)
	}
	s := got[key][0]
	if s.ID != "1" || s.URL != "https://www.vie-publique.fr/discours/1-a" || !s.Spoken.Equal(day("2025-12-30")) || len(s.Speakers) != 2 || !s.Speakers[1].IsJournalist() {
		t.Errorf("speech: %+v", s)
	}

	got, err = SpeechesOf(strings.NewReader(index), keys, day("2024-10-01"), day("2026-01-01"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got[key]) != 2 {
		t.Errorf("2-year window: %d speeches, want 2", len(got[key]))
	}
}

// TestDocument: the person's part of a speech is stored under the speech and
// the person; a text where the person says too little is stored empty, so
// it is not fetched again and costs no LLM call.
func TestDocument(t *testing.T) {
	target := plugins.Target{PersonID: 42, Name: "Laurent Nuñez"}
	doc := document(nunezInterview, target, []Turn{{Label: "SIMON LE BARON", Text: "Bonjour"}, {Label: "LAURENT NUNEZ", Text: "Bonjour,"}})
	if doc.URL != "viepublique:263000054/42" || doc.Content != "" || !doc.PublishedAt.Equal(day("2025-12-30")) {
		t.Errorf("greeting only: %+v", doc)
	}
	if doc.Metadata["source_url"] != nunezInterview.URL {
		t.Errorf("source url: %v", doc.Metadata["source_url"])
	}
	long := strings.Repeat("Nous renforcerons les contrôles aux frontières. ", 6)
	doc = document(nunezInterview, target, []Turn{{Label: "SIMON LE BARON", Text: "Que ferez-vous ?"}, {Label: "LAURENT NUNEZ", Text: long}})
	if !strings.Contains(doc.Content, "Question (SIMON LE BARON) : Que ferez-vous ?") {
		t.Errorf("content: %s", doc.Content)
	}
}
