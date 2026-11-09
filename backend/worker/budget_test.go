package worker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpendSplitRounds(t *testing.T) {
	now := time.Date(2026, 11, 9, 5, 0, 0, 0, time.UTC)
	b := &spendSplit{share: 0.6}
	assert.False(t, b.reached(now, 0, 100), "a new day starts a round: 60 of 100")
	assert.False(t, b.reached(now, 59.9, 100))
	assert.True(t, b.reached(now, 60, 100), "the share is spent")
	b.nextRound(now, 80, 100) // the pass spent 20
	assert.False(t, b.reached(now, 91.9, 100), "the next round: 60 percent of the 20 left")
	assert.True(t, b.reached(now, 92, 100))
	assert.False(t, b.reached(now.Add(24*time.Hour), 0, 100), "the next day starts over")
}

func TestExtractionShareFromEnv(t *testing.T) {
	t.Setenv("LLM_EXTRACTION_SHARE", "")
	v, err := extractionShareFromEnv()
	require.NoError(t, err)
	assert.Equal(t, defaultExtractionShare, v)
	t.Setenv("LLM_EXTRACTION_SHARE", "0.5")
	v, err = extractionShareFromEnv()
	require.NoError(t, err)
	assert.Equal(t, 0.5, v)
	t.Setenv("LLM_EXTRACTION_SHARE", "60")
	_, err = extractionShareFromEnv()
	assert.Error(t, err)
}

func TestAPIPays(t *testing.T) {
	ctx := context.Background()
	serving := true
	h := &Housekeeping{
		split:        &spendSplit{share: 0.6},
		seatsServing: func() bool { return serving },
		readSpend:    func(context.Context, time.Time) (float64, float64, error) { return 7, 100, nil },
	}
	_, _, pays, err := h.apiPays(ctx)
	require.NoError(t, err)
	assert.False(t, pays, "the seats serve: the key is not paying")
	serving = false
	spent, limit, pays, err := h.apiPays(ctx)
	require.NoError(t, err)
	assert.True(t, pays)
	assert.Equal(t, 7.0, spent)
	assert.Equal(t, 100.0, limit)
	h.split.share = 0
	_, _, pays, err = h.apiPays(ctx)
	require.NoError(t, err)
	assert.False(t, pays, "a share of 0 turns the split off")
}

func TestRejudgeBudgetFromEnv(t *testing.T) {
	t.Setenv("DETECT_REJUDGE_BUDGET", "")
	if v, err := rejudgeBudgetFromEnv(); err != nil || v != defaultRejudgeBudget {
		t.Errorf("unset: got %d, %v; want %d", v, err, defaultRejudgeBudget)
	}
	t.Setenv("DETECT_REJUDGE_BUDGET", "4500")
	if v, err := rejudgeBudgetFromEnv(); err != nil || v != 4500 {
		t.Errorf("4500: got %d, %v", v, err)
	}
	for _, bad := range []string{"-1", "many"} {
		t.Setenv("DETECT_REJUDGE_BUDGET", bad)
		if _, err := rejudgeBudgetFromEnv(); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}
