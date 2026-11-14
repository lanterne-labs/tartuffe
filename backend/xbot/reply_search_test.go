package xbot

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplySearchReadsXOncePerSlot(t *testing.T) {
	var r replySearch
	calls := 0
	search := func() ([]FoundTweet, error) {
		calls++
		return []FoundTweet{{ID: "1"}}, nil
	}
	slot := time.Date(2026, 11, 14, 13, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		found, err := r.find(slot, search)
		require.NoError(t, err)
		assert.Len(t, found, 1)
	}
	assert.Equal(t, 1, calls, "a draft tried again does not search again")
	_, err := r.find(slot.Add(24*time.Hour), search)
	require.NoError(t, err)
	assert.Equal(t, 2, calls, "the next slot searches")

	// A search that failed is not kept.
	var f replySearch
	_, err = f.find(slot, func() ([]FoundTweet, error) { return nil, errors.New("away") })
	assert.Error(t, err)
	_, err = f.find(slot, search)
	require.NoError(t, err)
	assert.Equal(t, 3, calls)
}
