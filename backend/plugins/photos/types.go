package photos

// Options configures the photo search behavior.
type Options struct {
	// Force re-searches photos even if the person already has one.
	Force bool
	// DryRun shows scored candidates without downloading or uploading.
	DryRun bool
	// MaxResults is the number of image search results to request (default 10).
	MaxResults int
	// MinWidth is the minimum acceptable image width in pixels (default 200).
	MinWidth int
	// MinHeight is the minimum acceptable image height in pixels (default 200).
	MinHeight int
}

// Result tracks what happened for a single person's photo search.
type Result struct {
	PersonID      int64
	PersonName    string
	ImageURL      string
	SearchResults int
	Downloaded    bool
	Uploaded      bool
	Skipped       bool
	SkipReason    string
	Error         error
}

// ScoredImage is an image candidate with a quality score.
type ScoredImage struct {
	URL            string
	ThumbnailURL   string
	Width          int
	Height         int
	Source         string
	Title          string
	Position       int
	Score          float64
	ScoreBreakdown string
}
