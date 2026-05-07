// Package complaints holds the user-feedback flow: complaints are filed by
// visitors via a Tally form (frontend) and triaged here on the backend by an
// LLM that decides whether to delete the offending fact / contradiction /
// stance, dismiss the complaint, or flag it as ambiguous and keep both.
package complaints

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// Kind identifies which type of object a complaint targets. Source of truth is
// the `target_kind` hidden field on the Tally submission.
type Kind string

const (
	KindFact          Kind = "fact"
	KindContradiction Kind = "contradiction"
	KindStance        Kind = "stance"
)

// Complaint is the parsed, validated form of a Tally submission for triage.
// Exactly one of FactID / ContradictionID / StanceID is populated, matching Kind.
type Complaint struct {
	SubmissionID    string    // Tally submission id, used to delete the row after triage.
	Kind            Kind      // From the target_kind hidden field.
	FactID          int64     // Set iff Kind == KindFact.
	ContradictionID int64     // Set iff Kind == KindContradiction.
	StanceID        int64     // Set iff Kind == KindStance.
	Label           string    // From target_label, for human-readable logs.
	Description     string    // The free-text textarea response.
	SourceURL       string    // The page the report was filed from (for debugging).
	SubmittedAt     time.Time // submittedAt on the Tally submission.
}

// TallyClient talks to the Tally REST API. List + Delete are the only endpoints
// we need; submissions are written by the frontend popup, not by us.
type TallyClient struct {
	httpClient *http.Client
	formID     string
	token      string
}

// NewTallyClientFromEnv reads TALLY_FORM_ID and TALLY_API_TOKEN. Returns an
// error if either is missing — the triage CLI cannot run without them.
func NewTallyClientFromEnv() (*TallyClient, error) {
	formID := os.Getenv("TALLY_FORM_ID")
	token := os.Getenv("TALLY_API_TOKEN")
	if formID == "" || token == "" {
		return nil, fmt.Errorf("TALLY_FORM_ID and TALLY_API_TOKEN must be set")
	}
	return &TallyClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		formID:     formID,
		token:      token,
	}, nil
}

// tallySubmissionsPage matches the JSON returned by GET /forms/{id}/submissions.
type tallySubmissionsPage struct {
	Page                              int                `json:"page"`
	Limit                             int                `json:"limit"`
	HasMore                           bool               `json:"hasMore"`
	TotalNumberOfSubmissionsPerFilter int                `json:"totalNumberOfSubmissionsPerFilter"`
	Questions                         []tallyQuestion    `json:"questions"`
	Submissions                       []tallySubmission  `json:"submissions"`
}

type tallyQuestion struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type tallySubmission struct {
	ID            string          `json:"id"`
	FormID        string          `json:"formId"`
	RespondentID  string          `json:"respondentId"`
	IsCompleted   bool            `json:"isCompleted"`
	SubmittedAt   time.Time       `json:"submittedAt"`
	Responses     []tallyResponse `json:"responses"`
}

type tallyResponse struct {
	ID         string          `json:"id"`
	QuestionID string          `json:"questionId"`
	Answer     json.RawMessage `json:"answer"`
}

// ListComplaints fetches every submission, paginates through HasMore, and
// returns parsed Complaint records. Submissions whose hidden fields don't
// validate (missing target_kind, ID/kind mismatch, etc.) are logged and
// dropped — they're left in Tally for manual review rather than auto-deleted.
func (c *TallyClient) ListComplaints(ctx context.Context) ([]Complaint, error) {
	var out []Complaint
	page := 1
	for {
		body, err := c.fetchPage(ctx, page)
		if err != nil {
			return nil, err
		}

		var resp tallySubmissionsPage
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("decode submissions page %d: %w (raw: %s)", page, err, truncate(string(body), 500))
		}

		titleByQID := make(map[string]string, len(resp.Questions))
		for _, q := range resp.Questions {
			titleByQID[q.ID] = q.Title
		}

		for _, sub := range resp.Submissions {
			if !sub.IsCompleted {
				continue
			}
			complaint, err := parseComplaint(sub, titleByQID)
			if err != nil {
				log.Warn().Err(err).Str("submission_id", sub.ID).Msg("Skipping unparseable Tally submission")
				continue
			}
			out = append(out, complaint)
		}

		if !resp.HasMore {
			break
		}
		page++
	}
	return out, nil
}

func (c *TallyClient) fetchPage(ctx context.Context, page int) ([]byte, error) {
	url := fmt.Sprintf("https://api.tally.so/forms/%s/submissions?page=%d", c.formID, page)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create list request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list submissions http call: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read list response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tally list returned status %d: %s", resp.StatusCode, truncate(string(body), 500))
	}
	return body, nil
}

// DeleteSubmission removes a Tally submission. Idempotent at the call site:
// 404 is treated as success so re-runs don't fail on already-cleaned rows.
func (c *TallyClient) DeleteSubmission(ctx context.Context, submissionID string) error {
	url := fmt.Sprintf("https://api.tally.so/forms/%s/submissions/%s", c.formID, submissionID)
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("create delete request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete submission http call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		log.Debug().Str("submission_id", submissionID).Msg("Tally submission already gone")
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("tally delete returned status %d: %s", resp.StatusCode, truncate(string(body), 500))
	}
	return nil
}

// parseComplaint walks a submission's responses, indexes them by hidden-field
// title, validates that target_kind / ID fields agree, and builds a Complaint.
func parseComplaint(sub tallySubmission, titleByQID map[string]string) (Complaint, error) {
	values := make(map[string]string, len(sub.Responses))
	for _, r := range sub.Responses {
		title := titleByQID[r.QuestionID]
		if title == "" {
			continue
		}
		val, ok := decodeAnswer(r.Answer)
		if !ok {
			continue
		}
		values[strings.TrimSpace(title)] = strings.TrimSpace(val)
	}

	kindStr := values["target_kind"]
	if kindStr == "" {
		return Complaint{}, fmt.Errorf("missing target_kind hidden field")
	}
	kind := Kind(strings.ToLower(kindStr))
	if kind != KindFact && kind != KindContradiction && kind != KindStance {
		return Complaint{}, fmt.Errorf("unknown target_kind %q", kindStr)
	}

	c := Complaint{
		SubmissionID: sub.ID,
		Kind:         kind,
		Label:        values["target_label"],
		Description:  pickDescription(values),
		SourceURL:    values["source_url"],
		SubmittedAt:  sub.SubmittedAt,
	}

	switch kind {
	case KindFact:
		id, err := parseInt64(values["fact_id"])
		if err != nil {
			return Complaint{}, fmt.Errorf("fact complaint missing/invalid fact_id: %w", err)
		}
		c.FactID = id
	case KindContradiction:
		id, err := parseInt64(values["contradiction_id"])
		if err != nil {
			return Complaint{}, fmt.Errorf("contradiction complaint missing/invalid contradiction_id: %w", err)
		}
		c.ContradictionID = id
	case KindStance:
		id, err := parseInt64(values["stance_id"])
		if err != nil {
			return Complaint{}, fmt.Errorf("stance complaint missing/invalid stance_id: %w", err)
		}
		c.StanceID = id
	}

	if strings.TrimSpace(c.Description) == "" {
		return Complaint{}, fmt.Errorf("complaint has empty description")
	}

	return c, nil
}

// pickDescription returns the textarea response. The hidden fields all have
// fixed names; whatever non-hidden, non-empty field is left is the user's text.
// Common Tally label is "What's wrong with this?"; we tolerate variations.
func pickDescription(values map[string]string) string {
	hidden := map[string]struct{}{
		"target_kind":      {},
		"target_label":     {},
		"fact_id":          {},
		"contradiction_id": {},
		"stance_id":        {},
		"source_url":       {},
	}
	for k, v := range values {
		if _, isHidden := hidden[k]; isHidden {
			continue
		}
		if v != "" {
			return v
		}
	}
	return ""
}

// decodeAnswer is permissive: Tally answers are typically strings for text,
// hidden, and textarea fields, but we tolerate numbers / arrays of strings too.
func decodeAnswer(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String(), true
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return arr[0], true
	}
	return "", false
}

func parseInt64(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	return strconv.ParseInt(s, 10, 64)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
