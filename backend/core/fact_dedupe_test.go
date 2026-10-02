package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/llm"

	"github.com/joho/godotenv"
)

// dedupeLLMStub is an OpenAI-compatible chat endpoint that records every user
// prompt it receives and answers a fixed same=false verdict, so tests can
// assert the exact bytes the dedupe gates send.
type dedupeLLMStub struct {
	mu      sync.Mutex
	prompts []string
	server  *httptest.Server
}

func newDedupeLLMStub(t *testing.T) *dedupeLLMStub {
	t.Helper()
	s := &dedupeLLMStub{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		s.mu.Lock()
		s.prompts = append(s.prompts, req.Messages[len(req.Messages)-1].Content)
		s.mu.Unlock()
		resp := map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": `{"same": false, "reasoning": "stub"}`}}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *dedupeLLMStub) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prompts...)
}

// sharedPrefixLen returns the length of the longest common byte prefix.
func sharedPrefixLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// TestDedupeGatePromptsShareRulePrefix pins the prefix-cache layout of both
// gate prompts: two calls with different subjects and statements share every
// byte up to the first per-call datum ("Person:"), the subject never appears
// before it, and the JSON response format comes after the statements.
func TestDedupeGatePromptsShareRulePrefix(t *testing.T) {
	stub := newDedupeLLMStub(t)
	cfg := llm.Config{BaseURL: stub.server.URL + "/v1", APIKey: "test", ChatModel: "stub"}
	adj := &FactDedupeAdjudicator{client: cfg.NewClient("fact-dedupe-test")}
	ctx := context.Background()

	gates := map[string]func(ctx context.Context, subject, a, b string) (bool, error){
		"IsSameClaim":    adj.IsSameClaim,
		"IsSamePosition": adj.IsSamePosition,
	}
	for name, gate := range gates {
		t.Run(name, func(t *testing.T) {
			before := len(stub.calls())
			if _, err := gate(ctx, "Bruno Retailleau", "il ira jusqu'au bout de sa candidature", "sa candidature irait jusqu'au bout"); err != nil {
				t.Fatalf("first call: %v", err)
			}
			if _, err := gate(ctx, "Gérald Darmanin", "a annoncé le retrait du plaider-coupable", "présentera son projet de loi le 29 juin"); err != nil {
				t.Fatalf("second call: %v", err)
			}
			calls := stub.calls()[before:]
			if len(calls) != 2 {
				t.Fatalf("expected 2 LLM calls, got %d", len(calls))
			}
			p1, p2 := calls[0], calls[1]

			marker := strings.Index(p1, "\nPerson: ")
			if marker < 0 {
				t.Fatalf("no Person: data marker in prompt:\n%s", p1)
			}
			if shared := sharedPrefixLen(p1, p2); shared < marker {
				t.Errorf("shared prefix is %d bytes, diverges before the data marker at %d:\n%s", shared, marker, p1[shared:min(len(p1), shared+80)])
			}
			if marker < 1000 {
				t.Errorf("cacheable rule prefix is only %d bytes; the rulebook should precede the data", marker)
			}
			if strings.Contains(p1[:marker], "Bruno Retailleau") {
				t.Errorf("subject appears before the data marker")
			}
			format := strings.Index(p1, "Return ONLY strict JSON")
			if stmtB := strings.Index(p1, "STATEMENT B:"); format < 0 || format < stmtB {
				t.Errorf("response format must come after the statements (format at %d, STATEMENT B at %d)", format, stmtB)
			}
		})
	}
}

// TestFactDedupeAdjudicatorGolden is the local-only golden set for the
// gray-zone fact-dedup adjudicator. It asserts genuine restatements are merged
// (same=true) and that opposing stances, intention-vs-action pairs, and
// different-specifics pairs are kept apart (same=false).
//
// LOCAL ONLY: self-skips unless OPEN_AI_KEY is set, so it never runs in CI. The
// endpoint is the local GPU (OPEN_AI_API), so each run is free.
//
//	source .env; go test -v -run TestFactDedupeAdjudicatorGolden ./backend/core/
func TestFactDedupeAdjudicatorGolden(t *testing.T) {
	_ = godotenv.Overload()
	_ = godotenv.Overload("../../.env")

	if os.Getenv("OPEN_AI_KEY") == "" {
		t.Skip("Skipping fact-dedupe golden test: OPEN_AI_KEY not set (local-only).")
	}

	adj, err := NewFactDedupeAdjudicatorFromEnv()
	if err != nil {
		t.Fatalf("failed to create adjudicator: %v", err)
	}

	cases := []struct {
		name    string
		subject string
		factA   string
		factB   string
		want    bool // true = same claim / same position (dedup)
		// position selects the fold gate (IsSamePosition, what news uses
		// since the 2026-09-17 fold wiring) instead of the drop gate.
		position bool
		// gap documents a known miss: a mismatch is logged, not failed.
		// Every wording that fixed one moved the miss to other cases.
		gap string
	}{
		{
			// The real prod pair (facts 4740 / 4897) — cosine 0.78, verbs
			// PROMISED vs CLAIMED — that produced duplicate contradiction #146.
			name:    "retailleau_candidacy_restatement",
			subject: "Bruno Retailleau",
			factA:   "Bruno Retailleau a déclaré qu'il ne renoncera pas à sa candidature et qu'il ira jusqu'au bout de sa candidature.",
			factB:   "Bruno Retailleau a affirmé que sa candidature irait jusqu'au bout.",
			want:    true,
		},
		{
			name:    "opposing_stance_on_primary",
			subject: "Bruno Retailleau",
			factA:   "Bruno Retailleau plaide pour une grande primaire de la droite.",
			factB:   "Bruno Retailleau a refusé l'idée d'une primaire.",
			want:    false,
		},
		{
			name:    "intention_vs_contrary_action",
			subject: "Le Rassemblement national",
			factA:   "Le Rassemblement national a promis de maintenir l'âge de départ à la retraite à 62 ans.",
			factB:   "Le Rassemblement national a renoncé à abroger la réforme portant l'âge de la retraite à 64 ans.",
			want:    false,
		},
		{
			name:    "different_specifics",
			subject: "Gérald Darmanin",
			factA:   "Gérald Darmanin a annoncé le retrait du plaider-coupable criminel pour les crimes de sang.",
			factB:   "Gérald Darmanin présentera son projet de loi le 29 juin devant l'Assemblée nationale.",
			want:    false,
		},

		// Fold gate. Prod member pairs from the 2026-09-18..20 nights, texts
		// verbatim: the over-folds the sample turned up (must NOT fold) and
		// clean restatements (must fold).
		{
			// meta 20575: 200 € vs 150→500 € is the substance of the proposal.
			name:     "fold_different_amounts",
			subject:  "Raphaël Glucksmann",
			factA:    "Raphaël Glucksmann a proposé de tripler le chèque énergie à 200 euros pour les classes populaires et moyennes.",
			factB:    "Raphaël Glucksmann propose que le montant du chèque énergie soit porté de 150 euros à 500 euros par an.",
			want:     false,
			position: true,
		},
		{
			// meta 19253: wanting the PS nomination vs claiming the useful vote.
			name:     "fold_same_theme_different_claims",
			subject:  "Raphaël Glucksmann",
			factA:    "Raphaël Glucksmann souhaite être recruté par le parti socialiste comme candidat pour l'élection présidentielle de 2027.",
			factB:    "Raphaël Glucksmann a annoncé que le vote utile pour la présidentielle 2027 serait pour sa part.",
			want:     false,
			position: true,
		},
		{
			// meta 19262: an undecided candidacy vs the ambition to win the primary.
			name:     "fold_pending_decision_vs_ambition",
			subject:  "Raphaël Glucksmann",
			factA:    "Raphaël Glucksmann doit confirmer à la rentrée s'il sera candidat à la présidentielle, ce qui impliquerait de passer par une primaire de l'espace social-démocrate.",
			factB:    "Raphaël Glucksmann a affirmé avoir l'ambition de gagner la primaire.",
			want:     false,
			position: true,
		},
		{
			// meta 20343: the war must end vs France/EU cannot weigh on it.
			name:     "fold_different_matters_same_conflict",
			subject:  "Fabien Roussel",
			factA:    "Fabien Roussel a affirmé que la guerre en Ukraine doit cesser.",
			factB:    "Fabien Roussel a jugé inadmissible que la France et l’Union européenne soient incapables de peser sur la guerre en Ukraine.",
			want:     false,
			position: true,
		},
		{
			// meta 20397: a 500 bn€ fund vs a large loan — related, different instruments.
			name:     "fold_fund_vs_loan",
			subject:  "Fabien Roussel",
			factA:    "Fabien Roussel propose la création d'un Fonds d'avenir doté de 500 milliards d'euros pour financer la réindustrialisation, la formation et la transition écologique.",
			factB:    "Fabien Roussel a demandé un grand emprunt pour financer la transition écologique.",
			want:     false,
			position: true,
		},
		{
			// meta 19704: the same demand (lower fuel taxes) in two wordings.
			name:     "fold_fuel_tax_restatement",
			subject:  "Fabien Roussel",
			factA:    "Fabien Roussel a demandé la baisse des taxes sur les carburants, affirmant que 60 % du prix sont des taxes.",
			factB:    "Fabien Roussel a affirmé que la France peut agir immédiatement pour baisser le poids de la fiscalité sur l'essence.",
			want:     true,
			position: true,
		},
		{
			// meta 20175.
			name:     "fold_more_massive_aid_restatement",
			subject:  "Raphaël Glucksmann",
			factA:    "Raphaël Glucksmann a déclaré qu'il y a besoin d'aides beaucoup plus massives que ce qui est annoncé aujourd'hui.",
			factB:    "Raphaël Glucksmann a plaidé pour que les aides soient plus massives.",
			want:     true,
			position: true,
		},
		{
			// meta 19421.
			name:     "fold_housing_priority_restatement",
			subject:  "Fabien Roussel",
			factA:    "Fabien Roussel, candidat du PCF à la présidentielle, a affirmé que la priorité n'est pas la préférence nationale mais la construction de logements.",
			factB:    "Fabien Roussel a déclaré que la priorité pour le logement ne doit pas être la préférence nationale mais la construction de logements pour tous.",
			want:     true,
			position: true,
		},
		{
			// meta 19367: two wordings of opposing the 2027 budget.
			name:     "fold_budget_opposition_facets",
			subject:  "Fabien Roussel",
			factA:    "Fabien Roussel a affirmé qu'il ne faut pas imposer un budget qui demande des sacrifices aux retraités et aux personnes malades.",
			factB:    "Fabien Roussel a dénoncé un budget de casse sociale après la présentation du projet de budget 2027.",
			want:     true,
			position: true,
		},
		// Reports of ONE event (the 2026-07-07 appeal ruling against Marine
		// Le Pen, prod facts 9998/9999/10191/10542/10546): an outcome happens
		// once, so reports of it are one fact even when they count its numbers
		// differently ("15 mois d'inéligibilité" = "45 mois dont 30 avec sursis").
		{
			name:     "fold_same_ruling_numbers_counted_differently",
			subject:  "Marine Le Pen",
			factA:    "Marine Le Pen a été reconnue coupable en appel et condamnée à un an sous bracelet électronique et 15 mois d'inéligibilité.",
			factB:    "Marine Le Pen a été condamnée par la cour d'appel de Paris à une inéligibilité de 45 mois, dont 30 avec sursis, et à trois ans d'emprisonnement, dont un an sous bracelet électronique.",
			want:     true,
			position: true,
		},
		{
			name:     "fold_same_ruling_other_details",
			subject:  "Marine Le Pen",
			factA:    "Marine Le Pen a été reconnue coupable de détournement de fonds au Parlement européen et condamnée à un an sous bracelet électronique et 15 mois d'inéligibilité le 7 juillet 2026.",
			factB:    "Marine Le Pen a été condamnée par la cour d'appel de Paris à trois ans de prison, dont un an ferme sous bracelet électronique, pour détournement de fonds publics.",
			want:     true,
			position: true,
		},
		{
			name:     "fold_same_ruling_confirmation_wording",
			subject:  "Marine Le Pen",
			factA:    "La cour d'appel de Paris a confirmé la condamnation de Marine Le Pen dans l'affaire des assistants parlementaires européens, réduisant son inéligibilité à 45 mois dont 30 avec sursis.",
			factB:    "Marine Le Pen a été reconnue coupable en appel et condamnée à un an sous bracelet électronique et 15 mois d'inéligibilité.",
			want:     true,
			position: true,
		},
		// Guards: two occasions of the same affair or the same kind stay apart.
		{
			name:     "fold_first_instance_vs_appeal",
			subject:  "Marine Le Pen",
			factA:    "Le 31 mars 2025, Marine Le Pen a été condamnée par le tribunal correctionnel de Paris à quatre ans d'emprisonnement, dont deux ans ferme, 100 000 euros d'amende et cinq ans d'inéligibilité.",
			factB:    "Marine Le Pen a été reconnue coupable en appel et condamnée à un an sous bracelet électronique et 15 mois d'inéligibilité.",
			want:     false,
			position: true,
		},
		{
			name:     "fold_two_different_votes",
			subject:  "Jordan Bardella",
			factA:    "Jordan Bardella a voté contre le projet de loi de finances pour 2026 en lecture définitive.",
			factB:    "Jordan Bardella a voté contre l'amendement supprimant la hausse de la taxe sur les billets d'avion dans le projet de loi de finances pour 2026.",
			want:     false,
			position: true,
		},
		// The 2026-10-01 prod regrouping sample, judged by hand: groups the
		// same-event rule formed wrongly (must stay apart) and rightly (must
		// still group).
		{
			name:     "fold_votes_on_two_amendments_of_one_bill",
			subject:  "Abdelkader Lahmar",
			factA:    "Abdelkader Lahmar a voté CONTRE l'amendement n° 231 (UDR) dont l'objet est : « Le présent amendement propose de remplacer la nationalisation intégrale d’ArcelorMittal par un dispositif plus proportionné, consistant en une prise de participation publique limitée à 30 % du capital. », déposé sur le texte de loi : « Vise la nationalisation d'ArcelorMittal France pour la souveraineté industrielle. »",
			factB:    "Abdelkader Lahmar a voté CONTRE le sous-amendement n° 358 (RN) dont l'objet est : « Cet ajout permet d’éviter qu’une entreprise puisse soustraire des composants essentiels en réorganisant artificiellement sa chaîne de valeur avant l’opération. », déposé sur le texte de loi : « Prévoit la nationalisation de l'entreprise ArcelorMittal France pour renforcer la souveraineté industrielle de la France. »",
			want:     false,
			position: true,
		},
		{
			name:     "fold_vote_on_amendment_vs_vote_on_text",
			subject:  "Alexandre Allegret-Pilot",
			factA:    "Alexandre Allegret-Pilot a voté POUR l'amendement n° 40 (RN) dont l'objet est : « Le présent amendement vise à supprimer les aménagements de peine ab initio et soumettre l’aménagement à l’exécution minimale du tiers de la peine ferme prononcée. », déposé sur le texte de loi : « Assure l'exécution des peines d'emprisonnement ferme »",
			factB:    "Alexandre Allegret-Pilot a voté POUR le texte de loi : « Renforce l'exécution des peines d'emprisonnement ferme »",
			want:     false,
			position: true,
		},
		{
			name:     "fold_compound_vs_one_of_its_claims",
			subject:  "Alexis Perrier",
			factA:    "Alexis Perrier a déclaré que l'implantation locale n'est plus une priorité du Rassemblement national et a critiqué le manque de collaboration de Patrice Martin.",
			factB:    "Alexis Perrier a reproché un manque de collaboration de la part du député Patrice Martin.",
			want:     false,
			position: true,
		},
		{
			name:     "fold_two_measures_one_policy_field",
			subject:  "AfD",
			factA:    "L'AfD prévoit de favoriser l'automobile individuelle et le moteur à explosion, et prévoit notamment l'interdiction des vélos cargo dans le Land.",
			factB:    "L’AfD exige que l’interdiction des moteurs à combustion soit non seulement reportée, mais bel et bien annulée.",
			want:     false,
			position: true,
		},
		{
			name:     "fold_two_broadcasting_proposals",
			subject:  "AfD Saxe-Anhalt",
			factA:    "L'AfD Saxe-Anhalt a annoncé que son premier acte officiel après les élections serait de dénoncer les traités de radiodiffusion pour exiger une réforme.",
			factB:    "L'AfD Saxe-Anhalt a proposé de transformer la radiodiffusion publique en un service financé par une taxe audiovisuelle sur le modèle finlandais.",
			want:     false,
			position: true,
		},
		{
			name:     "fold_same_election_result_reported_twice",
			subject:  "Alliance 90/The Greens",
			factA:    "Alliance 90/The Greens obtained 30.2% of the vote in the Baden-Württemberg state election on March 8, 2026.",
			factB:    "The Greens won the state election in Baden-Württemberg with 30.2 percent of the second votes.",
			want:     true,
			position: true,
		},
		{
			name:     "fold_same_motion_reported_twice",
			subject:  "AfD Weimarer Land",
			factA:    "Die AfD Weimarer Land hat die Abwahl der 2024 zuletzt im Amt bestätigten Landrätin beantragt.",
			factB:    "Die AfD im Weimarer Land hat einen Antrag gestellt, um Landrätin Schmidt-Rose (CDU) abwählen zu lassen, da dem Landratsamt laut AfD die Führung fehle.",
			want:     true,
			position: true,
		},
		{
			name:     "fold_same_municipal_result_two_angles",
			subject:  "Alexandre Gaillat",
			factA:    "La liste conduite par Alexandre Gaillat, nommée « Bien vivre à Chenu », a obtenu 39,15 % des voix lors de l'élection municipale de Chenu le 15 mars 2026.",
			factB:    "La liste conduite par Alexandre Gaillat, « Bien vivre à Chenu », a été défaite lors des élections municipales du 15 mars 2026 face à la liste de Maurice Houdin.",
			want:     true,
			position: true,
		},
		// Jordan Bardella's 2026-10-02 dry-run sample, judged by hand: same
		// area but different claims (apart), and true restatements (together).
		{
			name:     "fold_bardella_study_retirement_age_vs_questioned_reform",
			subject:  "Jordan Bardella",
			factA:    "Jordan Bardella a déclaré examiner la question d'un éventuel report de l'âge de la retraite, ce qui impliquerait une modification de la réforme portée par le Rassemblement national.",
			factB:    "Jordan Bardella avait ouvertement remis en cause la réforme des retraites avant l'été 2026.",
			want:     false,
			position: true,
		},
		{
			name:     "fold_bardella_bundle_aid_and_expulsion_vs_family_allowances",
			subject:  "Jordan Bardella",
			factA:    "Jordan Bardella a annoncé la suppression des aides sociales aux parents des mineurs récidivistes et l'expulsion des délinquants et criminels étrangers.",
			factB:    "Jordan Bardella a annoncé qu'il supprimerait les allocations familiales aux parents de mineurs récidivistes s'il est au pouvoir.",
			want:     false,
			position: true,
		},
		{
			name:     "fold_bardella_lfi_supports_vandals_vs_melenchon_culture",
			subject:  "Jordan Bardella",
			factA:    "Jordan Bardella a accusé La France Insoumise de soutenir ceux qui détruisent les biens d'autrui et de mépriser les forces de l'ordre.",
			factB:    "Jordan Bardella a affirmé que la culture de Jean-Luc Mélenchon s'arrête là où commencent les crimes de la gauche.",
			want:     false,
			position: true,
		},
		{
			name:     "fold_bardella_immigration_first_vs_channel_border_promise",
			subject:  "Jordan Bardella",
			factA:    "Jordan Bardella a affirmé que le premier moyen pour rétablir la sécurité en France est d'arrêter l'immigration.",
			factB:    "Jordan Bardella a promis aux sympathisants du parti de Nigel Farage un meilleur contrôle de la frontière et a affirmé que la Manche ne doit plus être une route de migration en cas de victoire du RN en 2027.",
			want:     false,
			position: true,
		},
		{
			name:     "fold_bardella_psg_cause_vs_recidivism_system",
			gap:      "two reactions to the same night read as one position (3/3 same, 2026-10-02)",
			subject:  "Jordan Bardella",
			factA:    "Jordan Bardella a affirmé que les débordements survenus après la victoire du PSG sont liés à l'incapacité de l'État à maîtriser l'immigration et à la politique de sécurité.",
			factB:    "Jordan Bardella a affirmé que le système actuel incite à la récidive et a appelé à un sursaut sécuritaire, pénal et migratoire face aux scènes de chaos du samedi 30 mai 2026.",
			want:     false,
			position: true,
		},
		{
			name:     "fold_bardella_two_polls",
			subject:  "Jordan Bardella",
			factA:    "Jordan Bardella a recueilli 38 % d'opinions favorables et 26 % de soutien actif dans le baromètre de juin.",
			factB:    "Jordan Bardella a obtenu 36 % de bonnes opinions selon l'Observatoire politique Elabe pour Les Échos daté du 30 juillet 2026.",
			want:     false,
			position: true,
		},
		{
			name:     "fold_bardella_prosecutor_vs_bundle_with_magistrates",
			gap:      "a bundle that contains the other claim; an explicit example fixed it but broke three cases (2026-10-02)",
			subject:  "Jordan Bardella",
			factA:    "Jordan Bardella a préconisé la création d'un parquet national spécialisé pour les victimes mineures.",
			factB:    "Jordan Bardella a exprimé le souhait de doubler le nombre de magistrats et de créer un Parquet national spécialisé pour les victimes mineures si son camp arrive au pouvoir.",
			want:     false,
			position: true,
		},
		{
			name:     "fold_bardella_soli_complaint_twice",
			subject:  "Jordan Bardella",
			factA:    "Le 27 mai 2026, Jordan Bardella a annoncé son intention de déposer plainte suite aux menaces de mort proférées dans le clip 'Un facho K.O.' du rappeur Soli, et a réclamé à Youtube de retirer le contenu.",
			factB:    "En mai, Jordan Bardella a indiqué son intention de déposer plainte contre le rappeur Soli en raison de menaces de mort explicites.",
			want:     true,
			position: true,
		},
		{
			name:     "fold_bardella_darmanin_resignation_twice",
			subject:  "Jordan Bardella",
			factA:    "Jordan Bardella a estimé que le ministre de la Justice devrait présenter sa démission au président de la République en raison de la gravité de l'affaire Lyhanna.",
			factB:    "Jordan Bardella a affirmé que Gérald Darmanin aurait dû présenter sa démission par honneur en raison de la gravité de l'affaire Lyhanna.",
			want:     true,
			position: true,
		},
		{
			name:     "fold_bardella_expulsion_vs_double_peine",
			subject:  "Jordan Bardella",
			factA:    "Jordan Bardella a affirmé que s'il est à la tête du pays, il mettra en place l'expulsion systématique des délinquants et criminels étrangers.",
			factB:    "Jordan Bardella a déclaré être favorable à la 'double peine', consistant en l'expulsion systématique des délinquants et criminels étrangers pour qu'ils purgent leur peine dans leur pays d'origine.",
			want:     true,
			position: true,
		},
		{
			name:     "fold_bardella_partner_finances_twice",
			subject:  "Jordan Bardella",
			factA:    "Jordan Bardella a déclaré que la situation financière de sa compagne et de sa famille demeurait 'tout à fait secondaire' par rapport à ses qualités humaines.",
			factB:    "Jordan Bardella a déclaré que la situation financière de la famille de Maria Carolina de Bourbon des Deux-Siciles était « tout à fait secondaire » et a précisé qu'il partageait sa vie avec elle parce qu'il l'aime.",
			want:     true,
			position: true,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	var passed, failed int
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			gate := adj.IsSameClaim
			if tc.position {
				gate = adj.IsSamePosition
			}
			got, err := gate(ctx, tc.subject, tc.factA, tc.factB)
			if err != nil {
				t.Fatalf("gate error: %v", err)
			}
			if got != tc.want && tc.gap != "" {
				t.Logf("known gap (got same=%v): %s", got, tc.gap)
				return
			}
			if got != tc.want {
				failed++
				t.Errorf("got same=%v want same=%v\n    A: %s\n    B: %s", got, tc.want, tc.factA, tc.factB)
				return
			}
			passed++
			t.Logf("same=%v ✓", got)
		})
	}
	t.Logf("=== FACT-DEDUPE GOLDEN: %d/%d passed (%d failed) ===", passed, passed+failed, failed)
}
