package translate

import (
	"context"
	"errors"
	"testing"
)

func TestLangsToFill(t *testing.T) {
	cases := []struct {
		name     string
		targets  []string
		src      string
		existing map[string]bool
		want     []string
	}{
		{"excludes source", []string{"en", "fr"}, "fr", nil, []string{"en"}},
		{"excludes existing", []string{"en", "fr"}, "fr", map[string]bool{"en": true}, []string{}},
		{"english source", []string{"en", "fr"}, "en", nil, []string{"fr"}},
		{"case-insensitive", []string{"EN", "FR"}, "Fr", map[string]bool{"en": true}, []string{}},
		{"three langs minus source", []string{"en", "fr", "de"}, "fr", nil, []string{"en", "de"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LangsToFill(tc.targets, tc.src, tc.existing)
			if !eqStr(got, tc.want) {
				t.Fatalf("LangsToFill(%v, %q, %v) = %v, want %v", tc.targets, tc.src, tc.existing, got, tc.want)
			}
		})
	}
}

func TestSourceHashStable(t *testing.T) {
	if SourceHash("abc") != SourceHash("abc") {
		t.Fatal("SourceHash not stable for identical input")
	}
	if SourceHash("abc") == SourceHash("abd") {
		t.Fatal("SourceHash collided on different input")
	}
}

// fakeTranslator returns a deterministic translation and counts calls.
type fakeTranslator struct {
	calls int
	fail  string // if dst == fail, return an error
}

func (f *fakeTranslator) Translate(ctx context.Context, text, src, dst string) (string, error) {
	f.calls++
	if dst == f.fail {
		return "", errors.New("boom")
	}
	return "[" + dst + "] " + text, nil
}

func TestFillMissing_SkipsSourceAndExisting(t *testing.T) {
	ft := &fakeTranslator{}
	got := FillMissing(context.Background(), ft, "Bonjour", "fr", []string{"en", "fr", "de"}, map[string]bool{})
	if got["en"] != "[en] Bonjour" || got["de"] != "[de] Bonjour" {
		t.Fatalf("unexpected translations: %v", got)
	}
	if _, ok := got["fr"]; ok {
		t.Fatalf("source language must be skipped: %v", got)
	}
	if ft.calls != 2 {
		t.Fatalf("expected 2 translator calls (en, de), got %d", ft.calls)
	}
}

func TestFillMissing_BestEffortOnFailure(t *testing.T) {
	ft := &fakeTranslator{fail: "de"} // de fails, en should still succeed
	got := FillMissing(context.Background(), ft, "Bonjour", "fr", []string{"en", "de"}, map[string]bool{})
	if got["en"] != "[en] Bonjour" {
		t.Fatalf("en should have succeeded: %v", got)
	}
	if _, ok := got["de"]; ok {
		t.Fatalf("failed language must be absent, not block others: %v", got)
	}
}

func eqStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
