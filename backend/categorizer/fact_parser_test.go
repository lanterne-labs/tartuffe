package categorizer

import (
	"testing"
	"time"
)

func TestParseFactJSON(t *testing.T) {
	now := time.Now()
	sourceURL := "https://example.com/article"

	t.Run("valid JSON array", func(t *testing.T) {
		input := []byte(`[{"subject_entity":"Marine Le Pen","subject_entity_type":"human","normalized_fact":"Marine Le Pen voted against bill X","embedding_text":"VOTED_AGAINST bill-X","original_quote":"voted against","topics":["politics"],"involved_people":[]}]`)
		facts, err := parseFactJSON(input, sourceURL, &now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(facts) != 1 {
			t.Fatalf("expected 1 fact, got %d", len(facts))
		}
		if facts[0].SubjectEntity != "Marine Le Pen" {
			t.Errorf("expected subject_entity 'Marine Le Pen', got %q", facts[0].SubjectEntity)
		}
	})

	t.Run("empty array with trailing text", func(t *testing.T) {
		input := []byte("[]\nNo verifiable facts are found in the given article because the content is editorial commentary.")
		facts, err := parseFactJSON(input, sourceURL, &now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(facts) != 0 {
			t.Fatalf("expected 0 facts, got %d", len(facts))
		}
	})

	t.Run("facts array with trailing text", func(t *testing.T) {
		input := []byte(`[{"subject_entity":"Marine Le Pen","subject_entity_type":"human","normalized_fact":"Marine Le Pen voted against bill X","embedding_text":"VOTED_AGAINST bill-X","original_quote":"voted against","topics":["politics"],"involved_people":[]}]
Here are the facts I extracted from the article.`)
		facts, err := parseFactJSON(input, sourceURL, &now)
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
		_, err := parseFactJSON(input, sourceURL, &now)
		if err == nil {
			t.Fatal("expected error for invalid JSON, got nil")
		}
	})

	t.Run("empty array no trailing text", func(t *testing.T) {
		input := []byte("[]")
		facts, err := parseFactJSON(input, sourceURL, &now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(facts) != 0 {
			t.Fatalf("expected 0 facts, got %d", len(facts))
		}
	})
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
