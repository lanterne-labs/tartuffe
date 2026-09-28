package categorizer

import (
	"testing"

	"digital-mirror/backend"
)

// Subject misspellings seen in prod (2026-05..09), each of which had created a
// person of its own.
func TestGroundSubject(t *testing.T) {
	tests := []struct {
		name        string
		subject     string
		factText    string
		source      string
		target      string
		wantSubject string
		wantText    string
		wantChanged bool
	}{
		{
			name:        "misspelling repaired to the source spelling, fact text too",
			subject:     "Édoued Philippe",
			factText:    "Édoued Philippe s'est dit favorable à la dénonciation des accords de 1968 avec l'Algérie.",
			source:      "En direct. Édouard Philippe veut dénoncer les accords de 1968 avec l'Algérie, a-t-il déclaré sur BFMTV.",
			target:      "gerald darmanin",
			wantSubject: "Édouard Philippe",
			wantText:    "Édouard Philippe s'est dit favorable à la dénonciation des accords de 1968 avec l'Algérie.",
			wantChanged: true,
		},
		{
			name:        "name absent from the source repaired to the target",
			subject:     "jean-1uc melenchon",
			factText:    "Jean-1uc Mélenchon a affirmé que le gouverneur Emmanuel Moulin mène une politique d'austérité.",
			source:      "Le gouverneur Emmanuel Moulin mène une politique d'austérité. #LFI",
			target:      "Jean-Luc Mélenchon",
			wantSubject: "Jean-Luc Mélenchon",
			wantText:    "Jean-Luc Mélenchon a affirmé que le gouverneur Emmanuel Moulin mène une politique d'austérité.",
			wantChanged: true,
		},
		{
			name:        "target wins the tie, displayed with the source's spelling",
			subject:     "francois bayro",
			factText:    "François Bayro a déclaré ne jamais s'être intéressé au détail de la gestion.",
			source:      "Quand François Bayrou fait la leçon à la cour d'appel de Paris.",
			target:      "francois bayrou",
			wantSubject: "François Bayrou",
			wantText:    "François Bayrou a déclaré ne jamais s'être intéressé au détail de la gestion.",
			wantChanged: true,
		},
		{
			name:        "same letters, broken separators",
			subject:     "manuel b_ompard",
			factText:    "Manuel B_ompard appelle à la mobilisation.",
			source:      "Manuel Bompard appelle à la mobilisation contre les politiques du gouvernement.",
			wantSubject: "Manuel Bompard",
			wantText:    "Manuel Bompard appelle à la mobilisation.",
			wantChanged: true,
		},
		{
			name:        "literal JSON escape inside the name",
			subject:     "edou\\u00addard philippe",
			factText:    "Édou\\u00addard Philippe a proposé de lutter contre les arrêts de travail injustifiés.",
			source:      "Il faut être très vigilant, estime Édouard Philippe.",
			wantSubject: "Édouard Philippe",
			wantText:    "Édouard Philippe a proposé de lutter contre les arrêts de travail injustifiés.",
			wantChanged: true,
		},
		{
			name:        "extra space splitting a word",
			subject:     "jordan bardel la",
			factText:    "Jordan Bardel la a réclamé une baisse de la TVA sur le carburant.",
			source:      "Jordan Bardella réclame une baisse de la TVA de 20% à 5,5% sur le carburant.",
			wantSubject: "Jordan Bardella",
			wantText:    "Jordan Bardella a réclamé une baisse de la TVA sur le carburant.",
			wantChanged: true,
		},
		{
			name:        "correct subject differing only by accents is left alone",
			subject:     "Jean-Luc Melenchon",
			factText:    "Jean-Luc Melenchon a déclaré vouloir un État impartial.",
			source:      "Jean-Luc Mélenchon a déclaré vouloir un État impartial.",
			wantSubject: "Jean-Luc Melenchon",
			wantText:    "Jean-Luc Melenchon a déclaré vouloir un État impartial.",
		},
		{
			name:        "handle resolved by the model is out of reach",
			subject:     "Éric Ciotti",
			factText:    "Éric Ciotti a appelé à l'union des droites.",
			source:      "Mon ami @eciotti appelle à l'union des droites.",
			wantSubject: "Éric Ciotti",
			wantText:    "Éric Ciotti a appelé à l'union des droites.",
		},
		{
			name:        "a different name in the source is not a spelling",
			subject:     "Marine Tondelier",
			factText:    "Marine Tondelier a demandé une primaire de la gauche.",
			source:      "La secrétaire nationale des Écologistes a demandé une primaire, selon Marine Le Pen.",
			wantSubject: "Marine Tondelier",
			wantText:    "Marine Tondelier a demandé une primaire de la gauche.",
		},
		{
			name:        "real soft hyphens inside the name",
			subject:     "Jean-Luc M\u00e9\u00adlen\u00adchon",
			factText:    "Jean-Luc M\u00e9\u00adlen\u00adchon a dit X.",
			source:      "Jean-Luc Mélenchon a dit X.",
			wantSubject: "Jean-Luc Mélenchon",
			wantText:    "Jean-Luc Mélenchon a dit X.",
			wantChanged: true,
		},
		{
			name:        "correct subject kept although the source spells it differently first",
			subject:     "Jean-Luc Mélenchon",
			factText:    "Jean-Luc Mélenchon veut X.",
			source:      "Jean Luc Mélenchon veut X. Plus tard, Jean-Luc Mélenchon a dit Y.",
			wantSubject: "Jean-Luc Mélenchon",
			wantText:    "Jean-Luc Mélenchon veut X.",
		},
		{
			name:        "near-namesake Bayrou/Baroin stays apart",
			subject:     "François Bayrou",
			factText:    "François Bayrou a dit X.",
			source:      "Le Premier ministre a dit X, selon François Baroin.",
			target:      "Marine Le Pen",
			wantSubject: "François Bayrou",
			wantText:    "François Bayrou a dit X.",
		},
		{
			name:        "near-namesake Le Pen/Le Guen stays apart",
			subject:     "Jean-Marie Le Pen",
			factText:    "Jean-Marie Le Pen a dit X.",
			source:      "Le fondateur du FN a dit X, rapporte Jean-Marie Le Guen.",
			wantSubject: "Jean-Marie Le Pen",
			wantText:    "Jean-Marie Le Pen a dit X.",
		},
		{
			name:        "near-namesake Marine/Marion stays apart",
			subject:     "Marine Le Pen",
			factText:    "Marine Le Pen a dit X.",
			source:      "La présidente du groupe RN a dit X, selon Marion Le Pen.",
			wantSubject: "Marine Le Pen",
			wantText:    "Marine Le Pen a dit X.",
		},
		{
			name:        "near-namesake Borne/Moreno stays apart",
			subject:     "Élisabeth Borne",
			factText:    "Élisabeth Borne a dit X.",
			source:      "La ministre a dit X, d'après Élisabeth Moreno.",
			wantSubject: "Élisabeth Borne",
			wantText:    "Élisabeth Borne a dit X.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fact := backend.Fact{SubjectEntity: tt.subject, NormalizedFactText: tt.factText}
			changed := groundSubject(&fact, tt.source, tt.target)
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if fact.SubjectEntity != tt.wantSubject {
				t.Errorf("subject = %q, want %q", fact.SubjectEntity, tt.wantSubject)
			}
			if fact.NormalizedFactText != tt.wantText {
				t.Errorf("fact text = %q, want %q", fact.NormalizedFactText, tt.wantText)
			}
		})
	}
}
