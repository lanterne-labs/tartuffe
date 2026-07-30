package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Pipeline step identifiers. A PipelineConfig lists the steps it wants; an empty
// list means every step (the nightly default).
const (
	StepNews           = "news"
	StepTwitter        = "twitter"
	StepFrpolitics     = "frpolitics"
	StepStanceMerge    = "stance-merge"
	StepContradictions = "contradictions"
	StepPhotos         = "photos"
	StepAudit          = "audit"
	StepTranslate      = "translate"
)

// allPipelineSteps is the execution order of the pipeline. Adding a step means
// adding it here and handling it in runPipeline.
var allPipelineSteps = []string{
	StepNews, StepTwitter, StepFrpolitics, StepStanceMerge,
	StepContradictions, StepPhotos, StepAudit, StepTranslate,
}

// defaultNewsMaxParse is how many articles per target the pipeline parses when a
// job doesn't override it.
const defaultNewsMaxParse = 5

// defaultNewsLocation restricts news search to this country by default.
const defaultNewsLocation = "France"

// PipelineConfig is the configuration of one pipeline run: which entities it
// covers, over which publication window, and which steps run. It is stored
// verbatim as a pipeline_jobs.config JSONB blob, so the worker can replay a job
// exactly as it was queued.
//
// The zero value is the nightly pipeline: every step, every search-queue target,
// no date restriction.
type PipelineConfig struct {
	// Targets are person/party names to process. Empty = the search queue
	// (the nightly behaviour). Names are resolved and canonicalized at enqueue.
	Targets []string `json:"targets,omitempty"`
	// DateFrom / DateTo restrict news and tweet search to a publication window.
	// Nil = whatever each source considers recent.
	DateFrom *time.Time `json:"date_from,omitempty"`
	DateTo   *time.Time `json:"date_to,omitempty"`
	// Steps lists the enabled steps. Empty = all of allPipelineSteps.
	Steps []string `json:"steps,omitempty"`
	// MaxParse caps successfully parsed articles per target (0 = default).
	MaxParse int `json:"max_parse,omitempty"`
	// WindowDays splits the date window into sub-windows of N days, so a long
	// backfill isn't collapsed into a single search page (0 = one window).
	WindowDays int `json:"window_days,omitempty"`
	// Location restricts news search to a country (empty = default).
	Location string `json:"location,omitempty"`
}

// Validate rejects configs that would fail late, at run time, on the worker.
func (c PipelineConfig) Validate() error {
	for _, s := range c.Steps {
		if !knownPipelineStep(s) {
			return fmt.Errorf("unknown pipeline step %q (known: %s)", s, strings.Join(allPipelineSteps, ", "))
		}
	}
	if c.DateFrom != nil && c.DateTo != nil && c.DateTo.Before(*c.DateFrom) {
		return fmt.Errorf("date range is inverted: from %s is after to %s",
			c.DateFrom.Format(time.RFC3339), c.DateTo.Format(time.RFC3339))
	}
	if c.MaxParse < 0 {
		return fmt.Errorf("max-parse must be >= 0, got %d", c.MaxParse)
	}
	if c.WindowDays < 0 {
		return fmt.Errorf("window-days must be >= 0, got %d", c.WindowDays)
	}
	if c.WindowDays > 0 && c.DateFrom == nil {
		return fmt.Errorf("window-days requires a start date")
	}
	return nil
}

func knownPipelineStep(step string) bool {
	for _, s := range allPipelineSteps {
		if s == step {
			return true
		}
	}
	return false
}

// enabled reports whether a step runs under this config.
func (c PipelineConfig) enabled(step string) bool {
	if len(c.Steps) == 0 {
		return true
	}
	for _, s := range c.Steps {
		if s == step {
			return true
		}
	}
	return false
}

// enabledSteps returns the enabled steps in execution order.
func (c PipelineConfig) enabledSteps() []string {
	if len(c.Steps) == 0 {
		return allPipelineSteps
	}
	out := make([]string, 0, len(c.Steps))
	for _, s := range allPipelineSteps {
		if c.enabled(s) {
			out = append(out, s)
		}
	}
	return out
}

// scoped reports whether the job targets an explicit set of entities rather than
// the whole search queue. Steps that cannot honour an entity scope skip when true.
func (c PipelineConfig) scoped() bool { return len(c.Targets) > 0 }

// hasDateWindow reports whether the job restricts search to a publication window.
func (c PipelineConfig) hasDateWindow() bool { return c.DateFrom != nil || c.DateTo != nil }

func (c PipelineConfig) maxParse() int {
	if c.MaxParse > 0 {
		return c.MaxParse
	}
	return defaultNewsMaxParse
}

func (c PipelineConfig) location() string {
	if c.Location != "" {
		return c.Location
	}
	return defaultNewsLocation
}

// windowLabel renders the date window for logs ("-" when unrestricted).
func (c PipelineConfig) windowLabel() string {
	if !c.hasDateWindow() {
		return "-"
	}
	from, to := "…", "…"
	if c.DateFrom != nil {
		from = c.DateFrom.Format("2006-01-02")
	}
	if c.DateTo != nil {
		to = c.DateTo.Format("2006-01-02")
	}
	if from == to {
		return from
	}
	return from + ".." + to
}

// DefaultName derives a stable job name from the config. The nightly pipeline
// (no targets, no window, all steps) is "daily"; anything else gets a name that
// encodes its scope, so re-queueing the exact same job while it is still pending
// is a no-op instead of a duplicate run.
func (c PipelineConfig) DefaultName() string {
	var parts []string
	for _, t := range c.Targets {
		parts = append(parts, slugifyJobName(t))
	}
	if c.hasDateWindow() {
		parts = append(parts, c.windowLabel())
	}
	if len(c.Steps) > 0 {
		steps := append([]string(nil), c.Steps...)
		sort.Strings(steps)
		parts = append(parts, strings.Join(steps, "+"))
	}
	if len(parts) == 0 {
		return "daily"
	}
	return strings.Join(parts, "-")
}

// slugifyJobName lowercases a name and collapses everything non-alphanumeric to
// single dashes, so job names stay readable in `dm pipeline jobs`.
func slugifyJobName(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// MarshalConfig serializes the config for storage in pipeline_jobs.config.
func (c PipelineConfig) MarshalConfig() ([]byte, error) {
	return json.Marshal(c)
}

// parsePipelineConfig decodes a pipeline_jobs.config blob. An empty blob is the
// nightly default rather than an error.
func parsePipelineConfig(raw []byte) (PipelineConfig, error) {
	var cfg PipelineConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("invalid pipeline job config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}
