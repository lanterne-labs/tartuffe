package pairfilter

import (
	"fmt"
	"os"
	"strconv"
)

// FromEnv builds the filter the settings ask for:
//
//	PAIR_FILTER_URL        the decision model's /v1/systemone; unset: no filter (All)
//	PAIR_FILTER_KEY        optional bearer token
//	PAIR_FILTER_THRESHOLD  optional, in (0, 1): default DefaultDecisionThreshold
//
// With a URL, the filter is the decision model with All as its fallback: when
// the model does not answer, every pair goes to the judge.
func FromEnv() (Filter, error) {
	url := os.Getenv("PAIR_FILTER_URL")
	if url == "" {
		return All{}, nil
	}
	threshold := DefaultDecisionThreshold
	if v := os.Getenv("PAIR_FILTER_THRESHOLD"); v != "" {
		t, err := strconv.ParseFloat(v, 64)
		if err != nil || t <= 0 || t >= 1 {
			return nil, fmt.Errorf("PAIR_FILTER_THRESHOLD=%q: want a number between 0 and 1", v)
		}
		threshold = t
	}
	return Fallback(NewDecision(url, os.Getenv("PAIR_FILTER_KEY"), threshold), All{}), nil
}
