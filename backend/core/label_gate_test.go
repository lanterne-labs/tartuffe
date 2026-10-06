package core

import (
	"testing"

	"digital-mirror/backend/llmtest"
)

// TestSameSubjectByLabelGolden pins the strict label gate on prod stance pairs
// the trigram source surfaces (labels verbatim, summaries abridged): the
// duplicate topics that must merge and the shared-generic-word pairs that
// must not. Local-only (live LLM).
func TestSameSubjectByLabelGolden(t *testing.T) {
	llmtest.Setup(t)
	sm, err := NewStanceManagerFromEnv(newMockStanceStorer())
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		person string
		a, b   StanceForMerge
		want   bool
	}{
		{
			name:   "environment_twice",
			person: "Jordan Bardella",
			a:      StanceForMerge{Label: "politique environnementale et régulations écologiques", Summary: "Jordan Bardella critique les régulations écologiques européennes et demande la suspension du mécanisme d'ajustement carbone pour les engrais."},
			b:      StanceForMerge{Label: "Politique environnementale et Pacte vert", Summary: "Jordan Bardella s'oppose au Pacte vert européen et à ses contraintes sur les agriculteurs."},
			want:   true,
		},
		{
			name:   "canada_rewording",
			person: "Emmanuel Macron",
			a:      StanceForMerge{Label: "relations entre la France et le Canada", Summary: "Emmanuel Macron salue le partenariat avec le Canada et annonce des coopérations économiques."},
			b:      StanceForMerge{Label: "relations franco-canadiennes", Summary: "Emmanuel Macron a reçu le Premier ministre canadien et évoqué la coopération bilatérale."},
			want:   true,
		},
		{
			name:   "racism_policy_prefix",
			person: "Aurore Bergé",
			a:      StanceForMerge{Label: "politique de lutte contre le racisme et l'antisémitisme", Summary: "Aurore Bergé présente un plan contre le racisme et l'antisémitisme."},
			b:      StanceForMerge{Label: "lutte contre le racisme et l'antisémitisme", Summary: "Aurore Bergé condamne des actes antisémites et appelle à la fermeté."},
			want:   true,
		},
		{
			name:   "student_grants",
			person: "Yaël Braun-Pivet",
			a:      StanceForMerge{Label: "politique sociale et bourses étudiantes", Summary: "Yaël Braun-Pivet défend la revalorisation des bourses étudiantes."},
			b:      StanceForMerge{Label: "bourses étudiantes et précarité étudiante", Summary: "Yaël Braun-Pivet s'inquiète de la précarité étudiante et soutient l'extension des bourses."},
			want:   true,
		},
		{
			name:   "hungary_vs_spain",
			person: "Emmanuel Macron",
			a:      StanceForMerge{Label: "relations bilatérales France-Hongrie", Summary: "Emmanuel Macron a reçu Viktor Orbán et évoqué les désaccords sur l'Ukraine."},
			b:      StanceForMerge{Label: "Relations bilatérales France-Espagne", Summary: "Emmanuel Macron et Pedro Sánchez ont signé un traité d'amitié."},
			want:   false,
		},
		{
			name:   "guns_vs_abortion",
			person: "AfD Saxe-Anhalt",
			a:      StanceForMerge{Label: "Réglementation des armes et permis de port d'arme", Summary: "Le parti veut assouplir le permis de port d'arme."},
			b:      StanceForMerge{Label: "réglementation de l'avortement", Summary: "Le parti veut durcir l'accès à l'avortement."},
			want:   false,
		},
		{
			name:   "algeria_vs_spain",
			person: "Olivier Faure",
			a:      StanceForMerge{Label: "relations diplomatiques avec l'Algérie", Summary: "Olivier Faure appelle à l'apaisement avec l'Algérie après l'affaire Sansal."},
			b:      StanceForMerge{Label: "relations diplomatiques avec l'Espagne et l'espace Schengen", Summary: "Olivier Faure défend la coopération avec l'Espagne sur les frontières Schengen."},
			want:   false,
		},
		{
			name:   "presidency_vs_financing",
			person: "Jordan Bardella",
			a:      StanceForMerge{Label: "présidence du Rassemblement national", Summary: "Jordan Bardella assume la présidence du parti et son organisation interne."},
			b:      StanceForMerge{Label: "financement bancaire du Rassemblement national", Summary: "Jordan Bardella dénonce le refus des banques françaises de prêter au parti."},
			want:   false,
		},
		{
			name:   "economic_vs_social",
			person: "Fabien Roussel",
			a:      StanceForMerge{Label: "politique économique et sociale", Summary: "Fabien Roussel propose un grand emprunt pour la réindustrialisation."},
			b:      StanceForMerge{Label: "politique sociale et pouvoir d'achat", Summary: "Fabien Roussel demande la baisse de la TVA sur l'énergie pour le pouvoir d'achat."},
			want:   false,
		},
		{
			name:   "area_vs_specific_country",
			person: "Emmanuel Macron",
			a:      StanceForMerge{Label: "relations diplomatiques de la France", Summary: "Emmanuel Macron défend la voix de la France dans le monde et le multilatéralisme."},
			b:      StanceForMerge{Label: "Relations diplomatiques et économiques avec la Syrie", Summary: "Emmanuel Macron a reçu le président syrien et conditionné la levée des sanctions."},
			want:   false,
		},
	}

	ctx, cancel := llmtest.Context(t)
	defer cancel()
	var failed int
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sm.SameSubjectByLabel(ctx, tc.person, tc.a, tc.b)
			if err != nil {
				t.Fatalf("gate error: %v", err)
			}
			if got != tc.want {
				failed++
				t.Errorf("got same=%v want same=%v\n    A: %s\n    B: %s", got, tc.want, tc.a.Label, tc.b.Label)
			}
		})
	}
	t.Logf("=== LABEL-GATE GOLDEN: %d/%d passed ===", len(cases)-failed, len(cases))
}
