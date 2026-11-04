package frpolitics

import (
	"strings"
	"testing"

	"digital-mirror/backend/llmtest"
)

// TestAmendmentStatementGolden: the statement of an amendment says what it
// changes, from the dispositif, not the context its exposé opens with. The
// three amendments of the audit of 2026-11-04 whose ballots quoted an exposé
// that hid the change (RN n° 43 read as a vote on family allowances).
//
// LOCAL ONLY: it skips without a model (llmtest.Setup).
//
//	go test -v -run TestAmendmentStatementGolden ./backend/plugins/frpolitics/
func TestAmendmentStatementGolden(t *testing.T) {
	llmtest.Setup(t)
	ctx, cancel := llmtest.Context(t)
	defer cancel()
	cases := []struct {
		name, title, dispositif, expose string
		want                            []string // lower-case words the statement must hold
	}{
		{name: "rn43_allocations_nationalite",
			title:      "l'amendement n° 43 de M. Ranc à l'article unique de la proposition de loi visant à étendre le versement des allocations familiales dès le premier enfant",
			dispositif: "Compléter cet article par l’alinéa suivant : « III. - Le présent article s’applique aux familles dont au moins l’un des deux parents est de nationalité française. »",
			expose:     "Le versement des allocations familiales a pour objectif de consolider la communauté nationale en aidant les familles à assumer au mieux la charge de leurs enfants. Les allocations familiales peuvent parfois constituer des incitations alléchantes pour ceux qui viennent en France dans le but de bénéficier de manière indue de la solidarité nationale. Pourtant, comme le rappelle l’exposé des motifs de cette proposition de loi, beaucoup de familles françaises peinent déjà à avoir le nombre d’enfants qu’elles désirent, notamment pour des raisons financières. Afin de pouvoir accompagner les familles françaises en veillant à ce que l’argent ne soit pas un obstacle à leur désir d’enfant, il est nécessaire que les prestations familiales leur soient distribuées en priorité. Le présent amendement vise donc à réserver les allocations familiales aux personnes de nationalité française ou à un ménage dont l’un des deux parents est de nationalité française. Cette mesure permettra de renouer avec l’objectif initial de la politique familiale française et nous donnera la possibilité de réaliser des économies substantielles à cette époque d’incertitudes budgétaires.",
			want:       []string{"nationalit", "français"}},
		{name: "lfi14_ecrans_information_sanitaire",
			title:      "l'amendement n° 14 de Mme Panot visant à rétablir l'article 3 de la proposition de loi visant à protéger les jeunes des risques liés à l'exposition aux écrans",
			dispositif: "Rétablir cet article dans la rédaction suivante : « Le chapitre III du titre III du livre I er de la deuxième partie du code la santé publique est complété par deux articles L. 2133‑3 et L. 2133‑4 ainsi rédigés : « Art. L. 2133‑3. – Les messages publicitaires en faveur de services de réseaux sociaux en ligne définis à l’article 6 de la loi n° 2004‑575 du 21 juin 2004 pour la confiance dans l’économie numérique doivent contenir une information à caractère sanitaire. Dans le cas des messages publicitaires sur internet, télévisés ou radiodiffusés, cette obligation ne s’applique qu’aux messages émis et diffusés à partir du territoire français et reçus sur ce territoire. La même obligation d’information s’impose à toute promotion, destinée au public, par voie d’imprimés et de publications périodiques édités par les producteurs ou distributeurs de ces services. « Le non‑respect de cette obligation d’information par les annonceurs et promoteurs est puni de 37 500 euros d’amende. Le montant de cette amende peut être porté à 30 % des dépenses consacrées à l’émission et à la diffusion des messages mentionnés au premier alinéa ou à la réalisation et à la distribution des imprimés et publications mentionnés au même premier alinéa. « Les modalités d’application du présent article sont déterminées par décret en Conseil d’État. » « Art. L. 2133‑4. – Les unités de conditionnement, emballages extérieurs, suremballages et boîtes de téléphones mobile et autres équipements terminaux de communica",
			expose:     "Par cet amendement, le groupe LFI réitère son soutien à une approche préventive des risques posés par une surexposition incontrôlée aux écrans et aux contenus qui s'y affichent. Supprimé en commission sur des considérations qui n'avaient aucun lien avec le fond des dispositions du présent article, ce dernier prévoit notamment la diffusion d'une information à caractère sanitaire dans les messages publicitaires en faveur de services de réseaux sociaux et est inspirée d'une obligation similaire applicable en matière de publicité en faveur de certains aliments ou boissons (art. L. 2133-1 du code de la santé publique : « les messages publicitaires en faveur de boissons avec ajouts de sucres, de sel ou d’édulcorants de synthèse ou de produits alimentaires manufacturés doivent contenir une information à caractère sanitaire »). Or, dans le cadre des travaux de la commission d'enquête sur les effets psychologiques de TikTok sur les mineurs, Mme Sarah Sauneron, directrice générale de la santé par intérim, indique que : « ces messages publicitaires font l’objet d’évaluations rigoureuses menées par l’Institut national de la santé et de la recherche médicale (Inserm) et par Santé publique France (SPF). Les études démontrent que ces messages, mis en place depuis 2007, ont significativement contribué à une meilleure connaissance des repères nutritionnels, désormais bien intégrés par la population ». Par conséquent, l'extension de ce principe aux réseaux sociaux ne peut qu'être bénéfique et ",
			want:       []string{"sanitaire", "réseaux sociaux"}},
		{name: "ecos122_troubles_mentaux",
			title:      "l'amendement n° 122 de Mme Regol à l'article premier de la proposition de loi relative à la rétention des personnes condamnées",
			dispositif: "Compléter cet article par l’alinéa suivant : « VI. – Les dispositions prévues au présent article ne sont pas applicables aux personnes condamnées à une peine privative de liberté, y compris pendant son exécution ou à son issue, lorsqu’elles n’ont pas été mises en mesure de bénéficier, au cours de l’exécution de leur peine, d’une prise en charge adaptée aux troubles mentaux dont elles souffrent. »",
			expose:     "Cet amendement du groupe Écologiste et social vise à mettre en lumière l’un des objectifs de l’article 1er de la présente proposition de loi, qui tend, en réalité, à étendre les possibilités de privation de liberté à l’issue de l’exécution d'une peine, y compris pour des personnes ne remplissant pas les conditions de la rétention de sûreté telles qu’encadrées par le Conseil constitutionnel. En l’état du texte, le préfet pourrait imposer à une personne, à sa sortie de détention, de se soumettre à un examen psychiatrique. En cas de refus de s’y présenter, celle-ci pourrait être contrainte, pendant une durée pouvant aller jusqu’à vingt-quatre heures supplémentaires, et donc privée de liberté, de subir cet examen de manière coercitive. Outre les graves atteintes aux libertés publiques que soulève un tel dispositif, celui-ci repose sur une approche contestable consistant à assimiler les phénomènes de radicalisation à des troubles psychiatriques.",
			want:       []string{"troubles mentaux"}},
	}
	llmtest.Group(t, func(t *testing.T) {
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				llmtest.Parallel(t)
				s, err := amendmentStatement(ctx, c.title, &AmendementInfo{Dispositif: c.dispositif, Expose: c.expose})
				if err != nil {
					t.Fatal(err)
				}
				low := strings.ToLower(s.SimpleStatement)
				for _, w := range c.want {
					if !strings.Contains(low, w) {
						t.Errorf("statement %q lacks %q", s.SimpleStatement, w)
					}
				}
				if s.Confidence < amendmentStatementMinConfidence {
					t.Errorf("confidence %.2f for %q", s.Confidence, s.SimpleStatement)
				}
				t.Logf("%s: %q (%.2f)", c.name, s.SimpleStatement, s.Confidence)
			})
		}
	})
}
