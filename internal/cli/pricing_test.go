package cli

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

func almostEqual(a, b, tol float64) bool {
	return math.Abs(a-b) < tol
}

func seedCache(t *testing.T, dir string, model string, cost float64, input, output, cacheWrite, cacheRead int) {
	t.Helper()
	m := &agentruntime.RunMetrics{
		TotalCostUSD:             cost,
		Model:                    model,
		InputTokens:              input,
		OutputTokens:             output,
		CacheCreationInputTokens: cacheWrite,
		CacheReadInputTokens:     cacheRead,
	}
	if err := recordModelRates(dir, m); err != nil {
		t.Fatalf("seedCache: %v", err)
	}
}

func TestRecordAndEstimate_SingleModel(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir, "claude-opus-4-6", 0.50, 10000, 5000, 2000, 8000)

	m := &agentruntime.RunMetrics{
		Model:                    "claude-opus-4-6",
		InputTokens:              20000,
		OutputTokens:             10000,
		CacheCreationInputTokens: 4000,
		CacheReadInputTokens:     16000,
	}
	estimateRunMetricsCost(dir, m)
	if m.TotalCostUSD <= 0 {
		t.Fatal("expected non-zero estimated cost")
	}
	// rate = 0.50 / 25000 = 0.00002; cost = 0.00002 * 50000 = 1.00
	if !almostEqual(m.TotalCostUSD, 1.0, 0.001) {
		t.Errorf("got %v, want ~1.0", m.TotalCostUSD)
	}
}

func TestRecordAndEstimate_ProviderPrefix(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir, "anthropic-vertex/claude-opus-4-6", 0.25, 5000, 2500, 1000, 4000)

	m := &agentruntime.RunMetrics{
		Model:        "anthropic-vertex/claude-opus-4-6",
		InputTokens:  5000,
		OutputTokens: 2500,
	}
	estimateRunMetricsCost(dir, m)
	if m.TotalCostUSD <= 0 {
		t.Fatal("expected non-zero cost")
	}
}

func TestRecordAndEstimate_DateSuffixFallback(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir, "claude-haiku-4-5", 0.10, 10000, 5000, 0, 0)

	m := &agentruntime.RunMetrics{
		Model:        "claude-haiku-4-5-20260301",
		InputTokens:  10000,
		OutputTokens: 5000,
	}
	estimateRunMetricsCost(dir, m)
	if m.TotalCostUSD <= 0 {
		t.Fatal("expected fallback to match claude-haiku-4-5")
	}
}

func TestEstimate_NoCache(t *testing.T) {
	dir := t.TempDir()
	m := &agentruntime.RunMetrics{
		Model:        "gpt-4o",
		InputTokens:  10000,
		OutputTokens: 5000,
	}
	estimateRunMetricsCost(dir, m)
	if m.TotalCostUSD != 0 {
		t.Errorf("expected 0 with no cache, got %v", m.TotalCostUSD)
	}
}

func TestEstimate_Noop_WhenCostPresent(t *testing.T) {
	dir := t.TempDir()
	m := &agentruntime.RunMetrics{
		TotalCostUSD: 0.50,
		Model:        "claude-opus-4-6",
		InputTokens:  10000,
		OutputTokens: 5000,
	}
	estimateRunMetricsCost(dir, m)
	if m.TotalCostUSD != 0.50 {
		t.Errorf("cost changed from 0.50 to %v", m.TotalCostUSD)
	}
}

func TestEstimate_Noop_EmptyDir(t *testing.T) {
	m := &agentruntime.RunMetrics{
		Model:        "claude-opus-4-6",
		InputTokens:  10000,
		OutputTokens: 5000,
	}
	estimateRunMetricsCost("", m)
	if m.TotalCostUSD != 0 {
		t.Errorf("expected 0 with empty dir, got %v", m.TotalCostUSD)
	}
}

func TestRecord_Noop_ZeroCost(t *testing.T) {
	dir := t.TempDir()
	if err := recordModelRates(dir, &agentruntime.RunMetrics{
		Model:        "claude-opus-4-6",
		InputTokens:  10000,
		OutputTokens: 5000,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := os.ReadFile(rateCachePath(dir))
	if err == nil {
		t.Fatal("expected no cache file when cost is zero")
	}
}

func TestRecordAndEstimate_PerModelUsage(t *testing.T) {
	dir := t.TempDir()
	if err := recordModelRates(dir, &agentruntime.RunMetrics{
		TotalCostUSD: 0.15,
		InputTokens:  15000,
		OutputTokens: 8000,
		PerModelUsage: map[string]agentruntime.ModelUsage{
			"anthropic-vertex/claude-sonnet-5": {
				InputTokens:  10000,
				OutputTokens: 5000,
				CostUSD:      0.10,
			},
			"anthropic-vertex/claude-haiku-4-5": {
				InputTokens:  5000,
				OutputTokens: 3000,
				CostUSD:      0.05,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

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
	estimateRunMetricsCost(dir, m)

	if m.TotalCostUSD <= 0 {
		t.Fatal("expected non-zero total cost")
	}
	sonnet := m.PerModelUsage["anthropic-vertex/claude-sonnet-5"]
	haiku := m.PerModelUsage["anthropic-vertex/claude-haiku-4-5"]
	if sonnet.CostUSD <= 0 || haiku.CostUSD <= 0 {
		t.Errorf("per-model costs not filled: sonnet=%v haiku=%v", sonnet.CostUSD, haiku.CostUSD)
	}
}

func TestRecordAndEstimate_PerModelUsage_MixedCosts(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir, "claude-haiku-4-5", 0.05, 5000, 3000, 0, 0)

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
	estimateRunMetricsCost(dir, m)

	if m.PerModelUsage["anthropic/claude-sonnet-5"].CostUSD != 0.07 {
		t.Error("existing CostUSD was overwritten")
	}
	if m.PerModelUsage["anthropic/claude-haiku-4-5"].CostUSD <= 0 {
		t.Error("haiku cost should be estimated from cache")
	}
	if m.TotalCostUSD <= 0.07 {
		t.Error("total should include both models")
	}
}

func TestEMA_SmoothsRates(t *testing.T) {
	dir := t.TempDir()
	// First sample: rate = 0.10 / 10000 = 0.00001
	seedCache(t, dir, "claude-opus-4-6", 0.10, 5000, 5000, 0, 0)
	// Second sample: rate = 0.20 / 10000 = 0.00002
	// EMA: 0.3 * 0.00002 + 0.7 * 0.00001 = 0.000013
	seedCache(t, dir, "claude-opus-4-6", 0.20, 5000, 5000, 0, 0)

	rc := loadRateCache(dir)
	rate, ok := rc.Rates["claude-opus-4-6"]
	if !ok {
		t.Fatal("expected cached rate")
	}
	if !almostEqual(rate.RatePerToken, 0.000013, 0.0000001) {
		t.Errorf("EMA rate = %v, want ~0.000013", rate.RatePerToken)
	}
	if rate.SampleCount != 2 {
		t.Errorf("sample count = %d, want 2", rate.SampleCount)
	}
}

func TestNormalizeModelKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude-opus-4-6", "claude-opus-4-6"},
		{"anthropic-vertex/claude-opus-4-6", "claude-opus-4-6"},
		{"anthropic/claude-sonnet-5", "claude-sonnet-5"},
		{"gpt-4o", "gpt-4o"},
		{"openai/gpt-4o", "gpt-4o"},
	}
	for _, tt := range tests {
		got := normalizeModelKey(tt.input)
		if got != tt.want {
			t.Errorf("normalizeModelKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLookupCachedRate_ExactThenSuffix(t *testing.T) {
	rc := rateCache{Rates: map[string]cachedRate{
		"claude-opus-4-6": {RatePerToken: 0.00002, SampleCount: 3},
		"gpt-4o":          {RatePerToken: 0.00001, SampleCount: 2},
	}}

	if _, ok := lookupCachedRate(rc, "claude-opus-4-6"); !ok {
		t.Error("exact match failed")
	}
	if _, ok := lookupCachedRate(rc, "anthropic-vertex/claude-opus-4-6"); !ok {
		t.Error("provider-prefix match failed")
	}
	if _, ok := lookupCachedRate(rc, "claude-opus-4-6-20260301"); !ok {
		t.Error("date-suffix fallback failed")
	}
	if _, ok := lookupCachedRate(rc, "anthropic/claude-opus-4-6-exp-20260301"); !ok {
		t.Error("compound suffix fallback failed")
	}
	if _, ok := lookupCachedRate(rc, "uncached-model"); ok {
		t.Error("expected no match for uncached model")
	}
}

func TestLookupCachedRate_NoFalseMatchAcrossModels(t *testing.T) {
	rc := rateCache{Rates: map[string]cachedRate{
		"gpt-4o": {RatePerToken: 0.00001, SampleCount: 2},
	}}

	if _, ok := lookupCachedRate(rc, "gpt-4o-mini"); ok {
		t.Error("gpt-4o-mini must NOT match gpt-4o (different model, ~30x price difference)")
	}
	if _, ok := lookupCachedRate(rc, "gpt-4o-mini-20260301"); ok {
		t.Error("gpt-4o-mini-20260301 must NOT match gpt-4o")
	}
	if _, ok := lookupCachedRate(rc, "gpt-4o"); !ok {
		t.Error("exact match for gpt-4o should still work")
	}
	if _, ok := lookupCachedRate(rc, "gpt-4o-20260301"); !ok {
		t.Error("date-suffix fallback for gpt-4o should work")
	}
}

func TestCacheFileAtomicity(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir, "claude-opus-4-6", 0.50, 10000, 5000, 0, 0)

	p := rateCachePath(dir)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("cache file missing: %v", err)
	}
	tmp := p + ".tmp"
	if _, err := os.Stat(tmp); err == nil {
		t.Error("tmp file should not persist after atomic write")
	}

	cacheDir := filepath.Dir(p)
	if _, err := os.Stat(cacheDir); err != nil {
		t.Fatalf("cache dir missing: %v", err)
	}
}

func TestRecordAndEstimate_OpenAIModel(t *testing.T) {
	dir := t.TempDir()
	seedCache(t, dir, "openai/gpt-4o", 0.30, 20000, 10000, 0, 0)

	m := &agentruntime.RunMetrics{
		Model:        "openai/gpt-4o",
		InputTokens:  10000,
		OutputTokens: 5000,
	}
	estimateRunMetricsCost(dir, m)
	if m.TotalCostUSD <= 0 {
		t.Fatal("expected non-zero cost for cached OpenAI model")
	}
	if !almostEqual(m.TotalCostUSD, 0.15, 0.001) {
		t.Errorf("got %v, want ~0.15", m.TotalCostUSD)
	}
}
