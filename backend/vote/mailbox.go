package vote

import "context"

// Mailbox is the inbound path of SPEC §7: where voters post registrations
// and ballots and where our server posts its acknowledgements, checkpoints
// and the close snapshot. It is a set of named boxes (today Nostr relays,
// one box per relay URL) that anyone can read, which the browser picks
// from and the auditors archive. Nothing in the ingest depends on what a
// box is: another transport is one more implementation of this interface
// (and of its browser-side twin, frontend/src/lib/vote/mailbox.ts).
type Mailbox interface {
	// Boxes lists the boxes voters may post to.
	Boxes() []string
	// AddBox adds a box (a relay a later setup names) while running.
	AddBox(box string)
	// Read delivers the voters' messages of a box from the time since on,
	// then live, to out, until ctx ends or the box fails (an error).
	Read(ctx context.Context, box string, since int64, out chan<- Message) error
	// Post publishes a server message to one box.
	Post(ctx context.Context, box string, msg Outgoing) error
	// PostAll publishes a server message to every box and returns how many
	// took it.
	PostAll(ctx context.Context, msg Outgoing) int
}

// MessageKind names what a voter's message holds.
type MessageKind int

const (
	MsgBallot MessageKind = iota + 1
	MsgRegister
	MsgComplaint
)

// Message is a voter's message as read from a box: its id in the box (the
// acknowledgement names it), the box, its kind, its body as posted and the
// time the box records.
type Message struct {
	ID   string
	Box  string
	Kind MessageKind
	Body []byte
	Time int64
}

// OutgoingKind names what the server posts.
type OutgoingKind int

const (
	OutAck OutgoingKind = iota + 1
	OutCheckpoint
	OutSnapshot
)

// Outgoing is a server message: its kind and its body (canonical JSON or
// a signed note).
type Outgoing struct {
	Kind OutgoingKind
	Body string
}
