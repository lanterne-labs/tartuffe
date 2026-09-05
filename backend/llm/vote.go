package llm

// MajorityVote calls once() up to `votes` times and returns a result from the
// largest agreeing group, where agreement is defined by key(). It stops as
// soon as one verdict holds a strict majority (a stable case costs 2 calls at
// votes=3). A call that returns an error casts no vote; if every call errors
// the last error is returned. Ties resolve to the first verdict seen.
//
// This exists because temperature-0 LLM verdicts are still samples: judges
// flip on boundary cases, and a single unlucky sample must not evict a real
// contradiction or mint a false edge. See contradiction.JudgeVotes /
// AuditVotes and core.AnalyzeVotes for the production vote counts.
func MajorityVote[T any](votes int, once func() (T, error), key func(T) string) (T, error) {
	if votes <= 1 {
		return once()
	}

	tally := make(map[string]int, votes)
	first := make(map[string]T, votes)
	var order []string
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
			return first[k], nil
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
