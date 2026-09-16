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

// verifyMaxTokens caps the refute response. The reply is two short lines; 256
// is ample even with a verbose REASON.
const verifyMaxTokens = 256

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
func VerifyContradiction(ctx context.Context, c *Client, factA, factB, claimedReason string) (keep bool, reason string, err error) {
	prompt := fmt.Sprintf(promptrules.RefuteContradictionPrompt, factA, factB, claimedReason)

	content, _, err := c.Chat(ctx, ChatRequest{
		Messages:    []ChatMessage{{Role: "user", Content: prompt}},
		Temperature: 0.0,
		MaxTokens:   verifyMaxTokens,
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
