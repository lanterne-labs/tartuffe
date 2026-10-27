package search

import "testing"

// The quota answers seen in prod mark ErrQuota; other errors do not.
func TestQuotaAnswers(t *testing.T) {
	for _, msg := range []string{`{"message":"Not enough credits","statusCode":400}`, "Your account has run out of searches."} {
		if !quotaMessage(msg) {
			t.Errorf("quotaMessage(%q) = false, want true", msg)
		}
	}
	if quotaMessage("Unprocessable Entity: invalid freshness") {
		t.Error("a bad request is not a quota answer")
	}
	if !quotaStatus(429) || !quotaStatus(402) || quotaStatus(422) || quotaStatus(500) {
		t.Error("quotaStatus: want 429 and 402 only")
	}
}
