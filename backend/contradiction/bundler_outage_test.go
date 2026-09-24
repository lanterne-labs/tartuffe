package contradiction

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"digital-mirror/backend/embedding"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/require"
)

// splitStancesStorer puts the two facts in different stances and offers one
// close candidate stance; it records any bundle assignment.
type splitStancesStorer struct{ assigned int }

func (*splitStancesStorer) GetFactStanceMap(context.Context, int64, []int64) (map[int64]int64, error) {
	return map[int64]int64{1: 10, 2: 20}, nil
}

func (*splitStancesStorer) FindTopStancesForPerson(context.Context, int64, []float32, int) ([]storage.StanceCandidate, error) {
	return []storage.StanceCandidate{{ID: 10, ClusterLabel: "retraites", Distance: 0.1}}, nil
}

func (s *splitStancesStorer) SetContradictionBundleStance(context.Context, int64, int64) error {
	s.assigned++
	return nil
}

type fixedEmbedder struct{}

func (fixedEmbedder) GenerateEmbeddings(_ context.Context, _ embedding.Purpose, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

// TestBundlerResolve_UnavailableLLM: an unavailable LLM at the relatedness
// check is returned instead of being read as "no candidate accepted".
func TestBundlerResolve_UnavailableLLM(t *testing.T) {
	t.Setenv("FALLBACK_URL", chatStub(t, http.StatusPaymentRequired, ""))
	t.Setenv("FALLBACK_CHAT_MODEL", "stub")
	t.Setenv("FALLBACK_KEY", "k")
	llm.MarkPrimaryDown("bundler outage test")
	t.Cleanup(llm.MarkPrimaryUp)

	storer := &splitStancesStorer{}
	cfg := llm.Config{BaseURL: "http://127.0.0.1:1/v1", APIKey: "test", ChatModel: "primary"}
	b := &Bundler{storer: storer, embedder: fixedEmbedder{}, client: cfg.NewClient("bundler-test"), topK: DefaultBundlerTopK, maxYesDist: DefaultBundlerMaxDistance}
	err := b.Resolve(context.Background(), 1, 7, 1, 2, "veut 62 ans", "vote 64 ans", "62 puis 64")
	require.ErrorIs(t, err, llm.ErrUnavailable)
	require.Zero(t, storer.assigned)
}

type fakeResolver struct{ err error }

func (f fakeResolver) Resolve(context.Context, int64, int64, int64, int64, string, string, string) error {
	return f.err
}

// TestResolveBundle: only an unavailable LLM stops the detection; any other
// bundler failure leaves the row solo and the run goes on.
func TestResolveBundle(t *testing.T) {
	ctx := context.Background()
	err := resolveBundle(ctx, fakeResolver{fmt.Errorf("bundler: %w", llm.ErrUnavailable)}, 1, 7, 1, 2, "a", "b", "r")
	require.ErrorIs(t, err, llm.ErrUnavailable)
	require.NoError(t, resolveBundle(ctx, fakeResolver{errors.New("stance lookup failed")}, 1, 7, 1, 2, "a", "b", "r"))
	require.NoError(t, resolveBundle(ctx, fakeResolver{}, 1, 7, 1, 2, "a", "b", "r"))
}
