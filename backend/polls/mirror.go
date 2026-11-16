package polls

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Vote is the event of a vote in progress, with the ballots its latest tally
// counted.
type Vote struct {
	Slug                  string
	MaxChoices, MinVoters int
	Counted               int
}

// FeaturedVote gives the vote in progress at a time: the first featured
// event open then that pairs names ("slug:id,…", the isoloir poll of each
// event, as the frontend's VITE_VOTE_POLLS), with the ballots its latest
// tally counted on the mirror (empty: the site's). Nil without pairs.
func FeaturedVote(events *PGStore, pairs, mirror string) (func(ctx context.Context, t time.Time) (*Vote, error), error) {
	ids := map[string]int{}
	for _, pair := range strings.Split(pairs, ",") {
		if pair = strings.TrimSpace(pair); pair == "" {
			continue
		}
		slug, id, ok := strings.Cut(pair, ":")
		n, err := strconv.Atoi(id)
		if !ok || err != nil {
			return nil, fmt.Errorf("the polls of the vote %q: want slug:id", pair)
		}
		ids[slug] = n
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if mirror == "" {
		mirror = "https://vote.tartuffe.me/vote-data"
	}
	return func(ctx context.Context, t time.Time) (*Vote, error) {
		list, err := events.ListEvents(ctx)
		if err != nil {
			return nil, err
		}
		for i := range list {
			e := &list[i]
			id, ok := ids[e.Slug]
			if !ok || !e.Featured || !IsOpen(e, t) {
				continue
			}
			counted, err := CountedBallots(ctx, mirror, id)
			if err != nil {
				return nil, err
			}
			return &Vote{Slug: e.Slug, MaxChoices: e.MaxChoices, MinVoters: e.MinVoters, Counted: counted}, nil
		}
		return nil, nil
	}, nil
}

// CountedBallots reads how many ballots the latest tally of a poll counted,
// from the vote's public mirror (polls/<id>/tallies/latest.json, isoloir SPEC
// §15). The proofs are not checked here: the number is for a person to read,
// not a result. No tally yet: 0.
func CountedBallots(ctx context.Context, mirror string, poll int) (int, error) {
	url := fmt.Sprintf("%s/polls/%d/tallies/latest.json?t=%d", strings.TrimRight(mirror, "/"), poll, time.Now().Unix())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return 0, fmt.Errorf("read the tally: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, fmt.Errorf("read the tally: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("read the tally: status %d", resp.StatusCode)
	}
	var entry struct {
		Kind string `json:"kind"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil || entry.Kind != "tally" {
		return 0, fmt.Errorf("read the tally: not a tally entry")
	}
	var body struct {
		Counted int `json:"counted"`
	}
	if err := json.Unmarshal([]byte(entry.Body), &body); err != nil {
		return 0, fmt.Errorf("read the tally body: %w", err)
	}
	return body.Counted, nil
}
