package complaints

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/llmtest"
	"digital-mirror/backend/storage"
)

// TestComplaintJudgeGolden pins the complaint judge (OpenAIJudge.Judge on the
// prompts of buildFactPrompt, buildContradictionPrompt and buildStancePrompt):
// is a user complaint right (delete_target) or wrong (dismiss_complaint)?
//
// Source: complaints live in Tally, not in the prod DB, and no real complaint
// is on record. The targets are real prod rows of 2026-10-10 (facts, quotes,
// source URLs, dates, contradictions, stances); the complaint texts are
// written for the test, as a user would write them. Each case states the label
// and the reason. The labels follow the project rules: a position reversal is
// a contradiction; two compatible proposals are not. A case with a "gap" is a
// known miss of the local model: a mismatch is logged, not failed. On Claude
// (-llm-claude, -llm-claude-team) the gap does not hold and the case fails.
//
// LOCAL ONLY: it skips when OPEN_AI_KEY is not set.
//
//	go test -v -run TestComplaintJudgeGolden ./backend/complaints/
func TestComplaintJudgeGolden(t *testing.T) {
	llmtest.Setup(t)

	judge, err := NewOpenAIJudgeFromEnv()
	if err != nil {
		t.Fatalf("NewOpenAIJudgeFromEnv: %v", err)
	}

	day := func(y int, m time.Month, d int) *time.Time {
		v := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		return &v
	}
	factPrompt := func(id int64, text, quote string, urls []string, complaint string) string {
		return buildFactPrompt(&core.FactForDedup{ID: id, NormalizedFactText: text, OriginalQuote: quote}, urls, complaint)
	}
	// side is one fact of a contradiction; quote is its source quote, "" when
	// the prod row has none.
	type side struct {
		id    int64
		date  *time.Time
		url   string
		text  string
		quote string
	}
	contradictionPrompt := func(id int64, a, b side, reason, complaint string) string {
		return buildContradictionPrompt(&storage.ContradictionWithFacts{
			ID: id, FactAID: a.id, FactADate: a.date, FactAText: a.text, FactASourceURLs: []string{a.url}, FactAQuote: a.quote,
			FactBID: b.id, FactBDate: b.date, FactBText: b.text, FactBSourceURLs: []string{b.url}, FactBQuote: b.quote,
			Reason: reason,
		}, complaint)
	}
	stancePrompt := func(id int64, label, summary string, factCount int, facts []core.StanceFactInfo, complaint string) string {
		return buildStancePrompt(&core.StanceDocument{ID: id, ClusterLabel: label, Summary: summary, FactCount: factCount}, facts, complaint)
	}
	sf := func(id int64, text string) core.StanceFactInfo {
		return core.StanceFactInfo{ID: id, NormalizedFactText: text}
	}

	cases := []struct {
		name   string
		prompt string
		want   Verdict
		reason string
		gap    string // a known miss of the local model: logged, not failed (llmtest.KnownGap)
	}{
		// Fact complaints.
		{
			name: "fact_satire_source",
			prompt: factPrompt(7375,
				"Edouard Philippe a proposé la fin des 35 heures, la possibilité de travailler jusqu'à 98 heures par semaine, la réduction des congés annuels à 3 jours et le recul de l'âge de la retraite à 82 ans.",
				"Outre la fin des 35 heures et la possibilité de travailler jusqu’à 98 heures par semaine, le maire du Havre veut réduire les congés annuels à 3 jours et reculer l’âge de la retraite à 82 ans avec un taux plein à partir de 87 ans.",
				[]string{"https://www.legorafi.fr/2026/06/09/edouard-philippe-je-veux-que-les-francais-puissent-continuer-a-travailler-apres-la-mort/"},
				"Ce fait vient du Gorafi, un site parodique. Édouard Philippe n'a jamais proposé la retraite à 82 ans."),
			want:   VerdictDeleteTarget,
			reason: "the only source is Le Gorafi, a satire site: the fact is fabricated",
		},
		{
			name: "fact_third_party_opinion",
			prompt: factPrompt(41315,
				"Les candidats ont estimé que Jordan Bardella était désormais 'disqualifié' suite aux accusations d'antisémitisme par Mediapart.",
				"ils se sont tous retrouvés pour estimer que le patron du RN Jordan Bardella, accusé d'antisémitisme par le journal Mediapart, était désormais 'disqualifié'.",
				[]string{"https://www.france24.com/fr/france/20261001-deuxieme-debat-primaire-gauche-possible-alliance-lfi-divise-genocide-gaza-glucksmann-faure"},
				"Ce n'est pas une déclaration de Bardella : c'est l'avis des candidats de la primaire de gauche sur lui. Ça n'a rien à faire dans sa fiche."),
			want:   VerdictDeleteTarget,
			reason: "the fact is the opinion of third parties, misattributed to Bardella as his record",
		},
		{
			name: "fact_denial_supported_by_quote",
			prompt: factPrompt(44022,
				"Jordan Bardella nie farouchement être l'auteur des écrits de jeunesse antisémites publiés par Mediapart.",
				"dont le président du Rassemblement national nie farouchement être l’auteur",
				[]string{"https://www.lemonde.fr/politique/article/2026/10/07/ecrits-antisemites-marine-le-pen-dit-maintenir-sa-confiance-en-jordan-bardella-et-s-en-prend-a-mediapart_6789594_823448.html"},
				"Bardella n'a jamais nié, il a reconnu être l'auteur de ces messages."),
			want:   VerdictDismissComplaint,
			reason: "the Le Monde quote says he denies it; the complaint has no source",
		},
		{
			name: "fact_retailleau_63_and_65",
			prompt: factPrompt(21964,
				"Bruno Retailleau a proposé d'instaurer un âge minimal de départ à la retraite à 63 ans.",
				"Le candidat LR à la présidentielle, Bruno Retailleau, a déclaré qu'il instaurerait un âge de départ minimal à 63 ans",
				[]string{"https://www.franceinfo.fr/politique/bruno-retailleau/s-il-est-elu-bruno-retailleau-veut-instaurer-un-age-minimal-de-depart-a-la-retraite-a-63-ans-le-taux-plein-a-65_8205254.html"},
				"Faux : Retailleau veut la retraite à 65 ans, pas à 63."),
			want:   VerdictDismissComplaint,
			reason: "hard: 63 is the minimum age and 65 the full rate (the URL says both); the fact is right",
		},
		{
			name: "fact_rant_without_problem",
			prompt: factPrompt(7082,
				"Eric Ciotti a félicité les sapeurs-pompiers de Nice pour leur intervention rapide lors d'un incendie d'immeuble rue Joséphine Baker le 07 juin 2026.",
				"Bravo aux sapeurs-pompiers de Nice pour leur intervention rapide sur un l’incendie d’un immeuble rue Joséphine Baker.",
				[]string{"https://x.com/eciotti"},
				"Ciotti est un menteur, supprimez toutes ses fiches."),
			want:   VerdictDismissComplaint,
			reason: "the complaint names no problem with the fact, which quotes his own post",
		},
		{
			name: "fact_shared_quote_two_persons",
			prompt: factPrompt(23898,
				"Raphaël Glucksmann refuse tout accord avec les 'insoumis'.",
				"le député socialiste de l’Essonne Jérôme Guedj et l’eurodéputé (Place publique) Raphaël Glucksmann, qui refusent tout accord avec les « insoumis ».",
				[]string{"https://www.lemonde.fr/politique/article/2026/09/24/presidentielle-2027-sans-etre-la-jean-luc-melenchon-a-parasite-le-premier-debat-de-la-primaire-sociale-democrate_6781568_823448.html"},
				"C'est Jérôme Guedj qui a dit ça, pas Glucksmann."),
			want:   VerdictDismissComplaint,
			reason: "hard: the quote names both Guedj and Glucksmann as refusing any deal",
		},

		// Contradiction complaints.
		{
			name: "contradiction_same_meaning_and_satire",
			prompt: contradictionPrompt(657,
				side{7375, day(2026, 6, 9), "https://www.legorafi.fr/2026/06/09/edouard-philippe-je-veux-que-les-francais-puissent-continuer-a-travailler-apres-la-mort/",
					"Edouard Philippe a proposé la fin des 35 heures, la possibilité de travailler jusqu'à 98 heures par semaine, la réduction des congés annuels à 3 jours et le recul de l'âge de la retraite à 82 ans.", ""},
				side{17675, day(2026, 9, 11), "https://www.liberation.fr/idees-et-debats/opinions/edouard-philippe-et-le-repos-avant-lelysee-pour-une-fois-quun-responsable-de-droite-ne-se-complait-pas-dans-leloge-de-leffort-a-tout-prix-20260911_4RY5M3JQEZHRLBZN6DYIKD6Z7U/",
					"À Matignon, l'ancien Premier ministre a bataillé pour faire reculer l’âge de départ à la retraite et pour durcir les conditions d’accès à l’assurance-chômage.", ""},
				"La proposition de reculer l'âge de la retraite (augmenter l'âge de départ) contredit l'action de batailler pour faire reculer l'âge de départ (diminuer l'âge de départ).",
				"« Reculer l'âge de départ » veut dire le repousser dans les deux phrases : il n'y a aucune contradiction. En plus le fait A vient du Gorafi."),
			want:   VerdictDeleteTarget,
			reason: "both facts push the age later; the reason misreads 'reculer'; fact A is satire",
		},
		{
			name: "contradiction_reason_does_not_match",
			prompt: contradictionPrompt(846,
				side{31061, day(2026, 9, 27), "https://www.capital.fr/votre-retraite/retraites-selon-edouard-philippe-ce-qui-pourrait-changer-en-2027-1530449",
					"Le programme d'Édouard Philippe prévoit de créer un pilier de capitalisation représentant 10 à 15 % des pensions d'ici quinze ans.", ""},
				side{35399, day(2026, 9, 29), "https://actu.orange.fr/politique/presidentielle-edouard-philippe-abat-sa-carte-retraites-le-camp-sortant-sous-pression-CNT000002sbpVx.html",
					"Édouard Philippe a précisé que l'âge de la retraite pour les personnes en situation de handicap resterait à 55 ans.", ""},
				"L'affirmation qu'un recul de l'âge de départ est inévitable contredit la promesse que l'âge de la retraite n'évoluerait pas pour les personnes en mauvaise santé.",
				"Un pilier de capitalisation et l'âge de 55 ans pour les personnes handicapées ne se contredisent pas. La raison parle de choses qui ne sont pas dans les deux faits."),
			want:   VerdictDeleteTarget,
			reason: "the two facts are compatible and the reason describes other statements",
		},
		{
			name: "contradiction_floor_and_cap",
			prompt: contradictionPrompt(774,
				side{23750, day(2026, 9, 24), "https://www.bfmtv.com/politique/elections/presidentielle/direct-emmanuel-macron-s-exprimera-ce-soir-a-20h-sur-la-crise-des-carburants-et-les-enjeux-internationaux_LN-202609240140.html",
					"Dominique de Villepin a proposé de mettre un plafond absolu à 2,50 euros pour les carburants.", ""},
				side{23775, day(2026, 9, 24), "https://www.bfmtv.com/politique/elections/presidentielle/direct-emmanuel-macron-s-exprimera-ce-soir-a-20h-sur-la-crise-des-carburants-et-les-enjeux-internationaux_LN-202609240140.html",
					"Dominique de Villepin a proposé d'instaurer un plancher à 1,75 euros pour les carburants.", ""},
				"La proposition d'un plancher de prix est opposée à la proposition d'un plafond de prix.",
				"Un plancher à 1,75 € et un plafond à 2,50 € vont ensemble : c'est un tunnel de prix, pas une contradiction."),
			want:   VerdictDeleteTarget,
			reason: "a floor below a cap is one price band; the two proposals are compatible",
		},
		{
			name: "contradiction_refusing_freeze_is_no_tax_rise",
			prompt: contradictionPrompt(711,
				side{15080, day(2026, 8, 30), "https://www.la-croix.com/societe/le-premier-ministre-sebastien-lecornu-dessine-un-budget-de-premier-semestre-20260830",
					"Sébastien Lecornu a promis de ne pas augmenter les impôts lors de l'adoption du budget à l'automne 2026.", ""},
				side{20295, day(2026, 9, 18), "https://x.com/seblecornu",
					"Sébastien Lecornu a déclaré refuser le gel du barème de l'impôt sur le revenu.", ""},
				"Le refus de geler le barème de l'impôt contredit l'affirmation selon laquelle le gouvernement n'augmenterait pas les impôts.",
				"Geler le barème, c'est augmenter l'impôt en douce. Refuser le gel, c'est donc tenir la promesse de ne pas augmenter les impôts. Pas de contradiction."),
			want:   VerdictDeleteTarget,
			reason: "hard: a freeze of the scale raises the tax; refusing it agrees with the promise",
		},
		{
			name: "contradiction_apology_vs_personal_failures",
			prompt: contradictionPrompt(702,
				side{7161, day(2026, 6, 8), "https://www.lefigaro.fr/actualite-france/en-direct-affaire-lyhanna-darmanin-reunit-les-procureurs-generaux-manifestations-attendues-devant-les-tribunaux-20260608",
					"Le ministre de la Justice a présenté ses excuses au nom de la justice le vendredi 5 juin 2026 suite à l'affaire Lyhanna.", ""},
				side{9411, day(2026, 6, 22), "https://www.rtl.fr/actu/justice-faits-divers/en-direct-affaire-lyhanna-134-personnes-en-detention-provisoire-dans-le-cadre-du-reexamen-des-70-000-plaintes-pour-violences-sexuelles-sur-mineurs-7900648589",
					"Gérald Darmanin a affirmé que l'affaire Lyhanna était due à des \"défaillances personnelles\" et non à un manque de moyens dans la justice et la gendarmerie.", ""},
				"Darmanin attribue d'abord l'affaire à des défaillances personnelles, puis affirme qu'il s'agit d'un échec grave de l'institution judiciaire.",
				"S'excuser au nom de la justice n'empêche pas de dire que l'échec vient de défaillances individuelles. Et aucun des deux faits ne parle d'« échec grave de l'institution »."),
			want:   VerdictDeleteTarget,
			reason: "hard: an apology and a cause are compatible; the reason quotes words in neither fact",
		},
		{
			name: "contradiction_user_denies_sourced_fact",
			prompt: contradictionPrompt(675,
				side{7031, day(2026, 6, 6), "https://www.laprovence.com/article/politique/60856770342397/raphael-glucksmann-de-passage-a-marseille-il-est-temps-de-reprendre-la-main-sur-notre-recit",
					"Raphaël Glucksmann a rejeté l'organisation d'une primaire, la qualifiant de « machine à perdre », préférant parler directement aux Français.",
					"la primaire est une machine à perdre"},
				side{18026, day(2026, 9, 12), "https://actu.fr/nouvelle-aquitaine/la-reole_33352/cetait-devenu-son-rendez-vous-de-rentree-raphael-glucksmann-ne-viendra-pas-dans-cette-ville-de-gironde_64780260.html",
					"Raphaël Glucksmann est engagé dans la primaire PS-PP qui se déroulera par scrutin électronique sur deux tours les 9-10 et 16-17 octobre.", ""},
				"Raphaël Glucksmann avait rejeté l'organisation d'une primaire, mais il est désormais engagé dans la primaire PS-PP.",
				"Glucksmann n'a jamais dit que la primaire était une machine à perdre, c'est inventé."),
			want:   VerdictDismissComplaint,
			reason: "fact A has a La Provence source and quote; the complaint gives no evidence",
		},

		// Stance complaints.
		{
			name: "stance_mixed_topics",
			prompt: stancePrompt(18496, "Symbolisme et identité politique",
				"Les déclarations concernent la nature des symboles politiques et identitaires. L'un des points porte sur l'absence d'histoire ou de symbolisme lié au drapeau européen. Par ailleurs, une distinction est opérée entre la stigmatisation des femmes et la volonté de se distancier de symboles perçus comme étant sous l'influence d'une idéologie.",
				2, []core.StanceFactInfo{
					sf(14906, "Jean-Philippe Tanguy a affirmé que le drapeau européen ne renvoie à aucune histoire ni aucun symbole."),
					sf(15686, "Jean-Philippe Tanguy a affirmé que le projet de son parti ne vise pas à stigmatiser les femmes portant le voile, mais qu'il est nécessaire d'abandonner certains symboles accaparés par une idéologie."),
				},
				"Cette position mélange le drapeau européen et le voile islamique, deux sujets qui n'ont rien à voir."),
			want:   VerdictDeleteTarget,
			reason: "the cluster joins the EU flag and the veil only through the word 'symbole'",
		},
		{
			name: "stance_anecdotes_not_a_position",
			prompt: stancePrompt(16585, "parcours politiques et figures de l'histoire politique française",
				"Olivier Faure a évoqué le parcours de François Hollande, rappelant son score initial de 3 % avant son accession à la présidence de la République, ainsi que la participation de Bernadette Chirac au combat féministe. Il a également soutenu Manuel Valls lors de la primaire socialiste de 2017 avant de se rallier à Benoît Hamon.",
				4, []core.StanceFactInfo{
					sf(6866, "Olivier Faure a affirmé que Bernadette Chirac a participé au combat féministe."),
					sf(15012, "Olivier Faure a rappelé que François Hollande avait commencé à 3 % avant de devenir président de la République."),
					sf(15555, "Olivier Faure a rappelé que François Hollande avait commencé à 3% avant de devenir président de la République lors des universités d'été."),
					sf(44269, "Olivier Faure a soutenu Manuel Valls lors de la primaire socialiste de 2017 avant de se rallier à Benoît Hamon."),
				},
				"Ce n'est pas une position politique : c'est un fourre-tout d'anecdotes sur Hollande, Bernadette Chirac et Valls sans lien entre elles."),
			want:   VerdictDeleteTarget,
			reason: "the cluster groups unrelated anecdotes under a catch-all label",
			gap:    "the local model passes 1 run in 3 at its sampling temperature (2026-10-11); Sonnet passes every run",
		},
		{
			name: "stance_rant_without_problem",
			prompt: stancePrompt(17091, "politique budgétaire et fiscale",
				"Sébastien Lecornu a présenté un plan de redressement de 54 milliards d'euros pour 2027, visant à ramener le déficit à 5 % du PIB, tout en promettant une stabilité fiscale et l'absence de hausse d'impôts généralisée. Il a d'abord affirmé qu'il n'utiliserait ni l'article 49.3 ni les ordonnances, avant d'annoncer qu'il ferait appel à l'article 49.3 pour faire adopter le budget sans vote parlementaire, une position qui contredit ses déclarations précédentes. Face à l'impasse, il a proposé une discussion « mesure par mesure » et un compromis avec les socialistes, tout en menaçant d'une austérité dure en cas d'absence de budget et en saisissant la justice pour des fuites médiatiques.",
				215, []core.StanceFactInfo{
					sf(388, "Sébastien Lecornu a annoncé le 20 janvier 2026 qu'il aurait recours à l'article 49-3 sur la partie « recettes » du projet de loi de finances."),
					sf(1778, "Sébastien Lecornu a missionné la sénatrice Sylvie Vermeillet ainsi que les députés Christine Pirès Beaune et Jean-René Cazeneuve sur les ressources financières locales en vue de la loi de finances 2027."),
					sf(1920, "Sébastien Lecornu a déclenché l'article 49.3 sur la partie \"dépenses\" du projet de loi de finances pour 2026."),
					sf(2166, "Sébastien Lecornu a annoncé qu'il ferait appel à l'article 49 alinéa 3 de la constitution pour faire adopter un budget pour la France sans vote du parlement."),
					sf(4966, "Sébastien Lecornu a affirmé qu'il n'y aurait pas de proposition d'augmentation d'impôts dans le budget proposé à l'automne prochain."),
				},
				"Lecornu est le meilleur Premier ministre qu'on ait eu, arrêtez de le salir."),
			want:   VerdictDismissComplaint,
			reason: "the complaint names no error in the label, the summary or the grouping",
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
				got, err := judge.Judge(ctx, tc.prompt)
				if err != nil {
					t.Fatalf("Judge: %v", err)
				}
				mu.Lock()
				defer mu.Unlock()
				mismatch := fmt.Sprintf("got %s want %s\n    model: %s\n    label reason: %s", got.Verdict, tc.want, got.Reason, tc.reason)
				switch {
				case got.Verdict == tc.want:
					passed++
					t.Logf("%s ✓ (%s)", got.Verdict, got.Reason)
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
	t.Logf("=== COMPLAINT GOLDEN: %d passed, %d failed, %d known gaps (of %d) ===", passed, failed, gaps, len(cases))
}
