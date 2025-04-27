package backend

import (
	"digital-mirror/backend/extractor" // Correct import path
	"time"
)

// Fact represents a single structured factual statement extracted from content.
// It includes the people involved in this specific statement, the normalized fact,
// and the original quote from the source.
type Fact struct {
	// NormalizedFactText is the core factual claim extracted and normalized by the LLM.
	// Example: "Person X plans to cut income taxes".
	NormalizedFactText string `json:"normalized_fact"`
	// OriginalQuote is the original sentence or snippet from the source document
	// corresponding to the NormalizedFactText. Provides context and verification.
	// Example: "In a fiery speech yesterday, Person X declared their unwavering intention to slash income taxes..."
	OriginalQuote string `json:"original_quote"`
	// Topics are relevant keywords or categories for the statement.
	Topics []string `json:"topics"`
	// InvolvedPeople lists the names of individuals mentioned in or relevant to the statement.
	// Should contain at least one name.
	InvolvedPeople []string `json:"involved_people"`
	// SourceURL is the URL where the statement was found.
	SourceURL string `json:"source_url"`
	// PublishedAt is the publication date of the source content, if available.
	PublishedAt *time.Time `json:"published_at"`
	// Embedding is the vector embedding of the NormalizedFactText (optional, usually filled later).
	Embedding []float32 `json:"embedding,omitempty"` // Use float32 for common embedding sizes
}

// FactExtractor defines the interface for extracting structured facts from text content using an LLM.
type FactExtractor interface {
	// ExtractFacts processes the given ExtractedContent and returns a slice of Fact structs.
	// It needs the original personName searched for context in prompt generation.
	ExtractFacts(personName string, content extractor.ExtractedContent) ([]Fact, error) // Ensure type reference is correct
}

// --- Shared Prompt Template ---

const (
	// FactExtractionSystemPrompt is the template used to instruct the LLM for fact extraction.
	// It expects %s placeholders for: personName, sourceURL, content text.
	// It explicitly asks for a JSON array output matching the Fact structure fields
	// (normalized_fact, original_quote, topics, involved_people).
	FactExtractionSystemPrompt = `You are an expert AI assistant specialized in extracting factual statements from news articles and online content. Your goal is to identify specific claims, actions, or statements attributed to individuals, focusing primarily on '%s'.

Given the following text content from URL '%s':
--- TEXT START ---
%s
--- TEXT END ---

Please extract all distinct factual statements made by or about the people mentioned. For each fact, provide:
1. 'normalized_fact': The core factual claim, concisely summarized and normalized (e.g., "Person A announced plan B", "Person C criticized policy D"). Focus on the objective statement.
2. 'original_quote': A concise snippet from the original text (max ~250 chars) that directly supports the normalized_fact. If the supporting text is spread out, use " [...] " to bridge the essential parts. Example: "Person A stated X [...] and plans to achieve Y."
3. 'topics': A list of 1-3 relevant topic keywords (e.g., ["economy", "tax reform"]).
4. 'involved_people': A list of all names of people directly involved in or mentioned in the statement.

Respond ONLY with a valid JSON array containing objects matching this structure. Each object represents one distinct fact. If no relevant facts are found, return an empty JSON array []. Do not include any explanations or commentary outside the JSON array.

Example Output Format:
[
  {
    "normalized_fact": "Gabriel Attal stated the government aims to reduce unemployment.",
    "original_quote": "Gabriel Attal stated the government aims to reduce unemployment [...] within two years.",
    "topics": ["employment", "government policy"],
    "involved_people": ["Gabriel Attal"]
  },
  {
    "normalized_fact": "Marine Le Pen criticized the proposed budget.",
    "original_quote": "Marine Le Pen criticized the proposed budget, calling it 'unrealistic [...]' during the debate.",
    "topics": ["budget", "politics"],
    "involved_people": ["Marine Le Pen"]
  }
]`
) 