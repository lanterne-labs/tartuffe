package categorizer

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/promptrules"
	"digital-mirror/backend/util"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// --- OpenAI-compatible Chat Completion Implementation ---

const (
	// Static chunk-sizing fallbacks. The dynamic sizer in ExtractFactsWithConfig
	// reduces these further whenever (prompt + chunk + output) would exceed the
	// model's context window. Kept modest so even an "estimator off" case still
	// fits in modern small-context models (~12k tokens).
	chunkingThresholdChars = 18000
	chunkSizeChars         = 16000
	overlapChars           = 500

	// Model-context budgeting. Matches the local self-hosted Gemma (~12k tokens).
	// If you swap to a larger-context model, bump maxContextTokens to match —
	// the dynamic sizer will let chunks grow back toward chunkSizeChars.
	maxContextTokens    = 12000
	outputBudgetTokens  = 2048 // mirrors MaxTokens in callChatAPIWithPrompt
	contextSafetyTokens = 256  // headroom for system framing / role tokens
	// Conservative chars-per-token ratio. French news typically lands at ~3.5;
	// 3 over-estimates token cost slightly, which we want (better undershoot
	// the budget than overflow). The reactive halve-and-retry in
	// callChatAPIWithPrompt catches whatever still overflows.
	approxCharsPerToken = 3
)

// dynamicChunkBudgetChars returns the largest chunk size in chars that keeps
// (prompt + chunk + output_budget + safety) below the model context cap.
// promptOverheadChars is the rendered prompt size with empty Content.
func dynamicChunkBudgetChars(promptOverheadChars int) int {
	overheadTokens := promptOverheadChars/approxCharsPerToken + 1
	available := maxContextTokens - outputBudgetTokens - contextSafetyTokens - overheadTokens
	if available <= 0 {
		// Prompt alone already exceeds the budget — fall back to a minimal
		// chunk so we still emit *something* and the LLM can flag the overflow.
		return 512
	}
	return available * approxCharsPerToken
}

// OpenAIExtractor implements FactExtractor using an OpenAI-compatible Chat Completions API.
type OpenAIExtractor struct {
	client *llm.Client
}

// NewOpenAIExtractorFromEnv creates an OpenAIExtractor using OPEN_AI_ env vars.
// Required: OPEN_AI_KEY, OPEN_AI_API (base, e.g., https://api.openai.com/v1)
// Optional: OPEN_AI_CHAT_MODEL (defaults to llm.DefaultChatModel)
func NewOpenAIExtractorFromEnv() (*OpenAIExtractor, error) {
	client, err := llm.NewClientFromEnv("fact-extractor")
	if err != nil {
		return nil, err
	}
	return &OpenAIExtractor{client: client}, nil
}

// GetChunkingParameters implements the FactExtractor interface.
// Returns the static fallbacks; callers that need to fit a known prompt within
// the model context should use dynamicChunkBudgetChars() to shrink further.
func (m *OpenAIExtractor) GetChunkingParameters() backend.ChunkingParameters {
	return backend.ChunkingParameters{
		ThresholdChars: chunkingThresholdChars,
		ChunkSizeChars: chunkSizeChars,
		OverlapChars:   overlapChars,
	}
}

// ExtractFactsWithConfig extracts facts using the unified core prompt with optional plugin instructions.
// This is the primary extraction method - all other methods delegate to this one.
func (m *OpenAIExtractor) ExtractFactsWithConfig(ctx context.Context, cfg promptrules.FactExtractorConfig) ([]backend.Fact, error) {
	if strings.TrimSpace(cfg.Content) == "" {
		log.Warn().Str("url", cfg.SourceURL).Msg("ExtractFactsWithConfig called with empty content")
		return []backend.Fact{}, nil
	}

	// Pin `now` once for the whole article so every chunk's prompt shows the
	// SAME TODAY's-date — otherwise multi-chunk articles could resolve
	// "yesterday" to different absolute dates across chunks if the call straddled
	// midnight. Tests can override by setting cfg.Now before calling.
	if cfg.Now.IsZero() {
		cfg.Now = time.Now()
	}

	chunkParams := m.GetChunkingParameters()

	// Dynamic sizing: measure THIS call's prompt overhead (template + plugin
	// instructions + target/url, but with empty Content) and shrink the chunk
	// size whenever the static default would push us past the model context.
	overheadCfg := cfg
	overheadCfg.Content = ""
	promptOverhead := promptrules.BuildFactExtractionPrompt(overheadCfg)
	dynBudget := dynamicChunkBudgetChars(len(promptOverhead))
	if dynBudget < chunkParams.ChunkSizeChars {
		log.Debug().Str("url", cfg.SourceURL).Int("prompt_overhead_chars", len(promptOverhead)).Int("static_chunk", chunkParams.ChunkSizeChars).Int("dynamic_chunk", dynBudget).Msg("Dynamic chunk budget binds — shrinking chunks to fit model context")
		chunkParams.ChunkSizeChars = dynBudget
		if chunkParams.ThresholdChars > dynBudget {
			chunkParams.ThresholdChars = dynBudget
		}
	}

	var allExtractedFacts []backend.Fact

	if len(cfg.Content) > chunkParams.ThresholdChars {
		log.Info().Str("url", cfg.SourceURL).Int("content_length", len(cfg.Content)).Int("threshold", chunkParams.ThresholdChars).Int("chunk_size", chunkParams.ChunkSizeChars).Msg("Content exceeds threshold, applying chunking for fact extraction")
		chunks := util.ChunkText(cfg.Content, chunkParams.ChunkSizeChars, chunkParams.OverlapChars)
		log.Info().Str("url", cfg.SourceURL).Int("chunk_count", len(chunks)).Msg("Content split into chunks")

		for i, chunk := range chunks {
			chunkNum := i + 1
			log.Debug().Str("url", cfg.SourceURL).Int("chunk_num", chunkNum).Int("total_chunks", len(chunks)).Int("chunk_len", len(chunk)).Msg("Attempting fact extraction for chunk")

			chunkFacts, err := m.extractChunkWithReactiveRetry(ctx, cfg, chunk)
			if err != nil {
				// A document counts as extracted only when every chunk was: a
				// skipped chunk would have its content deleted with the rest.
				return nil, fmt.Errorf("failed to extract facts from chunk %d/%d for url %s: %w", chunkNum, len(chunks), cfg.SourceURL, err)
			}
			allExtractedFacts = append(allExtractedFacts, chunkFacts...)
			log.Debug().Str("url", cfg.SourceURL).Int("chunk_num", chunkNum).Int("total_chunks", len(chunks)).Int("facts_extracted", len(chunkFacts)).Msg("Successfully processed chunk")
		}
	} else {
		log.Debug().Str("url", cfg.SourceURL).Int("content_length", len(cfg.Content)).Int("threshold", chunkParams.ThresholdChars).Msg("Content below threshold, processing directly without chunking")

		facts, err := m.extractChunkWithReactiveRetry(ctx, cfg, cfg.Content)
		if err != nil {
			return nil, fmt.Errorf("failed to extract facts from content for url %s: %w", cfg.SourceURL, err)
		}
		allExtractedFacts = facts
	}

	// Consolidate: group by (CanonicalVerb, EmbeddingText), keep the most specific (longest NormalizedFactText)
	type groupKey struct {
		Verb      string
		Embedding string
	}
	groupMap := make(map[groupKey]backend.Fact)
	for _, fact := range allExtractedFacts {
		key := groupKey{Verb: fact.CanonicalVerb, Embedding: fact.EmbeddingText}
		existing, exists := groupMap[key]
		if !exists || len(fact.NormalizedFactText) > len(existing.NormalizedFactText) {
			groupMap[key] = fact
		}
	}
	finalFacts := make([]backend.Fact, 0, len(groupMap))
	for _, fact := range groupMap {
		finalFacts = append(finalFacts, fact)
	}

	log.Info().Str("url", cfg.SourceURL).Int("initial_facts", len(allExtractedFacts)).Int("final_facts", len(finalFacts)).Msg("Fact extraction completed")
	return finalFacts, nil
}

// ExtractFacts extracts facts from content about a person/target using the core prompt.
// This is a convenience method that wraps ExtractFactsWithConfig.
func (m *OpenAIExtractor) ExtractFacts(ctx context.Context, targetName string, content extractor.ExtractedContent) ([]backend.Fact, error) {
	cfg := promptrules.FactExtractorConfig{
		TargetName: targetName,
		SourceURL:  content.URL,
		Content:    content.Content,
	}
	return m.ExtractFactsWithConfig(ctx, cfg)
}

// ExtractFactsWithType extracts facts using plugin instructions appropriate for the target type.
// targetType should be "person" or "party". Falls back to default for unknown types.
func (m *OpenAIExtractor) ExtractFactsWithType(ctx context.Context, targetName string, targetType string, content extractor.ExtractedContent) ([]backend.Fact, error) {
	var pluginInstructions string
	switch targetType {
	case "party":
		pluginInstructions = promptrules.PartyInstructions
	case "person":
		// Person extraction uses core prompt without additional instructions
		pluginInstructions = ""
	default:
		pluginInstructions = ""
	}

	cfg := promptrules.FactExtractorConfig{
		TargetName:         targetName,
		SourceURL:          content.URL,
		Content:            content.Content,
		PluginInstructions: pluginInstructions,
	}
	return m.ExtractFactsWithConfig(ctx, cfg)
}

// ExtractFactsForPersonWithParties extracts facts about a person while providing party context.
// The LLM is informed about the person's party affiliations to help contextualize statements.
// affiliatedParties should be a list of party names the person is affiliated with.
func (m *OpenAIExtractor) ExtractFactsForPersonWithParties(ctx context.Context, personName string, affiliatedParties []string, content extractor.ExtractedContent) ([]backend.Fact, error) {
	partiesStr := strings.Join(affiliatedParties, ", ")
	if partiesStr == "" {
		partiesStr = "(none)"
	}

	pluginInstructions := fmt.Sprintf(promptrules.PersonWithPartiesInstructions, partiesStr)

	cfg := promptrules.FactExtractorConfig{
		TargetName:         personName,
		SourceURL:          content.URL,
		Content:            content.Content,
		PluginInstructions: pluginInstructions,
	}
	return m.ExtractFactsWithConfig(ctx, cfg)
}

// extractChunkWithReactiveRetry builds the prompt for `chunk` and calls the
// chat API. On a context-overflow 400 from the model (the only error the
// static + dynamic sizers can still miss — typically when the tokenizer is
// denser than our chars/token estimate), it halves the chunk and retries each
// half once. Two recursion levels max; deeper overflows are returned as-is so
// the caller logs the failure rather than spinning.
func (m *OpenAIExtractor) extractChunkWithReactiveRetry(ctx context.Context, cfg promptrules.FactExtractorConfig, chunk string) ([]backend.Fact, error) {
	chunkCfg := promptrules.FactExtractorConfig{
		TargetName:         cfg.TargetName,
		SourceURL:          cfg.SourceURL,
		Content:            chunk,
		PluginInstructions: cfg.PluginInstructions,
		Now:                cfg.Now,
	}
	prompt := promptrules.BuildFactExtractionPrompt(chunkCfg)
	facts, err := m.callChatAPIWithPrompt(ctx, prompt, cfg.SourceURL)
	if err == nil {
		return facts, nil
	}
	if !isContextOverflowError(err) || len(chunk) < 1024 {
		return nil, err
	}
	mid := len(chunk) / 2
	log.Warn().Str("url", cfg.SourceURL).Int("chunk_len", len(chunk)).Msg("Context overflow — splitting chunk in half and retrying")
	leftFacts, leftErr := m.extractChunkWithReactiveRetry(ctx, cfg, chunk[:mid])
	rightFacts, rightErr := m.extractChunkWithReactiveRetry(ctx, cfg, chunk[mid:])
	// If both halves still fail, surface the original error so the operator
	// sees the real cause (not "context overflow after split"). One failed half
	// fails the chunk: returning the other half alone would lose its facts.
	if leftErr != nil && rightErr != nil {
		return nil, err
	}
	if leftErr != nil {
		return nil, fmt.Errorf("first half of split chunk: %w", leftErr)
	}
	if rightErr != nil {
		return nil, fmt.Errorf("second half of split chunk: %w", rightErr)
	}
	out := append([]backend.Fact{}, leftFacts...)
	out = append(out, rightFacts...)
	return out, nil
}

// isContextOverflowError detects the "maximum context length" 400 family that
// every OpenAI-compatible backend emits when input + output exceeds the model
// window. Substring match — wire format varies by provider but the phrase is
// consistent across OpenAI / vLLM / llama.cpp / llama-swap / Together / etc.
func isContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "maximum context length") ||
		strings.Contains(msg, "context length") && strings.Contains(msg, "exceed") ||
		strings.Contains(msg, "context_length_exceeded")
}

// callChatAPIWithPrompt performs a single call to the Chat API with a pre-formatted prompt.
func (m *OpenAIExtractor) callChatAPIWithPrompt(ctx context.Context, systemPrompt string, sourceURL string) ([]backend.Fact, error) {
	log.Debug().Str("url", m.client.URL).Int("prompt_chars", len(systemPrompt)).Msg("Attempting LLM chat API call for fact extraction")
	startTime := time.Now()

	jsonContent, _, err := m.client.Chat(ctx, llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "system", Content: systemPrompt},
		},
		Temperature: 0.1,
		// outputBudgetTokens (2048) — see the chunk-sizing constants block at the
		// top of this file. Changing this value MUST also update outputBudgetTokens
		// so the dynamic chunk sizer keeps reserving the right amount of headroom.
		MaxTokens: outputBudgetTokens,
		// Intentionally NO response_format. Local Gemma (llama.cpp) enforces
		// {"type":"json_object"} strictly — the root must be a JSON object, not
		// an array — and collapses the prompt's requested array of facts into a
		// single object, capping yield at 1 fact per article. ParseFactJSON
		// already tolerates ```json fences and array/object roots.
	})

	duration := time.Since(startTime)
	if err != nil {
		// A context-overflow 400 surfaces here as an llm.ClientError whose
		// message carries the body, which is what isContextOverflowError reads.
		log.Error().Err(err).Dur("duration", duration).Str("url", m.client.URL).Msg("Chat API call failed")
		return nil, err
	}
	log.Debug().Dur("duration", duration).Int("content_chars", len(jsonContent)).Str("url", m.client.URL).Msg("Chat API call successful")

	chunkFacts, err := ParseFactJSON([]byte(jsonContent), sourceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to parse facts from chat response content: %w", err)
	}

	return chunkFacts, nil
}
