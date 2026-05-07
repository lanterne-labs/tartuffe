package complaints

import (
	"context"
	"digital-mirror/backend/llm"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Verdict is the LLM's decision on a single complaint.
type Verdict string

const (
	// VerdictDeleteTarget: the complaint is valid; remove the offending fact /
	// contradiction / stance and the complaint row.
	VerdictDeleteTarget Verdict = "delete_target"
	// VerdictDismissComplaint: the complaint is wrong; keep the target, drop the complaint.
	VerdictDismissComplaint Verdict = "dismiss_complaint"
	// VerdictAmbiguous: the complaint has merit but isn't clear-cut; keep both
	// for human review.
	VerdictAmbiguous Verdict = "ambiguous"
)

// VerdictResult bundles the verdict with the model's reasoning.
type VerdictResult struct {
	Verdict Verdict
	Reason  string
}

// Judge evaluates a complaint prompt and returns a verdict. The interface lets
// the triage loop be tested with a fake judge.
type Judge interface {
	Judge(ctx context.Context, prompt string) (VerdictResult, error)
}

// OpenAIJudge uses the OpenAI-compatible chat API (same env vars as the rest
// of the codebase) to evaluate complaints in JSON mode.
type OpenAIJudge struct {
	client *llm.BaseLLMClient
	apiURL string
	apiKey string
	model  string
}

// NewOpenAIJudgeFromEnv reads OPEN_AI_KEY, OPEN_AI_API, OPEN_AI_CHAT_MODEL.
func NewOpenAIJudgeFromEnv() (*OpenAIJudge, error) {
	key := os.Getenv("OPEN_AI_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPEN_AI_KEY environment variable not set")
	}
	base := os.Getenv("OPEN_AI_API")
	if base == "" {
		return nil, fmt.Errorf("OPEN_AI_API environment variable not set")
	}
	model := os.Getenv("OPEN_AI_CHAT_MODEL")
	if strings.TrimSpace(model) == "" {
		model = "gpt-4o-mini"
	}
	base = strings.TrimRight(base, "/")
	apiURL := base
	if !strings.HasSuffix(base, "/chat/completions") {
		apiURL = base + "/chat/completions"
	}
	return &OpenAIJudge{
		client: llm.NewBaseLLMClient(),
		apiURL: apiURL,
		apiKey: key,
		model:  model,
	}, nil
}

// Judge calls the LLM and parses the verdict. Returns an error on transport
// failure or unparseable response; a parsed-but-unknown verdict is returned as
// VerdictAmbiguous so triage stays conservative.
func (j *OpenAIJudge) Judge(ctx context.Context, prompt string) (VerdictResult, error) {
	payload := llm.ChatRequest{
		Model:          j.model,
		Messages:       []llm.ChatMessage{{Role: "user", Content: prompt}},
		Temperature:    0.1,
		MaxTokens:      512,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return VerdictResult{}, fmt.Errorf("marshal judge request: %w", err)
	}

	respBytes, err := j.client.DoRequest(ctx, j.apiURL, j.apiKey, bodyBytes)
	if err != nil {
		return VerdictResult{}, fmt.Errorf("judge API call: %w", err)
	}

	var chatResp llm.ChatResponse
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		return VerdictResult{}, fmt.Errorf("decode judge response: %w (raw: %s)", err, truncate(string(respBytes), 500))
	}
	if chatResp.Error != nil {
		return VerdictResult{}, fmt.Errorf("judge API error: %s", chatResp.Error.Message)
	}
	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return VerdictResult{}, fmt.Errorf("judge API returned no content")
	}

	content := chatResp.Choices[0].Message.Content
	var parsed struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return VerdictResult{}, fmt.Errorf("parse judge JSON: %w (raw: %s)", err, truncate(content, 500))
	}

	v := Verdict(strings.ToLower(strings.TrimSpace(parsed.Verdict)))
	switch v {
	case VerdictDeleteTarget, VerdictDismissComplaint, VerdictAmbiguous:
		return VerdictResult{Verdict: v, Reason: strings.TrimSpace(parsed.Reason)}, nil
	default:
		// Unknown verdict — keep both, let a human look.
		return VerdictResult{
			Verdict: VerdictAmbiguous,
			Reason:  fmt.Sprintf("unrecognized verdict %q from model; original reason: %s", parsed.Verdict, parsed.Reason),
		}, nil
	}
}
