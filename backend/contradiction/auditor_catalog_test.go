package contradiction

import (
	"strings"
	"testing"

	"digital-mirror/backend/promptrules"
)

// TestCatalogEnumInSync guards the single-source contract between the shared
// false-positive catalog (promptrules.FalsePositivePatterns) and the auditor's
// closed rule enum: every catalog bucket [token] must be an allowed audit rule,
// and every allowed rule (except "none") must have a bucket in the catalog.
// If this fails, the auditor would either reject a token it just read from the
// catalog (strict-parse error) or describe a bucket it cannot name.
func TestCatalogEnumInSync(t *testing.T) {
	catalogTokens := map[string]bool{}
	for _, line := range strings.Split(promptrules.FalsePositivePatterns, "\n") {
		open := strings.Index(line, "[")
		close := strings.Index(line, "]")
		if open == 0 && close > open { // bucket lines start with "- [token] ..."
			t.Fatalf("unexpected: bracket at line start without leading dash: %q", line)
		}
		// Bucket lines look like "- [token] description".
		if strings.HasPrefix(strings.TrimSpace(line), "- [") && close > open {
			catalogTokens[line[open+1:close]] = true
		}
	}
	if len(catalogTokens) == 0 {
		t.Fatal("no [token] buckets parsed from FalsePositivePatterns — catalog format changed?")
	}

	allowed := append(append([]string{}, contradictionAuditAllowedRulesList...), contradictionAuditUnlistedRules...)
	allowedSet := map[string]bool{}
	for _, token := range allowed {
		allowedSet[token] = true
	}
	for token := range catalogTokens {
		if !allowedSet[token] {
			t.Errorf("catalog bucket %q is not in the auditor allowed-rule enum", token)
		}
	}
	for _, token := range allowed {
		if token == "none" {
			continue
		}
		if !catalogTokens[token] {
			t.Errorf("auditor rule %q has no [%s] bucket in the shared catalog", token, token)
		}
	}
}

// TestCatalogSprintfSafe asserts the catalog contains no literal '%', since it is
// concatenated into fmt.Sprintf templates (detector, auditor, verifier). A stray
// '%' would corrupt every assembled prompt.
func TestCatalogSprintfSafe(t *testing.T) {
	if strings.Contains(promptrules.FalsePositivePatterns, "%") {
		t.Error("FalsePositivePatterns contains a literal '%' — will break fmt.Sprintf assembly; reword without the symbol")
	}
}
