package polls

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"digital-mirror/backend/polls/semaphore"
)

// The kinds of an anonymous ballot.
const (
	KindChoices  = "choices"
	KindBlank    = "blank"
	KindWithdraw = "withdraw"
)

// AnonBallot is one anonymous ballot: what it says and the proof that a voter
// of the window's tree cast it.
type AnonBallot struct {
	EventID int64 `json:"poll"`
	Window
	// Seq orders the ballots of one nullifier: the highest counts.
	Seq int `json:"seq"`
	// Kind is choices (Choices approved, in increasing id order, Favourite
	// one of them or 0), blank (a blank vote: no candidate, counted on its
	// own) or withdraw (no ballot any more).
	Kind      string          `json:"kind"`
	Choices   []int64         `json:"choices"`
	Favourite int64           `json:"favourite,omitempty"`
	Proof     semaphore.Proof `json:"proof"`
}

// Message is the text the proof binds: the browser builds the same one,
// so a ballot cannot be changed under its proof.
//
//	tartuffe-ballot/v1;poll=12;series=0;window=10;seq=3;choices=4,7,9;favourite=7
//	tartuffe-ballot/v1;poll=12;series=1;window=9;seq=1;blank
func (b AnonBallot) Message() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "tartuffe-ballot/v1;poll=%d;series=%d;window=%d;seq=%d;", b.EventID, b.Series, b.Index, b.Seq)
	switch b.Kind {
	case KindChoices:
		sb.WriteString("choices=")
		for i, id := range b.Choices {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(strconv.FormatInt(id, 10))
		}
		if b.Favourite != 0 {
			sb.WriteString(";favourite=")
			sb.WriteString(strconv.FormatInt(b.Favourite, 10))
		}
	default:
		sb.WriteString(b.Kind)
	}
	return sb.String()
}

// maxSeq bounds a ballot's sequence number (a voter re-votes far fewer
// times).
const maxSeq = 1_000_000

// CheckAnonBallot checks the ballot against the event at now, its proof
// included. It does not check the root (the store knows the roots) nor that
// the seq is above the nullifier's last one.
func CheckAnonBallot(e *Event, candidates []int64, b AnonBallot, now time.Time) error {
	if b.EventID != e.ID {
		return fmt.Errorf("%w: the ballot is for another poll", ErrBallot)
	}
	if !IsOpen(e, now) {
		return ErrClosed
	}
	if b.Series < 0 || b.Series >= Series || WindowAt(b.Series, now) != b.Window {
		return fmt.Errorf("%w: the window is not open", ErrBallot)
	}
	if b.Seq < 1 || b.Seq > maxSeq {
		return fmt.Errorf("%w: seq %d", ErrBallot, b.Seq)
	}
	switch b.Kind {
	case KindChoices:
		if len(b.Choices) == 0 {
			return fmt.Errorf("%w: no choice (a blank vote is its own kind)", ErrBallot)
		}
		if err := CheckBallot(e, candidates, b.Choices, b.Favourite, now); err != nil {
			return err
		}
	case KindBlank, KindWithdraw:
		if len(b.Choices) != 0 || b.Favourite != 0 {
			return fmt.Errorf("%w: a %s ballot names no candidate", ErrBallot, b.Kind)
		}
	default:
		return fmt.Errorf("%w: kind %q", ErrBallot, b.Kind)
	}
	if b.Proof.Scope != Scope(e.ID, b.Window) {
		return fmt.Errorf("%w: the proof is for another scope", ErrBallot)
	}
	if b.Proof.Message != semaphore.Digest([]byte(b.Message())).String() {
		return fmt.Errorf("%w: the proof is for another ballot", ErrBallot)
	}
	if err := semaphore.Verify(b.Proof); err != nil {
		return fmt.Errorf("%w: %v", ErrBallot, err)
	}
	return nil
}
