package frpolitics

import (
	"context"
	"net/http"
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
	info, err := LookupAmendement(context.Background(), 17, 372, &date)
	if err != nil {
		t.Fatal(err)
	}
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
	amendment, ok, err := resolveVotedObject(context.Background(), law, orgCache)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("resolveVotedObject gated a resolvable sous-amendement")
	}
	phrase := votedObjectPhrase(amendment, "", LawSummary{SimpleStatement: "Nationalise ArcelorMittal France pour préserver la souveraineté industrielle"})
	t.Logf("phrase: %s", phrase)
	for _, want := range []string{"le sous-amendement n° 372", "(UDR)", "visant à accorder", "déposé sur le texte de loi"} {
		if !strings.Contains(phrase, want) {
			t.Errorf("phrase missing %q: %s", want, phrase)
		}
	}
}

// roundTripFunc makes a function an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// An amendments archive that is not available is an error, not a gate: a gate
// would read the failed download as "no amendment" and drop the scrutin
// without a trace.
func TestResolveVotedObject_UnavailableArchiveFails(t *testing.T) {
	t.Setenv("FRPOLITICS_CACHE_DIR", t.TempDir())
	origClient := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Header: http.Header{}}, nil
	})}
	defer func() { httpClient = origClient }()

	date := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	law := Law{Title: "l'amendement n° 113 de M. Dharréville après l'article premier du projet de loi", Legislature: 99, VoteDate: &date}
	_, ok, err := resolveVotedObject(context.Background(), law, nil)
	if err == nil {
		t.Fatal("expected an error for an unavailable archive")
	}
	if ok {
		t.Fatal("an unresolved amendment must not be ok")
	}
}

// A cached archive that is not a zip file is an error, and the file is
// removed, so the next run downloads the archive again.
func TestLookupAmendement_BadArchiveRemoved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FRPOLITICS_CACHE_DIR", dir)
	path := filepath.Join(dir, "Amendements_98.json.zip")
	if err := os.WriteFile(path, []byte("not a zip file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LookupAmendement(context.Background(), 98, 1, nil); err == nil {
		t.Fatal("expected an error for a bad archive")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the bad archive must be removed, stat: %v", err)
	}
}
