package entityvalidator

import (
	"encoding/json"
	"sync"
	"testing"

	"digital-mirror/backend/llmtest"
)

// TestLLMFilter_ScopeGolden locks the R10 out-of-scope rule added after the
// 2026-07-24 audit (mirror-6ij): real-but-non-political entities (athletes,
// musicians, magazines) must be rejected, while politicians, advocacy orgs and
// politicians-with-entertainment-pasts must stay accepted. Evidence is
// hand-supplied, so this costs local LLM calls only — no Serper/Wiki quota.
//
// LOCAL ONLY: skips without OPEN_AI_KEY.
//
//	source .env; go test -v -run TestLLMFilter_ScopeGolden ./backend/entityvalidator/
func TestLLMFilter_ScopeGolden(t *testing.T) {
	llmtest.Setup(t)

	f, err := NewLLMFilter()
	if err != nil {
		t.Fatalf("NewLLMFilter: %v", err)
	}
	ctx, cancel := llmtest.Context(t)
	defer cancel()

	mkPrev := func(pages []WikiPage, hits []GoogleHit) []FilterRun {
		var prev []FilterRun
		if pages != nil {
			raw, _ := json.Marshal(wikiExtra{Pages: pages})
			prev = append(prev, FilterRun{Filter: "wiki", Result: FilterResult{Decision: Defer, Extra: raw}})
		}
		if hits != nil {
			raw, _ := json.Marshal(googleExtra{Hits: hits})
			prev = append(prev, FilterRun{Filter: "google", Result: FilterResult{Decision: Defer, Extra: raw}})
		}
		return prev
	}

	cases := []struct {
		name       string
		entityType string
		pages      []WikiPage
		hits       []GoogleHit
		wantReal   bool
	}{
		// R11 (review of 2026-10-30, mirror-y0q): organs of the state are not
		// political actors; advocacy organisations and unions stay in scope.
		{
			name: "parquet de bobigny", entityType: "organization", wantReal: false,
			hits: []GoogleHit{{Title: "Tribunal judiciaire de Bobigny - Le parquet", Snippet: "Le parquet de Bobigny, dirigé par le procureur de la République, conduit l'action publique en Seine-Saint-Denis.", URL: "https://www.tribunal-bobigny.justice.fr/parquet"}},
		},
		{
			name: "prefecture de maine-et-loire", entityType: "organization", wantReal: false,
			pages: []WikiPage{{Lang: "fr", Title: "Préfecture de Maine-et-Loire", Extract: "La préfecture de Maine-et-Loire est l'administration de l'État dans le département, dirigée par le préfet, à Angers."}},
		},
		{
			name: "tribunal de l'union europeenne", entityType: "organization", wantReal: false,
			pages: []WikiPage{{Lang: "fr", Title: "Tribunal (Union européenne)", Extract: "Le Tribunal est une juridiction de la Cour de justice de l'Union européenne, compétente en première instance pour les recours des particuliers et des entreprises."}},
		},
		{
			name: "licra", entityType: "organization", wantReal: true,
			pages: []WikiPage{{Lang: "fr", Title: "Ligue internationale contre le racisme et l'antisémitisme", Extract: "La Licra est une association française de lutte contre le racisme et l'antisémitisme, fondée en 1927, qui intervient dans le débat public et devant les tribunaux."}},
		},
		{
			name: "jimin", entityType: "human", wantReal: false,
			pages: []WikiPage{{Lang: "en", Title: "Jimin", Extract: "Park Ji-min, known mononymously as Jimin, is a South Korean singer and dancer. In 2013, he made his debut as a member of the boy band BTS."}},
		},
		{
			name: "didier deschamps", entityType: "human", wantReal: false,
			pages: []WikiPage{{Lang: "fr", Title: "Didier Deschamps", Extract: "Didier Deschamps est un footballeur international français devenu entraîneur, sélectionneur de l'équipe de France depuis 2012."}},
		},
		{
			name: "fisheyelemag", entityType: "organization", wantReal: false,
			hits: []GoogleHit{{Title: "Fisheye Magazine", Snippet: "Le magazine de la photographie et de ses tendances — actualité photo, portfolios et festivals.", URL: "https://www.fisheyemagazine.fr"}},
		},
		{
			name: "gérald darmanin", entityType: "human", wantReal: true,
			pages: []WikiPage{{Lang: "fr", Title: "Gérald Darmanin", Extract: "Gérald Darmanin est un homme politique français, garde des Sceaux, ministre de la Justice, après avoir été ministre de l'Intérieur."}},
		},
		{
			name: "sos racisme", entityType: "organization", wantReal: true,
			pages: []WikiPage{{Lang: "fr", Title: "SOS Racisme", Extract: "SOS Racisme est une association française créée en 1984, engagée dans la lutte contre le racisme, l'antisémitisme et les discriminations, intervenant régulièrement dans le débat public."}},
		},
		{
			name: "volodymyr zelensky", entityType: "human", wantReal: true,
			pages: []WikiPage{{Lang: "en", Title: "Volodymyr Zelenskyy", Extract: "Volodymyr Zelenskyy is a Ukrainian politician and former comedian and actor who has served as the sixth president of Ukraine since 2019."}},
		},
	}

	// The cases run concurrently with -llm-claude: mu guards the counts.
	var mu sync.Mutex
	var passed, failed int
	llmtest.Group(t, func(t *testing.T) {
		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				llmtest.Parallel(t)
				res, err := f.Check(ctx, Candidate{Name: tc.name, EntityType: tc.entityType, Previous: mkPrev(tc.pages, tc.hits)})
				if err != nil {
					t.Fatalf("Check: %v", err)
				}
				mu.Lock()
				defer mu.Unlock()
				gotReal := res.Decision == Approve
				if gotReal != tc.wantReal {
					failed++
					t.Errorf("got decision=%v (real=%v) want real=%v — reason: %s", res.Decision, gotReal, tc.wantReal, res.Reason)
					return
				}
				passed++
				t.Logf("decision=%v ✓ (%s)", res.Decision, res.Reason)
			})
		}
	})
	t.Logf("SCOPE GOLDEN: %d/%d", passed, passed+failed)
}
