package contradiction

import (
	"fmt"
	"sync"
	"testing"

	"digital-mirror/backend/llmtest"
	"digital-mirror/backend/storage"
)

// bundleContradiction is one prod contradiction: the two facts and the
// recorded reason, as the bundler gets them.
type bundleContradiction struct {
	id           int64 // contradictions.id in prod
	a, b, reason string
}

// TestBundlerGolden pins the YES/NO gate of the bundler (askLLMRelated): does
// a contradiction belong to a candidate stance of the same person?
//
// Source: the prod DB on 2026-10-10. The positive cases and most negative
// cases come from the 13 contradictions whose facts sit in two different
// stances (the bundler path): the candidates are those two stances. The other
// negative cases pair a contradiction with the nearest other stance of the
// same person (cosine distance of the stance embeddings). Texts are verbatim;
// two long summaries are abridged. Each case states the label and the reason. The labels follow the prompt: YES only when the
// contradiction is about the issue of the stance, not a shared word. A case
// with a "gap" is a known miss of the local model: a mismatch is logged, not
// failed. On Claude (-llm-claude, -llm-claude-team) the gap does not hold and
// the case fails.
//
// LOCAL ONLY: it skips when OPEN_AI_KEY is not set.
//
//	go test -v -run TestBundlerGolden ./backend/contradiction/
func TestBundlerGolden(t *testing.T) {
	llmtest.Setup(t)

	b, err := NewBundlerFromEnv(nil, nil)
	if err != nil {
		t.Fatalf("NewBundlerFromEnv: %v", err)
	}

	// The contradictions, with their prod ids.
	afdTax := bundleContradiction{137,
		"L'AfD garantit qu'il n'y aura aucune nouvelle taxe ni augmentation d'impôt sous sa direction.",
		"L'AfD soutient que le montant minimum de distribution annuel de la taxe de protection contre les incendies devrait être doublé pour atteindre 9 millions d'euros.",
		"L'AfD soutient le doublement d'une taxe alors qu'elle a promis qu'il n'y aurait aucune nouvelle taxe ni augmentation d'impôt."}
	ciottiTransport := bundleContradiction{177,
		"Éric Ciotti a annoncé que la gratuité des transports en commun pour les habitants de la Métropole âgés de 65 ans et plus, ainsi que pour les personnels hospitalaux du CHU et les agents pénitentiaires, sera effective à compter du 1er septembre.",
		"Éric Ciotti a écarté l'extension de la gratuité totale des transports publics en raison de la situation budgétaire et d'un plan d'économies de 60 millions d'euros dévoilé en avril.",
		"L'annonce de la mise en œuvre de la gratuité pour certains groupes contredit l'affirmation préalable selon laquelle l'extension de cette gratuité était écartée."}
	lePenInnocence := bundleContradiction{467,
		"Marine Le Pen a affirmé son innocence sur le plateau de TF1 après avoir été reconnue coupable par la cour d'appel.",
		"Marine Le Pen a été reconnue coupable de détournement de fonds au Parlement européen et condamnée à un an sous bracelet électronique et 15 mois d'inéligibilité le 7 juillet 2026.",
		"La condamnation en appel pour détournement de fonds publics contredit directement l'affirmation de sa « bonne foi » et le rejet de l'existence d'un « système »."}
	ciottiPrisonStaff := bundleContradiction{503,
		"Éric Ciotti a déclaré sur X que l'extension de la gratuité aux personnels pénitentiaires est une marque de reconnaissance envers ceux qui exercent des missions essentielles de santé et de sécurité.",
		"Éric Ciotti a écarté l'extension de la gratuité totale des transports publics en raison de la situation budgétaire et d'un plan d'économies de 60 millions d'euros dévoilé en avril.",
		"Le soutien explicite d'Éric Ciotti à l'extension de la gratuité est contredit par sa décision d'écarter cette même extension pour des raisons budgétaires."}
	faureLFI := bundleContradiction{614,
		"Olivier Faure a rappelé que François Hollande avait commencé à 3% avant de devenir président de la République lors des universités d'été.",
		"Olivier Faure a affirmé qu'il n'avait pas de problèmes à passer des accords avec LFI, citant les municipales comme exemple.",
		"Olivier Faure affirme désormais être ouvert à des accords avec LFI, ce qui contredit sa déclaration précédente affirmant qu'aucun accord n'était possible avec Jean-Luc Mélenchon."}
	tanguyVeil := bundleContradiction{659,
		"Jean-Philippe Tanguy a affirmé que le projet de son parti ne vise pas à stigmatiser les femmes portant le voile, mais qu'il est nécessaire d'abandonner certains symboles accaparés par une idéologie.",
		"Jean-Philippe Tanguy a promis l'application d'une amende à toute femme portant le voile en cas de victoire de Marine Le Pen à la présidentielle 2027, lors de son intervention sur BFM-TV le 30 août.",
		"La promesse d'appliquer une amende aux femmes portant le voile contredit l'affirmation selon laquelle le projet ne vise pas à les stigmatiser."}
	glucksmannPrimary := bundleContradiction{675,
		"Raphaël Glucksmann a rejeté l'organisation d'une primaire, la qualifiant de « machine à perdre », préférant parler directement aux Français.",
		"Raphaël Glucksmann est engagé dans la primaire PS-PP qui se déroulera par scrutin électronique sur deux tours les 9-10 et 16-17 octobre.",
		"Raphaël Glucksmann avait rejeté l'organisation d'une primaire, mais il est désormais engagé dans la primaire PS-PP."}
	faurePrimary := bundleContradiction{687,
		"Olivier Faure refuse pour l'heure de participer à une primaire.",
		"Olivier Faure a annoncé sa candidature à la primaire de la gauche et la sortie de son livre-programme intitulé « La Loi du plus juste » ce jeudi.",
		"Olivier Faure a d'abord refusé de participer à une primaire avant d'annoncer sa candidature à celle-ci une semaine plus tard."}
	lecornuTax := bundleContradiction{711,
		"Sébastien Lecornu a promis de ne pas augmenter les impôts lors de l'adoption du budget à l'automne 2026.",
		"Sébastien Lecornu a déclaré refuser le gel du barème de l'impôt sur le revenu.",
		"Le refus de geler le barème de l'impôt contredit l'affirmation selon laquelle le gouvernement n'augmenterait pas les impôts."}
	villepinFuel := bundleContradiction{774,
		"Dominique de Villepin a proposé de mettre un plafond absolu à 2,50 euros pour les carburants.",
		"Dominique de Villepin a proposé d'instaurer un plancher à 1,75 euros pour les carburants.",
		"La proposition d'un plancher de prix est opposée à la proposition d'un plafond de prix."}
	sebaihiNarco := bundleContradiction{792,
		"Sabrina Sebaihi a voté CONTRE le texte de loi : « Renforce les mesures pour lutter contre le narcotrafic »",
		"Sabrina Sebaihi a voté POUR le texte de loi : « Sort la France du piège du narcotrafic »",
		"Sabrina Sebaihi a voté POUR le texte de loi, alors qu'elle est enregistrée comme ayant voté CONTRE."}
	retailleauCentre := bundleContradiction{900,
		"Bruno Retailleau a rejeté toute alliance avec le centrisme macroniste, qualifiant l'« en-même-temps » de « grand vide », et a prôné une union uniquement autour d'une droite assumée.",
		"Bruno Retailleau a présenté une liste d'union avec le centre comprenant Annick Billon et Maxence de Rugy.",
		"Sa position de rejet catégorique de toute alliance avec le centre est contredite par l'action concrète de présenter une liste d'union avec le centre."}
	ciottiParking := bundleContradiction{915,
		"Éric Ciotti a annoncé la suppression du stationnement gratuit dont bénéficiaient les élus niçois.",
		"Eric Ciotti a annoncé que les Niçois bénéficieront de 2 heures de stationnement gratuit ainsi que de la gratuité après 19h dès le 1er septembre 2026.",
		"L'annonce de la suppression du stationnement gratuit est contredite par l'annonce ultérieure d'une mesure de gratuité (2 heures et après 19h) pour le même sujet."}
	dintorniDefense := bundleContradiction{954,
		"Christelle D'Intorni a voté POUR le texte de loi : « Augmente les moyens et la programmation des dépenses de défense pour la période 2024-2030. »",
		"Christelle D'Intorni a voté CONTRE le texte de loi : « Définit la programmation des ressources de la défense pour la période 2024-2030 »",
		"Christelle D'Intorni a voté pour le texte de loi le 18 mai, alors qu'elle a voté contre ce même texte le 4 mai."}

	// The stances, with their prod ids (stance_documents.id).
	stance := func(id int64, label, summary string) storage.StanceCandidate {
		return storage.StanceCandidate{ID: id, ClusterLabel: label, Summary: summary}
	}
	afdFiscal := stance(12827, "politique fiscale",
		"L'AfD s'engage à ne pas instaurer de nouvelles taxes ou d'augmentations d'impôts durant son mandat. Le parti propose également l'abolition de la redevance audiovisuelle.")
	afdCivilProtection := stance(12878, "politique de protection civile et de secours",
		"L'AfD propose d'augmenter le montant annuel de la taxe de protection contre les incendies pour atteindre 9 millions d'euros. Le parti prévoit également la création d'un cadre législatif unifié pour regrouper les missions de secours, de protection contre les incendies et d'aide aux sinistrés.")
	ciottiFreeTransport := stance(14360, "gratuité des transports en commun et mobilité",
		"Éric Ciotti a annoncé la mise en place de la gratuité des transports en commun pour les seniors de plus de 65 ans ainsi que pour certains personnels hospitaliers et pénitentiaires. Cette mesure vise, selon lui, à renforcer la solidarité et le pouvoir d'achat. Il a également proposé le développement de solutions de covoiturage et de groupes d'échanges pour les trajets domicile-travail.")
	ciottiNiceFinances := stance(14172, "gestion des finances publiques et des services municipaux de Nice",
		"Éric Ciotti a mis en place un plan d'économies de 60 millions d'euros à Nice, incluant la suppression de la distribution gratuite de fournitures scolaires pour 22 000 élèves et l'opposition à l'extension de la gratuité des transports publics. Il a également engagé des procédures judiciaires et un signalement auprès du procureur contre Christian Estosi concernant 312 000 euros de frais de représentation. Par ailleurs, il propose de simplifier l'administration et de s'appuyer sur des comités de quartier pour optimiser la gestion locale.")
	ciottiPenal := stance(14293, "politique pénale et sécurité publique",
		"Éric Ciotti défend une politique pénale durcie, prônant notamment la perpétuité réelle pour les pédocriminels, la suppression des aménagements de peine pour les crimes contre les mineurs et l'instauration de peines planchers. Il multiplie les propositions de loi visant à sanctionner financièrement les casseurs et leurs parents, ainsi que les instigateurs de blocages d'établissements scolaires, tout en demandant l'élargissement de l'accès au FIJAIS pour les parents. Par ailleurs, il réclame un renforcement des effectifs carcéraux et une justice exemplaire face aux violences urbaines et aux atteintes à l'ordre public.")
	ciottiLocalTax := stance(14125, "politique fiscale et attractivité économique locale",
		"Les déclarations portent sur la réduction de la pression fiscale et des dépenses publiques pour dynamiser l'économie locale. Les mesures évoquées incluent la baisse des taxes foncières et la mise en place de stationnement gratuit ou facilité en centre-ville. Un différend existe concernant la gestion des charges économiques et des impôts locaux par rapport à d'autres visions politiques.")
	lePenAssistants := stance(15887, "Affaire des assistants parlementaires européens",
		"Marine Le Pen a été condamnée en appel pour détournement de fonds publics, entraînant des peines d'emprisonnement sous bracelet électronique et d'inéligibilité. Elle a fermement contesté ces accusations, affirmant son innocence et dénonçant l'application de certaines qualifications juridiques, tout en annonçant un pourvoi en cassation. Parallèlement, elle a porté plainte contre l'ancien secrétaire général du groupe RN, Ludovic de Danne, pour des faits de faux, usage de faux, abus de confiance et escroquerie. Ses conseils ont par ailleurs sollicité une prorogation de quinze jours pour construire leur argumentaire.")
	lePenCandidacy := stance(15312, "Candidature à l'élection présidentielle de 2027",
		"Marine Le Pen a lancé sa campagne pour 2027 en présentant un binôme avec Jordan Bardella, tout en affirmant que le Rassemblement national et les alliés de l'UDR sont les seuls capables d'obtenir une majorité à l'Assemblée. Sa candidature fait face à une tension juridique majeure : malgré ses affirmations d'innocence et son intention de se pourvoir en cassation après sa condamnation à 15 mois d'inéligibilité, elle a également rejeté les rumeurs de rupture politique.")
	faureHistory := stance(16585, "parcours politiques et figures de l'histoire politique française",
		"Olivier Faure a évoqué le parcours de François Hollande, rappelant son score initial de 3 % avant son accession à la présidence de la République, ainsi que la participation de Bernadette Chirac au combat féministe. Il a également soutenu Manuel Valls lors de la primaire socialiste de 2017 avant de se rallier à Benoît Hamon.")
	faure2027 := stance(16451, "élection présidentielle 2027",
		"Olivier Faure a officialisé sa candidature à la primaire de la gauche pour 2027, prônant l'unité et la fédération pour retrouver une majorité, tout en demandant de cesser de centrer les débats sur Jean-Luc Mélenchon. Il défend une stratégie d'alliances législatives incluant LFI, les écologistes et les communistes, refusant d'exclure tout rapprochement pour ne pas « insulter l'avenir ». Sur le plan budgétaire, il affirme l'impératif de contenir et réduire la dette sans impacter les plus vulnérables, tandis que des tensions internes persistent, notamment après le rejet par les militants d'une primaire ouverte.")
	faurePrimaryCampaign := stance(20190, "campagne pour la primaire de la gauche",
		"Olivier Faure a annoncé sa candidature à la primaire de la gauche avec la publication de son livre-programme « La Loi du plus juste », y dénonçant le capitalisme autoritaire et la fragmentation de la gauche. Il critique la « brutalisation » du débat public, accusant notamment Raphaël Glucksmann de vouloir rendre les acteurs politiques irréconciliables, et avertit ses concurrents qu'il est impossible de prétendre rassembler le pays en adoptant une posture de division. Faure réaffirme sa volonté de défendre le rassemblement de la gauche et des écologistes dans le cadre de la primaire socialiste. Par ailleurs, il a affirmé que l'intégrité des votes de la primaire n'a absolument pas été compromise par la cyberattaque.")
	tanguyVeilStance := stance(18643, "port du voile islamique et laïcité",
		"Jean-Philippe Tanguy a affirmé que le programme de Marine Le Pen prévoyait de sanctionner le port du voile islamique par une amende s'il arrivait au pouvoir.")
	retailleauCandidacy := stance(13397, "candidature présidentielle 2027",
		"Bruno Retailleau a annoncé sa candidature à l'élection présidentielle de 2027, se présentant comme le candidat de la rupture et de la reconstruction face au macronisme. Il a multiplié les déclarations affirmant son intention d'aller jusqu'au bout de sa campagne, tout en refusant catégoriquement toute alliance avec le bloc central ou le Rassemblement national. Sa stratégie repose sur une position de « troisième voie » de droite, visant à s'imposer face à ses rivaux Édouard Philippe et Gabriel Attal, qu'il qualifie de « contaminés » par le quinquennat d'Emmanuel Macron.")
	glucksmannStrategy := stance(16758, "stratégie électorale et alliances de la gauche",
		"Raphaël Glucksmann a initialement exprimé des réserves sur sa participation à l'élection présidentielle de 2027, refusant l'idée d'une primaire qu'il qualifiait de « machine à perdre », avant de soutenir le principe du processus pour trancher la ligne politique. Il s'est depuis montré fermement opposé à toute alliance avec La France insoumise, qu'il juge incompatible avec ses doctrines et potentiellement perdante face à l'extrême droite, tout en promettant une union de la gauche démocratique avec les écologistes et les communistes. Validé pour la primaire, il prône désormais une refondation de la gauche et rejette la stigmatisation des électeurs d'Emmanuel Macron, considérée comme une erreur stratégique.")
	lecornuBudget := stance(17091, "politique budgétaire et fiscale",
		"Sébastien Lecornu a présenté un plan de redressement de 54 milliards d'euros pour 2027, visant à ramener le déficit à 5 % du PIB, tout en promettant une stabilité fiscale et l'absence de hausse d'impôts généralisée. Il a d'abord affirmé qu'il n'utiliserait ni l'article 49.3 ni les ordonnances, avant d'annoncer qu'il ferait appel à l'article 49.3 pour faire adopter le budget sans vote parlementaire, une position qui contredit ses déclarations précédentes. Face à l'impasse, il a proposé une discussion « mesure par mesure » et un compromis avec les socialistes, tout en menaçant d'une austérité dure en cas d'absence de budget et en saisissant la justice pour des fuites médiatiques.")
	lecornuLycees := stance(17178, "gouvernement Lecornu et gestion de la crise lycéenne",
		"Sébastien Lecornu a présenté son gouvernement comme une équipe de mission, libre de toute contrainte partisane et non engagée dans la campagne présidentielle de 2027. Face au mouvement de blocage des lycées, il a confié à ses ministres une feuille de route en cinq chantiers et exigé la présentation de premières décisions chiffrées avant la fin d'octobre 2026. Il a par ailleurs promis de prendre le temps nécessaire pour rechercher des compromis avec les parlementaires, tout en saluant la mobilisation sur la plateforme de consultation Agora.")
	villepinBudget := stance(13922, "politique budgétaire et financement de la protection sociale",
		"Les propositions portent sur la mise en place d'un cadre législatif pour limiter les déficits publics sur un cycle de cinq ans. Ces mesures incluent également la révision des niches fiscales et une réforme du financement de la protection sociale par l'élargissement de l'assiette de revenus.")
	sebaihiFraud := stance(27117, "lutte contre les fraudes sociales et fiscales",
		"Sabrina Sebaihi a voté CONTRE le texte de loi : « Renforce la lutte contre les fraudes sociales et fiscales. »")
	dintorniMinors := stance(18435, "sécurité et protection des mineurs",
		"Christelle D'intorni a exprimé son positionnement sur diverses mesures de sécurité, notamment en faveur du renforcement de la protection des commerçants face au narcotrafic. Elle s'est opposée à certains amendements concernant la protection des mineurs sur les réseaux sociaux, mais elle a également voté pour le texte de loi visant à répondre aux violences sexuelles et sexistes contre les femmes et les enfants. Par ailleurs, elle a soutenu le maintien du préfet comme signataire des contrats d'association pour les établissements scolaires privés et a déposé une plainte concernant un dispositif numérique hostile à Éric Ciotti.")

	cases := []struct {
		name   string
		contr  bundleContradiction
		stance storage.StanceCandidate
		want   bool // true: the contradiction belongs to the stance
		reason string
		gap    string // a known miss of the local model: logged, not failed (llmtest.KnownGap)
	}{
		// Positive cases.
		{"afd_tax_promise_vs_fiscal", afdTax, afdFiscal, true,
			"the broken promise is 'no new tax'; the fiscal stance holds that promise", ""},
		{"ciotti_transport_vs_free_transport", ciottiTransport, ciottiFreeTransport, true,
			"both facts are about free public transport, the issue of the stance", ""},
		{"ciotti_transport_vs_nice_finances", ciottiTransport, ciottiNiceFinances, true,
			"hard: the finance stance names the refusal to extend free transport, which is claim B", ""},
		{"lepen_innocence_vs_assistants_case", lePenInnocence, lePenAssistants, true,
			"the contradiction is the conviction in the assistants case against her claim of innocence", ""},
		{"tanguy_veil_vs_veil_stance", tanguyVeil, tanguyVeilStance, true,
			"the contradiction is about the fine on the veil, the issue of the stance", ""},
		{"faure_primary_vs_primary_campaign", faurePrimary, faurePrimaryCampaign, true,
			"the reversal is about his participation in the primary of the left", ""},
		{"faure_primary_vs_2027", faurePrimary, faure2027, true,
			"the 2027 stance records his candidacy at the primary, the act of claim B", ""},
		{"retailleau_centre_vs_candidacy", retailleauCentre, retailleauCandidacy, true,
			"the stance holds his refusal of any alliance with the central bloc, claim A",
			"the local model does not find the commitment inside the candidacy stance (2026-10-11); Opus and the seat run pass it"},
		{"ciotti_parking_vs_local_tax", ciottiParking, ciottiLocalTax, true,
			"the stance names free parking in the city centre, the issue of both claims", ""},
		{"lecornu_tax_vs_budget", lecornuTax, lecornuBudget, true,
			"the stance holds his promise of no general tax increase, claim A", ""},
		{"glucksmann_primary_vs_strategy", glucksmannPrimary, glucksmannStrategy, true,
			"the stance records the 'machine à perdre' refusal and his later support of the primary", ""},

		// Negative cases.
		{"afd_tax_promise_vs_civil_protection", afdTax, afdCivilProtection, false,
			"prod said YES (bundle 12878), wrong: the broken promise is fiscal; the stance is about rescue services and only shares the fire tax", ""},
		{"ciotti_prison_staff_vs_penal", ciottiPrisonStaff, ciottiPenal, false,
			"only the word 'pénitentiaire' is shared; the contradiction is about free transport", ""},
		{"lepen_innocence_vs_candidacy", lePenInnocence, lePenCandidacy, false,
			"hard: the candidacy summary mentions the conviction, but the issue of the contradiction is the court case", ""},
		{"faure_lfi_vs_history", faureLFI, faureHistory, false,
			"the reason is about accords with LFI; the stance is about anecdotes on Hollande and Valls", ""},
		{"dintorni_defense_vs_minors", dintorniDefense, dintorniMinors, false,
			"defence programming votes have no link to the protection of minors", ""},
		{"sebaihi_narcotrafic_vs_fraud", sebaihiNarco, sebaihiFraud, false,
			"hard: both are crime laws, but the fraud law is a different text from the two narcotrafic laws", ""},
		{"lecornu_tax_vs_lycees", lecornuTax, lecornuLycees, false,
			"the stance is about the government team and the school blockades, not taxes", ""},
		{"villepin_fuel_vs_budget", villepinFuel, villepinBudget, false,
			"fuel price cap and floor have no link to the deficit and social protection financing", ""},
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
				got, err := b.askLLMRelated(ctx, bundleQuery(tc.contr.a, tc.contr.b, tc.contr.reason), tc.stance)
				if err != nil {
					t.Fatalf("bundler gate error: %v", err)
				}
				mu.Lock()
				defer mu.Unlock()
				mismatch := fmt.Sprintf("got related=%v want related=%v (contradiction %d, stance %d %q)\n    label reason: %s",
					got, tc.want, tc.contr.id, tc.stance.ID, tc.stance.ClusterLabel, tc.reason)
				switch {
				case got == tc.want:
					passed++
					t.Logf("related=%v ✓", got)
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
	t.Logf("=== BUNDLE GOLDEN: %d passed, %d failed, %d known gaps (of %d) ===", passed, failed, gaps, len(cases))
}
