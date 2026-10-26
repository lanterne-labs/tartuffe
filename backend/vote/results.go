package vote

import (
	"context"
	"sort"
	"time"

	"digital-mirror/backend/polls"
	"digital-mirror/backend/vote/ballot"
)

// Results are what a tally's decrypted values mean (SPEC §9.1): the
// voters, each option's first choices and rankings, the pairwise margins,
// the Schulze order on the margins and the Condorcet winner if any. Only
// the options a setup names (candidates and blank) take part.
type Results struct {
	Voters  int64           `json:"voters"`
	Options []OptionResult  `json:"options"`
	Margins map[int][]int64 `json:"margins"` // margins[i][j] = d(i, j) − d(j, i)
	Winner  *int            `json:"condorcet_winner,omitempty"`
}

// OptionResult is one option's numbers and its Schulze rank (1 = first;
// equal strengths give equal ranks).
type OptionResult struct {
	Slot   int    `json:"slot"`
	Kind   string `json:"kind"`
	Name   string `json:"name,omitempty"`
	First  int64  `json:"first"`
	Ranked int64  `json:"ranked"`
	Rank   int    `json:"rank"`
	Wins   int    `json:"wins"`
}

// marginIndex returns the index of margin[i<j] among the values.
func marginIndex(i, j int) int {
	if i > j {
		i, j = j, i
	}
	idx := 1 + 2*ballot.N
	for a := 0; a < ballot.N; a++ {
		for b := a + 1; b < ballot.N; b++ {
			if a == i && b == j {
				return idx
			}
			idx++
		}
	}
	return -1
}

// Margin returns d(i, j) − d(j, i): positive when i beats j.
func Margin(v [ballot.V]int64, i, j int) int64 {
	if i == j {
		return 0
	}
	m := v[marginIndex(i, j)]
	if i > j {
		return -m
	}
	return m
}

// ComputeResults applies Schulze (margins) to the decrypted values over
// the setup's named options.
func ComputeResults(v [ballot.V]int64, s *Setup) Results {
	var slots []int
	kinds := map[int]Option{}
	for _, o := range s.Options {
		if o.Kind == "candidate" || o.Kind == "blank" {
			slots = append(slots, o.Slot)
			kinds[o.Slot] = o
		}
	}
	n := len(slots)
	res := Results{Voters: v[0], Margins: map[int][]int64{}}
	// d[i][j] − d[j][i] is the margin; Schulze on margins.
	d := make([][]int64, n)
	for a := range slots {
		d[a] = make([]int64, n)
		for b := range slots {
			d[a][b] = Margin(v, slots[a], slots[b])
		}
		res.Margins[slots[a]] = d[a]
	}
	p := make([][]int64, n)
	for a := range p {
		p[a] = make([]int64, n)
		for b := range p[a] {
			if a != b && d[a][b] > 0 {
				p[a][b] = d[a][b]
			}
		}
	}
	for k := 0; k < n; k++ {
		for a := 0; a < n; a++ {
			if a == k {
				continue
			}
			for b := 0; b < n; b++ {
				if b == a || b == k {
					continue
				}
				if m := min(p[a][k], p[k][b]); m > p[a][b] {
					p[a][b] = m
				}
			}
		}
	}
	wins := make([]int, n)
	for a := 0; a < n; a++ {
		duels := 0
		for b := 0; b < n; b++ {
			if a == b {
				continue
			}
			if p[a][b] > p[b][a] {
				wins[a]++
			}
			if d[a][b] > 0 {
				duels++
			}
		}
		if n > 1 && duels == n-1 {
			w := slots[a]
			res.Winner = &w
		}
	}
	for a, slot := range slots {
		rank := 1
		for b := range slots {
			if wins[b] > wins[a] {
				rank++
			}
		}
		res.Options = append(res.Options, OptionResult{Slot: slot, Kind: kinds[slot].Kind, Name: kinds[slot].Name,
			First: v[1+slot], Ranked: v[1+ballot.N+slot], Rank: rank, Wins: wins[a]})
	}
	sort.SliceStable(res.Options, func(i, j int) bool { return res.Options[i].Rank < res.Options[j].Rank })
	return res
}

// PollResults is what the site's pages get of an event's results
// (/api/polls/{slug}): the latest tally's numbers with the candidates'
// cards, or hidden (below min_voters, or a blackout), with the voter count
// kept either way.
type PollResults struct {
	Voters          int64        `json:"voters"`
	Hidden          string       `json:"hidden,omitempty"`
	UpdatedAt       *time.Time   `json:"updated_at,omitempty"`
	KReal           int          `json:"k_real"`
	CondorcetWinner *int         `json:"condorcet_winner,omitempty"`
	Options         []PollOption `json:"options"`
}

// PollOption is one option's line.
type PollOption struct {
	Slot     int     `json:"slot"`
	Kind     string  `json:"kind"`
	PersonID int64   `json:"person_id,omitempty"`
	Name     string  `json:"name"`
	ImageURL *string `json:"image_url,omitempty"`
	Honesty  float64 `json:"honesty_score"`
	First    *int64  `json:"first,omitempty"`
	Ranked   *int64  `json:"ranked,omitempty"`
	Rank     *int    `json:"rank,omitempty"`
}

// ResultsOf gives the results function the polls serve: the latest tally
// of the event, decoded, with cards from people.
func ResultsOf(store *Store, people polls.People) func(ctx context.Context, e *polls.Event) (any, error) {
	return func(ctx context.Context, e *polls.Event) (any, error) {
		setups, err := store.Setups(ctx, e.ID)
		if err != nil {
			return nil, err
		}
		out := PollResults{KReal: DefaultKReal, Options: []PollOption{}}
		if len(setups) == 0 {
			return out, nil
		}
		cur := setups[len(setups)-1].Setup
		out.KReal = cur.KReal
		lt, err := store.LastTally(ctx, e.ID)
		if err != nil {
			return nil, err
		}
		var ids []int64
		for _, o := range cur.Options {
			if o.Kind == "candidate" {
				ids = append(ids, o.PersonID)
			}
		}
		cards, err := people.Cards(ctx, ids)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		var res *Results
		if lt != nil {
			vals, err := store.TallyValues(lt)
			if err != nil {
				return nil, err
			}
			r := ComputeResults(vals, &cur)
			res = &r
			out.Voters = r.Voters
			at := lt.At
			out.UpdatedAt = &at
		}
		out.Hidden = polls.Hidden(e, out.Voters, now)
		if res == nil {
			out.Hidden = polls.HiddenTooFewVoters
		}
		for _, o := range cur.Options {
			if o.Kind != "candidate" && o.Kind != "blank" {
				continue
			}
			po := PollOption{Slot: o.Slot, Kind: o.Kind, PersonID: o.PersonID, Name: o.Name}
			if c, ok := cards[o.PersonID]; ok && o.Kind == "candidate" {
				po.Name, po.ImageURL, po.Honesty = c.Name, c.ImageURL, c.HonestyScore
			}
			if res != nil && out.Hidden == "" {
				for _, r := range res.Options {
					if r.Slot == o.Slot {
						first, ranked, rank := r.First, r.Ranked, r.Rank
						po.First, po.Ranked, po.Rank = &first, &ranked, &rank
					}
				}
			}
			out.Options = append(out.Options, po)
		}
		if res != nil && out.Hidden == "" {
			out.CondorcetWinner = res.Winner
			sort.SliceStable(out.Options, func(i, j int) bool {
				ri, rj := 99, 99
				if out.Options[i].Rank != nil {
					ri = *out.Options[i].Rank
				}
				if out.Options[j].Rank != nil {
					rj = *out.Options[j].Rank
				}
				return ri < rj
			})
		}
		return out, nil
	}
}
