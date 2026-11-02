package frpolitics

import "time"

// Law represents a legisative item derived from a scrutin file
type Law struct {
	// Key uniquely identifies a law within legislature, e.g., "16-1234"
	Key string
	// Title is the human-readable title of the law/dossier
	Title string
	// URL is the canonical page with details about the law
	URL string
	// Legislature indicates the legislature number (e.g., 16)
	Legislature int
	// VoteDate is the date/time of the vote if known
	VoteDate *time.Time
	// SeanceRef is the Assemblée's séance id the scrutin was held in
	// (scrutin.seanceRef, e.g. RUANR5L17S2026IDS29979) — the same id the
	// séance transcripts carry, which is how a vote and what its author said
	// in the same sitting get paired without embeddings.
	SeanceRef string
}

// VotePosition represents a standardized vote stance
type VotePosition string

const (
	// VoteFor indicates a "pour" vote
	VoteFor VotePosition = "pour"
	// VoteAgainst indicates a "contre" vote
	VoteAgainst VotePosition = "contre"
	// VoteAbstention indicates an abstention
	VoteAbstention VotePosition = "abstention"
	// VoteNonVotant indicates not voting / absent
	VoteNonVotant VotePosition = "nonVotant"
)

// VoteEvent captures a deputy's vote on a law
type VoteEvent struct {
	// AssembleeID is the Assemblée Nationale person identifier (e.g., "PA123")
	AssembleeID string
	// OrganeRef is the political group reference (e.g., "PO845401")
	OrganeRef string
	// DisplayName is the deputy full name (e.g., "Jean Dupont") when available
	DisplayName string
	// Position is the vote stance
	Position VotePosition
}

// LawSummary is the AI derived summary of the law
type LawSummary struct {
	// ShortTitle is a concise, human-readable name for the law (e.g. "Motion de censure")
	ShortTitle string
	// SimpleStatement is a compact, human-readable summary
	SimpleStatement string
	// Confidence is an optional confidence score [0..1]
	Confidence float64
}

// Options holds run-time parameters for the votes run
type Options struct {
	// Since filters processing to laws after this date when provided
	Since *time.Time
	// MaxLaws limits the number of laws summarized per run
	MaxLaws int
	// MaxVotes limits the number of vote events processed (for testing); 0 = unlimited
	MaxVotes int
	// PartyThreshold is the majority percentage (0..1) for party-level facts
	PartyThreshold float64
	// MinConfidence is the minimum AI confidence threshold (0..1) for accepting summarized laws; default 0.5
	MinConfidence float64
	// DisableKeywordFilter when true, skips keyword-based filtering (useful for tests)
	DisableKeywordFilter bool
	// DisableAIFilter when true, skips AI confidence filtering (useful for tests)
	DisableAIFilter bool
	// Legislatures enumerates legislature numbers to fetch from (e.g., 16)
	Legislatures []int
	// RevisitHours is the law-URL revisit window in hours: 0 means the 24h
	// default, a negative value means always visitable (see selectLaws)
	RevisitHours int
}

// GroupStats represents voting statistics for a political group on a specific law
type GroupStats struct {
	// Position is the majority voting position of the group
	Position VotePosition
	// For is the number of group members who voted "for"
	For int
	// Against is the number of group members who voted "against"
	Against int
	// Abst is the number of group members who abstained
	Abst int
	// NonVot is the number of group members present who did not vote
	// (usually 0: the absent are in no count)
	NonVot int
	// Members is the size of the group at the scrutin (nombreMembresGroupe)
	Members int
}

// LawGroupStats maps organeRef to voting statistics for a specific law
type LawGroupStats map[string]GroupStats

// LawEventMap maps law keys to their associated vote events
type LawEventMap map[string][]VoteEvent

// OrganeInfoMap maps organeRef to organizational information
type OrganeInfoMap map[string]OrganeInfo

// ParseStats accumulates parsing-time counters to avoid per-item logs
type ParseStats struct {
	// FilesProcessed is the number of scrutin files processed
	FilesProcessed int
	// JsonErrors is the number of JSON parsing errors encountered
	JsonErrors int
	// EventsParsed is the total number of vote events parsed (complete + dropped)
	EventsParsed int
	// EventsComplete is the number of successfully parsed vote events with valid names
	EventsComplete int
	// EventsDropped is the number of vote events dropped due to missing name mappings
	EventsDropped int
}
