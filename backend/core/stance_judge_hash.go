package core

import (
	"crypto/sha256"
	"encoding/hex"
)

// StanceJudgeHash identifies the rules the stance analysis judges a pair
// under: its prompt, which embeds the shared contradiction rules. The
// analysis records its verdicts on the pairs of a new fact and the key facts
// of its stance (fact_pair_checks, StanceStorer.RecordJudgedPairs) under this
// hash; the maintenance pass keeps the negatives of the current hash and
// re-opens the others for the detector, as it does with the detector's own
// rulebook hash (contradiction.JudgeRulebookHash). Any edit to the prompt
// changes the hash; nothing else does.
func StanceJudgeHash() string {
	h := sha256.New()
	h.Write([]byte("stance-analyze\x00"))
	h.Write([]byte(stanceAnalysisPromptTpl))
	return hex.EncodeToString(h.Sum(nil))[:16]
}
