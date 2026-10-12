package core

import (
	"fmt"
	"sync"
	"testing"

	"digital-mirror/backend/llmtest"
)

// TestEntityDedupGolden pins the pair adjudicator of `dm persons dedupe`
// (EntityDedupAdjudicator.AdjudicatePair): are two similar names the same
// real-world entity?
//
// Source: the prod DB on 2026-10-10. Every pair is a trigram candidate of the
// persons table (similarity >= 0.5, the query of FindDuplicateCandidates);
// ids, names, types and fact counts are the prod values. No pair is merged in
// prod yet, so no prod verdict exists. A sample of facts of each row confirmed
// the identity of the hard cases. Each case states the label and the reason.
// A case with a "gap" is a known miss of the local model: a mismatch is
// logged, not failed. On Claude (-llm-claude, -llm-claude-team) the gap does
// not hold and the case fails.
//
// LOCAL ONLY: it skips when OPEN_AI_KEY is not set.
//
//	go test -v -run TestEntityDedupGolden ./backend/core/
func TestEntityDedupGolden(t *testing.T) {
	llmtest.Setup(t)

	adj, err := NewEntityDedupAdjudicatorFromEnv()
	if err != nil {
		t.Fatalf("NewEntityDedupAdjudicatorFromEnv: %v", err)
	}

	ref := func(id int64, name, entityType string, facts int) EntityRef {
		return EntityRef{ID: id, Name: name, EntityType: entityType, FactCount: facts}
	}
	cases := []struct {
		name   string
		a, b   EntityRef
		want   bool // true: same entity
		reason string
		gap    string // a known miss of the local model: logged, not failed (llmtest.KnownGap)
	}{
		// Same entity.
		{"melenchon_space", ref(116, "jean-luc melenchon", "human", 809), ref(5438, "jean- luc melenchon", "human", 4), true,
			"one stray space in the same name", ""},
		{"orban_name_order", ref(1374, "viktor orban", "human", 0), ref(1800, "orban viktor", "human", 0), true,
			"Hungarian name order of the same person", ""},
		{"afd_acronym", ref(125, "alternative pour l'allemagne", "party", 62), ref(271, "alternative pour l'allemagne (afd)", "party", 116), true,
			"the acronym in brackets names the same national party", ""},
		{"netanyahu_transliteration", ref(950, "benjamin netanyahu", "human", 2), ref(8420, "benyamin netanyahu", "human", 1), true,
			"two transliterations of the same name", ""},
		{"sharaa_transliteration", ref(2783, "ahmed al-sharaa", "human", 0), ref(5524, "ahmed al-charaa", "human", 3), true,
			"English and French transliterations of the Syrian president", ""},
		{"retailleau_typo", ref(21, "bruno retailleau", "human", 669), ref(696, "bruno retaileau", "human", 0), true,
			"one letter missing in the same name", ""},
		{"soc_acronym", ref(4343, "socialistes et apparentes (soc)", "party", 2), ref(6258, "socialistes et apparentes", "party", 79), true,
			"the acronym of the same Assemblée group", ""},
		{"reconquete_punctuation", ref(4, "reconquete", "party", 7), ref(5417, "reconquete !", "party", 1), true,
			"the exclamation mark is part of the official name of the same party", ""},
		{"ben_salman_spelling", ref(617, "mohammed ben salmane", "human", 0), ref(3187, "mohammed ben salman", "human", 0), true,
			"French and English spellings of the same name", ""},
		{"droite_republicaine_typo", ref(4746, "droit republicain", "party", 3), ref(6255, "droite republicaine", "party", 73), true,
			"hard: the facts of row 4746 name the group 'Droite Républicaine (DR)'; the name lost its 'e'",
			"needs world knowledge the local model lacks (2026-10-11); Opus 21/21"},
		{"lucas_lundy_double_name", ref(2319, "benjamin lucas", "human", 1), ref(4034, "benjamin lucas-lundy", "human", 66), true,
			"hard: both rows quote the Génération.s deputy, who now uses the double name Lucas-Lundy",
			"needs world knowledge the local model lacks (2026-10-11); Opus 21/21"},

		// Different entities.
		{"afd_vs_saxony_anhalt", ref(271, "alternative pour l'allemagne (afd)", "party", 116), ref(3343, "alternative pour l'allemagne (afd) saxe-anhalt", "party", 137), false,
			"a national party against its regional branch", ""},
		{"greens_vs_south_australian", ref(3707, "australian greens", "party", 5), ref(5220, "south australian greens", "party", 3), false,
			"a national party against its state branch", ""},
		{"conservative_bc_vs_canada", ref(3392, "conservative party of bc", "party", 0), ref(3486, "conservative party of canada", "party", 0), false,
			"a provincial party against the federal party, two separate parties", ""},
		{"trump_vs_trump_jr", ref(20, "donald trump", "human", 92), ref(3938, "donald trump jr.", "human", 1), false,
			"father and son", ""},
		{"patrice_vs_patrick_martin", ref(4019, "patrice martin", "human", 45), ref(4720, "patrick martin", "human", 13), false,
			"two first names; the facts show a mayor of Le Montellier and the president of the Medef", ""},
		{"patriotes_vs_pastef", ref(142, "les patriotes", "party", 0), ref(2334, "pastef - les patriotes", "party", 0), false,
			"a French party against a Senegalese party", ""},
		{"parti_de_gauche_vs_prg", ref(134, "parti de gauche", "party", 1), ref(341, "parti radical de gauche", "party", 0), false,
			"two different French parties", ""},
		{"cpgb_vs_new_cpb", ref(915, "communist party of great britain", "party", 0), ref(2134, "new communist party of britain", "party", 0), false,
			"hard: the New Communist Party of Britain split from the CPGB; two parties", ""},
		{"dup_vs_pyd", ref(911, "democratic unionist party", "party", 0), ref(1137, "democratic union party", "party", 0), false,
			"hard: the DUP of Northern Ireland against the Kurdish PYD of Syria", ""},
		{"alexandra_martin_two_deputies", ref(4575, "alexandra martin", "human", 11), ref(8467, "alexandra martin (gironde)", "human", 5), false,
			"hard: two deputies with the same name; the qualifier (gironde) separates them",
			"needs world knowledge the local model lacks (2026-10-11); Opus 21/21"},
	}

	ctx, cancel := llmtest.Context(t)
	defer cancel()

	// The cases run concurrently with -llm-claude: mu guards the counts.
	var mu sync.Mutex
	var passed, failed, gaps int
	llmtest.Group(t, func(t *testing.T) {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				llmtest.Parallel(t)
				v, err := adj.AdjudicatePair(ctx, tc.a, tc.b)
				if err != nil {
					t.Fatalf("AdjudicatePair: %v", err)
				}
				mu.Lock()
				defer mu.Unlock()
				mismatch := fmt.Sprintf("got same=%v want same=%v: %q vs %q\n    model: %s\n    label reason: %s",
					v.Same, tc.want, tc.a.Name, tc.b.Name, v.Reasoning, tc.reason)
				switch {
				case v.Same == tc.want:
					passed++
					t.Logf("same=%v ✓ (%s)", v.Same, v.Reasoning)
				case llmtest.KnownGap(t, tc.gap):
					gaps++
					t.Log(mismatch)
				default:
					failed++
					t.Error(mismatch)
				}
			})
		}
	})
	t.Logf("=== ENTITY-DEDUP GOLDEN: %d passed, %d failed, %d known gaps (of %d) ===", passed, failed, gaps, len(cases))
}
