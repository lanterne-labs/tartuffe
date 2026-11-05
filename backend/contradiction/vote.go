package contradiction

// JudgeVotes is how many times a binary judging call (audit, verify) is
// repeated before its verdict is taken by majority, on an endpoint that votes:
// the local GPU box. On Claude the judges ask once (llm.Votes).
//
// 3 since 2026-09-01. It is a knob rather than a constant because the golden
// suites showed the judges are NOT deterministic
// even at temperature 0: vLLM's continuous batching makes a verdict depend on
// what else is in flight, so the same case can flip between runs. Measured on
// the audit golden, a full-suite run failed a DIFFERENT random 3-4 cases each
// time while those same cases passed 5/5 in isolation.
//
// That instability is a property of the judge, not of the tests — production
// takes single-sample verdicts today and is exposed to exactly the same flips.
// Voting is therefore implemented here, in production code, so the goldens
// measure the behaviour prod actually has.
var JudgeVotes = 3

// AuditVotes is the vote count for the AUDITOR specifically. The auditor runs
// every 5 days over stored rows and auto-evicts, so its per-call cost is
// negligible while a wrong eviction is destructive — it gets 5 votes where the
// insert-time verifier gets 3.
var AuditVotes = 5
