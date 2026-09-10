package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

// cachedRate stores a model's blended per-token USD rate derived from
// successful runs. The exponential moving average smooths out runs with
// unusual cache-hit ratios.
type cachedRate struct {
	RatePerToken float64   `json:"rate_per_token"`
	SampleCount  int       `json:"sample_count"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// rateCache is the on-disk format for pricing-rates.json.
type rateCache struct {
	Rates map[string]cachedRate `json:"rates"`
}

func rateCachePath(fullsendDir string) string {
	return filepath.Join(fullsendDir, ".fullsend-cache", "pricing-rates.json")
}

// normalizeModelKey strips the provider prefix from a model spec for use
// as a cache key. "anthropic-vertex/claude-opus-4-6" → "claude-opus-4-6".
func normalizeModelKey(model string) string {
	if _, after, ok := strings.Cut(model, "/"); ok {
		return after
	}
	return model
}

func loadRateCache(fullsendDir string) rateCache {
	data, err := os.ReadFile(rateCachePath(fullsendDir))
	if err != nil {
		return rateCache{Rates: make(map[string]cachedRate)}
	}
	var rc rateCache
	if err := json.Unmarshal(data, &rc); err != nil || rc.Rates == nil {
		return rateCache{Rates: make(map[string]cachedRate)}
	}
	return rc
}

func saveRateCache(fullsendDir string, rc rateCache) error {
	p := rateCachePath(fullsendDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rc, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// isStrippableSuffix reports whether a trailing dash-delimited segment is
// safe to strip during model-name fallback. Only date suffixes (all digits,
// 6+ chars like "20260301") and known non-identity tags are strippable.
// This prevents false matches like gpt-4o-mini → gpt-4o.
func isStrippableSuffix(s string) bool {
	if len(s) >= 6 {
		allDigits := true
		for _, c := range s {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return true
		}
	}
	switch s {
	case "exp", "preview", "latest", "beta":
		return true
	}
	return false
}

// lookupCachedRate resolves a model to its cached rate. Tries exact match
// first (after provider-prefix strip), then strips trailing date suffixes
// and known non-identity tags like "exp" or "preview".
func lookupCachedRate(rc rateCache, model string) (cachedRate, bool) {
	key := normalizeModelKey(model)
	if r, ok := rc.Rates[key]; ok {
		return r, true
	}
	for s := key; ; {
		i := strings.LastIndex(s, "-")
		if i <= 0 {
			break
		}
		suffix := s[i+1:]
		if !isStrippableSuffix(suffix) {
			break
		}
		s = s[:i]
		if r, ok := rc.Rates[s]; ok {
			return r, true
		}
	}
	return cachedRate{}, false
}

func sumTokens(input, output, reasoning, cacheWrite, cacheRead int) int {
	return input + output + reasoning + cacheWrite + cacheRead
}

const emaAlpha = 0.3

func updateRate(existing cachedRate, exists bool, newRate float64) cachedRate {
	rate := newRate
	count := 1
	if exists {
		rate = emaAlpha*newRate + (1-emaAlpha)*existing.RatePerToken
		count = existing.SampleCount + 1
	}
	return cachedRate{
		RatePerToken: rate,
		SampleCount:  count,
		UpdatedAt:    time.Now(),
	}
}

// recordModelRates writes blended per-model rates from a successful run
// to the on-disk cache. No-op when TotalCostUSD is zero. Returns an error
// if the cache file cannot be written.
func recordModelRates(fullsendDir string, m *agentruntime.RunMetrics) error {
	if m.TotalCostUSD <= 0 || fullsendDir == "" {
		return nil
	}

	rc := loadRateCache(fullsendDir)

	if len(m.PerModelUsage) > 0 {
		for spec, u := range m.PerModelUsage {
			total := sumTokens(u.InputTokens, u.OutputTokens, 0,
				u.CacheCreationInputTokens, u.CacheReadInputTokens)
			if total == 0 || u.CostUSD <= 0 {
				continue
			}
			key := normalizeModelKey(spec)
			existing, exists := rc.Rates[key]
			rc.Rates[key] = updateRate(existing, exists, u.CostUSD/float64(total))
		}
	} else {
		// Exclude ReasoningTokens: the deferred TokensEvent on cancelled
		// runs does not capture them, so the rate denominator must match
		// the token types available during estimation.
		total := sumTokens(m.InputTokens, m.OutputTokens, 0,
			m.CacheCreationInputTokens, m.CacheReadInputTokens)
		if total == 0 {
			return nil
		}
		key := normalizeModelKey(m.Model)
		existing, exists := rc.Rates[key]
		rc.Rates[key] = updateRate(existing, exists, m.TotalCostUSD/float64(total))
	}

	return saveRateCache(fullsendDir, rc)
}

// estimateRunMetricsCost fills in TotalCostUSD on per-iteration RunMetrics
// when it is zero but tokens are present, using cached rates from prior
// successful runs. No-op when cost is already present or when no cached
// rate exists for the model.
func estimateRunMetricsCost(fullsendDir string, m *agentruntime.RunMetrics) {
	if m.TotalCostUSD > 0 {
		return
	}
	total := sumTokens(m.InputTokens, m.OutputTokens, m.ReasoningTokens,
		m.CacheCreationInputTokens, m.CacheReadInputTokens)
	if total == 0 || fullsendDir == "" {
		return
	}

	rc := loadRateCache(fullsendDir)

	if len(m.PerModelUsage) > 0 {
		var costSum float64
		for spec, u := range m.PerModelUsage {
			if u.CostUSD > 0 {
				costSum += u.CostUSD
				continue
			}
			tokens := sumTokens(u.InputTokens, u.OutputTokens, 0,
				u.CacheCreationInputTokens, u.CacheReadInputTokens)
			if tokens == 0 {
				continue
			}
			rate, ok := lookupCachedRate(rc, spec)
			if !ok {
				continue
			}
			u.CostUSD = rate.RatePerToken * float64(tokens)
			m.PerModelUsage[spec] = u
			costSum += u.CostUSD
		}
		m.TotalCostUSD = costSum
		return
	}

	rate, ok := lookupCachedRate(rc, m.Model)
	if !ok {
		return
	}
	m.TotalCostUSD = rate.RatePerToken * float64(total)
}
