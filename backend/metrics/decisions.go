package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// Every judgement call the pipeline makes is counted here: which way each
// gate and judge went, so "how often does this happen?" is a query instead
// of a guess. The counts are accumulated in memory and flushed to the
// database, so the all-time totals survive the end of each process (a run, a
// deploy); the web service exports the database totals (see
// StartDecisionCollector).
const (
	StageEntityGate      = "entity_gate"      // a new subject entity: kept or rejected as out of scope
	StageFactDedupe      = "fact_dedupe"      // a new fact against the person's similar facts
	StageStanceAssign    = "stance_assign"    // where a fact's topic belongs
	StageStanceAnalyze   = "stance_analyze"   // a fact against the facts of its stance
	StageStanceMerge     = "stance_merge"     // two stances of one person
	StageContradiction   = "contradiction"    // a candidate pair, and the verifier's veto
	StageDocumentExtract = "document_extract" // a fetched document through the LLM
	StageStanceReassign  = "stance_reassign"  // a one-fact stance re-judged against the person's others
	StageFactParse       = "fact_parse"       // an extracted fact through the deterministic guards
)

// Outcomes, grouped by stage. Each set is closed: these are label values.
const (
	// StageEntityGate
	OutcomeEntityKept     = "kept"
	OutcomeEntityRejected = "rejected"

	// StageFactDedupe
	OutcomeNoCandidates = "no_candidates" // nothing similar enough to judge
	OutcomeFolded       = "folded"        // same position: kept in the claim group of the fact it restates
	OutcomeDistinct     = "distinct"      // judged different: stored on its own
	OutcomeGateError    = "gate_error"    // the judge failed twice; treated as distinct

	// StageStanceAssign
	OutcomeStanceJoined      = "joined"       // an existing stance fit
	OutcomeStanceNew         = "new"          // no stance fit: a new one
	OutcomeStanceLabelRescue = "label_rescue" // joined a stance only the label pass surfaced

	// StageStanceAnalyze / StageContradiction
	OutcomeAnalyzeDuplicate     = "duplicate"
	OutcomeAnalyzeNewInfo       = "new_info"
	OutcomeAnalyzeContradiction = "contradiction"
	OutcomeContradictionFound   = "found"
	OutcomeContradictionNone    = "none"
	OutcomeContradictionRefuted = "refuted" // the verifier vetoed a detected contradiction
	OutcomeError                = "error"

	// StageStanceMerge
	OutcomeMergeConfirmed      = "confirmed"
	OutcomeMergeRejected       = "rejected"
	OutcomeMergeViaLabel       = "confirmed_via_label" // surfaced by label similarity, not embeddings
	OutcomeMergeLabelRejected  = "rejected_via_label"
	OutcomeMergeViaLookup      = "confirmed_via_lookup" // named by the stance lookup, confirmed by the overlap gate
	OutcomeMergeLookupRejected = "rejected_via_lookup"

	// StageStanceReassign
	OutcomeReassignJoined = "joined" // the singleton's fact found a home
	OutcomeReassignKept   = "kept"   // it still deserves its own topic

	// StageDocumentExtract
	OutcomeDocExtracted = "extracted"
	OutcomeDocNoFacts   = "no_facts"
	OutcomeDocFailed    = "failed"
	OutcomeDocSkipped   = "skipped" // empty or too thin to extract from

	// StageFactParse
	OutcomeProxySpeaker    = "proxy_speaker"    // an aide's statement credited to the person they work for: dropped
	OutcomeSubjectRepaired = "subject_repaired" // a misspelled subject name replaced by the spelling in the source
)

// DecisionCount is one (stage, outcome) tally.
type DecisionCount struct {
	Stage   string
	Outcome string
	Count   int64
}

// DecisionSink persists decision counts. *storage.PostgresStorer implements
// it; the interface keeps metrics free of a storage dependency.
type DecisionSink interface {
	AddDecisionCounts(ctx context.Context, counts []DecisionCount) error
}

var decisions = struct {
	mu     sync.Mutex
	counts map[DecisionCount]int64
}{counts: map[DecisionCount]int64{}}

// RecordDecision counts one judgement. Cheap and non-blocking: the counts are
// flushed in batches (FlushDecisions).
func RecordDecision(stage, outcome string) {
	AddDecision(stage, outcome, 1)
}

// AddDecision counts n judgements of the same kind.
func AddDecision(stage, outcome string, n int64) {
	if n == 0 {
		return
	}
	key := DecisionCount{Stage: stage, Outcome: outcome}
	decisions.mu.Lock()
	decisions.counts[key] += n
	decisions.mu.Unlock()
}

// takeDecisions empties the in-memory tallies and returns them.
func takeDecisions() []DecisionCount {
	decisions.mu.Lock()
	defer decisions.mu.Unlock()
	if len(decisions.counts) == 0 {
		return nil
	}
	out := make([]DecisionCount, 0, len(decisions.counts))
	for key, n := range decisions.counts {
		out = append(out, DecisionCount{Stage: key.Stage, Outcome: key.Outcome, Count: n})
	}
	decisions.counts = map[DecisionCount]int64{}
	return out
}

// FlushDecisions writes the accumulated tallies to the sink. On failure the
// counts are put back so the next flush retries them.
func FlushDecisions(ctx context.Context, sink DecisionSink) error {
	counts := takeDecisions()
	if len(counts) == 0 {
		return nil
	}
	if err := sink.AddDecisionCounts(ctx, counts); err != nil {
		for _, c := range counts {
			AddDecision(c.Stage, c.Outcome, c.Count)
		}
		return err
	}
	return nil
}

// StartDecisionFlusher flushes the tallies every interval until ctx is
// cancelled, so a process that dies mid-run (an outage, a deploy) loses at
// most one interval of counts. Callers still flush on exit.
func StartDecisionFlusher(ctx context.Context, sink DecisionSink, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := FlushDecisions(ctx, sink); err != nil {
					log.Warn().Err(err).Msg("Failed to flush decision counters; retrying at the next tick")
				}
			}
		}
	}()
}

// DecisionsTotal is the database's all-time count per (stage, outcome),
// refreshed by StartDecisionCollector. A gauge, not a counter: the value is
// read from the database, not accumulated by this process.
var DecisionsTotal = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "decisions_total",
		Help:      "Pipeline judgement calls by stage and outcome, all time",
	},
	[]string{"stage", "outcome"},
)

// DecisionsDaily is the same count for the current day, so a dashboard can
// show today's rate without a range query over a gauge.
var DecisionsDaily = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "decisions_today",
		Help:      "Pipeline judgement calls by stage and outcome, current day",
	},
	[]string{"stage", "outcome"},
)

func init() {
	// The service serves metrics.Registry, not the default registerer (see
	// api.Serve): a gauge registered anywhere else is simply never exported.
	Registry.MustRegister(DecisionsTotal, DecisionsDaily)
}

// DecisionReader reads back the persisted counts. *storage.PostgresStorer
// implements it.
type DecisionReader interface {
	// DecisionTotals returns the all-time and current-day counts.
	DecisionTotals(ctx context.Context) (allTime, today []DecisionCount, err error)
}

// StartDecisionCollector refreshes the decision gauges from the database
// every interval until ctx is cancelled. Only the web process runs it; the
// counts it exports are those every process wrote.
func StartDecisionCollector(ctx context.Context, reader DecisionReader, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			refreshDecisionGauges(ctx, reader)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func refreshDecisionGauges(ctx context.Context, reader DecisionReader) {
	allTime, today, err := reader.DecisionTotals(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to read decision counters")
		return
	}
	DecisionsTotal.Reset()
	for _, c := range allTime {
		DecisionsTotal.WithLabelValues(c.Stage, c.Outcome).Set(float64(c.Count))
	}
	DecisionsDaily.Reset()
	for _, c := range today {
		DecisionsDaily.WithLabelValues(c.Stage, c.Outcome).Set(float64(c.Count))
	}
}
