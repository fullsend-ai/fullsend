package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// claudeStreamUserMessage is one line of Claude Code's stream-json *input*
// format: the shape the mailbox feeder hands to `--input-format
// stream-json`. Both the run's opening prompt and every steer go in this
// way, so a steer is indistinguishable in kind from the prompt — content,
// never capability.
type claudeStreamUserMessage struct {
	Type    string `json:"type"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
}

// claudeInputLine encodes text as one NDJSON user message. Encoding
// through encoding/json is what keeps a multi-line steer on one line: a
// literal newline in the text would otherwise end the record and the
// remainder would be parsed as a second, malformed message.
func claudeInputLine(text string) (string, error) {
	var m claudeStreamUserMessage
	m.Type = "user"
	m.Message.Role = "user"
	m.Message.Content = text
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("encoding stream-json input: %w", err)
	}
	return string(b), nil
}

// steerFeederFragment is the POSIX sh fragment that feeds the mailbox into
// the agent's stdin. `tail -n +1 -f` starts at the first line, so the
// opening prompt written before launch is delivered even though the agent
// starts after it, and the file stays open for every later append.
//
// The pid is recorded because the run ends by killing this feeder: closing
// the agent's stdin is the only way to make a print-mode session exit, and
// the sandbox image ships no pkill/pgrep to find the process by name (the
// same constraint the stray-process sweep works around). `wait` keeps the
// subshell — and therefore the pipe — alive until the feeder is killed.
func steerFeederFragment(mailboxPath, pidPath string) string {
	return fmt.Sprintf("{ tail -n +1 -f %s & echo $! > %s ; wait ; }",
		shellQuote(mailboxPath), shellQuote(pidPath))
}

// Steer implements Steerer for Claude Code: it appends the message to the
// mailbox the in-sandbox feeder is tailing, and Claude Code consumes it at
// the next tool boundary — inside the running turn, not after it (probed
// on 2.1.259).
//
// The runner MUST hold its sandbox write lock (run.go's sandboxMu) across
// this call: the append is a sandbox exec and races the OIDC refresher and
// the OpenAI re-seeder, which are serialized through that lock.
func (ClaudeRuntime) Steer(ctx context.Context, sandboxName string, msg SteerMessage) error {
	f, ok := lookupSteerFeed(sandboxName)
	if !ok {
		return errNoSteerSession
	}
	line, err := claudeInputLine(renderSteerEnvelope(msg))
	if err != nil {
		return err
	}
	return f.appendLine(ctx, msg, line)
}

// Settle implements Steerer for Claude Code. It does not close stdin
// mid-turn: it records that no further steers will arrive and stops the
// feeder only once every message written has been echoed back and no turn
// is in flight. When the agent is still working, the stream handler makes
// that same check on the next result.
//
// The runner MUST hold its sandbox write lock across this call, as for
// Steer.
func (ClaudeRuntime) Settle(ctx context.Context, sandboxName string) error {
	f, ok := lookupSteerFeed(sandboxName)
	if !ok {
		// Run already returned, or was never steerable. A no-op by
		// contract, so a runner can `defer Settle` on every path.
		return nil
	}
	if f.settle() {
		return f.stopFeeder(ctx)
	}
	return nil
}

// claudeSteerAggregator folds a steered run's N result events into one set
// of RunMetrics. Which fields add and which replace is not symmetric, and
// the asymmetry is measured, not assumed — from two turns of one Claude
// Code 2.1.259 session:
//
//	result 1: num_turns 1, total_cost_usd 0.0529, usage{in 2, out 5,
//	          cache_read 25322, cache_creation 11955}
//	result 2: num_turns 1, total_cost_usd 0.0607, usage{in 2, out 5,
//	          cache_read 37277, cache_creation 58}
//
// and the same result 2 carries modelUsage{inputTokens 4, outputTokens 10,
// cacheReadInputTokens 62599, cacheCreationInputTokens 12013} — exactly
// the sums of both turns, and costUSD equal to total_cost_usd.
//
// So `usage` and `num_turns` are PER-TURN and add up, while
// total_cost_usd is ALREADY CUMULATIVE for the session and must be taken,
// not summed. Do not "fix" the cost line into a sum: turn 2 above is worth
// about $0.008, and summing would report $0.11 for an $0.06 run, with the
// error growing on every steer.
type claudeSteerAggregator struct {
	turns      int
	input      int
	output     int
	reasoning  int
	cacheRead  int
	cacheWrite int
}

// onResult folds one turn's authoritative totals into metrics.
func (a *claudeSteerAggregator) onResult(e ResultEvent, metrics *RunMetrics) {
	a.turns += e.NumTurns
	a.input += e.InputTokens
	a.output += e.OutputTokens
	a.reasoning += e.ReasoningTokens
	a.cacheRead += e.CacheReadInputTokens
	a.cacheWrite += e.CacheCreationInputTokens

	metrics.NumTurns = a.turns
	metrics.TotalCostUSD = e.TotalCostUSD
	a.publish(metrics)
}

// onTokens folds the parser's incremental snapshot into metrics. That
// snapshot is cumulative across the whole stream, not per-turn, so it
// leads the completed-turn sum while a turn is in flight and trails it
// afterwards (it is emitted only every tokenThreshold tokens). Taking the
// larger of the two per field keeps a killed run's partial turn without
// letting a throttled snapshot undo a finished turn's totals.
func (a *claudeSteerAggregator) onTokens(e TokensEvent, metrics *RunMetrics) {
	metrics.InputTokens = max(a.input, e.InputTokens)
	metrics.OutputTokens = max(a.output, e.OutputTokens)
	metrics.ReasoningTokens = max(a.reasoning, e.ReasoningTokens)
	metrics.CacheReadInputTokens = max(a.cacheRead, e.CacheRead)
	metrics.CacheCreationInputTokens = max(a.cacheWrite, e.CacheWrite)
}

func (a *claudeSteerAggregator) publish(metrics *RunMetrics) {
	metrics.InputTokens = max(metrics.InputTokens, a.input)
	metrics.OutputTokens = max(metrics.OutputTokens, a.output)
	metrics.ReasoningTokens = max(metrics.ReasoningTokens, a.reasoning)
	metrics.CacheReadInputTokens = max(metrics.CacheReadInputTokens, a.cacheRead)
	metrics.CacheCreationInputTokens = max(metrics.CacheCreationInputTokens, a.cacheWrite)
}

// steerEchoTime resolves an echo's delivery time, falling back to now when
// the runtime reported no usable timestamp. DeliveredAt is read by the
// runner to decide whether an update landed before or after a given point,
// so an unparsable timestamp must not become the zero time.
func steerEchoTime(raw string) time.Time {
	if raw == "" {
		return time.Now()
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Now()
	}
	return t
}

// Ensure ClaudeRuntime implements Steerer.
var _ Steerer = ClaudeRuntime{}
