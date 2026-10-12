package llm

import (
	"fmt"
	"strings"
)

// claudePrice is the price of one model in USD per million tokens.
type claudePrice struct {
	input, output, cacheWrite5m, cacheWrite1h, cacheRead float64
}

// claudePrices are the list prices of 2026-10-09
// (https://platform.claude.com/docs/en/about-claude/pricing). Haiku 5.5 has
// a second price above 100,000 prompt tokens (claudeLongPrices).
var claudePrices = map[string]claudePrice{
	"claude-opus-5-5":   {input: 4, output: 20, cacheWrite5m: 5, cacheWrite1h: 8, cacheRead: 0.20},
	"claude-sonnet-5-5": {input: 2, output: 10, cacheWrite5m: 2.50, cacheWrite1h: 4, cacheRead: 0.10},
	"claude-haiku-5-5":  {input: 0.10, output: 0.50, cacheWrite5m: 0.125, cacheWrite1h: 0.20, cacheRead: 0.01},
}

var claudeLongPrices = map[string]claudePrice{
	"claude-haiku-5-5": {input: 0.50, output: 2.50, cacheWrite5m: 0.625, cacheWrite1h: 1, cacheRead: 0.05},
}

const claudeLongPromptTokens = 100_000

// claudeBatchDiscount is the price factor of the Message Batches API on input
// and output tokens. The cost here applies it to the cache classes too; the
// pricing page does not state this. Compare the sum of a pilot run with the
// bill of the console before you trust it.
const claudeBatchDiscount = 0.5

// ClaudeCostUSD returns the cost of one reply with usage u. batch applies the
// discount of the Message Batches API. An unknown model is an error: a cost of
// 0 would let the spend guard pass anything.
func ClaudeCostUSD(model string, u ClaudeUsage, batch bool) (float64, error) {
	base, ok := claudePriceKey(model)
	if !ok {
		return 0, fmt.Errorf("no price for claude model %q", model)
	}
	p := claudePrices[base]
	prompt := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	if long, ok := claudeLongPrices[base]; ok && prompt > claudeLongPromptTokens {
		p = long
	}
	// The usage of an older reply can lack the split by TTL: count the whole
	// cache write at the 5-minute price then.
	write5m, write1h := u.CacheCreation.Ephemeral5m, u.CacheCreation.Ephemeral1h
	if write5m+write1h == 0 {
		write5m = u.CacheCreationInputTokens
	}
	cost := (float64(u.InputTokens)*p.input +
		float64(u.OutputTokens)*p.output +
		float64(write5m)*p.cacheWrite5m +
		float64(write1h)*p.cacheWrite1h +
		float64(u.CacheReadInputTokens)*p.cacheRead) / 1e6
	if batch {
		cost *= claudeBatchDiscount
	}
	return cost, nil
}

// claudePriceKey finds the price entry of a model: the name itself, or the
// entry the name extends with a dated suffix ("claude-sonnet-5-5-20261001").
func claudePriceKey(model string) (string, bool) {
	if _, ok := claudePrices[model]; ok {
		return model, true
	}
	for k := range claudePrices {
		if strings.HasPrefix(model, k+"-") {
			return k, true
		}
	}
	return "", false
}
