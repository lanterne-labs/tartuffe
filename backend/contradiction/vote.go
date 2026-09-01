package contradiction

// JudgeVotes is how many times a binary judging call (audit, verify) is
// repeated before its verdict is taken by majority.
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

// majorityVote calls once() up to `votes` times and returns a result from the
// largest agreeing group, where agreement is defined by key().
//
// It stops as soon as one verdict holds a strict majority, so a stable case
// normally costs 2 calls rather than 3 — only genuinely contested cases pay
// full price. A call that returns an error casts no vote; if every call errors
// the last error is returned. Ties (possible only when errors reduce the
// number of votes cast) resolve to the first verdict seen, so the outcome
// never depends on map iteration order.
func majorityVote[T any](votes int, once func() (T, error), key func(T) string) (T, error) {
	if votes <= 1 {
		return once()
	}

	tally := make(map[string]int, votes)
	first := make(map[string]T, votes)
	var order []string // first-seen order, for deterministic tie-breaks
	var lastErr error

	for i := 0; i < votes; i++ {
		v, err := once()
		if err != nil {
			lastErr = err
			continue
		}
		k := key(v)
		if _, seen := tally[k]; !seen {
			first[k] = v
			order = append(order, k)
		}
		tally[k]++
		if tally[k] > votes/2 {
			return first[k], nil // unbeatable majority, skip the rest
		}
	}

	if len(order) == 0 {
		var zero T
		return zero, lastErr
	}
	best := order[0]
	for _, k := range order[1:] {
		if tally[k] > tally[best] {
			best = k
		}
	}
	return first[best], nil
}
