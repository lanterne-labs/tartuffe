package contradiction

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"digital-mirror/backend/promptrules"
)

// JudgeRulebookHash identifies the exact judging rules a pair verdict was
// produced under: the detector prompt (which embeds the shared contradiction
// rules and the false-positive catalog), the refutation prompt of the verify
// pass, and the vote count. It is stored on every fact_pair_checks row; the
// nightly pipeline re-opens negative verdicts carrying a different hash so a
// rulebook change re-judges the whole backlog of NOs — the counterpart of the
// auditor's last_audit_hash for YESes. Any edit to those prompts changes the
// hash; nothing else does.
func JudgeRulebookHash() string {
	h := sha256.New()
	h.Write([]byte(contradictionPromptTpl))
	h.Write([]byte{0})
	h.Write([]byte(promptrules.RefuteContradictionPrompt))
	h.Write([]byte{0})
	fmt.Fprintf(h, "votes=%d", JudgeVotes)
	return hex.EncodeToString(h.Sum(nil))[:16]
}
