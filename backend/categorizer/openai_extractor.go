package categorizer

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/core"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/util"
	"encoding/json"
	"fmt"
	"os"
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

// openAIChatModelDefault is a sensible default, can be overridden via OPEN_AI_CHAT_MODEL
const openAIChatModelDefault = "gpt-4o-mini"

// buildChatAPIURL composes the full chat completions endpoint from the OPEN_AI_API base.
func buildChatAPIURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

// OpenAIExtractor implements FactExtractor using an OpenAI-compatible Chat Completions API.
type OpenAIExtractor struct {
	// apiKey is the API key to authenticate requests.
	apiKey string
	// apiURL is the full endpoint for chat completions.
	apiURL string
	// model is the model to use for chat completions.
	model string
	// baseClient handles the underlying HTTP communication.
	baseClient *BaseLLMClient
}

// NewOpenAIExtractorFromEnv creates an OpenAIExtractor using OPEN_AI_ env vars.
// Required: OPEN_AI_KEY, OPEN_AI_API (base, e.g., https://api.openai.com/v1)
// Optional: OPEN_AI_CHAT_MODEL (defaults to openAIChatModelDefault)
func NewOpenAIExtractorFromEnv() (*OpenAIExtractor, error) {
	apiKey := os.Getenv("OPEN_AI_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPEN_AI_KEY environment variable not set")
	}
	base := os.Getenv("OPEN_AI_API")
	if base == "" {
		return nil, fmt.Errorf("OPEN_AI_API environment variable not set")
	}
	model := os.Getenv("OPEN_AI_CHAT_MODEL")
	if strings.TrimSpace(model) == "" {
		model = openAIChatModelDefault
	}
	return &OpenAIExtractor{
		apiKey:     apiKey,
		apiURL:     buildChatAPIURL(base),
		model:      model,
		baseClient: NewBaseLLMClient(),
	}, nil
}

// NOTE: Chat types (ChatMessage, ChatRequest, ChatResponse, etc.) are now
// defined as type aliases in base_llm_client.go pointing to backend/llm package.
// This allows shared usage across packages without import cycles.

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
func (m *OpenAIExtractor) ExtractFactsWithConfig(ctx context.Context, cfg core.FactExtractorConfig) ([]backend.Fact, error) {
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
	promptOverhead := core.BuildFactExtractionPrompt(overheadCfg)
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
				log.Error().Err(err).Str("url", cfg.SourceURL).Int("chunk_num", chunkNum).Int("total_chunks", len(chunks)).Msg("Failed to extract facts for chunk")
				continue
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
	cfg := core.FactExtractorConfig{
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
		pluginInstructions = core.PartyInstructions
	case "person":
		// Person extraction uses core prompt without additional instructions
		pluginInstructions = ""
	default:
		pluginInstructions = ""
	}

	cfg := core.FactExtractorConfig{
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

	pluginInstructions := fmt.Sprintf(core.PersonWithPartiesInstructions, partiesStr)

	cfg := core.FactExtractorConfig{
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
func (m *OpenAIExtractor) extractChunkWithReactiveRetry(ctx context.Context, cfg core.FactExtractorConfig, chunk string) ([]backend.Fact, error) {
	chunkCfg := core.FactExtractorConfig{
		TargetName:         cfg.TargetName,
		SourceURL:          cfg.SourceURL,
		Content:            chunk,
		PluginInstructions: cfg.PluginInstructions,
		Now:                cfg.Now,
	}
	prompt := core.BuildFactExtractionPrompt(chunkCfg)
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
	// sees the real cause (not "context overflow after split").
	if leftErr != nil && rightErr != nil {
		return nil, err
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
	reqPayload := ChatRequest{
		Model: m.model,
		Messages: []ChatMessage{
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
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal chat request payload: %w", err)
	}

	log.Debug().Str("url", m.apiURL).Int("prompt_chars", len(systemPrompt)).Msg("Attempting LLM chat API call for fact extraction")
	startTime := time.Now()

	bodyBytes, err := m.baseClient.DoRequest(ctx, m.apiURL, m.apiKey, payloadBytes)

	duration := time.Since(startTime)
	if err != nil {
		log.Error().Err(err).Dur("duration", duration).Str("url", m.apiURL).Msg("Chat API call failed")
		var chatResp ChatResponse
		if parseErr := json.Unmarshal(bodyBytes, &chatResp); parseErr == nil && chatResp.Error != nil {
			return nil, fmt.Errorf("Chat API returned error: [%s] %s - %s (request status: %w)", chatResp.Error.Code, chatResp.Error.Type, chatResp.Error.Message, err)
		}
		return nil, err
	}
	log.Debug().Dur("duration", duration).Int("response_bytes", len(bodyBytes)).Str("url", m.apiURL).Msg("Chat API call successful")

	var chatResp ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to decode chat API response envelope: %w. Body: %s", err, string(bodyBytes))
	}

	if chatResp.Error != nil {
		return nil, fmt.Errorf("Chat API returned error: [%s] %s - %s", chatResp.Error.Code, chatResp.Error.Type, chatResp.Error.Message)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("Chat API returned no choices")
	}

	jsonContent := chatResp.Choices[0].Message.Content
	chunkFacts, err := ParseFactJSON([]byte(jsonContent), sourceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to parse facts from chat response content: %w", err)
	}

	return chunkFacts, nil
}
