package contradiction

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// The hash must be deterministic across calls and short hex — and, by
// construction, change with any edit of the judging prompts (covered by
// hashing the templates themselves, not a manually maintained version).
func TestJudgeRulebookHash(t *testing.T) {
	h := JudgeRulebookHash()
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{16}$`), h)
	require.Equal(t, h, JudgeRulebookHash())
}
