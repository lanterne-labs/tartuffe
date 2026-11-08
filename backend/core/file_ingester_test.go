package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"digital-mirror/backend"
)

// A fact extracted from a file takes the file's publication date: the facts of
// `dm ingest files` were stored without one, and those whose statement carried
// no date were skipped.
func TestDatePublished(t *testing.T) {
	file := time.Date(2022, 9, 6, 0, 0, 0, 0, time.UTC)
	llm := time.Date(2022, 9, 7, 0, 0, 0, 0, time.UTC)
	own := time.Date(2022, 9, 5, 0, 0, 0, 0, time.UTC)

	facts := []backend.Fact{
		{},                         // nothing of its own: the file's date
		{ArticlePublishedAt: &llm}, // the file's date goes before the LLM's
		{PublishedAt: &own},        // a date already set stays
	}
	datePublished(facts, &file)
	require.Equal(t, &file, facts[0].PublishedAt)
	require.Equal(t, &file, facts[1].PublishedAt)
	require.Equal(t, &own, facts[2].PublishedAt)

	// A file without a date: the LLM's date, else none.
	facts = []backend.Fact{{ArticlePublishedAt: &llm}, {}}
	datePublished(facts, nil)
	require.Equal(t, &llm, facts[0].PublishedAt)
	require.Nil(t, facts[1].PublishedAt)
}
