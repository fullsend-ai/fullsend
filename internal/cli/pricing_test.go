package cli

import (
	"math"
	"testing"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

func almostEqual(a, b, tol float64) bool {
	return math.Abs(a-b) < tol
}

func TestLookupRates_ExactMatch(t *testing.T) {
	r, ok := lookupRates("claude-sonnet-5")
	if !ok {
		t.Fatal("expected match for claude-sonnet-5")
	}
	if r.Input != 2 || r.Output != 10 {
		t.Errorf("got Input=%v Output=%v, want 2/10", r.Input, r.Output)
	}
}

func TestLookupRates_DateSuffix(t *testing.T) {
	r, ok := lookupRates("claude-haiku-4-5-20251001")
	if !ok {
		t.Fatal("expected match for claude-haiku-4-5-20251001")
	}
	if r.Input != 1 || r.Output != 5 {
		t.Errorf("got Input=%v Output=%v, want 1/5", r.Input, r.Output)
	}
}

func TestLookupRates_ProviderPrefix(t *testing.T) {
	r, ok := lookupRates("anthropic-vertex/claude-opus-4-6")
	if !ok {
		t.Fatal("expected match for anthropic-vertex/claude-opus-4-6")
	}
	if r.Input != 5 || r.Output != 25 {
		t.Errorf("got Input=%v Output=%v, want 5/25", r.Input, r.Output)
	}
}

func TestLookupRates_ProviderPrefixWithDate(t *testing.T) {
	r, ok := lookupRates("anthropic/claude-sonnet-4-5-20241022")
	if !ok {
		t.Fatal("expected match for anthropic/claude-sonnet-4-5-20241022")
	}
	if r.Input != 3 || r.Output != 15 {
		t.Errorf("got Input=%v Output=%v, want 3/15", r.Input, r.Output)
	}
}

func TestLookupRates_CompoundSuffix(t *testing.T) {
	r, ok := lookupRates("claude-opus-4-6-exp-20250826")
	if !ok {
		t.Fatal("expected match for claude-opus-4-6-exp-20250826")
	}
	if r.Input != 5 {
		t.Errorf("got Input=%v, want 5", r.Input)
	}
}

func TestLookupRates_Unknown(t *testing.T) {
	_, ok := lookupRates("gpt-4o")
	if ok {
		t.Fatal("expected no match for gpt-4o")
	}
}

func TestLookupRates_Empty(t *testing.T) {
	_, ok := lookupRates("")
	if ok {
		t.Fatal("expected no match for empty string")
	}
}

func TestEstimateCostFromTokens(t *testing.T) {
	cost := estimateCostFromTokens("claude-sonnet-5", 10000, 5000, 0, 0, 0)
	if !almostEqual(cost, 0.07, 0.0001) {
		t.Errorf("got %v, want 0.07", cost)
	}
}

func TestEstimateCostFromTokens_WithCache(t *testing.T) {
	cost := estimateCostFromTokens("claude-opus-5", 5000, 2000, 0, 10000, 20000)
	if !almostEqual(cost, 0.1475, 0.0001) {
		t.Errorf("got %v, want 0.1475", cost)
	}
}

func TestEstimateCostFromTokens_WithReasoning(t *testing.T) {
	cost := estimateCostFromTokens("claude-sonnet-5", 1000, 500, 2000, 0, 0)
	if !almostEqual(cost, 0.027, 0.0001) {
		t.Errorf("got %v, want 0.027", cost)
	}
}

func TestEstimateCostFromTokens_UnknownModel(t *testing.T) {
	cost := estimateCostFromTokens("unknown-model", 10000, 5000, 0, 0, 0)
	if cost != 0 {
		t.Errorf("expected 0 for unknown model, got %v", cost)
	}
}

func TestEstimateRunMetricsCost(t *testing.T) {
	m := &agentruntime.RunMetrics{
		Model:                    "claude-opus-4-6",
		InputTokens:              20000,
		OutputTokens:             8000,
		CacheCreationInputTokens: 5000,
		CacheReadInputTokens:     50000,
	}
	estimateRunMetricsCost(m)
	if !almostEqual(m.TotalCostUSD, 0.35625, 0.001) {
		t.Errorf("got %v, want 0.35625", m.TotalCostUSD)
	}
}

func TestEstimateRunMetricsCost_PerModelUsage(t *testing.T) {
	m := &agentruntime.RunMetrics{
		InputTokens:  15000,
		OutputTokens: 8000,
		PerModelUsage: map[string]agentruntime.ModelUsage{
			"anthropic-vertex/claude-sonnet-5": {
				InputTokens:  10000,
				OutputTokens: 5000,
			},
			"anthropic-vertex/claude-haiku-4-5": {
				InputTokens:  5000,
				OutputTokens: 3000,
			},
		},
	}
	estimateRunMetricsCost(m)
	// sonnet: (10000*2 + 5000*10) / 1e6 = 0.07
	// haiku:  (5000*1 + 3000*5) / 1e6 = 0.02
	if !almostEqual(m.TotalCostUSD, 0.09, 0.001) {
		t.Errorf("got %v, want 0.09", m.TotalCostUSD)
	}
	if !almostEqual(m.PerModelUsage["anthropic-vertex/claude-sonnet-5"].CostUSD, 0.07, 0.001) {
		t.Errorf("sonnet cost = %v, want 0.07", m.PerModelUsage["anthropic-vertex/claude-sonnet-5"].CostUSD)
	}
	if !almostEqual(m.PerModelUsage["anthropic-vertex/claude-haiku-4-5"].CostUSD, 0.02, 0.001) {
		t.Errorf("haiku cost = %v, want 0.02", m.PerModelUsage["anthropic-vertex/claude-haiku-4-5"].CostUSD)
	}
}

func TestEstimateRunMetricsCost_PerModelUsage_MixedCosts(t *testing.T) {
	m := &agentruntime.RunMetrics{
		InputTokens:  15000,
		OutputTokens: 8000,
		PerModelUsage: map[string]agentruntime.ModelUsage{
			"anthropic/claude-sonnet-5": {
				InputTokens:  10000,
				OutputTokens: 5000,
				CostUSD:      0.07,
			},
			"anthropic/claude-haiku-4-5": {
				InputTokens:  5000,
				OutputTokens: 3000,
			},
		},
	}
	estimateRunMetricsCost(m)
	if !almostEqual(m.TotalCostUSD, 0.09, 0.001) {
		t.Errorf("got %v, want 0.09", m.TotalCostUSD)
	}
	if m.PerModelUsage["anthropic/claude-sonnet-5"].CostUSD != 0.07 {
		t.Error("existing CostUSD was overwritten")
	}
}

func TestEstimateRunMetricsCost_Noop_WhenCostPresent(t *testing.T) {
	m := &agentruntime.RunMetrics{
		TotalCostUSD: 0.50,
		Model:        "claude-sonnet-5",
		InputTokens:  10000,
		OutputTokens: 5000,
	}
	estimateRunMetricsCost(m)
	if m.TotalCostUSD != 0.50 {
		t.Errorf("cost changed from 0.50 to %v", m.TotalCostUSD)
	}
}
