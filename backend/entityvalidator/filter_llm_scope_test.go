package entityvalidator

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
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
	_ = godotenv.Overload()
	_ = godotenv.Overload("../../.env")
	if os.Getenv("OPEN_AI_KEY") == "" {
		t.Skip("Skipping scope golden: OPEN_AI_KEY not set (local-only).")
	}

	f, err := NewLLMFilter()
	if err != nil {
		t.Fatalf("NewLLMFilter: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
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

	var passed, failed int
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			res, err := f.Check(ctx, Candidate{Name: tc.name, EntityType: tc.entityType, Previous: mkPrev(tc.pages, tc.hits)})
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
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
	t.Logf("SCOPE GOLDEN: %d/%d", passed, passed+failed)
}
