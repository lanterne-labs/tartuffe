package eval

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// LoadCorpus reads a YAML eval file from disk, parses it, and validates the
// invariants the runner relies on (unique fact IDs, non-empty mandatory
// fields, YES/NO expectation values, every PairSpec referring to known IDs).
// Returns the parsed Corpus or an error describing the first violation.
func LoadCorpus(path string) (*Corpus, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Corpus
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.normalize(); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &c, nil
}

// AllFacts flattens persons→stances→facts into a single slice in declaration
// order. Useful for iteration that doesn't care about person/stance grouping.
func (c *Corpus) AllFacts() []Fact {
	var out []Fact
	for _, p := range c.Persons {
		for _, s := range p.Stances {
			out = append(out, s.Facts...)
		}
	}
	return out
}

// FactByID returns the fact with the given logical ID, or nil if absent.
// Index is built lazily on each call; corpora are small (~tens of facts).
func (c *Corpus) FactByID(id string) *Fact {
	for i := range c.Persons {
		for j := range c.Persons[i].Stances {
			for k := range c.Persons[i].Stances[j].Facts {
				if c.Persons[i].Stances[j].Facts[k].ID == id {
					return &c.Persons[i].Stances[j].Facts[k]
				}
			}
		}
	}
	return nil
}

// PersonOfFact returns the person who owns the given fact ID, or empty string.
func (c *Corpus) PersonOfFact(id string) string {
	for _, p := range c.Persons {
		for _, s := range p.Stances {
			for _, f := range s.Facts {
				if f.ID == id {
					return p.Name
				}
			}
		}
	}
	return ""
}

// normalize validates structure, parses dates, applies defaults, and confirms
// every PairSpec references a known fact ID. Mutates the receiver in place.
func (c *Corpus) normalize() error {
	if len(c.Persons) == 0 {
		return fmt.Errorf("no persons defined")
	}

	seenIDs := make(map[string]bool)
	for pi := range c.Persons {
		p := &c.Persons[pi]
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("person #%d has empty name", pi+1)
		}
		if p.EntityType == "" {
			p.EntityType = "human"
		}
		if len(p.Stances) == 0 {
			return fmt.Errorf("person %q has no stances", p.Name)
		}
		for si := range p.Stances {
			s := &p.Stances[si]
			if len(s.Facts) == 0 {
				return fmt.Errorf("person %q stance %q has no facts", p.Name, s.Axis)
			}
			for fi := range s.Facts {
				f := &s.Facts[fi]
				if strings.TrimSpace(f.ID) == "" {
					return fmt.Errorf("person %q stance %q fact #%d has empty id", p.Name, s.Axis, fi+1)
				}
				if seenIDs[f.ID] {
					return fmt.Errorf("duplicate fact id %q", f.ID)
				}
				seenIDs[f.ID] = true
				if f.Text == "" {
					return fmt.Errorf("fact %q has empty text", f.ID)
				}
				if f.Verb == "" {
					return fmt.Errorf("fact %q has empty verb", f.ID)
				}
				if f.EmbeddingText == "" {
					return fmt.Errorf("fact %q has empty embedding_text", f.ID)
				}
				if f.SourceURL == "" {
					return fmt.Errorf("fact %q has empty source_url", f.ID)
				}
				if f.Lang == "" {
					f.Lang = "fr"
				}
				if f.Date != "" {
					t, err := time.Parse("2006-01-02", f.Date)
					if err != nil {
						return fmt.Errorf("fact %q has invalid date %q (want YYYY-MM-DD): %w", f.ID, f.Date, err)
					}
					f.ParsedDate = &t
				}
			}
		}
	}

	for i, ps := range c.ExpectedPairs {
		expected := strings.ToUpper(strings.TrimSpace(ps.Expected))
		if expected != "YES" && expected != "NO" {
			return fmt.Errorf("expected_pairs[%d] has invalid expected %q (want YES or NO)", i, ps.Expected)
		}
		c.ExpectedPairs[i].Expected = expected
		if !seenIDs[ps.A] {
			return fmt.Errorf("expected_pairs[%d] references unknown id %q (a)", i, ps.A)
		}
		if !seenIDs[ps.B] {
			return fmt.Errorf("expected_pairs[%d] references unknown id %q (b)", i, ps.B)
		}
		if ps.A == ps.B {
			return fmt.Errorf("expected_pairs[%d] is self-referential (%q)", i, ps.A)
		}
	}

	return nil
}
