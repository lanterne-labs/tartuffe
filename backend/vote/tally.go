package vote

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"digital-mirror/backend/polls"
	"digital-mirror/backend/vote/ballot"
	"digital-mirror/backend/vote/grumpkin"
)

// TallyEntry is the tally body of SPEC §9.1: the sums of the latest ballot
// of every pseudonym up to Upto, their count, the decrypted plaintexts and
// the proofs of decryption.
type TallyEntry struct {
	V     int        `json:"v"`
	Event int64      `json:"event"`
	Upto  int64      `json:"upto"`
	Count int64      `json:"c"`
	S0    string     `json:"s0"`
	S     []string   `json:"s"`
	P     []string   `json:"p"`
	DLEQ  []DLEQJSON `json:"dleq"`
}

// DLEQJSON is a decryption proof in a body.
type DLEQJSON struct {
	A string `json:"a"`
	B string `json:"b"`
	Z string `json:"z"`
}

// Tallier holds the election secret keys and publishes the results
// (SPEC §9). It decrypts every logged ballot for itself, counts the
// pseudonyms whose values changed since the last tally, and once K_REAL
// have, fires at a later checkpoint with probability 1/2, outside
// blackouts and after tally_min_gap. It writes tally entries to the log
// through the database; it listens on no port.
type Tallier struct {
	store  *Store
	event  *polls.Event
	sk     ballot.SecretKeys
	pk     ballot.PublicKeys
	setups []LoggedSetup
	now    func() time.Time
	rand   func() float64

	latest      map[string]decrypted // pseudonym → its counted ballot
	lastUpto    int64                // log seq of the last tally's upto
	lastTallyAt time.Time
	changed     map[string]bool // pseudonyms changed since the last tally
	scanned     int64           // log seq up to which ballots were decrypted
	armedAt     int64           // the checkpoint when K_REAL was reached, 0 when not
}

type decrypted struct {
	seq    uint32
	logSeq int64
	values [ballot.V]int64
	ct     ballot.Ciphertext
}

// NewTallier loads the setups and replays the logged ballots and tallies.
func NewTallier(ctx context.Context, store *Store, event *polls.Event, keys *Keys) (*Tallier, error) {
	sk, err := keys.Secret()
	if err != nil {
		return nil, err
	}
	pk, err := keys.Public().ParsedKeys()
	if err != nil {
		return nil, err
	}
	setups, err := store.Setups(ctx, event.ID)
	if err != nil {
		return nil, err
	}
	if len(setups) == 0 {
		return nil, fmt.Errorf("vote: no setup logged for %s", event.Slug)
	}
	for j := range pk {
		if setups[0].Setup.PK[j] != keys.PK[j] {
			return nil, errors.New("vote: the keys are not the setup's")
		}
	}
	t := &Tallier{store: store, event: event, sk: sk, pk: pk, setups: setups, now: time.Now, rand: rand.Float64,
		latest: map[string]decrypted{}, changed: map[string]bool{}}
	last, err := store.LastTally(ctx, event.ID)
	if err != nil {
		return nil, err
	}
	if last != nil {
		t.lastUpto = last.Upto
		t.lastTallyAt = last.At
	}
	// Replay: the ballots up to the last tally set the counted state, the
	// ones after it the pending changes.
	if err := t.scan(ctx, t.lastUpto); err != nil {
		return nil, err
	}
	t.changed = map[string]bool{}
	if err := t.scan(ctx, 1<<62); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Tallier) setup(now time.Time) *Setup {
	cur := t.setups[0]
	for _, s := range t.setups[1:] {
		if !now.Before(s.LoggedAt.Add(24 * time.Hour)) {
			cur = s
		}
	}
	return &cur.Setup
}

// scan decrypts the ballots logged after the last scanned seq, up to the
// bound, and records the changes.
func (t *Tallier) scan(ctx context.Context, upto int64) error {
	rows, err := t.store.BallotsAfter(ctx, t.event.ID, t.scanned, upto)
	if err != nil {
		return err
	}
	for _, b := range rows {
		p, err := ParseBallot(b.Body)
		if err != nil {
			return fmt.Errorf("logged ballot %d: %w", b.LogSeq, err)
		}
		vals, err := ballot.DecryptBallot(t.sk, p.Ciphertext)
		if err != nil {
			// The proof guarantees a valid ballot: this is a bug or a key
			// mismatch, never a voter's doing.
			log.Error().Err(err).Int64("log_seq", b.LogSeq).Msg("vote: a logged ballot does not decrypt")
			continue
		}
		if _, err := ballot.FromValues(vals); err != nil {
			log.Error().Err(err).Int64("log_seq", b.LogSeq).Msg("vote: a logged ballot is not a ranking")
			continue
		}
		prev, had := t.latest[b.Nullifier]
		if had && prev.seq >= b.Seq {
			continue
		}
		d := decrypted{seq: b.Seq, logSeq: b.LogSeq, values: vals, ct: p.Ciphertext}
		if !had || prev.values != vals {
			t.changed[b.Nullifier] = true
		}
		t.latest[b.Nullifier] = d
		t.scanned = b.LogSeq
	}
	return nil
}

// Run polls the log and publishes tallies until ctx ends.
func (t *Tallier) Run(ctx context.Context) error {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := t.Step(ctx); err != nil {
				log.Error().Err(err).Msg("vote: tally")
			}
		}
	}
}

// Step scans the new ballots and publishes a tally when the rule fires.
func (t *Tallier) Step(ctx context.Context) error {
	if err := t.scan(ctx, 1<<62); err != nil {
		return err
	}
	now := t.now()
	s := t.setup(now)
	if len(t.changed) < s.KReal {
		t.armedAt = 0
		return nil
	}
	for _, b := range s.Blackouts {
		if !now.Before(b.From) && now.Before(b.To) {
			return nil
		}
	}
	if s.TallyMinGap > 0 && !t.lastTallyAt.IsZero() && now.Sub(t.lastTallyAt) < time.Duration(s.TallyMinGap)*time.Second {
		return nil
	}
	cp, err := t.store.LastCheckpoint(ctx)
	if err != nil || cp == nil {
		return err
	}
	// The firing lag: not at the checkpoint that reached K_REAL, then at
	// each later checkpoint with probability 1/2.
	if t.armedAt == 0 {
		t.armedAt = cp.Index
		return nil
	}
	if cp.Index == t.armedAt || t.rand() < 0.5 {
		return nil
	}
	return t.Publish(ctx, cp.LogSize)
}

// Publish sums the counted ballots up to the log seq upto, decrypts,
// proves and appends the tally.
func (t *Tallier) Publish(ctx context.Context, upto int64) error {
	var cts []ballot.Ciphertext
	var total [ballot.V]int64
	for _, d := range t.latest {
		if d.logSeq > upto {
			continue
		}
		cts = append(cts, d.ct)
		for k := range total {
			total[k] += d.values[k]
		}
	}
	count := int64(len(cts))
	if count == 0 {
		return nil
	}
	sum := ballot.Sum(cts)
	var P [ballot.J]*big.Int
	for j := 0; j < ballot.J; j++ {
		P[j] = new(big.Int)
		w := big.NewInt(1)
		for k := 0; k < ballot.PerCT; k++ {
			idx := ballot.PerCT*j + k
			d := count
			if idx < ballot.V {
				d = total[idx] + count
			}
			P[j].Add(P[j], new(big.Int).Mul(big.NewInt(d), w))
			w = new(big.Int).Mul(w, ballot.Digit)
		}
	}
	if _, err := ballot.DecryptSum(t.sk, sum, P); err != nil {
		return err
	}
	event := fieldOf(t.event.ID)
	entry := TallyEntry{V: Version, Event: t.event.ID, Upto: upto, Count: count}
	c0, err := sum.C0.Compress()
	if err != nil {
		return err
	}
	entry.S0 = "0x" + hex.EncodeToString(c0[:])
	for j := 0; j < ballot.J; j++ {
		cj, err := sum.C[j].Compress()
		if err != nil {
			return err
		}
		entry.S = append(entry.S, "0x"+hex.EncodeToString(cj[:]))
		entry.P = append(entry.P, "0x"+P[j].Text(16))
		pr := ballot.ProveDLEQ(t.sk[j], t.pk[j], event, uint64(upto), j, sum.C0, sum.C[j], P[j])
		a, _ := pr.A.Compress()
		b, _ := pr.B.Compress()
		entry.DLEQ = append(entry.DLEQ, DLEQJSON{A: "0x" + hex.EncodeToString(a[:]), B: "0x" + hex.EncodeToString(b[:]), Z: "0x" + pr.Z.Text(16)})
	}
	body, err := Canonical(entry)
	if err != nil {
		return err
	}
	seq, err := t.store.Log.Append(ctx, KindTally, body)
	if err != nil {
		return err
	}
	t.lastUpto = upto
	t.lastTallyAt = t.now()
	t.armedAt = 0
	for n, d := range t.latest {
		if d.logSeq <= upto {
			delete(t.changed, n)
		}
	}
	log.Info().Int64("seq", seq).Int64("upto", upto).Int64("count", count).Int64("voters", total[0]).Msg("vote: tally published")
	return nil
}

// VerifyTally checks a tally entry against the sums of the ballots given
// (the latest valid ballot per pseudonym up to its upto): the sums, the
// digits and every DLEQ proof. It returns the decrypted values.
func VerifyTally(entry TallyEntry, pk ballot.PublicKeys, cts []ballot.Ciphertext) ([ballot.V]int64, error) {
	var zero [ballot.V]int64
	if entry.V != Version || entry.Count != int64(len(cts)) {
		return zero, fmt.Errorf("vote: the tally counts %d ballots, the log %d", entry.Count, len(cts))
	}
	sum := ballot.Sum(cts)
	s0, err := ParsePoint(entry.S0)
	if err != nil || !s0.Equal(sum.C0) {
		return zero, errors.New("vote: the tally's s0 is not the sum of the ballots")
	}
	if len(entry.S) != ballot.J || len(entry.P) != ballot.J || len(entry.DLEQ) != ballot.J {
		return zero, errors.New("vote: the tally has not J components")
	}
	var P [ballot.J]*big.Int
	event := fieldOf(entry.Event)
	for j := 0; j < ballot.J; j++ {
		sj, err := ParsePoint(entry.S[j])
		if err != nil || !sj.Equal(sum.C[j]) {
			return zero, fmt.Errorf("vote: the tally's s[%d] is not the sum of the ballots", j)
		}
		pj, ok := new(big.Int).SetString(strings.TrimPrefix(entry.P[j], "0x"), 16)
		if !ok || pj.Sign() < 0 {
			return zero, fmt.Errorf("vote: p[%d] is not hex", j)
		}
		P[j] = pj
		a, err := ParsePoint(entry.DLEQ[j].A)
		if err != nil {
			return zero, fmt.Errorf("vote: dleq[%d].a: %w", j, err)
		}
		b, err := ParsePoint(entry.DLEQ[j].B)
		if err != nil {
			return zero, fmt.Errorf("vote: dleq[%d].b: %w", j, err)
		}
		z, ok := new(big.Int).SetString(strings.TrimPrefix(entry.DLEQ[j].Z, "0x"), 16)
		if !ok {
			return zero, fmt.Errorf("vote: dleq[%d].z is not hex", j)
		}
		if err := ballot.VerifyDLEQ(ballot.DLEQ{A: a, B: b, Z: z}, pk[j], event, uint64(entry.Upto), j, sum.C0, sum.C[j], pj); err != nil {
			return zero, fmt.Errorf("vote: component %d: %w", j, err)
		}
	}
	return ballot.Unpack(P, entry.Count)
}

// LoggedTally is a tally entry with its place in the log.
type LoggedTally struct {
	Seq  int64
	At   time.Time
	Upto int64
	Body TallyEntry
}

// LastTally returns the latest tally of an event, nil before the first.
func (s *Store) LastTally(ctx context.Context, eventID int64) (*LoggedTally, error) {
	rows, err := s.pool.Query(ctx, `SELECT seq, body, created_at FROM vote_log WHERE kind = $1 ORDER BY seq DESC`, KindTally)
	if err != nil {
		return nil, fmt.Errorf("tallies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var lt LoggedTally
		var body string
		if err := rows.Scan(&lt.Seq, &body, &lt.At); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &lt.Body); err != nil {
			return nil, fmt.Errorf("tally %d: %w", lt.Seq, err)
		}
		if lt.Body.Event == eventID {
			lt.Upto = lt.Body.Upto
			return &lt, nil
		}
	}
	return nil, rows.Err()
}

// BallotsAfter returns the logged ballots with after < log_seq ≤ upto, in
// log order.
func (s *Store) BallotsAfter(ctx context.Context, eventID, after, upto int64) ([]LoggedBallot, error) {
	rows, err := s.pool.Query(ctx, `SELECT nullifier, seq, pi_hash, log_seq, checkpoint, body FROM vote2_ballots
		WHERE event_id = $1 AND log_seq > $2 AND log_seq <= $3 ORDER BY log_seq`, eventID, after, upto)
	if err != nil {
		return nil, fmt.Errorf("ballots: %w", err)
	}
	defer rows.Close()
	var out []LoggedBallot
	for rows.Next() {
		var b LoggedBallot
		var seq int32
		if err := rows.Scan(&b.Nullifier, &seq, &b.PIHash, &b.LogSeq, &b.Checkpoint, &b.Body); err != nil {
			return nil, err
		}
		b.Seq = uint32(seq)
		b.EventID = eventID
		out = append(out, b)
	}
	return out, rows.Err()
}

var _ = grumpkin.Generator
