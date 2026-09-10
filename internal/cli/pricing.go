package cli

import (
	"strings"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

// modelRates holds per-million-token prices (USD) for an Anthropic model.
type modelRates struct {
	Input      float64
	Output     float64
	CacheWrite float64
	CacheRead  float64
}

// knownModelRates maps model-ID prefixes to their published rates.
// Source: https://docs.anthropic.com/en/docs/about-claude/pricing
var knownModelRates = map[string]modelRates{
	"claude-fable-5-1":  {10, 50, 12.50, 0.25},
	"claude-mythos-5-1": {10, 50, 12.50, 0.25},
	"claude-fable-5":    {10, 50, 12.50, 1},
	"claude-mythos-5":   {10, 50, 12.50, 1},
	"claude-opus-5":     {5, 25, 6.25, 0.50},
	"claude-opus-4-8":   {5, 25, 6.25, 0.50},
	"claude-opus-4-7":   {5, 25, 6.25, 0.50},
	"claude-opus-4-6":   {5, 25, 6.25, 0.50},
	"claude-opus-4-5":   {5, 25, 6.25, 0.50},
	"claude-sonnet-5":   {2, 10, 2.50, 0.20},
	"claude-sonnet-4-6": {3, 15, 3.75, 0.30},
	"claude-sonnet-4-5": {3, 15, 3.75, 0.30},
	"claude-haiku-4-5":  {1, 5, 1.25, 0.10},
	"claude-haiku-3-5":  {0.80, 4, 1, 0.08},
}

// lookupRates resolves a model ID to its pricing rates. It handles
// provider prefixes ("anthropic-vertex/claude-opus-4-6") and trailing
// date suffixes ("claude-haiku-4-5-20251001").
func lookupRates(model string) (modelRates, bool) {
	if _, after, ok := strings.Cut(model, "/"); ok {
		model = after
	}
	if r, ok := knownModelRates[model]; ok {
		return r, true
	}
	for s := model; ; {
		i := strings.LastIndex(s, "-")
		if i <= 0 {
			break
		}
		s = s[:i]
		if r, ok := knownModelRates[s]; ok {
			return r, true
		}
	}
	return modelRates{}, false
}

func estimateCostFromTokens(model string, input, output, reasoning, cacheWrite, cacheRead int) float64 {
	rates, ok := lookupRates(model)
	if !ok {
		return 0
	}
	return (float64(input)*rates.Input +
		float64(output+reasoning)*rates.Output +
		float64(cacheWrite)*rates.CacheWrite +
		float64(cacheRead)*rates.CacheRead) / 1_000_000
}

// estimateRunMetricsCost fills in TotalCostUSD on per-iteration RunMetrics
// when it is zero but tokens are present.
func estimateRunMetricsCost(m *agentruntime.RunMetrics) {
	if m.TotalCostUSD > 0 {
		return
	}
	totalTokens := m.InputTokens + m.OutputTokens +
		m.CacheCreationInputTokens + m.CacheReadInputTokens
	if totalTokens == 0 {
		return
	}

	if len(m.PerModelUsage) > 0 {
		var total float64
		for spec, u := range m.PerModelUsage {
			if u.CostUSD > 0 {
				total += u.CostUSD
				continue
			}
			est := estimateCostFromTokens(spec,
				u.InputTokens, u.OutputTokens, 0,
				u.CacheCreationInputTokens, u.CacheReadInputTokens)
			if est > 0 {
				u.CostUSD = est
				m.PerModelUsage[spec] = u
				total += est
			}
		}
		m.TotalCostUSD = total
		return
	}

	m.TotalCostUSD = estimateCostFromTokens(m.Model,
		m.InputTokens, m.OutputTokens, m.ReasoningTokens,
		m.CacheCreationInputTokens, m.CacheReadInputTokens)
}
