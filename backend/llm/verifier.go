package llm

import (
	"context"
	"digital-mirror/backend/promptrules"
	"fmt"
	"regexp"
	"strings"
)

// verifyVerdictRegex matches the "VERDICT: YES" / "VERDICT: NO" line emitted by
// the refute prompt. Case-insensitive, tolerant of surrounding markdown/space.
var verifyVerdictRegex = regexp.MustCompile(`(?i)VERDICT\s*:\s*(YES|NO)`)

// verifyReasonRegex matches the "REASON: ..." line of the refute prompt.
var verifyReasonRegex = regexp.MustCompile(`(?i)REASON\s*:\s*(.*)`)

// verifyMaxTokens caps the refute response: a REASON line, then the VERDICT
// line. On a hard case Qwen3.8 (no thinking) reasons at length inside the
// REASON line ("Wait, looking closer...") and 512 tokens cut it before the
// VERDICT. The verifier runs once per flagged contradiction, so a large cap
// costs little.
const verifyMaxTokens = 1536

// VerifyContradiction runs the adversarial refutation pass
// (promptrules.RefuteContradictionPrompt) over a single claimed contradiction
// and reports whether it should be KEPT.
//
//   - keep == true  → the pass confirms a real contradiction (VERDICT: YES).
//   - keep == false → the pass refutes it as a false positive (VERDICT: NO).
//
// On model uncertainty the prompt instructs the model to answer NO, so an
// ambiguous case naturally yields keep == false (drop it). A non-nil err means
// the LLM call failed or returned no parseable verdict — that is an OPERATIONAL
// failure, not a verdict: callers should log it and fall open (keep the
// contradiction) rather than silently dropping a possibly-real one.
//
// This is the single source of truth for the verification call. Both
// contradiction.OpenAIContradictionDetector and core.StanceManager delegate
// here so the stance path and the batch paths apply identical refutation logic.
//
// A statement that stands for a group of restatements carries them in Also,
// so the pass judges the same details the detector saw.
func VerifyContradiction(ctx context.Context, c *Client, a, b promptrules.Statement, claimedReason string) (keep bool, reason string, err error) {
	prompt := fmt.Sprintf(promptrules.RefuteContradictionPrompt, a.Render(), b.Render(), claimedReason)

	content, _, err := c.Chat(ctx, ChatRequest{
		Messages:    []ChatMessage{{Role: "user", Content: prompt}},
		Temperature: 0.0,
		MaxTokens:   verifyMaxTokens,
		Task:        TaskVerify,
	})
	if err != nil {
		return false, "", fmt.Errorf("verify API call failed: %w", err)
	}

	vm := verifyVerdictRegex.FindStringSubmatch(content)
	if vm == nil {
		return false, "", fmt.Errorf("verify response had no VERDICT line: %q", strings.TrimSpace(content))
	}
	keep = strings.EqualFold(vm[1], "YES")

	if rm := verifyReasonRegex.FindStringSubmatch(content); rm != nil {
		reason = strings.TrimSpace(rm[1])
	}
	return keep, reason, nil
}
