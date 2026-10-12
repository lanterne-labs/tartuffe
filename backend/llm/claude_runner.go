package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// ClaudeJob is one request for ClaudeBatchRunner. Key names the job for the
// caller and must be unique in one Run. Two jobs with the same request (the
// votes of one judge) need different keys, because the key is part of the
// stored reply's identity.
type ClaudeJob struct {
	Key     string
	Request ClaudeRequest
}

// ClaudeJobResult is the outcome of one job. Err is set when the batch did
// not answer it (errored, canceled, expired); a later Run sends it again.
type ClaudeJobResult struct {
	Reply   ClaudeReply
	CostUSD float64
	Err     error
}

// ClaudeBatchRunner runs jobs through the Message Batches API and keeps every
// succeeded reply in Dir, keyed by a hash of the job. A second Run of the same
// jobs costs nothing, and a Run that stops while a batch is in flight
// collects that batch on its next start.
type ClaudeBatchRunner struct {
	Client *ClaudeBatchClient
	Dir    string
	// MaxUSD caps the spend of the store: the cost of the stored replies
	// plus the worst-case cost of the batch to submit. 0 means no batch.
	MaxUSD       float64
	PollInterval time.Duration
}

// maxBatchRequests is the request limit of one batch (the byte limit is
// 256 MB).
const maxBatchRequests = 100_000

// Run returns one result per job, keyed by ClaudeJob.Key. It returns an error
// only when it cannot run: a bad job, the spend cap, or a batch API failure.
func (r *ClaudeBatchRunner) Run(ctx context.Context, jobs []ClaudeJob) (map[string]ClaudeJobResult, error) {
	if err := os.MkdirAll(filepath.Join(r.Dir, "replies"), 0o755); err != nil {
		return nil, fmt.Errorf("claude runner store: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(r.Dir, "batches"), 0o755); err != nil {
		return nil, fmt.Errorf("claude runner store: %w", err)
	}
	if err := r.collectPending(ctx); err != nil {
		return nil, err
	}

	ids := make(map[string]string, len(jobs)) // job key -> custom id
	seen := make(map[string]bool, len(jobs))
	out := make(map[string]ClaudeJobResult, len(jobs))
	var missing []ClaudeBatchItem
	for _, j := range jobs {
		if seen[j.Key] {
			return nil, fmt.Errorf("claude runner: duplicate job key %q", j.Key)
		}
		seen[j.Key] = true
		id, err := claudeCustomID(j)
		if err != nil {
			return nil, err
		}
		ids[j.Key] = id
		res, ok, err := r.loadReply(id)
		if err != nil {
			return nil, err
		}
		if ok {
			cost, err := ClaudeCostUSD(res.Message.Model, res.Message.Usage, true)
			if err != nil {
				return nil, err
			}
			out[j.Key] = ClaudeJobResult{Reply: res.Message, CostUSD: cost}
			continue
		}
		missing = append(missing, ClaudeBatchItem{CustomID: id, Params: j.Request})
	}
	if len(missing) > 0 {
		if err := r.checkBudget(missing); err != nil {
			return nil, err
		}
		results, err := r.runBatch(ctx, missing)
		if err != nil {
			return nil, err
		}
		for key, id := range ids {
			if _, done := out[key]; done {
				continue
			}
			res, ok := results[id]
			switch {
			case !ok:
				out[key] = ClaudeJobResult{Err: fmt.Errorf("claude batch: no result for job %q", key)}
			case res.Type != "succeeded":
				out[key] = ClaudeJobResult{Err: fmt.Errorf("claude batch: job %q %s %s", key, res.Type, res.Error)}
			default:
				cost, err := ClaudeCostUSD(res.Message.Model, res.Message.Usage, true)
				if err != nil {
					return nil, err
				}
				out[key] = ClaudeJobResult{Reply: res.Message, CostUSD: cost}
			}
		}
	}
	return out, nil
}

// SpentUSD is the cost of every reply in the store.
func (r *ClaudeBatchRunner) SpentUSD() (float64, error) {
	files, err := filepath.Glob(filepath.Join(r.Dir, "replies", "*.json"))
	if err != nil {
		return 0, err
	}
	var sum float64
	for _, f := range files {
		res, ok, err := r.loadReply(strings.TrimSuffix(filepath.Base(f), ".json"))
		if err != nil {
			return 0, err
		}
		if ok {
			cost, err := ClaudeCostUSD(res.Message.Model, res.Message.Usage, true)
			if err != nil {
				return 0, err
			}
			sum += cost
		}
	}
	return sum, nil
}

// checkBudget refuses a batch whose worst case (no cache hit, every reply at
// max_tokens) would take the store over MaxUSD.
func (r *ClaudeBatchRunner) checkBudget(items []ClaudeBatchItem) error {
	spent, err := r.SpentUSD()
	if err != nil {
		return err
	}
	var worst float64
	for _, it := range items {
		c, err := worstCaseCostUSD(it.Params)
		if err != nil {
			return err
		}
		worst += c
	}
	if spent+worst > r.MaxUSD {
		return fmt.Errorf("claude runner: spend cap: spent $%.2f + worst case $%.2f for %d requests > cap $%.2f", spent, worst, len(items), r.MaxUSD)
	}
	log.Info().Float64("spent_usd", spent).Float64("worst_case_usd", worst).Int("requests", len(items)).Msg("claude batch within the spend cap")
	return nil
}

// worstCaseCostUSD estimates the input at 3 characters per token (the
// conservative ratio of the local chunker), every input token a 1h cache
// write (the highest input price), and the output at max_tokens.
func worstCaseCostUSD(p ClaudeRequest) (float64, error) {
	chars := 0
	for _, b := range p.System {
		chars += len(b.Text)
	}
	for _, m := range p.Messages {
		for _, b := range m.Content {
			chars += len(b.Text)
		}
	}
	u := ClaudeUsage{CacheCreationInputTokens: chars / 3, OutputTokens: p.MaxTokens}
	u.CacheCreation.Ephemeral1h = chars / 3
	return ClaudeCostUSD(p.Model, u, true)
}

// runBatch submits the items as one batch and waits for it.
func (r *ClaudeBatchRunner) runBatch(ctx context.Context, items []ClaudeBatchItem) (map[string]ClaudeBatchResult, error) {
	if len(items) > maxBatchRequests {
		return nil, fmt.Errorf("claude runner: %d requests, more than the %d of one batch", len(items), maxBatchRequests)
	}
	b, err := r.Client.Create(ctx, items)
	if err != nil {
		return nil, err
	}
	// The pending file lets the next Run collect this batch if this one stops.
	if err := writeJSON(filepath.Join(r.Dir, "batches", b.ID+".json"), b); err != nil {
		return nil, err
	}
	log.Info().Str("batch_id", b.ID).Int("requests", len(items)).Msg("claude batch submitted")
	results, err := r.wait(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]ClaudeBatchResult, len(results))
	for _, res := range results {
		out[res.CustomID] = res
	}
	return out, nil
}

// collectPending waits for the batches a previous Run left in flight.
func (r *ClaudeBatchRunner) collectPending(ctx context.Context) error {
	files, err := filepath.Glob(filepath.Join(r.Dir, "batches", "*.json"))
	if err != nil {
		return err
	}
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		log.Info().Str("batch_id", id).Msg("collecting a claude batch of a previous run")
		if _, err := r.wait(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// wait polls a batch until it ends, stores its succeeded replies and removes
// its pending file.
func (r *ClaudeBatchRunner) wait(ctx context.Context, id string) ([]ClaudeBatchResult, error) {
	poll := r.PollInterval
	if poll <= 0 {
		poll = 30 * time.Second
	}
	for {
		b, err := r.Client.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if b.ProcessingStatus == "ended" {
			results, err := r.Client.Results(ctx, b)
			if err != nil {
				return nil, err
			}
			for _, res := range results {
				if res.Type != "succeeded" {
					continue
				}
				if err := writeJSON(filepath.Join(r.Dir, "replies", res.CustomID+".json"), res); err != nil {
					return nil, err
				}
			}
			log.Info().Str("batch_id", id).Int("succeeded", b.RequestCounts.Succeeded).Int("errored", b.RequestCounts.Errored).
				Int("expired", b.RequestCounts.Expired).Int("canceled", b.RequestCounts.Canceled).
				Dur("turnaround", time.Since(b.CreatedAt)).Msg("claude batch ended")
			if err := os.Remove(filepath.Join(r.Dir, "batches", id+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			return results, nil
		}
		log.Info().Str("batch_id", id).Int("processing", b.RequestCounts.Processing).Int("succeeded", b.RequestCounts.Succeeded).Msg("claude batch in progress")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

func (r *ClaudeBatchRunner) loadReply(id string) (ClaudeBatchResult, bool, error) {
	data, err := os.ReadFile(filepath.Join(r.Dir, "replies", id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return ClaudeBatchResult{}, false, nil
	}
	if err != nil {
		return ClaudeBatchResult{}, false, err
	}
	var res ClaudeBatchResult
	if err := json.Unmarshal(data, &res); err != nil {
		return ClaudeBatchResult{}, false, fmt.Errorf("stored claude reply %s: %w", id, err)
	}
	return res, true, nil
}

// claudeCustomID is a hash of the job key and the request without its cache
// breakpoint, in the alphabet and the length the batch API accepts
// (^[a-zA-Z0-9_-]{1,64}$). A request with another breakpoint finds the same
// stored reply.
func claudeCustomID(j ClaudeJob) (string, error) {
	params, err := json.Marshal(j.Request.canonical())
	if err != nil {
		return "", fmt.Errorf("claude job %q: %w", j.Key, err)
	}
	h := sha256.New()
	h.Write([]byte(j.Key))
	h.Write([]byte{0})
	h.Write(params)
	return "j" + hex.EncodeToString(h.Sum(nil))[:48], nil
}

func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
