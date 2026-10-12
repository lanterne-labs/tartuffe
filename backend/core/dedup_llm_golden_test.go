package core

import (
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"

	"digital-mirror/backend/llmtest"
)

// TestDedupBatchGolden pins the batch verifier of `dm dedupe run`
// (DedupLLMVerifier.VerifyBatch): in a batch of facts of one person, which
// facts are duplicates, and which facts are not valid facts?
//
// Source: the prod DB on 2026-10-10. Each case is one pair of facts of the
// same person with a cosine similarity in the uncertain band (0.75 to 0.90),
// the band that goes to the LLM. Texts, verbs, embedding texts and quotes are
// the prod values. As in prod, only the first fact of a pair has its quote
// (FindSimilarFactsForDedup does not load the quote of the neighbour). The
// grouping of prod does not tell which path merged a pair, so the labels are
// made from the texts. Each case states the label and the reason.
//
// A case passes when the duplicate pairs of the reply and the invalid facts
// of the reply are exactly the expected ones. A case with a "gap" is a known
// miss of the local model: a mismatch is logged, not failed. On Claude
// (-llm-claude, -llm-claude-team) the gap does not hold and the case fails.
//
// LOCAL ONLY: it skips when OPEN_AI_KEY is not set.
//
//	go test -v -run TestDedupBatchGolden ./backend/core/
func TestDedupBatchGolden(t *testing.T) {
	llmtest.Setup(t)

	v, err := NewDedupLLMVerifierFromEnv()
	if err != nil {
		t.Fatalf("NewDedupLLMVerifierFromEnv: %v", err)
	}

	fact := func(id int64, verb, embeddingText, text, quote string) FactForDedup {
		return FactForDedup{ID: id, CanonicalVerb: verb, EmbeddingText: embeddingText, NormalizedFactText: text, OriginalQuote: quote}
	}
	cases := []struct {
		name    string
		facts   []FactForDedup
		dups    [][2]int64 // pairs of facts in one duplicate group
		invalid []int64    // facts the reply must flag as invalid
		reason  string
		gap     string // a known miss of the local model: logged, not failed (llmtest.KnownGap)
	}{
		// Duplicates.
		{
			name: "bardella_antisemitic_denial",
			facts: []FactForDedup{
				fact(44022, "DENIED", "auteur-écrits-jeunesse antisémites", "Jordan Bardella nie farouchement être l'auteur des écrits de jeunesse antisémites publiés par Mediapart.", "dont le président du Rassemblement national nie farouchement être l’auteur"),
				fact(32360, "DENIED", "écrits-antisémites", "Jordan Bardella a démenti des écrits antisémites qui lui auraient été attribués en 2013 selon Mediapart.", ""),
			},
			dups:   [][2]int64{{44022, 32360}},
			reason: "the same denial of the same Mediapart writings, reworded",
		},
		{
			name: "bardella_pensions_fr_en",
			facts: []FactForDedup{
				fact(4669, "SUPPORTED", "retraites durabilité-économique", "Jordan Bardella a défendu l'idée que le système de retraite n'est pas soutenable sur le plan économique et que la démographie menace le système par répartition.", "le système des retraites « n’est pas soutenable sur le plan économique » , la démographie envoie le système par répartition dans le mur."),
				fact(5031, "CLAIMED", "pension-system sustainability", "Jordan Bardella stated that France's pension system is not economically sustainable due to demographic drivers.", ""),
			},
			dups:   [][2]int64{{4669, 5031}},
			reason: "the same statement in French and in English; the verbs differ but the claim is one",
		},
		{
			name: "retailleau_disciplinary_court",
			facts: []FactForDedup{
				fact(6992, "PROPOSED", "réforme-justice cour-disciplinaire", "Bruno Retailleau a proposé de créer une cour disciplinaire de la magistrature composée d'un collège de citoyens désignés par tirage au sort, afin de remplacer le rôle actuel du Conseil supérieur de la magistture en matière de sanctions.", "a proposé, dans une interview au Parisien , samedi 6 juin , de créer « une cour disciplinaire de lamagistrature » , afin de prononcer d’éventuelles sanctions à la place de l’actuel Conseil supérieur de la magistature."),
				fact(6873, "PROPOSED", "cour-disciplinaire magistrature", "Bruno Retailleau a proposé de créer une cour disciplinaire de la magistrature composée d'un collège de citoyens désignés par tirage au sort, aux côtés de magistrats et de personnalités qualifiées.", ""),
			},
			dups:   [][2]int64{{6992, 6873}},
			reason: "the same proposal in the same Le Parisien interview, two wordings",
		},
		{
			name: "faure_fuel_aid_doubled",
			facts: []FactForDedup{
				fact(19550, "DEMANDED", "aides-carburant grands-rouleurs", "Olivier Faure a demandé le doublement des aides sur le carburant ciblées pour les grands rouleurs et leur automatisation.", "Le doublement des aides sur le carburant ciblées pour les grands rouleurs et leur automatisation."),
				fact(19886, "DEMANDED", "aides-carburant", "Olivier Faure a réclamé que les aides soient doublées concernant la hausse du carburant.", ""),
			},
			dups:   [][2]int64{{19550, 19886}},
			reason: "the second fact is a subset of the first: the same demand to double the fuel aid",
		},
		{
			name: "glucksmann_no_deal_with_lfi",
			facts: []FactForDedup{
				fact(42619, "REFUSED", "compromissions-politiques insoumis", "Raphaël Glucksmann a déclaré refuser les compromissions avec les Insoumis et Jean-Luc Mélenchon.", "je refuse les compromissions avec les Insoumis et avec Jean Luc Mélenchon."),
				fact(23898, "OPPOSED", "alliances-politiques insoumis", "Raphaël Glucksmann refuse tout accord avec les 'insoumis'.", ""),
			},
			dups:   [][2]int64{{42619, 23898}},
			reason: "the same refusal of any deal with LFI, reworded",
		},
		{
			name: "glucksmann_sanction_platforms",
			facts: []FactForDedup{
				fact(13731, "DEMANDED", "sanctions plateformes-numériques ingérences-étrangères", "Raphaël Glucksmann a appelé les autorités françaises et européennes à sanctionner les plateformes numériques comme X et TikTok pour protéger les scrutins contre les ingérences étrangères.", "il faut aussi que « les grandes plateformes américaines et chinoises, en particulier X et TikTok, soient obligées de respecter les règles européennes » et sanctionnées si elles ne le font pas"),
				fact(13734, "DEMANDED", "lutte-ingérences-plateformes-sociales", "Raphaël Glucksmann a appelé les autorités françaises et européennes à ne pas avoir 'la main qui tremble' face aux ingérences étrangères et a exigé que les plateformes comme X et TikTok respectent et soient sanctionnées si elles ne respectent pas les règles européennes.", ""),
			},
			dups:   [][2]int64{{13731, 13734}},
			reason: "the same RTL demand; the first fact is a subset of the second",
		},
		{
			name: "ciotti_60m_savings",
			facts: []FactForDedup{
				fact(16721, "CLAIMED", "économies-budgétaires Nice", "Eric Ciotti a affirmé avoir engagé 60 millions d'euros d'économies pour remettre de l'ordre dans les comptes à Nice.", "Pour rendre aux Niçois, il fallait d’abord remettre de l’ordre dans les comptes : 60 millions d’euros d’économies engagées."),
				fact(15229, "CLAIMED", "dépenses-publiques-Nice", "Éric Ciotti a déclaré qu'en quelques semaines, il a réduit les dépenses publiques de 60 millions d'euros et baissé les impôts de 60 millions d'euros à Nice.", ""),
			},
			dups:   [][2]int64{{16721, 15229}},
			reason: "the first fact (60 M EUR of savings) is a subset of the second",
		},

		// Not duplicates.
		{
			name: "retailleau_age_65_vs_63",
			facts: []FactForDedup{
				fact(23874, "PROPOSED", "âge-retraite-taux-plein", "Bruno Retailleau veut fixer à 65 ans l'âge à partir duquel les Français pourront obtenir une retraite à taux plein.", "Le candidat du parti Les Républicains veut fixer à 65 ans l'âge à partir duquel les Français pourront obtenir une retraite à taux plein."),
				fact(21964, "PROPOSED", "âge-retraite", "Bruno Retailleau a proposé d'instaurer un âge minimal de départ à la retraite à 63 ans.", ""),
			},
			reason: "two different measures: the full-rate age (65) and the minimum age (63)",
		},
		{
			name: "ciotti_firefighters",
			facts: []FactForDedup{
				fact(10488, "OPPOSED", "agression sapeurs-pompiers", "Eric Ciotti a condamné avec la plus grande fermeté l'agression de deux sapeurs-pompiers.", "Je condamne avec la plus grande fermeté l’agression de deux sapeurs-pompiers, blessés alors qu’ils intervenaient pour porter secours à un individu victime d’un malaise… qui s’en est directement pris à eux."),
				fact(7082, "SUPPORTED", "intervention-sapeurs-pompiers", "Eric Ciotti a félicité les sapeurs-pompiers de Nice pour leur intervention rapide lors d'un incendie d'immeuble rue Joséphine Baker le 07 juin 2026.", ""),
			},
			reason: "two different events: an assault on firefighters and a building fire",
		},
		{
			name: "faure_censure_forecast_vs_vote",
			facts: []FactForDedup{
				fact(40135, "ANNOUNCED", "censure-politique-ps", "Olivier Faure a déclaré qu'il y aurait une censure lors de l'université d'été du PS le 28 août.", "Il y aura censure."),
				fact(9945, "VOTED_FOR", "censure-gouvernement", "Olivier Faure a déclaré avoir voté la censure du gouvernement initiée par le groupe écologiste.", ""),
			},
			reason: "an announcement on 28 August against a vote on a motion of the ecologist group: different acts",
		},
		{
			name: "retailleau_pension_suspension_vs_campaign",
			facts: []FactForDedup{
				fact(18975, "DEMANDED", "réforme-retraites gouvernement", "Bruno Retailleau a demandé au gouvernement de lever la suspension de la réforme des retraites.", "Ce que j’attends, par exemple, c’est qu’il lève cette suspension. Ce serait une décision responsable."),
				fact(24157, "PROPOSED", "réforme-retraites", "Bruno Retailleau a présenté la réforme des retraites comme un sujet de campagne.", ""),
			},
			reason: "a demand to the government against a campaign theme: different claims on one topic",
		},
		{
			name: "bardella_finances_vs_savings",
			facts: []FactForDedup{
				fact(44689, "CLAIMED", "finances-publiques situation", "Jordan Bardella a affirmé que les finances de la France sont dans une situation cataclysmique.", "Nous allons remettre de l'ordre dans les finances de la France, qui sont dans une situation cataclysmique."),
				fact(15002, "PROMISED", "économies finances publiques", "Jordan Bardella a affirmé qu'ils économiseront 125 milliards d'euros d'ici à 2032 et a annoncé vouloir supprimer une myriade de 120 taxes et impôts à faible rendement.", ""),
			},
			reason: "a diagnosis against a promise of savings: different claims",
		},
		{
			name: "glucksmann_doctrines_vs_no_deal",
			facts: []FactForDedup{
				fact(40158, "CLAIMED", "alliances-politiques La France insoumise", "Raphaël Glucksmann a affirmé qu'il existe des doctrines incompatibles entre lui et La France insoumise.", "Il rappelle qu’il y a des « doctrines incompatibles » entre lui et LFI."),
				fact(23898, "OPPOSED", "alliances-politiques insoumis", "Raphaël Glucksmann refuse tout accord avec les 'insoumis'.", ""),
			},
			reason: "hard: a claim of incompatible doctrines is not the refusal of a deal; one topic, two claims",
		},
		{
			name: "glucksmann_double_primary_entourage",
			facts: []FactForDedup{
				fact(7958, "REFUSED", "double primaire politique", "Raphaël Glucksmann refuse de se soumettre à l'idée d'une double primaire proposée par Olivier Faure.", "L’idée mise en table par Olivier Faure d’une double primaire n’a pas du tout convaincu le camp Glucksmann, qui refuse toujours de s’y soumettre."),
				fact(11154, "OPPOSED", "primaire double-option", "L'entourage de Raphaël Glucksmann s'est déclaré opposé à l'idée d'une double primaire.", ""),
			},
			dups:   [][2]int64{{7958, 11154}},
			reason: "the entourage fact is a subset of the refusal (the prompt rule: a subset is a duplicate); Qwen and Opus agree (2026-10-11)",
		},
		{
			name: "faure_tva_sociale",
			facts: []FactForDedup{
				fact(42780, "OPPOSED", "tva-sociale", "Olivier Faure a affirmé qu'il se bat toujours contre la TVA sociale.", "Je me battais et je me bats toujours contre la TVA sociale"),
				fact(17169, "OPPOSED", "tva-sociale efficacite", "Olivier Faure a qualifié la TVA sociale de mesure nocive et inefficace.", ""),
			},
			reason: "hard: two statements of one opposition on unknown occasions are not one statement reworded; a restatement goes to the stance",
			gap:    "the local model groups two statements of one position (2026-10-11); Sonnet and Opus keep them apart",
		},

		// Invalid facts.
		{
			name: "bardella_third_party_opinion",
			facts: []FactForDedup{
				fact(41055, "CLAIMED", "état-situation Jordan Bardella", "Jordan Bardella a déclaré que 'tout va bien' pour lui malgré les accusations d'antisémitisme.", "Franchement, tout va bien."),
				fact(41315, "CLAIMED", "qualification-politique Jordan-Bardella", "Les candidats ont estimé que Jordan Bardella était désormais 'disqualifié' suite aux accusations d'antisémitisme par Mediapart.", ""),
			},
			invalid: []int64{41315},
			reason:  "41315 is the opinion of other candidates about Bardella, not his own position",
			gap:     "the local model never fills invalid_facts (Qwen3.8, 2026-10-11); Opus applies the rule",
		},
		{
			name: "bardella_tv_appearance",
			facts: []FactForDedup{
				fact(16744, "CLAIMED", "accord-migratoire nigel-farage", "Jordan Bardella s'est exprimé sur BFM-TV le samedi 6 septembre 2026 pour s'expliquer sur son accord migratoire avec Nigel Farage.", "s’expliquer sur son accord migratoire avec Nigel Farage et l’arrêt programmé du soutien à l’Ukraine."),
				fact(16664, "ANNOUNCED", "immigration-accord-farage", "Jordan Bardella reached an agreement on immigration with Nigel Farage.", ""),
			},
			invalid: []int64{16744},
			reason:  "16744 only schedules a TV appearance and holds no claim; 16664 is the act itself",
			gap:     "the local model never fills invalid_facts (Qwen3.8, 2026-10-11); Opus applies the rule",
		},
		{
			name: "faure_debate_lineup",
			facts: []FactForDedup{
				fact(33051, "SUPPORTED", "débat intelligence artificielle", "Olivier Faure a participé à un débat sur l'intelligence artificielle le 25 septembre 2026 à Marseille, face à Xavier Bertrand, Manuel Bompard et des représentants de l'extrême droite.", "un premier plateau comprenant deux représentants de l’extrême droite (Sarah Knafo pour Reconquête ! et le député de Moselle Alexandre Loubet pour le Rassemblement national) face à Xavier Bertrand (Les Républicains), Manuel Bompard (La France insoumise, LFI) et Olivier Faure (Parti socialiste)."),
				fact(32461, "PROPOSED", "encadrement-intelligence-artificielle", "Olivier Faure a appelé à savoir cadrer l'intelligence artificielle pour qu'elle soit au service de l'homme.", ""),
			},
			invalid: []int64{33051},
			reason:  "33051 is a debate line-up (scheduling) with no position; 32461 is his position",
			gap:     "the local model never fills invalid_facts (Qwen3.8, 2026-10-11); Opus applies the rule",
		},
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
				resp, err := v.VerifyBatch(ctx, tc.facts)
				if err != nil {
					t.Fatalf("VerifyBatch: %v", err)
				}
				gotDups, gotInvalid := dedupBatchDecision(resp)
				wantDups := normalizedPairs(tc.dups)
				wantInvalid := sortedIDs(tc.invalid)
				mu.Lock()
				defer mu.Unlock()
				mismatch := fmt.Sprintf("got dups=%v invalid=%v want dups=%v invalid=%v\n    reply: %+v\n    label reason: %s",
					gotDups, gotInvalid, wantDups, wantInvalid, resp, tc.reason)
				switch {
				case slices.Equal(gotDups, wantDups) && slices.Equal(gotInvalid, wantInvalid):
					passed++
					t.Logf("dups=%v invalid=%v ✓", gotDups, gotInvalid)
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
	t.Logf("=== DEDUP-BATCH GOLDEN: %d passed, %d failed, %d known gaps (of %d) ===", passed, failed, gaps, len(cases))
}

// dedupBatchDecision returns the decision of a reply: every pair of facts in
// one duplicate group, and the invalid facts, both sorted.
func dedupBatchDecision(resp *LLMDedupeResponse) (dups []string, invalid []int64) {
	var pairs [][2]int64
	for _, g := range resp.UniqueGroups {
		members := append([]int64{g.KeepID}, g.DuplicateIDs...)
		for i := range members {
			for j := i + 1; j < len(members); j++ {
				pairs = append(pairs, [2]int64{members[i], members[j]})
			}
		}
	}
	for _, f := range resp.InvalidFacts {
		invalid = append(invalid, f.ID)
	}
	return normalizedPairs(pairs), sortedIDs(invalid)
}

// normalizedPairs writes each pair as "low-high" and sorts the list, so two
// sets of unordered pairs compare with slices.Equal.
func normalizedPairs(pairs [][2]int64) []string {
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		lo, hi := min(p[0], p[1]), max(p[0], p[1])
		out = append(out, fmt.Sprintf("%d-%d", lo, hi))
	}
	sort.Strings(out)
	return slices.Compact(out)
}

func sortedIDs(ids []int64) []int64 {
	out := append([]int64{}, ids...)
	slices.Sort(out)
	return slices.Compact(out)
}
