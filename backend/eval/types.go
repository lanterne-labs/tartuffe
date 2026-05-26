// Package eval defines the synthetic test-corpus types and loader for the
// end-to-end contradiction-detection evaluation pipeline. See eval/cases.yaml
// for the canonical corpus and `dm eval run` for the runner.
package eval

import "time"

// Corpus is the top-level structure of a YAML eval file.
type Corpus struct {
	Persons       []Person   `yaml:"persons"`
	ExpectedPairs []PairSpec `yaml:"expected_pairs"`
}

// Person is one fictional subject with their stances and facts.
type Person struct {
	Name       string   `yaml:"name"`
	EntityType string   `yaml:"entity_type"`
	Stances    []Stance `yaml:"stances"`
}

// Stance groups facts that the corpus author considers to be on the same
// underlying axis. It is metadata only — the stance manager re-classifies
// every fact through its own LLM call, so this field is for human readers
// and post-run diagnostics, not for instructing the system.
type Stance struct {
	Axis  string `yaml:"axis"`
	Facts []Fact `yaml:"facts"`
}

// Fact is one pre-structured fact to be inserted via FactInserter. It mirrors
// core.StoreFactInput plus an identifier the author uses to reference the fact
// from expected_pairs.
type Fact struct {
	ID            string `yaml:"id"`
	Text          string `yaml:"text"`
	TextEn        string `yaml:"text_en"`
	Verb          string `yaml:"verb"`
	EmbeddingText string `yaml:"embedding_text"`
	Quote         string `yaml:"quote"`
	Date          string `yaml:"date"` // YYYY-MM-DD; converted at load time.
	SourceURL     string `yaml:"source_url"`
	Lang          string `yaml:"lang"` // Optional; defaults to "fr".

	// Filled by the loader so downstream code can use *time.Time directly.
	ParsedDate *time.Time `yaml:"-"`
}

// PairSpec is one expected verdict on a pair of facts. The runner compares
// the detector's output against this list; pairs NOT in this list are
// implicitly expected to be NO (so any positive flag is a false positive).
type PairSpec struct {
	A        string `yaml:"a"`
	B        string `yaml:"b"`
	Expected string `yaml:"expected"` // "YES" or "NO".
	Rule     string `yaml:"rule"`     // Free-form taxonomy slug, not enforced.
	Notes    string `yaml:"notes"`
}

// IsContradiction returns true when the spec expects the pair to be flagged.
func (p PairSpec) IsContradiction() bool { return p.Expected == "YES" }
