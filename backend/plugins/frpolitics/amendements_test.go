package frpolitics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanAmdTextAndPurpose(t *testing.T) {
	raw := "Ce sous-amendement vise &#x00E0; accorder &#x00E0; la Cour des comptes un d&#x00E9;lai suffisant pour conduire un audit exhaustif."
	got := cleanAmdText(raw)
	want := "Ce sous-amendement vise à accorder à la Cour des comptes un délai suffisant pour conduire un audit exhaustif."
	if got != want {
		t.Errorf("cleanAmdText = %q, want %q", got, want)
	}
	p := purposePhrase(&AmendementInfo{Expose: got})
	if !strings.HasPrefix(p, "visant à accorder à la Cour des comptes") {
		t.Errorf("purposePhrase = %q, want 'visant à ...' with the lead-in stripped", p)
	}
	d := purposePhrase(&AmendementInfo{Dispositif: "À l'alinéa 2, substituer aux mots : « de six mois », les mots : « d'un an »."})
	if !strings.Contains(d, "dispositif") || !strings.Contains(d, "six mois") {
		t.Errorf("dispositif fallback = %q", d)
	}
	if purposePhrase(&AmendementInfo{}) != "" {
		t.Error("empty amendment must yield empty purpose (gate)")
	}
}

// TestLookupAmendement_ProdCase resolves the exact sous-amendement behind prod
// scrutin 17-4373 from the real archive. Local-only: skips when the archive is
// not in the cache dir (~300MB, never in CI).
func TestLookupAmendement_ProdCase(t *testing.T) {
	if _, err := os.Stat(filepath.Join(getCacheDir(), "Amendements_17.json.zip")); err != nil {
		t.Skip("Amendements_17.json.zip not cached; local-only test")
	}
	date := time.Date(2025, 11, 27, 0, 0, 0, 0, time.UTC)
	info := LookupAmendement(context.Background(), 17, 372, &date)
	if info == nil {
		t.Fatal("sous-amendement 372 (2025-11-27) not found")
	}
	if info.AuthorGroupRef != "PO872880" {
		t.Errorf("author group = %s, want PO872880 (UDR)", info.AuthorGroupRef)
	}
	if !strings.Contains(info.Expose, "délai suffisant") {
		t.Errorf("exposé = %q, want the audit-delay justification", info.Expose)
	}
	if !strings.Contains(info.Dispositif, "six mois") || !strings.Contains(info.Dispositif, "un an") {
		t.Errorf("dispositif = %q, want the 6-months→1-year substitution", info.Dispositif)
	}

	// Full phrase as the fact text will carry it.
	law := Law{Title: "le sous-amendement n° 372 de M. Verny à l'amendement n° 96 de M. Bernhardt après l'article premier bis de la proposition de loi visant à la nationalisation d'ArcelorMittal France", Legislature: 17, VoteDate: &date}
	orgCache := map[string]OrganeInfo{"PO872880": {OrganeRef: "PO872880", ShortName: "UDR"}}
	phrase, ok := describeVotedObject(context.Background(), law, LawSummary{SimpleStatement: "Nationalise ArcelorMittal France pour préserver la souveraineté industrielle"}, orgCache)
	if !ok {
		t.Fatal("describeVotedObject gated a resolvable sous-amendement")
	}
	t.Logf("phrase: %s", phrase)
	for _, want := range []string{"le sous-amendement n° 372", "(UDR)", "visant à accorder", "déposé sur le texte de loi"} {
		if !strings.Contains(phrase, want) {
			t.Errorf("phrase missing %q: %s", want, phrase)
		}
	}
}
