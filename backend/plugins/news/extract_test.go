package news

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/plugins"
	"digital-mirror/backend/search"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oneFactReply is a chat completion carrying one valid extracted fact.
const oneFactReply = `{"choices":[{"message":{"role":"assistant","content":"[{\"subject_entity\":\"Jean Dupont\",\"subject_entity_type\":\"human\",\"normalized_fact\":\"Jean Dupont a promis de baisser la TVA.\",\"lang\":\"fr\",\"canonical_verb\":\"PROMISED\",\"embedding_text\":\"baisse de la TVA\",\"original_quote\":\"Nous baisserons la TVA\"}]"}}]}`

// noFactReply is a chat completion extracting nothing.
const noFactReply = `{"choices":[{"message":{"role":"assistant","content":"[]"}}]}`

// stubServer answers every request with status and body.
func stubServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// stubLLM returns a fact extractor whose chat endpoint replies chatReply and an
// embedder whose endpoint answers embedStatus/embedBody. No fallback.
func stubLLM(t *testing.T, chatReply string, embedStatus int, embedBody string) (*categorizer.OpenAIExtractor, *embedding.OpenAIEmbeddingGenerator) {
	t.Helper()
	t.Setenv("OPEN_AI_KEY", "k")
	t.Setenv("OPEN_AI_CHAT_MODEL", "m")
	t.Setenv("OPEN_AI_EMBED_MODEL", "e")
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("CF_ACCESS_CLIENT_ID", "")
	t.Cleanup(llm.MarkPrimaryUp)

	t.Setenv("OPEN_AI_API", stubServer(t, http.StatusOK, chatReply).URL)
	ext, err := categorizer.NewOpenAIExtractorFromEnv()
	require.NoError(t, err)
	t.Setenv("OPEN_AI_API", stubServer(t, embedStatus, embedBody).URL)
	emb, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	require.NoError(t, err)
	return ext, emb
}

// failingFactStorer fails every write: the inserter cannot store anything.
type failingFactStorer struct{}

var errStoreDown = errors.New("store down")

func (failingFactStorer) FindOrInsertPersons(context.Context, []string) ([]int64, error) {
	return nil, errStoreDown
}
func (failingFactStorer) FindOrInsertPersonWithType(context.Context, string, string) (int64, error) {
	return 0, errStoreDown
}
func (failingFactStorer) FindSimilarActiveFacts(context.Context, core.SimilarFactQuery) ([]core.SimilarFact, error) {
	return nil, errStoreDown
}
func (failingFactStorer) InsertFact(context.Context, core.StoreFactInput) (int64, error) {
	return 0, errStoreDown
}
func (failingFactStorer) DeleteFact(context.Context, int64) error { return errStoreDown }
func (failingFactStorer) LinkSourceToFact(context.Context, int64, string, string, *time.Time, string, map[string]any) (int64, error) {
	return 0, errStoreDown
}
func (failingFactStorer) LinkPersonsToSourceMention(context.Context, int64, []int64) error {
	return errStoreDown
}

type noopStance struct{}

func (noopStance) ProcessNewFact(context.Context, int64, int64, string, []float32, string, *time.Time) (*core.StanceProcessResult, error) {
	return &core.StanceProcessResult{}, nil
}

// unavailableFactStorer fails the subject lookup as the entity gate does when
// no LLM endpoint answers.
type unavailableFactStorer struct{ failingFactStorer }

func (unavailableFactStorer) FindOrInsertPersonWithType(context.Context, string, string) (int64, error) {
	return 0, fmt.Errorf("entity gate: %w", llm.ErrUnavailable)
}

// embeddingOK is an embeddings response carrying one vector.
const embeddingOK = `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}]}`

// TestExtractDocumentFailsOnEmbeddingOrInsertError: a fact that cannot be
// embedded or stored because no LLM endpoint answers fails the document, so it
// stays pending instead of being marked extracted with its facts lost. Any
// other failure of one fact skips that fact and is counted.
func TestExtractDocumentFailsOnEmbeddingOrInsertError(t *testing.T) {
	ctx := context.Background()
	target := plugins.Target{Name: "Jean Dupont", PersonID: 1}
	doc := core.SourceDocument{ID: 1, URL: "https://example.test/a", Content: "Jean Dupont: « Nous baisserons la TVA »."}

	t.Run("embedding rejected", func(t *testing.T) {
		ext, emb := stubLLM(t, oneFactReply, http.StatusBadRequest, `{"error":{"message":"bad input"}}`)
		p := &Provider{FactExtractor: ext, Embedder: emb}
		res := &Result{}
		_, err := p.extractDocument(ctx, target, doc, nil, Options{}, core.NewFactInserter(failingFactStorer{}, noopStance{}), res)
		require.NoError(t, err)
		assert.Len(t, res.Errors, 1)
	})

	t.Run("embedding unavailable", func(t *testing.T) {
		ext, emb := stubLLMEmbeddingDown(t)
		p := &Provider{FactExtractor: ext, Embedder: emb}
		_, err := p.extractDocument(ctx, target, doc, nil, Options{}, core.NewFactInserter(failingFactStorer{}, noopStance{}), &Result{})
		require.ErrorIs(t, err, llm.ErrUnavailable)
	})

	t.Run("insert failed", func(t *testing.T) {
		ext, emb := stubLLM(t, oneFactReply, http.StatusOK, embeddingOK)
		p := &Provider{FactExtractor: ext, Embedder: emb}
		res := &Result{}
		_, err := p.extractDocument(ctx, target, doc, nil, Options{}, core.NewFactInserter(failingFactStorer{}, noopStance{}), res)
		require.NoError(t, err)
		require.Len(t, res.Errors, 1)
		assert.ErrorIs(t, res.Errors[0], errStoreDown)
	})

	t.Run("insert unavailable", func(t *testing.T) {
		ext, emb := stubLLM(t, oneFactReply, http.StatusOK, embeddingOK)
		p := &Provider{FactExtractor: ext, Embedder: emb}
		_, err := p.extractDocument(ctx, target, doc, nil, Options{}, core.NewFactInserter(unavailableFactStorer{}, noopStance{}), &Result{})
		require.ErrorIs(t, err, llm.ErrUnavailable)
	})
}

// stubLLMEmbeddingDown returns a fact extractor and an embedder for a primary
// marked down (breaker open) and a fallback that serves chat with one fact
// but rejects embeddings for lack of credits: every embedding fails with
// llm.ErrUnavailable.
func stubLLMEmbeddingDown(t *testing.T) (*categorizer.OpenAIExtractor, *embedding.OpenAIEmbeddingGenerator) {
	t.Helper()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = io.WriteString(w, `{"error":{"message":"Insufficient credits"}}`)
			return
		}
		_, _ = io.WriteString(w, oneFactReply)
	}))
	t.Cleanup(fallback.Close)
	stubLLM(t, oneFactReply, http.StatusOK, embeddingOK)
	t.Setenv("FALLBACK_URL", fallback.URL)
	t.Setenv("FALLBACK_KEY", "fb")
	t.Setenv("FALLBACK_CHAT_MODEL", "fm")
	ext, err := categorizer.NewOpenAIExtractorFromEnv()
	require.NoError(t, err)
	emb, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	require.NoError(t, err)
	llm.MarkPrimaryDown("test")
	return ext, emb
}

type emptyNewsSearcher struct{}

func (emptyNewsSearcher) SearchNews(string, search.NewsSearchOptions) ([]search.NewsResult, error) {
	return nil, nil
}

// TestCollectFactsExtractsLeftoversWhenNothingNew: a document an earlier run
// left unfinished is extracted even when the search brings nothing new.
func TestCollectFactsExtractsLeftoversWhenNothingNew(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	_, err := storer.Pool().Exec(ctx, "TRUNCATE TABLE source_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	personID, err := storer.FindOrInsertPersonWithType(ctx, "jean leftover", "human")
	require.NoError(t, err)
	url := "https://example.test/leftover"
	ids, err := storer.SaveFetchedDocuments(ctx, []core.SourceDocument{{
		SourceType: "news", URL: url, PersonID: personID, Content: "Un article sans position.",
	}})
	require.NoError(t, err)

	ext, emb := stubLLM(t, noFactReply, http.StatusOK, `{}`)
	p := NewProvider(emptyNewsSearcher{}, nil, ext, emb, storer, &core.StanceManager{})
	_, err = p.CollectFacts(ctx, plugins.Target{Name: "jean leftover", PersonID: personID}, Options{MaxParse: 5})
	require.NoError(t, err)

	var state string
	require.NoError(t, storer.Pool().QueryRow(ctx, `SELECT state FROM source_documents WHERE id = $1`, ids[url]).Scan(&state))
	assert.Equal(t, core.DocExtracted, state)
}
