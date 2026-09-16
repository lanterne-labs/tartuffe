package translate

import (
	"context"
	"errors"
	"testing"

	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRepairStorer struct {
	facts   []storage.FactNeedingTranslation
	listErr error
}

func (f *fakeRepairStorer) GetFactsMissingEnglish(context.Context, int) ([]storage.FactNeedingTranslation, error) {
	return f.facts, f.listErr
}

func (f *fakeRepairStorer) SetFactEnglish(context.Context, int64, string) error { return nil }

func TestRepairMissingEnglish_NothingToDoNeedsNoTranslator(t *testing.T) {
	// No LLM configuration: the empty case must still succeed because the
	// translator is only built when there is work.
	t.Setenv("OPEN_AI_KEY", "")
	t.Setenv("OPEN_AI_API", "")
	filled, failed, err := RepairMissingEnglish(context.Background(), &fakeRepairStorer{}, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, filled)
	assert.Equal(t, 0, failed)
}

func TestRepairMissingEnglish_ListError(t *testing.T) {
	_, _, err := RepairMissingEnglish(context.Background(), &fakeRepairStorer{listErr: errors.New("boom")}, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list facts missing english")
}

func TestRepairMissingEnglish_TranslatorUnavailable(t *testing.T) {
	t.Setenv("OPEN_AI_KEY", "")
	t.Setenv("OPEN_AI_API", "")
	fs := &fakeRepairStorer{facts: []storage.FactNeedingTranslation{{ID: 1, Text: "bonjour", Lang: "fr"}}}
	_, _, err := RepairMissingEnglish(context.Background(), fs, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create translator")
}
