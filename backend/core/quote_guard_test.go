package core

import "testing"

// The inserter tests exercise the guard with the plugin source types; the
// plugins register those in their own init, which this test binary never links.
func init() {
	for _, st := range []string{"news", "twitter", "assemblee_debat"} {
		RequireQuoteForSourceType(st)
	}
}

func TestRequireQuoteForSourceType(t *testing.T) {
	const registered = "quote_guard_test_registered"
	const unregistered = "quote_guard_test_unregistered"
	RequireQuoteForSourceType(registered)

	cases := []struct {
		name       string
		sourceType string
		quote      string
		want       bool
	}{
		{"registered empty quote", registered, "", true},
		{"registered whitespace quote", registered, " \n\t", true},
		{"registered with quote", registered, "verbatim snippet", false},
		{"unregistered empty quote", unregistered, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := quoteMissing(StoreFactInput{SourceType: tc.sourceType, OriginalQuote: tc.quote})
			if got != tc.want {
				t.Fatalf("quoteMissing(%q, %q) = %v, want %v", tc.sourceType, tc.quote, got, tc.want)
			}
		})
	}
}

func TestRequireQuoteForSourceType_RejectsEmpty(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on empty source type")
		}
	}()
	RequireQuoteForSourceType("")
}
