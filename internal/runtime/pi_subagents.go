package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Sub-agent accounting. fullsend-agent.js appends one JSON line per child
// to the manifest's usageFile; Run reads it after the iteration and folds
// it into RunMetrics so metrics.json carries the children's cost. The
// parent's own numbers come from the --mode json stream — a child's do not
// appear there at all, because each child is a separate pi process. Run
// folds on every Agent-enabled iteration, dispatching or not, so the
// parent's entry is always in the breakdown (see foldPiSubagentUsage).
//
// The fold covers exactly the fields a per-model entry has: input,
// output, cache-creation and cache-read tokens, and cost. Reasoning
// tokens are not among them — RunMetrics carries a run-level
// ReasoningTokens with no ModelUsage counterpart — so the
// breakdown-sums-to-the-totals invariant is about those five and not
// about every number in metrics.json.

// piSubagentUnknownModel keys a usage record that carries no model spec:
// the line has a seq and a usage object but its "model" is empty or
// missing. Better a visible bucket than silently dropped cost.
const piSubagentUnknownModel = "unknown"

// piSubagentUsageRecord is the line shape fullsend-agent.js writes. Fields
// the runner does not aggregate (description, stopReason, durationMs) are
// left out: the child session transcript carries them.
type piSubagentUsageRecord struct {
	Model string `json:"model"`
	Usage struct {
		Input      int     `json:"input"`
		Output     int     `json:"output"`
		CacheRead  int     `json:"cacheRead"`
		CacheWrite int     `json:"cacheWrite"`
		Cost       float64 `json:"cost"`
	} `json:"usage"`
}

// piSubagentUsageMaxBytes bounds the usage file the runner reads back. The
// file is agent-reachable inside the sandbox (as everything under the
// config dir is), so an unbounded read is an unbounded allocation in the
// runner; the same reason piAgentProbe caps its probe output. A truncated
// tail becomes a skipped malformed line, which foldPiSubagentUsage reports.
const piSubagentUsageMaxBytes = 1 << 20 // 1 MiB, ~4k child records

// piSubagentUsageReadSuffix marks a usage file the runner has already
// folded. The read renames before printing, so the fold is idempotent per
// iteration: a retry whose ClearIterationArtifacts failed finds no
// unconsumed file and cannot count the same children twice.
const piSubagentUsageReadSuffix = ".read"

// piSubagentUsageReadCommand renders the in-sandbox read of the usage file:
// take it out of the way first, then print what it holds. A missing file is
// the ordinary case — the agent dispatched no sub-agent — so the command
// succeeds with empty output rather than failing the run.
func piSubagentUsageReadCommand(usageFile string) string {
	f := shellQuote(usageFile)
	r := shellQuote(usageFile + piSubagentUsageReadSuffix)
	return fmt.Sprintf("if [ -f %s ]; then mv -f %s %s && head -c %d %s; fi 2>/dev/null || true",
		f, f, r, piSubagentUsageMaxBytes, r)
}

// foldPiSubagentUsage adds the children's usage to m's totals and builds
// m.PerModelUsage: one entry per child model spec plus the parent's own
// iteration under parentSpec, so the breakdown sums to the totals. It
// returns the number of child records folded and the number of lines that
// were not usage records.
//
// The parent entry is added whenever this runs, not only when children were
// found. Iterations are summed across a run (internal/cli aggregateRunMetrics),
// so an iteration that dispatched nothing must still contribute its own
// tokens to the breakdown — otherwise a retry run where only one iteration
// dispatched children has a per_model_usage that no longer sums to the
// totals.
//
// Malformed lines are skipped: the file is written by the extension inside
// the sandbox, so a truncated last line (a child killed mid-append, or the
// read hitting piSubagentUsageMaxBytes) must not cost the run its metrics.
func foldPiSubagentUsage(data []byte, parentSpec string, m *RunMetrics) (folded, skipped int) {
	records, skipped := parsePiSubagentUsage(data)
	per := m.PerModelUsage
	if per == nil {
		per = make(map[string]ModelUsage, len(records)+1)
	}
	addUsage := func(key string, u ModelUsage) {
		entry := per[key]
		entry.Add(u)
		per[key] = entry
	}
	if parentSpec == "" {
		parentSpec = piSubagentUnknownModel
	}
	addUsage(parentSpec, ModelUsage{
		Requests:                 1,
		InputTokens:              m.InputTokens,
		OutputTokens:             m.OutputTokens,
		CacheCreationInputTokens: m.CacheCreationInputTokens,
		CacheReadInputTokens:     m.CacheReadInputTokens,
		CostUSD:                  m.TotalCostUSD,
	})
	for _, rec := range records {
		key := strings.TrimSpace(rec.Model)
		if key == "" {
			key = piSubagentUnknownModel
		}
		// Clamped at zero: the file is written inside the sandbox, so a
		// negative count is either a truncated append reparsed as a number
		// or an agent-authored line, and either way subtracting it would
		// let the breakdown understate the run's real spend - the one
		// number metrics.json exists to make auditable.
		u := ModelUsage{
			Requests:                 1,
			InputTokens:              nonNegative(rec.Usage.Input),
			OutputTokens:             nonNegative(rec.Usage.Output),
			CacheCreationInputTokens: nonNegative(rec.Usage.CacheWrite),
			CacheReadInputTokens:     nonNegative(rec.Usage.CacheRead),
			CostUSD:                  nonNegativeCost(rec.Usage.Cost),
		}
		addUsage(key, u)
		m.InputTokens += u.InputTokens
		m.OutputTokens += u.OutputTokens
		m.CacheCreationInputTokens += u.CacheCreationInputTokens
		m.CacheReadInputTokens += u.CacheReadInputTokens
		m.TotalCostUSD += u.CostUSD
	}
	m.PerModelUsage = per
	return len(records), skipped
}

// nonNegative and nonNegativeCost clamp a usage figure at zero. See the
// call site in foldPiSubagentUsage. Only the float form has to consider the
// non-finite values; an int can hold neither NaN nor an infinity.
func nonNegative(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func nonNegativeCost(v float64) float64 {
	// The inverted comparison catches NaN and -Inf; math.IsInf catches the
	// one non-finite value it lets through. Either would poison every total
	// it is added to for the rest of the run, and TotalCostUSD is the
	// number metrics.json exists to make auditable. Neither reaches here
	// through encoding/json today -- JSON has no NaN or Infinity literal
	// and the decoder rejects a number that overflows float64 outright --
	// but this clamp is the only thing between an agent-writable file and
	// the run totals, so it does not lean on that.
	if !(v > 0) || math.IsInf(v, 1) {
		return 0
	}
	return v
}

// parsePiSubagentUsage decodes the usage JSONL, dropping lines that are not
// a usage record and counting them. A record has to carry both a seq and a
// usage object: `{}` has neither, and a line with only one of them is a
// truncated or hand-written entry whose numbers cannot be trusted to be the
// whole of what a child spent. Both cases are counted as skipped rather
// than folded, so the gap shows up in the run log instead of as a silent
// hole in per_model_usage.
func parsePiSubagentUsage(data []byte) ([]piSubagentUsageRecord, int) {
	var out []piSubagentUsageRecord
	skipped := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 8*1024), maxTranscriptLineSize)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		// Two passes rather than one struct with pointer fields: an
		// embedded record whose Usage is shadowed by a probe field is
		// never filled by encoding/json (the shallower field wins).
		var probe struct {
			Usage *json.RawMessage `json:"usage"`
			Seq   *int             `json:"seq"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			skipped++
			continue
		}
		if probe.Usage == nil || probe.Seq == nil {
			skipped++
			continue
		}
		var rec piSubagentUsageRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			skipped++
			continue
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		// A line past maxTranscriptLineSize; the rest of the file is not
		// read, so report it rather than silently losing the tail.
		skipped++
	}
	return out, skipped
}
