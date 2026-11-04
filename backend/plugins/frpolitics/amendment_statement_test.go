package frpolitics

import "testing"

// The ballot of the audit of 2026-11-04 (fact 145151): the quoted exposé
// opened with context and hid that amendment n° 43 reserves the allowances
// to French families.
const ballot43 = "Anne Le Hénanff a voté CONTRE l'amendement n° 43 (RN) dont l'objet est : « Le versement des allocations familiales a pour objectif de consolider la communauté nationale en aidant les familles à assumer au mieux la charge de leurs enfants. Les allocations familiales peuvent … », déposé sur le texte de loi : « Étend le versement des allocations familiales aux familles dès le premier enfant. » (scrutin du 5 juin 2025)"

func TestRewordBallot(t *testing.T) {
	purpose := "(« Réserve les allocations familiales aux familles de nationalité française »)"
	got, ok := rewordBallot(ballot43, purpose)
	want := "Anne Le Hénanff a voté CONTRE l'amendement n° 43 (RN) (« Réserve les allocations familiales aux familles de nationalité française »), déposé sur le texte de loi : « Étend le versement des allocations familiales aux familles dès le premier enfant. » (scrutin du 5 juin 2025)"
	if !ok || got != want {
		t.Fatalf("got %q (%v)", got, ok)
	}
	// "visant à" and "dont le dispositif est" forms, a sous-amendement without group.
	for _, in := range []string{
		"X a voté POUR le sous-amendement n° 372 visant à porter le délai à un an, déposé sur le texte de loi : « Nationalise ArcelorMittal. »",
		"X a voté POUR le sous-amendement n° 372 dont le dispositif est : « À l'alinéa 2, substituer aux mots : « de six mois », les mots : « d'un an ». », déposé sur le texte de loi : « Nationalise ArcelorMittal. »",
	} {
		got, ok := rewordBallot(in, "(« Porte le délai à un an »)")
		if !ok || got != "X a voté POUR le sous-amendement n° 372 (« Porte le délai à un an »), déposé sur le texte de loi : « Nationalise ArcelorMittal. »" {
			t.Errorf("got %q (%v) for %q", got, ok, in)
		}
	}
	// A ballot already stated, or on the text itself, is left alone.
	if _, ok := rewordBallot(want, purpose); ok {
		t.Error("a stated ballot was reworded again")
	}
	if _, ok := rewordBallot("X a voté POUR le texte de loi : « Étend … »", purpose); ok {
		t.Error("a text-level ballot was reworded")
	}
}

func TestParseAmendmentStatement(t *testing.T) {
	s, err := parseAmendmentStatement(`{"reasoning": "The dispositif adds a nationality condition.", "statement": "Réserve les allocations familiales aux familles de nationalité française.", "confidence": 0.9}`)
	if err != nil || s.SimpleStatement != "Réserve les allocations familiales aux familles de nationalité française." || s.Confidence != 0.9 {
		t.Fatalf("got %+v, %v", s, err)
	}
	if _, err := parseAmendmentStatement(`{"reasoning": "x", "statement": " ", "confidence": 0.9}`); err == nil {
		t.Error("an empty statement is an error")
	}
}

// A nil *amendmentStatements keeps the quoted exposé, with no LLM call.
func TestAmendmentPurposeNilQuotesExpose(t *testing.T) {
	var a *amendmentStatements
	p, stated, err := a.purpose(t.Context(), Law{}, &AmendementInfo{Expose: "Cet amendement vise à porter le délai à un an."})
	if err != nil || stated || p != "visant à porter le délai à un an" {
		t.Fatalf("got %q %v %v", p, stated, err)
	}
}
