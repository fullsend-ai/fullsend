package runtime

import (
	"bytes"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuf is a bytes.Buffer safe to read while the watchdog goroutine writes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func countLines(s, substr string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

// TestStallWatchdog_EventsKeepItQuiet: a stream that keeps producing events is
// never killed, however long the run lasts.
func TestStallWatchdog_EventsKeepItQuiet(t *testing.T) {
	var kills atomic.Int32
	w := startStallWatchdogTo(io.Discard, 150*time.Millisecond, ui.New(io.Discard), func() { kills.Add(1) })
	require.NotNil(t, w)
	defer w.stop()

	// Three times the timeout, with an event well inside every window.
	deadline := time.Now().Add(450 * time.Millisecond)
	for time.Now().Before(deadline) {
		w.note()
		time.Sleep(15 * time.Millisecond)
	}

	assert.Zero(t, kills.Load(), "a stream that keeps emitting events must not be killed")
	assert.NoError(t, w.stalledErr())
}

// TestStallWatchdog_SilenceKills: past the threshold the sandbox command is
// killed and the run fails with the distinct sentinel, not a generic error.
func TestStallWatchdog_SilenceKills(t *testing.T) {
	var kills atomic.Int32
	w := startStallWatchdogTo(io.Discard, 100*time.Millisecond, ui.New(io.Discard), func() { kills.Add(1) })
	require.NotNil(t, w)
	defer w.stop()

	assert.Eventually(t, func() bool { return kills.Load() > 0 }, 2*time.Second, 10*time.Millisecond,
		"watchdog did not invoke the kill path after the stall timeout")

	err := w.stalledErr()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStalled)
	assert.Contains(t, err.Error(), "no runtime events for 100ms")

	// The kill fires once, and stop() after the fact keeps the verdict.
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, int32(1), kills.Load(), "the watchdog must kill once, then stand down")
	w.stop()
	assert.ErrorIs(t, w.stalledErr(), ErrStalled)
}

// TestStallWatchdog_ZeroDisables: FULLSEND_STALL_TIMEOUT=0 means no watchdog,
// and every method stays safe on the nil that represents it.
func TestStallWatchdog_ZeroDisables(t *testing.T) {
	var kills atomic.Int32
	w := startStallWatchdogTo(io.Discard, 0, ui.New(io.Discard), func() { kills.Add(1) })
	require.Nil(t, w, "a zero timeout must disable the watchdog")

	w.note()
	w.stop()
	assert.NoError(t, w.stalledErr())

	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, kills.Load())
}

// TestStallWatchdog_WarnsOncePerStallEpisode: half a timeout of silence warns
// exactly once — not once per poll — and an event rearms the warning.
func TestStallWatchdog_WarnsOncePerStallEpisode(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")

	var kills atomic.Int32
	annotations := &syncBuf{}
	w := startStallWatchdogTo(annotations, 500*time.Millisecond, ui.New(io.Discard), func() { kills.Add(1) })
	require.NotNil(t, w)
	defer w.stop()

	// Past the half-threshold (250ms), still short of the kill (500ms). The
	// poll runs every 25ms, so a per-poll warning would print several.
	time.Sleep(350 * time.Millisecond)
	assert.Equal(t, 1, countLines(annotations.String(), "::warning::"),
		"expected exactly one warning per stall episode, got: %q", annotations.String())
	assert.Contains(t, annotations.String(), "::warning::no agent events for 250ms")

	// An event ends the episode; the next silence warns again.
	w.note()
	time.Sleep(350 * time.Millisecond)
	assert.Equal(t, 2, countLines(annotations.String(), "::warning::"),
		"an event should rearm the warning for the next stall episode")
	assert.Zero(t, kills.Load(), "warning at half the threshold must not kill the run")
}

// TestStallWatchdog_NoWarningAfterStop: once stop() returns, a queued tick
// must not emit a stall warning — the select can still draw ticker.C after
// stopped closes, and a completed run warning about inactivity is misleading.
func TestStallWatchdog_NoWarningAfterStop(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")

	annotations := &syncBuf{}
	w := startStallWatchdogTo(annotations, 200*time.Millisecond, ui.New(io.Discard), func() {})
	require.NotNil(t, w)

	// Deep in the warn window (past 100ms, short of the 200ms kill), so ticks
	// queued around stop() would warn if the disarm didn't suppress them.
	time.Sleep(140 * time.Millisecond)
	w.stop()
	before := countLines(annotations.String(), "::warning::")

	// Several more poll intervals: any warning emitted now is the race.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, before, countLines(annotations.String(), "::warning::"),
		"a stopped watchdog must not emit new warnings, got: %q", annotations.String())
}

// TestStallWatchdog_LateEventSuppressesWarning: note() is not serialized with
// the tick, so an event can land after the tick computed its silence but
// before the warning is emitted. The lastEvent re-check under mu must
// suppress that warning — a stall notice right after activity resumed is
// misleading. Driven through warnIfStillSilent directly because the race
// window in watch() cannot be hit deterministically.
func TestStallWatchdog_LateEventSuppressesWarning(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")

	annotations := &syncBuf{}
	// An hour-long timeout: the poll ticker contributes nothing during the
	// test, so the warn path is driven only by the direct calls below.
	w := startStallWatchdogTo(annotations, time.Hour, ui.New(io.Discard), func() {})
	require.NotNil(t, w)
	defer w.stop()

	// The tick read lastEvent == 0, then an event arrived before the warning
	// was emitted: the stale episode must stay silent.
	time.Sleep(2 * time.Millisecond) // note() must store a value distinct from 0
	w.note()
	w.warnIfStillSilent(0)
	assert.Empty(t, annotations.String(),
		"an event arriving after the silence calculation must suppress the warning")

	// The same call with the current lastEvent emits, proving the lastEvent
	// guard — not some other condition — is what suppressed it above.
	w.warnIfStillSilent(w.lastEvent.Load())
	assert.Equal(t, 1, countLines(annotations.String(), "::warning::"))
}

// TestParsePiStream_DiscardedLinesResetTheSilenceClock: pi lines with no
// AgentEvent mapping — tool_execution_update streams continuously while a
// tool runs — must still count as liveness, so an actively streaming tool is
// never killed as stalled. Garbage and blank lines must not count.
func TestParsePiStream_DiscardedLinesResetTheSilenceClock(t *testing.T) {
	w := startStallWatchdogTo(io.Discard, time.Hour, ui.New(io.Discard), func() {})
	require.NotNil(t, w)
	defer w.stop()

	var events []AgentEvent
	lines := 0
	onLine := func() { lines++; w.note() }

	time.Sleep(2 * time.Millisecond)
	input := `{"type":"tool_execution_update","toolCallId":"t1"}` + "\n" +
		`{"type":"turn_start"}` + "\n" +
		"not json\n" +
		"\n"
	_, err := parsePiStreamLines(strings.NewReader(input),
		func(e AgentEvent) { events = append(events, e) }, onLine)
	require.NoError(t, err)

	assert.Equal(t, 2, lines, "each well-formed line is liveness; garbage and blanks are not")
	assert.Positive(t, w.lastEvent.Load(),
		"a discarded lifecycle line must reset the watchdog's silence clock")
	// The lifecycle lines themselves emit nothing; the only event is the
	// EOF fallback result for the truncated stream.
	require.Len(t, events, 1)
	assert.IsType(t, ResultEvent{}, events[0])
}

// TestParseClaudeStream_ToolResultLinesResetTheSilenceClock: Claude Code
// `user` tool_result lines produce no AgentEvent, but they are stream
// activity and must feed the watchdog.
func TestParseClaudeStream_ToolResultLinesResetTheSilenceClock(t *testing.T) {
	w := startStallWatchdogTo(io.Discard, time.Hour, ui.New(io.Discard), func() {})
	require.NotNil(t, w)
	defer w.stop()

	var events []AgentEvent
	lines := 0
	onLine := func() { lines++; w.note() }

	time.Sleep(2 * time.Millisecond)
	input := `{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}` + "\n"
	err := parseClaudeStreamLines(strings.NewReader(input),
		func(e AgentEvent) { events = append(events, e) }, onLine)
	require.NoError(t, err)

	assert.Equal(t, 1, lines)
	assert.Empty(t, events, "user tool_result lines map to no AgentEvent")
	assert.Positive(t, w.lastEvent.Load(),
		"a tool_result line must reset the watchdog's silence clock")
}

// TestParsePiStream_OversizedLinesResetTheSilenceClock: a line exceeding
// streamBufSize is consumed without semantic parsing, but a runtime writing
// megabytes is alive — the drained line must still feed the watchdog.
func TestParsePiStream_OversizedLinesResetTheSilenceClock(t *testing.T) {
	w := startStallWatchdogTo(io.Discard, time.Hour, ui.New(io.Discard), func() {})
	require.NotNil(t, w)
	defer w.stop()

	var events []AgentEvent
	lines := 0
	onLine := func() { lines++; w.note() }

	time.Sleep(2 * time.Millisecond)
	input := `{"type":"big","pad":"` + strings.Repeat("a", streamBufSize+1) + `"}` + "\n"
	_, err := parsePiStreamLines(strings.NewReader(input),
		func(e AgentEvent) { events = append(events, e) }, onLine)
	require.NoError(t, err)

	assert.Equal(t, 1, lines, "a fully consumed oversized line must count as liveness")
	assert.Positive(t, w.lastEvent.Load(),
		"an oversized line must reset the watchdog's silence clock")
	// Semantic parsing still skips it; the only event is the EOF fallback
	// result for the truncated stream.
	require.Len(t, events, 1)
	assert.IsType(t, ResultEvent{}, events[0])
}

// TestParseClaudeStream_OversizedLinesResetTheSilenceClock: the Claude parser
// twin of the pi oversized-line liveness test.
func TestParseClaudeStream_OversizedLinesResetTheSilenceClock(t *testing.T) {
	w := startStallWatchdogTo(io.Discard, time.Hour, ui.New(io.Discard), func() {})
	require.NotNil(t, w)
	defer w.stop()

	var events []AgentEvent
	lines := 0
	onLine := func() { lines++; w.note() }

	time.Sleep(2 * time.Millisecond)
	input := `{"type":"big","pad":"` + strings.Repeat("a", streamBufSize+1) + `"}` + "\n"
	err := parseClaudeStreamLines(strings.NewReader(input),
		func(e AgentEvent) { events = append(events, e) }, onLine)
	require.NoError(t, err)

	assert.Equal(t, 1, lines, "a fully consumed oversized line must count as liveness")
	assert.Positive(t, w.lastEvent.Load(),
		"an oversized line must reset the watchdog's silence clock")
	assert.Empty(t, events, "oversized lines stay excluded from semantic parsing")
}

// TestStallWatchdog_NoAnnotationsOutsideCI keeps the workflow-command syntax
// out of local terminals, as the event renderer does.
func TestStallWatchdog_NoAnnotationsOutsideCI(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")

	annotations := &syncBuf{}
	console := &syncBuf{}
	w := startStallWatchdogTo(annotations, 100*time.Millisecond, ui.New(console), func() {})
	require.NotNil(t, w)
	defer w.stop()

	assert.Eventually(t, func() bool { return strings.Contains(console.String(), "no agent events for 50ms") },
		2*time.Second, 10*time.Millisecond, "the warning must still reach the printer outside CI")
	assert.Empty(t, annotations.String(), "no workflow annotations outside CI")
}

// TestRuntimeRun_DoesNotRebindTheCallerContext guards the regression that
// reported every successful run as "cancelled": a body-scope
// `ctx, cancel := context.WithCancel(ctx)` rebinds the ctx an earlier-
// registered defer closed over, and defers run LIFO, so the cancel fires
// first and the status notifier reads ctx.Err() != nil. The stall watchdog
// must therefore kill through the cancel ExecStreamReader already returns —
// a context derived inside the sandbox package, owned by nobody else —
// rather than deriving anything from the caller's ctx.
//
// It reads the source because the failure is invisible at the package
// boundary: the run still works, only the reported status is wrong.
func TestRuntimeRun_DoesNotRebindTheCallerContext(t *testing.T) {
	// Body-scope (one-tab) rebinding of the ctx parameter. A derived context
	// on a NEW variable name is fine; only rebinding `ctx` is the hazard.
	rebind := regexp.MustCompile(`(?m)^\tctx(, [A-Za-z_][A-Za-z0-9_]*)? :?= context\.`)

	for _, file := range streamingRuntimeFiles(t) {
		src, err := os.ReadFile(file)
		require.NoError(t, err)

		assert.NotRegexp(t, rebind, string(src),
			"%s rebinds the caller's ctx at function-body scope; the watchdog must reuse the ExecStreamReader cancel instead", file)
		assert.Contains(t, string(src), "stallKill(sandbox.Exec, params.SandboxName, os.Stderr, cancel)",
			"%s must arm the watchdog on the sandbox command's own cancel, not on a context it derives", file)
	}
}

// streamingRuntimeFiles returns the runtime sources that drain a stream
// through sandbox.ExecStreamReader — the ones the watchdog applies to. It is
// derived from the call sites rather than listed, so a fourth streaming
// runtime cannot be added without the watchdog: that omission is exactly how
// codex shipped unguarded while claude and pi were covered.
func streamingRuntimeFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	var files []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, readErr := os.ReadFile(name)
		require.NoError(t, readErr)
		if strings.Contains(string(src), "sandbox.ExecStreamReader(") {
			files = append(files, name)
		}
	}
	require.ElementsMatch(t, []string{"claude.go", "pi_run.go", "codex_run.go"}, files)
	return files
}

// TestStreamingRuntimesArmTheWatchdog: claude, pi and codex share one
// ExecStreamReader -> handler -> parse shape, so all three must arm the
// watchdog, feed it per stream line, and report its verdict ahead of the
// Wait error.
func TestStreamingRuntimesArmTheWatchdog(t *testing.T) {
	for _, file := range streamingRuntimeFiles(t) {
		src, err := os.ReadFile(file)
		require.NoError(t, err)
		s := string(src)
		assert.Contains(t, s, "startStallWatchdog(params.StallTimeout, printer,", "%s must arm the watchdog", file)
		assert.Contains(t, s, ", stall.note)", "%s must feed the watchdog per stream line", file)
		assert.Contains(t, s, "stall.stalledErr()", "%s must report ErrStalled ahead of the Wait error", file)
	}
}

// TestStallKill_TerminatesTheAgentThenReleasesTheClient: cancelling the exec
// only SIGKILLs the local openshell client, so the kill must sweep the
// sandbox first — otherwise the agent keeps writing the workspace and
// spending tokens until teardown, and indefinitely under --keep-sandbox.
func TestStallKill_TerminatesTheAgentThenReleasesTheClient(t *testing.T) {
	t.Parallel()

	var order []string
	var sandboxName string
	execFn := func(name, cmd string, _ time.Duration) (string, string, int, error) {
		sandboxName = name
		assert.Equal(t, killStrayProcessesScript(), cmd)
		order = append(order, "sweep")
		return "stray processes killed: 2\n", "", 0, nil
	}
	var out bytes.Buffer

	stallKill(execFn, "sb-1", &out, func() { order = append(order, "cancel") })()

	assert.Equal(t, []string{"sweep", "cancel"}, order,
		"the in-sandbox agent must be killed before the client is released")
	assert.Equal(t, "sb-1", sandboxName)
	assert.Contains(t, out.String(), "Terminated 2 stalled sandbox process(es)")
}

// TestStallKill_ReleasesTheClientWhenTheSweepFails: a broken exec channel
// must not strand the run — the client cancel still has to happen, with the
// sweep failure reported rather than swallowed.
func TestStallKill_ReleasesTheClientWhenTheSweepFails(t *testing.T) {
	t.Parallel()

	cancelled := false
	execFn := func(string, string, time.Duration) (string, string, int, error) {
		return "", "", 0, errors.New("gateway unreachable")
	}
	var out bytes.Buffer

	stallKill(execFn, "sb-2", &out, func() { cancelled = true })()

	assert.True(t, cancelled, "cancel must run even when the sandbox sweep fails")
	assert.Contains(t, out.String(), "could not terminate the stalled agent inside the sandbox")
	assert.Contains(t, out.String(), "gateway unreachable")
}

// TestStallKill_QuietWhenNothingWasRunning: a sweep that signalled nothing
// says nothing, so the stall verdict is not diluted by a "killed 0" line.
func TestStallKill_QuietWhenNothingWasRunning(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	stallKill(recordingExec(new([]string), "stray processes killed: 0\n", "", 0, nil), "sb-3", &out, func() {})()

	assert.Empty(t, out.String())
}

// TestParseCodexStream_ProgressLinesResetTheSilenceClock: codex item.started
// and item.updated lines map to no AgentEvent but stream while a command
// runs, so they must feed the watchdog — the same rule as pi and Claude.
func TestParseCodexStream_ProgressLinesResetTheSilenceClock(t *testing.T) {
	w := startStallWatchdogTo(io.Discard, time.Hour, ui.New(io.Discard), func() {})
	require.NotNil(t, w)
	defer w.stop()

	var events []AgentEvent
	lines := 0
	onLine := func() { lines++; w.note() }

	time.Sleep(2 * time.Millisecond)
	input := `{"type":"item.started","item":{"type":"command_execution"}}` + "\n" +
		`{"type":"item.updated","item":{"type":"command_execution"}}` + "\n" +
		"not json\n" +
		"\n"
	_, err := parseCodexStreamLines(strings.NewReader(input),
		func(e AgentEvent) { events = append(events, e) }, onLine)
	require.NoError(t, err)

	assert.Equal(t, 2, lines, "each well-formed line is liveness; garbage and blanks are not")
	assert.Positive(t, w.lastEvent.Load(),
		"a progress line must reset the watchdog's silence clock")
	// The progress lines emit nothing; the only event is the terminal
	// ResultEvent for a stream that carried no turn outcome.
	require.Len(t, events, 1)
	assert.IsType(t, ResultEvent{}, events[0])
}

// TestParseCodexStream_OversizedLinesResetTheSilenceClock: the codex twin of
// the pi and Claude oversized-line liveness tests.
func TestParseCodexStream_OversizedLinesResetTheSilenceClock(t *testing.T) {
	w := startStallWatchdogTo(io.Discard, time.Hour, ui.New(io.Discard), func() {})
	require.NotNil(t, w)
	defer w.stop()

	lines := 0
	time.Sleep(2 * time.Millisecond)
	input := `{"type":"big","pad":"` + strings.Repeat("a", streamBufSize+1) + `"}` + "\n"
	_, err := parseCodexStreamLines(strings.NewReader(input), func(AgentEvent) {},
		func() { lines++; w.note() })
	require.NoError(t, err)

	assert.Equal(t, 1, lines, "a fully consumed oversized line must count as liveness")
	assert.Positive(t, w.lastEvent.Load(),
		"an oversized line must reset the watchdog's silence clock")
}

// TestStallDetectionLatency_IsThePollInterval: the runner sizes its
// arm/disarm decision on this, so it must be the interval watch() actually
// ticks at — a fraction of the timeout, capped at 30s and floored at 1ms.
func TestStallDetectionLatency_IsThePollInterval(t *testing.T) {
	t.Parallel()

	assert.Equal(t, stallMaxPoll, StallDetectionLatency(15*time.Minute), "long timeouts are capped")
	assert.Equal(t, 15*time.Second, StallDetectionLatency(5*time.Minute), "below the cap it is timeout/20")
	assert.Equal(t, stallMinPoll, StallDetectionLatency(time.Nanosecond), "a tiny timeout still ticks")
}
