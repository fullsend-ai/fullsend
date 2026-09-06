package runtime

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFoldPiSubagentUsage covers the usage file the Agent extension appends
// one line per sub-agent to: the per-model breakdown includes the parent's
// own iteration so it reconciles with the run totals, and the totals grow
// by what the children spent.
func TestFoldPiSubagentUsage(t *testing.T) {
	t.Parallel()
	const parent = "anthropic-vertex/claude-opus-4-6"
	const child = "anthropic-vertex/claude-sonnet-4-6"
	lines := `{"seq":1,"model":"` + child + `","provider":"anthropic-vertex","description":"security pass","durationMs":1000,"usage":{"input":100,"output":20,"cacheRead":5,"cacheWrite":3,"cost":0.25},"stopReason":"stop","isError":false}
not json
{"seq":2,"model":"` + child + `","usage":{"input":10,"output":2,"cost":0.05},"stopReason":"error","isError":true}
{"seq":3,"model":"","usage":{"input":7,"output":1,"cost":0.01}}
`
	m := &RunMetrics{TotalCostUSD: 1.5, InputTokens: 1000, OutputTokens: 200, CacheReadInputTokens: 50, CacheCreationInputTokens: 30}
	n, skipped := foldPiSubagentUsage([]byte(lines), parent, m)

	assert.Equal(t, 3, n, "every well-formed record counts, including the failed one and the one with no model")
	assert.Equal(t, 1, skipped, "the line that is not JSON is reported, so its cost is not silently lost")
	assert.InDelta(t, 1.5+0.25+0.05+0.01, m.TotalCostUSD, 1e-9, "child cost lands in the run total")
	assert.Equal(t, 1000+100+10+7, m.InputTokens)
	assert.Equal(t, 200+20+2+1, m.OutputTokens)
	assert.Equal(t, 50+5, m.CacheReadInputTokens)
	assert.Equal(t, 30+3, m.CacheCreationInputTokens)

	require := assert.New(t)
	require.Equal(ModelUsage{Requests: 1, InputTokens: 1000, OutputTokens: 200, CacheReadInputTokens: 50, CacheCreationInputTokens: 30, CostUSD: 1.5},
		m.PerModelUsage[parent], "the parent's own iteration is one entry, keyed by its model spec")
	require.Equal(ModelUsage{Requests: 2, InputTokens: 110, OutputTokens: 22, CacheReadInputTokens: 5, CacheCreationInputTokens: 3, CostUSD: 0.30},
		m.PerModelUsage[child])
	require.Equal(ModelUsage{Requests: 1, InputTokens: 7, OutputTokens: 1, CostUSD: 0.01},
		m.PerModelUsage[piSubagentUnknownModel], "a record without a model spec is still accounted for")
}

// An empty or absent usage file is the common case (no sub-agent was
// dispatched). The totals must be untouched, but the parent's own entry
// still has to appear: iterations are summed, so an iteration missing from
// the breakdown makes the run's per_model_usage stop matching its totals.
func TestFoldPiSubagentUsage_NoChildren(t *testing.T) {
	t.Parallel()
	const parent = "anthropic-vertex/claude-opus-4-6"
	for _, data := range []string{"", "  \n\n"} {
		m := &RunMetrics{TotalCostUSD: 2, InputTokens: 5}
		n, skipped := foldPiSubagentUsage([]byte(data), parent, m)
		assert.Zero(t, n, data)
		assert.Zero(t, skipped, data)
		assert.Equal(t, ModelUsage{Requests: 1, InputTokens: 5, CostUSD: 2}, m.PerModelUsage[parent], data)
		assert.Len(t, m.PerModelUsage, 1, data)
		assert.Equal(t, 2.0, m.TotalCostUSD, "the parent's own numbers are not double-counted into the totals")
		assert.Equal(t, 5, m.InputTokens)
	}

	// A `{}` line carries neither a seq nor a usage object, so it is not a
	// sub-agent that ran — but it is not a record either, and saying so is
	// how a truncated read surfaces.
	m := &RunMetrics{TotalCostUSD: 2, InputTokens: 5}
	n, skipped := foldPiSubagentUsage([]byte("{}\n"), parent, m)
	assert.Zero(t, n)
	assert.Equal(t, 1, skipped)

	// Half a record is not a record. A line with a seq but no usage object
	// (a child killed between the two, or a hand-written entry) would fold
	// as a zero-cost sub-agent and hide the missing spend; a usage object
	// with no seq is not something the extension ever writes.
	for _, line := range []string{
		`{"seq":4,"model":"m"}`,
		`{"usage":{"input":10,"output":2,"cost":0.05},"model":"m"}`,
	} {
		m := &RunMetrics{TotalCostUSD: 2, InputTokens: 5}
		n, skipped := foldPiSubagentUsage([]byte(line+"\n"), parent, m)
		assert.Zero(t, n, line)
		assert.Equal(t, 1, skipped, line)
		assert.Equal(t, 2.0, m.TotalCostUSD, line)
		assert.Len(t, m.PerModelUsage, 1, line)
	}
}

// TestFoldPiSubagentUsage_ClampsNegativeAndNonFinite covers the numbers in
// the usage file, which is written inside the sandbox: a negative, NaN or
// infinite figure must not be able to subtract from (or poison) the run
// totals that metrics.json reports.
func TestFoldPiSubagentUsage_ClampsNegativeAndNonFinite(t *testing.T) {
	t.Parallel()
	const parent = "anthropic-vertex/claude-opus-4-6"
	const child = "anthropic-vertex/claude-sonnet-4-6"
	lines := `{"seq":1,"model":"` + child + `","usage":{"input":-1000,"output":-5,"cacheRead":-7,"cacheWrite":-9,"cost":-100}}
{"seq":2,"model":"` + child + `","usage":{"input":10,"output":2,"cacheRead":1,"cacheWrite":1,"cost":0.05}}
`
	m := &RunMetrics{TotalCostUSD: 2, InputTokens: 100, OutputTokens: 20, CacheReadInputTokens: 3, CacheCreationInputTokens: 4}
	n, skipped := foldPiSubagentUsage([]byte(lines), parent, m)
	assert.Equal(t, 2, n)
	assert.Zero(t, skipped)
	assert.InDelta(t, 2+0.05, m.TotalCostUSD, 1e-9, "a negative cost cannot reduce the run total")
	assert.Equal(t, 110, m.InputTokens)
	assert.Equal(t, 22, m.OutputTokens)
	assert.Equal(t, 4, m.CacheReadInputTokens)
	assert.Equal(t, 5, m.CacheCreationInputTokens)
	assert.Equal(t, ModelUsage{Requests: 2, InputTokens: 10, OutputTokens: 2, CacheReadInputTokens: 1, CacheCreationInputTokens: 1, CostUSD: 0.05},
		m.PerModelUsage[child], "the clamped record still counts as a request, with zeroed figures")

	// JSON has no NaN or Infinity literal and the decoder rejects a number
	// that overflows float64, so no single record reaches the fold
	// non-finite today; the clamp is what keeps one out of the totals
	// whatever produced it.
	assert.Zero(t, nonNegativeCost(math.NaN()))
	assert.Zero(t, nonNegativeCost(math.Inf(1)), "+Inf would poison total_cost_usd for the rest of the run")
	assert.Zero(t, nonNegativeCost(math.Inf(-1)))
	assert.Zero(t, nonNegativeCost(-0.0))
	assert.Equal(t, 1.5, nonNegativeCost(1.5))
	assert.Equal(t, math.MaxFloat64, nonNegativeCost(math.MaxFloat64), "a finite figure, however large, is still folded")
	assert.Zero(t, nonNegative(-1))
	assert.Equal(t, 7, nonNegative(7))
}

// A truncated tail — the read hit piSubagentUsageMaxBytes, or a child was
// killed mid-append — must cost the run one skipped line, not its metrics.
func TestFoldPiSubagentUsage_TruncatedTail(t *testing.T) {
	t.Parallel()
	const parent = "anthropic-vertex/claude-opus-4-6"
	const child = "anthropic-vertex/claude-sonnet-4-6"
	data := `{"seq":1,"model":"` + child + `","usage":{"input":10,"output":2,"cost":0.05}}
{"seq":2,"model":"` + child + `","usage":{"input":10,"outp`
	m := &RunMetrics{TotalCostUSD: 1, InputTokens: 100}
	n, skipped := foldPiSubagentUsage([]byte(data), parent, m)
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, skipped)
	assert.InDelta(t, 1.05, m.TotalCostUSD, 1e-9)
}

func TestPiSubagentUsageReadCommand(t *testing.T) {
	t.Parallel()
	const usage = "/sandbox/pi-config/subagents/usage.jsonl"
	cmd := piSubagentUsageReadCommand(usage)
	assert.Contains(t, cmd, "mv -f '"+usage+"' '"+usage+piSubagentUsageReadSuffix+"'",
		"the file is consumed as it is read, so a failed ClearIterationArtifacts cannot double-count it")
	assert.Contains(t, cmd, "head -c 1048576 '"+usage+piSubagentUsageReadSuffix+"'",
		"the read is bounded: the file is agent-reachable inside the sandbox")
	assert.NotContains(t, cmd, "cat ", "an unbounded read is an unbounded allocation in the runner")
	assert.Contains(t, cmd, "|| true", "a missing file is the no-children case, not a failure")
}
