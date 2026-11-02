package contradiction

import (
	"digital-mirror/backend/llmtest"
	"digital-mirror/backend/promptrules"
	"fmt"
	"os"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAuditGolden is the golden regression set for the contradiction AUDITOR
// (the judge behind `dm contradictions check --apply` and the 5-day pipeline
// auto-evict, which runs Apply:true). In prod a deletion additionally requires
// a confirming 2nd audit pass AND a verifier cross-veto (applyAuditRemoval),
// so a keep-failure here surfaces as an "unstable" row, not an instant delete.
// It loads the SAME testdata/verify_cases.yaml as TestVerifyGolden and asserts:
//   - expect=keep  -> the auditor must NOT return remove (keep or ambiguous are fine;
//     --apply only deletes on remove, so "not remove" == "not evicted"). This is the
//     critical direction: the auditor must never auto-delete a real contradiction.
//   - expect=drop  -> the auditor SHOULD return remove (so the FP actually gets evicted).
//
// The auditor and verifier are DIFFERENT prompts; a case can pass one and fail the
// other. The auditor over-removing reversals (announcement_then_action /
// third_party_narrative misapplied) is exactly why this test exists.
//
// LOCAL ONLY: self-skips unless OPEN_AI_KEY is set (free local GPU); never runs in CI.
//
//	source .env; go test -v -run TestAuditGolden ./backend/contradiction/
func TestAuditGolden(t *testing.T) {
	llmtest.Setup(t)

	raw, err := os.ReadFile("testdata/verify_cases.yaml")
	if err != nil {
		t.Fatalf("reading verify_cases.yaml: %v", err)
	}
	var cf verifyCaseFile
	if err := yaml.Unmarshal(raw, &cf); err != nil {
		t.Fatalf("parsing verify_cases.yaml: %v", err)
	}
	if len(cf.Cases) == 0 {
		t.Fatal("no cases in verify_cases.yaml")
	}

	auditor, err := NewOpenAIContradictionAuditorFromEnv()
	if err != nil {
		t.Fatalf("failed to create auditor: %v", err)
	}

	ctx, cancel := llmtest.Context(t)
	defer cancel()

	// The cases run concurrently with -llm-claude: mu guards the counts.
	var mu sync.Mutex
	var keepPass, keepFail, dropPass, dropFail int
	count := func(n *int) { mu.Lock(); *n++; mu.Unlock() }
	llmtest.Group(t, func(t *testing.T) {
		for _, tc := range cf.Cases {
			tc := tc
			if !tc.judgedBy("audit") {
				continue
			}
			wantKeep, err := expectToKeep(tc.Expect)
			if err != nil {
				t.Fatalf("case %q: %v", tc.Name, err)
			}
			t.Run(tc.Name, func(t *testing.T) {
				llmtest.Parallel(t)
				res := auditor.Audit(ctx, promptrules.Statement{Text: tc.FactA, Also: tc.AlsoA}, promptrules.Statement{Text: tc.FactB, Also: tc.AlsoB}, caseDate(t, tc.DateA), caseDate(t, tc.DateB), tc.ClaimedReason)
				if res.Err != nil {
					t.Fatalf("Audit error: %v", res.Err)
				}
				removed := res.Verdict == VerdictRemove
				if wantKeep {
					// Critical: a real contradiction must NOT be auto-removed.
					if removed {
						count(&keepFail)
						t.Errorf("[%s] AUDITOR REMOVED a keep case (rule=%s)\n    A: %s\n    B: %s\n    audit: %s",
							tc.Bucket, res.Rule, tc.FactA, tc.FactB, res.Reasoning)
						return
					}
					count(&keepPass)
					t.Logf("[%s] kept (verdict=%s) ✓", tc.Bucket, res.Verdict)
					return
				}
				// FP case: the auditor should flag it for removal.
				if !removed {
					count(&dropFail)
					t.Errorf("[%s] auditor did NOT remove an FP (verdict=%s rule=%s)\n    A: %s\n    B: %s\n    audit: %s",
						tc.Bucket, res.Verdict, res.Rule, tc.FactA, tc.FactB, res.Reasoning)
					return
				}
				count(&dropPass)
				t.Logf("[%s] removed (rule=%s) ✓", tc.Bucket, res.Rule)
			})
		}
	})

	fmt.Fprintf(os.Stderr,
		"\n=== AUDIT GOLDEN: keep %d/%d (must-not-remove), drop %d/%d (should-remove) ===\n",
		keepPass, keepPass+keepFail, dropPass, dropPass+dropFail)
}
