package core

import (
	"crypto/sha256"
	"digital-mirror/backend"
	"digital-mirror/backend/util"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
)

// sourceDump represents the dumped content and extracted facts for a single article.
type sourceDump struct {
	SourceURL      string           `json:"source_url"`
	Title          string           `json:"title"`
	DatePublished  *time.Time       `json:"date_published,omitempty"`
	Content        string           `json:"content"`
	ExtractedFacts []sourceDumpFact `json:"extracted_facts"`
}

// sourceDumpFact is a simplified fact representation for the source dump.
type sourceDumpFact struct {
	SubjectEntity  string `json:"subject_entity"`
	NormalizedFact string `json:"normalized_fact"`
	OriginalQuote  string `json:"original_quote"`
	CanonicalVerb  string `json:"canonical_verb,omitempty"`
}

// DumpSourceAndFacts writes a JSON file containing the article content and extracted facts.
// The file is placed in <dir>/<normalized_target>/<sha256(url)>.json.
func DumpSourceAndFacts(dir, targetName, sourceURL, title, content string, datePublished *time.Time, facts []backend.Fact) error {
	// Create target subdirectory
	normalizedTarget := util.NormalizeToDirName(targetName)
	targetDir := filepath.Join(dir, normalizedTarget)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("failed to create source dump directory %s: %w", targetDir, err)
	}

	// Build dump struct
	dump := sourceDump{
		SourceURL:      sourceURL,
		Title:          title,
		DatePublished:  datePublished,
		Content:        content,
		ExtractedFacts: make([]sourceDumpFact, len(facts)),
	}
	for i, f := range facts {
		dump.ExtractedFacts[i] = sourceDumpFact{
			SubjectEntity:  f.SubjectEntity,
			NormalizedFact: f.NormalizedFactText,
			OriginalQuote:  f.OriginalQuote,
			CanonicalVerb:  f.CanonicalVerb,
		}
	}

	// Write JSON file named by SHA256 of URL
	hash := sha256.Sum256([]byte(sourceURL))
	filename := hex.EncodeToString(hash[:]) + ".json"
	filePath := filepath.Join(targetDir, filename)

	data, err := json.MarshalIndent(dump, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal source dump: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write source dump file: %w", err)
	}

	log.Info().
		Str("path", filePath).
		Str("url", sourceURL).
		Int("facts", len(facts)).
		Msg("Dumped source content and extracted facts")

	return nil
}
