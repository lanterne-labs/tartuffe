package ballot

import (
	"errors"
	"fmt"
	"math/big"
)

// Ranking is what a voter expressed: Len options in order (0 is a
// withdrawal), the blank vote only last.
type Ranking struct {
	Len   int
	Slots [3]int
}

// Validate applies the ranking rules of SPEC §6.1 under the setup's mask
// (bit c set when option c may be ranked), exactly as the circuit does.
func (r Ranking) Validate(mask uint32) error {
	if r.Len < 0 || r.Len > 3 {
		return errors.New("ballot: a ranking has at most 3 options")
	}
	for k := 0; k < 3; k++ {
		o := r.Slots[k]
		if k >= r.Len {
			if o != 0 {
				return errors.New("ballot: an unused slot must be 0")
			}
			continue
		}
		if o < 0 || o >= N {
			return fmt.Errorf("ballot: option %d out of range", o)
		}
		if mask>>uint(o)&1 == 0 {
			return fmt.Errorf("ballot: option %d is not allowed by the setup", o)
		}
		for j := 0; j < k; j++ {
			if r.Slots[j] == o {
				return fmt.Errorf("ballot: option %d ranked twice", o)
			}
		}
		if o == Blank && k != r.Len-1 {
			return errors.New("ballot: the blank vote must come last")
		}
	}
	return nil
}

// Values returns the V values of the ranking in {−1, 0, 1}: voter,
// first[c], ranked[c], margin[i<j] in lexicographic order.
func (r Ranking) Values() [V]int8 {
	pos := [N]int{}
	for c := range pos {
		pos[c] = 3
	}
	for k := 0; k < r.Len; k++ {
		pos[r.Slots[k]] = k
	}
	var v [V]int8
	if r.Len >= 1 {
		v[0] = 1
	}
	for c := 0; c < N; c++ {
		if r.Len >= 1 && r.Slots[0] == c {
			v[1+c] = 1
		}
		if pos[c] < 3 {
			v[1+N+c] = 1
		}
	}
	idx := 1 + 2*N
	for i := 0; i < N; i++ {
		for j := i + 1; j < N; j++ {
			switch {
			case pos[i] < pos[j]:
				v[idx] = 1
			case pos[j] < pos[i]:
				v[idx] = -1
			}
			idx++
		}
	}
	return v
}

// Pack turns values into the J plaintexts: digits v + 1 in base 2^36,
// the first value lowest, the unused digits of the last plaintext at 1.
func Pack(v [V]int8) [J]*big.Int {
	var p [J]*big.Int
	for j := 0; j < J; j++ {
		acc := new(big.Int)
		w := big.NewInt(1)
		for k := 0; k < PerCT; k++ {
			idx := PerCT*j + k
			d := int64(1)
			if idx < V {
				d = int64(v[idx]) + 1
			}
			acc.Add(acc, new(big.Int).Mul(big.NewInt(d), w))
			w = new(big.Int).Mul(w, Digit)
		}
		p[j] = acc
	}
	return p
}

// Unpack is the inverse of Pack for a sum over count ballots: each digit,
// in [0, 2·count], minus count. It fails on a digit out of range.
func Unpack(p [J]*big.Int, count int64) ([V]int64, error) {
	var out [V]int64
	for j := 0; j < J; j++ {
		x := new(big.Int).Set(p[j])
		mask := new(big.Int).Sub(Digit, big.NewInt(1))
		for k := 0; k < PerCT; k++ {
			d := new(big.Int).And(x, mask).Int64()
			x.Rsh(x, DigitBits)
			idx := PerCT*j + k
			if d < 0 || d > 2*count {
				return out, fmt.Errorf("ballot: digit %d of plaintext %d is %d, outside [0, %d]", k, j, d, 2*count)
			}
			if idx < V {
				out[idx] = d - count
			} else if d != count {
				return out, fmt.Errorf("ballot: padding digit %d of plaintext %d is %d, not %d", k, j, d, count)
			}
		}
		if x.Sign() != 0 {
			return out, fmt.Errorf("ballot: plaintext %d has bits above its digits", j)
		}
	}
	return out, nil
}

// FromValues reconstructs the ranking a single ballot's values encode, or
// reports that they are not a valid ballot's. It is the check the tallier
// runs on each decrypted ballot.
func FromValues(v [V]int64) (Ranking, error) {
	var r Ranking
	if v[0] == 0 {
		for _, x := range v {
			if x != 0 {
				return r, errors.New("ballot: a withdrawal with nonzero values")
			}
		}
		return r, nil
	}
	if v[0] != 1 {
		return r, errors.New("ballot: voter is not 0 or 1")
	}
	// The ranked options, ordered by how many ranked options they beat.
	var ranked []int
	for c := 0; c < N; c++ {
		if v[1+N+c] == 1 {
			ranked = append(ranked, c)
		} else if v[1+N+c] != 0 {
			return r, errors.New("ballot: ranked is not 0 or 1")
		}
	}
	if len(ranked) == 0 || len(ranked) > 3 {
		return r, errors.New("ballot: a vote ranks 1 to 3 options")
	}
	pos := map[int]int{}
	for _, c := range ranked {
		beats := 0
		for _, d := range ranked {
			if d != c && margin(v, c, d) == 1 {
				beats++
			}
		}
		pos[c] = len(ranked) - 1 - beats
	}
	r.Len = len(ranked)
	seen := map[int]bool{}
	for _, c := range ranked {
		if seen[pos[c]] {
			return r, errors.New("ballot: duels do not form an order")
		}
		seen[pos[c]] = true
		r.Slots[pos[c]] = c
	}
	// Everything else must equal what this ranking gives.
	if want := r.Values(); !sameValues(want, v) {
		return r, errors.New("ballot: values do not come from a ranking")
	}
	return r, nil
}

func margin(v [V]int64, a, b int) int64 {
	if a == b {
		return 0
	}
	sign := int64(1)
	if a > b {
		a, b = b, a
		sign = -1
	}
	idx := 1 + 2*N
	for i := 0; i < N; i++ {
		for j := i + 1; j < N; j++ {
			if i == a && j == b {
				return sign * v[idx]
			}
			idx++
		}
	}
	return 0
}

func sameValues(a [V]int8, b [V]int64) bool {
	for i := range a {
		if int64(a[i]) != b[i] {
			return false
		}
	}
	return true
}
