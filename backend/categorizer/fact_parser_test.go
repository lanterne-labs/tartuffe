package categorizer

import (
	"testing"
	"time"
)

func TestParseFactJSON(t *testing.T) {
	now := time.Now()
	sourceURL := "https://example.com/article"

	t.Run("valid JSON array with new format", func(t *testing.T) {
		input := []byte(`[{"reasoning":"Type 2: official vote record","subject_entity":"Marine Le Pen","subject_entity_type":"human","normalized_fact":"Marine Le Pen a voté contre la loi X","normalized_fact_en":"Marine Le Pen voted against bill X","lang":"fr","canonical_verb":"VOTED_AGAINST","embedding_text":"loi-X","original_quote":"a voté contre","involved_people":[]}]`)
		facts, err := ParseFactJSON(input, sourceURL, &now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(facts) != 1 {
			t.Fatalf("expected 1 fact, got %d", len(facts))
		}
		if facts[0].SubjectEntity != "Marine Le Pen" {
			t.Errorf("expected subject_entity 'Marine Le Pen', got %q", facts[0].SubjectEntity)
		}
		if facts[0].CanonicalVerb != "VOTED_AGAINST" {
			t.Errorf("expected canonical_verb 'VOTED_AGAINST', got %q", facts[0].CanonicalVerb)
		}
		if facts[0].EmbeddingText != "loi-X" {
			t.Errorf("expected embedding_text 'loi-X', got %q", facts[0].EmbeddingText)
		}
		if facts[0].Lang != "fr" {
			t.Errorf("expected lang 'fr', got %q", facts[0].Lang)
		}
		if facts[0].NormalizedFactTextEn != "Marine Le Pen voted against bill X" {
			t.Errorf("expected English translation, got %q", facts[0].NormalizedFactTextEn)
		}
	})

	t.Run("English source omits normalized_fact_en", func(t *testing.T) {
		input := []byte(`[{"subject_entity":"Donald Trump","subject_entity_type":"human","normalized_fact":"Donald Trump threatened to strike Iranian power plants.","lang":"en","canonical_verb":"THREATENED","embedding_text":"iran power-plants","original_quote":"we hit the power plants","involved_people":[]}]`)
		facts, err := ParseFactJSON(input, sourceURL, &now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(facts) != 1 {
			t.Fatalf("expected 1 fact, got %d", len(facts))
		}
		if facts[0].Lang != "en" {
			t.Errorf("expected lang 'en', got %q", facts[0].Lang)
		}
		if facts[0].NormalizedFactTextEn != "" {
			t.Errorf("expected empty NormalizedFactTextEn for English source, got %q", facts[0].NormalizedFactTextEn)
		}
	})

	t.Run("missing lang is rejected", func(t *testing.T) {
		input := []byte(`[{"subject_entity":"Marine Le Pen","subject_entity_type":"human","normalized_fact":"Marine Le Pen a voté contre la loi X","canonical_verb":"VOTED_AGAINST","embedding_text":"loi-X","original_quote":"contre","involved_people":[]}]`)
		facts, err := ParseFactJSON(input, sourceURL, &now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(facts) != 0 {
			t.Fatalf("expected 0 facts (missing lang), got %d", len(facts))
		}
	})

	t.Run("backward compat: verb parsed from embedding_text", func(t *testing.T) {
		input := []byte(`[{"subject_entity":"Marine Le Pen","subject_entity_type":"human","normalized_fact":"Marine Le Pen a voté contre la loi X","normalized_fact_en":"Marine Le Pen voted against bill X","lang":"fr","embedding_text":"VOTED_AGAINST loi-X","original_quote":"contre","involved_people":[]}]`)
		facts, err := ParseFactJSON(input, sourceURL, &now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(facts) != 1 {
			t.Fatalf("expected 1 fact, got %d", len(facts))
		}
		// canonical_verb should be parsed from embedding_text
		if facts[0].CanonicalVerb != "VOTED_AGAINST" {
			t.Errorf("expected parsed canonical_verb 'VOTED_AGAINST', got %q", facts[0].CanonicalVerb)
		}
		// embedding_text should have verb stripped
		if facts[0].EmbeddingText != "loi-X" {
			t.Errorf("expected verb-free embedding_text 'loi-X', got %q", facts[0].EmbeddingText)
		}
	})

	t.Run("empty array with trailing text", func(t *testing.T) {
		input := []byte("[]\nNo verifiable facts are found in the given article because the content is editorial commentary.")
		facts, err := ParseFactJSON(input, sourceURL, &now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(facts) != 0 {
			t.Fatalf("expected 0 facts, got %d", len(facts))
		}
	})

	t.Run("facts array with trailing text", func(t *testing.T) {
		input := []byte(`[{"subject_entity":"Marine Le Pen","subject_entity_type":"human","normalized_fact":"Marine Le Pen voted against bill X","lang":"en","canonical_verb":"VOTED_AGAINST","embedding_text":"bill-X","original_quote":"voted against","involved_people":[]}]
Here are the facts I extracted from the article.`)
		facts, err := ParseFactJSON(input, sourceURL, &now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(facts) != 1 {
			t.Fatalf("expected 1 fact, got %d", len(facts))
		}
		if facts[0].NormalizedFactText != "Marine Le Pen voted against bill X" {
			t.Errorf("unexpected normalized_fact: %q", facts[0].NormalizedFactText)
		}
	})

	t.Run("completely invalid JSON", func(t *testing.T) {
		input := []byte("this is not json at all")
		_, err := ParseFactJSON(input, sourceURL, &now)
		if err == nil {
			t.Fatal("expected error for invalid JSON, got nil")
		}
	})

	t.Run("empty array no trailing text", func(t *testing.T) {
		input := []byte("[]")
		facts, err := ParseFactJSON(input, sourceURL, &now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(facts) != 0 {
			t.Fatalf("expected 0 facts, got %d", len(facts))
		}
	})
}

func TestParseVerbFromEmbeddingText(t *testing.T) {
	tests := []struct {
		input     string
		wantVerb  string
		wantTopic string
		wantOK    bool
	}{
		{"DENIED wrongdoing parliamentary-assistants-case", "DENIED", "wrongdoing parliamentary-assistants-case", true},
		{"CLAIMED innocence", "CLAIMED", "innocence", true},
		{"PROMISED tax-cut income-tax", "PROMISED", "tax-cut income-tax", true},
		{"VOTED_FOR tax-increase", "VOTED_FOR", "tax-increase", true},
		{"VOTED_AGAINST immigration-reform", "VOTED_AGAINST", "immigration-reform", true},
		{"WAS_SENTENCED prison", "WAS_SENTENCED", "prison", true},
		{"WAS_CONVICTED fraud", "WAS_CONVICTED", "fraud", true},
		{"WAS_ACQUITTED charges", "WAS_ACQUITTED", "charges", true},
		{"WAS_FINED tax-evasion", "WAS_FINED", "tax-evasion", true},
		{"RECEIVED_FUNDS party-financing", "RECEIVED_FUNDS", "party-financing", true},
		{"CHANGED_POSITION climate-policy", "CHANGED_POSITION", "climate-policy", true},
		{"ANNOUNCED new-policy", "ANNOUNCED", "new-policy", true},
		{"SUPPORTED workers-rights", "SUPPORTED", "workers-rights", true},
		{"OPPOSED immigration-reform", "OPPOSED", "immigration-reform", true},
		{"PROPOSED budget-increase", "PROPOSED", "budget-increase", true},
		{"CONFIRMED attendance", "CONFIRMED", "attendance", true},
		{"ADMITTED wrongdoing", "ADMITTED", "wrongdoing", true},
		{"REFUSED cooperation", "REFUSED", "cooperation", true},
		{"DEMANDED action", "DEMANDED", "action", true},
		{"THREATENED sanctions", "THREATENED", "sanctions", true},
		// Edge cases
		{"wrongdoing parliamentary-assistants-case", "", "", false}, // no verb prefix
		{"", "", "", false},                   // empty string
		{"DENIED", "DENIED", "", true},        // verb only, no topic
		{"notaverb something", "", "", false}, // unknown first word
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			verb, topic, ok := ParseVerbFromEmbeddingText(tt.input)
			if ok != tt.wantOK {
				t.Errorf("ParseVerbFromEmbeddingText(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if verb != tt.wantVerb {
				t.Errorf("ParseVerbFromEmbeddingText(%q) verb = %q, want %q", tt.input, verb, tt.wantVerb)
			}
			if topic != tt.wantTopic {
				t.Errorf("ParseVerbFromEmbeddingText(%q) topic = %q, want %q", tt.input, topic, tt.wantTopic)
			}
		})
	}
}

func TestExtractFirstJSON(t *testing.T) {
	t.Run("clean JSON passes through", func(t *testing.T) {
		input := []byte(`[{"key":"value"}]`)
		result := extractFirstJSON(input)
		if string(result) != `[{"key":"value"}]` {
			t.Errorf("expected unchanged JSON, got %q", string(result))
		}
	})

	t.Run("trailing text stripped", func(t *testing.T) {
		input := []byte(`[{"key":"value"}]
Some trailing explanation text here`)
		result := extractFirstJSON(input)
		if string(result) != `[{"key":"value"}]` {
			t.Errorf("expected JSON without trailing text, got %q", string(result))
		}
	})

	t.Run("empty array with trailing text", func(t *testing.T) {
		input := []byte("[] No facts found.")
		result := extractFirstJSON(input)
		if string(result) != "[]" {
			t.Errorf("expected '[]', got %q", string(result))
		}
	})

	t.Run("invalid JSON returned as-is", func(t *testing.T) {
		input := []byte("not json")
		result := extractFirstJSON(input)
		if string(result) != "not json" {
			t.Errorf("expected input returned as-is, got %q", string(result))
		}
	})
}

func TestSpokenByProxy(t *testing.T) {
	cases := []struct {
		subject, quote string
		want           bool
	}{
		{"Jean-Luc Mélenchon", "le bras droit de Jean-Luc Mélenchon a dénoncé « l'entêtement » de Fabien Roussel.", true},
		{"le bras droit de Jean-Luc Mélenchon", "a dénoncé « l'entêtement » de Fabien Roussel.", true},
		{"Éric Ciotti", "L'entourage d'Éric Ciotti assure qu'il ne sera pas candidat.", true},
		{"Donald Trump", "An aide to Donald Trump said the talks were over.", true},
		{"Jean-Luc Mélenchon", "Jean-Luc Mélenchon a estimé que la politique d'austérité était « en partie responsable ».", false},
		// "approche de" must not read as "proche de".
		{"Marine Le Pen", "Une approche de Marine Le Pen sur l'immigration a été défendue par elle-même.", false},
		// The aide named in the quote works for someone other than the subject.
		{"Fabien Roussel", "le bras droit de Jean-Luc Mélenchon a dénoncé la candidature de Fabien Roussel.", false},
	}
	for _, c := range cases {
		if got := spokenByProxy(c.subject, c.quote); got != c.want {
			t.Errorf("spokenByProxy(%q, %q) = %v, want %v", c.subject, c.quote, got, c.want)
		}
	}
}
