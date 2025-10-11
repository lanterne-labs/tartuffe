package backend

import (
	"context"
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

// ChunkingParameters defines the parameters for chunking text before fact extraction.
type ChunkingParameters struct {
	ThresholdChars int // Content length (in chars) above which to chunk
	ChunkSizeChars int // Target size for each chunk (in chars)
	OverlapChars   int // Character overlap between chunks
}

// FactExtractor defines the interface for extracting structured facts from text content using an LLM.
type FactExtractor interface {
	// ExtractFacts processes the given ExtractedContent and returns a slice of Fact structs.
	// It needs the original personName searched for context in prompt generation.
	ExtractFacts(ctx context.Context, personName string, content extractor.ExtractedContent) ([]Fact, error)

	// GetChunkingParameters returns the recommended chunking parameters for the specific LLM implementation.
	GetChunkingParameters() ChunkingParameters
}

// --- Shared Prompt Template ---

const (
	// FactExtractionSystemPrompt is the template used to instruct the LLM for fact extraction.
	// Updated to focus on people's actions/statements and capturing context for contradiction checks.
	FactExtractionSystemPrompt = `You are an expert AI assistant specialized in extracting specific, verifiable factual statements about **what people say or do**, primarily focusing on '%s'. Ignore general statements or descriptions unless they directly report an action or statement by a person.

Given the following text content from URL '%s':
--- TEXT START ---
%s
--- TEXT END ---

Please extract all distinct factual statements where a person **performs an action** (e.g., voted, proposed, met, signed) or **makes a statement** (e.g., said, claimed, denied, criticized). For each fact, provide:
1.  'normalized_fact': A concise summary of the specific action or statement attributed to the person(s). **Crucially, preserve essential context needed for contradiction analysis later.** This includes:
    *   **Person(s):** Clearly identify by their **full name(s)** who performed the action or made the statement.
    *   **Precise Action/Statement:** Use specific verbs. What exactly did they *do* or *say*?
    *   **Object/Subject:** What policy, bill, event, or topic was the action/statement about? Be specific if possible (e.g., "bill H.R. 123", "the 2024 budget proposal").
    *   **Negations:** Explicitly include terms like "not", "denied", "rejected", "failed to".
    *   **Dates/Timeframes:** Include specific dates, years, or time references (e.g., "on Jan 15, 2024", "during the 2023 summit", "last week") if mentioned and relevant. This is vital for context.
    *   **Locations:** Include specific locations if relevant (e.g., "in Paris", "at the G7 meeting").
    *   **Key Quantities/Values:** Include specific numbers central to the claim if applicable (e.g., "voted against the $5M allocation").
    *   **Qualifiers:** Retain important qualifiers if they alter the claim's certainty or scope (e.g., "reportedly", "allegedly", "plans to").
    Keep it objective. Avoid introductory phrases.
2.  'original_quote': A concise snippet (max ~250 chars) from the original text that directly supports the normalized_fact, using " [...] " to bridge essential parts if necessary.
3.  'topics': 1-3 relevant topic keywords. Include specific identifiers like bill numbers or years if applicable.
4.  'involved_people': A list of the **full names of all specific human individuals** directly involved in the action or statement (e.g., "John Doe", "Jane Smith"). **Do NOT include organizations, titles without specific names (e.g., "a senator"), roles (e.g., "spokesperson"), or generic groups (e.g., "reporters", "doctors").** If a full name is not available for a specific individual, do not include them.

Respond ONLY with a valid JSON array containing objects matching this structure. Return an empty array [] if no relevant actions or statements are found. Do not include explanations outside the JSON.

Example Output Format:
[
  {
    "normalized_fact": "Alice Green proposed new environmental regulations during the May 2024 summit.",
    "original_quote": "At the recent summit [May 2024], Alice Green presented a new proposal regarding environmental regulations.",
    "topics": ["environment", "regulations", "summit 2024"],
    "involved_people": ["Alice Green"]
  },
  {
    "normalized_fact": "Bob White commented on Alice Green's proposal, calling it ambitious but necessary.",
    "original_quote": "Bob White later commented that the proposal [by Alice Green] was ambitious but necessary.",
    "topics": ["environment", "regulations", "commentary"],
    "involved_people": ["Bob White", "Alice Green"]
  },
 {
    "normalized_fact": "Senator Davis stated he was against the infrastructure bill H.R. 500 in 2022.",
    "original_quote": "In 2022, Senator Davis declared his opposition to the costly H.R. 500 infrastructure bill.",
    "topics": ["infrastructure", "legislation", "H.R. 500", "2022"],
    "involved_people": ["Senator Davis"]
  }
]`
)
