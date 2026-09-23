package contradiction

import (
	"testing"
	"time"

	"digital-mirror/backend/promptrules"
)

func st(text string, also ...string) promptrules.Statement {
	return promptrules.Statement{Text: text, Also: also}
}

// AuditInputHash must be a pure function of everything that determines an audit
// verdict — stable across calls, and different whenever any input that could
// change the verdict changes.
func TestAuditInputHash(t *testing.T) {
	d1 := time.Date(2026, 2, 8, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

	base := AuditInputHash(st("fact A"), st("fact B"), &d1, &d2, "reason")

	if got := AuditInputHash(st("fact A"), st("fact B"), &d1, &d2, "reason"); got != base {
		t.Fatalf("hash not stable: %s vs %s", got, base)
	}

	variants := map[string]string{
		"fact A changed": AuditInputHash(st("fact A'"), st("fact B"), &d1, &d2, "reason"),
		"fact B changed": AuditInputHash(st("fact A"), st("fact B'"), &d1, &d2, "reason"),
		"date A changed": AuditInputHash(st("fact A"), st("fact B"), &d2, &d2, "reason"),
		"date B nil":     AuditInputHash(st("fact A"), st("fact B"), &d1, nil, "reason"),
		"reason changed": AuditInputHash(st("fact A"), st("fact B"), &d1, &d2, "reason'"),
		"facts swapped":  AuditInputHash(st("fact B"), st("fact A"), &d1, &d2, "reason"),
	}
	for name, got := range variants {
		if got == base {
			t.Errorf("%s: hash did not change", name)
		}
	}

	// A statement's other wordings are an input: gaining a member re-opens the
	// pinned verdict, since the auditor now sees details it had not seen.
	if AuditInputHash(st("fact A", "a restatement with 43,8 milliards"), st("fact B"), &d1, &d2, "reason") == base {
		t.Error("adding a member did not change the hash")
	}

	// Field separation: moving a suffix across the fact boundary must not collide.
	if AuditInputHash(st("ab"), st("c"), nil, nil, "r") == AuditInputHash(st("a"), st("bc"), nil, nil, "r") {
		t.Error("field-boundary collision between fact A and fact B")
	}
}
