package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// useBudgetRunStub installs an openshell stub that lets runAgent get all the
// way through the validation loop: sandbox creation succeeds, the sandbox
// reports Ready, and the claude invocation (recognised by its
// `--output-format stream-json` flag) streams a canned result event whose
// total_cost_usd is costPerIteration. Every claude invocation appends a RUN
// line to the returned log file, so tests can count how many iterations
// actually started. With failDownload, `sandbox download` exits 1, so
// target-repo extraction fails every iteration and the loop retries via
// its `continue` path without ever reaching validation.
func useBudgetRunStub(t *testing.T, costPerIteration float64, failDownload bool) string {
	t.Helper()
	neutralizeAgentsRepoFallback(t)
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("FULLSEND_MINT_URL", "")
	t.Setenv("FULLSEND_GCP_OIDC_URL", "")

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	downloadAction := `mkdir -p "$5"; exit 0`
	if failDownload {
		downloadAction = "exit 1"
	}
	resultLine := fmt.Sprintf(
		`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":%v,"usage":{"input_tokens":10,"output_tokens":5}}`,
		costPerIteration)
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuoteForTest(logPath) + "\n" +
		"case \"$1 $2\" in\n" +
		"  'gateway list') echo default-gateway; exit 0 ;;\n" +
		"  'sandbox get') echo 'Phase: Ready'; exit 0 ;;\n" +
		"  'sandbox exec')\n" +
		"    case \"$*\" in\n" +
		"      *'--output-format stream-json'*)\n" +
		"        echo RUN >> " + shellQuoteForTest(logPath) + "\n" +
		"        printf '%s\\n' " + shellQuoteForTest(`{"type":"system","subtype":"init","model":"stub-model"}`) + "\n" +
		"        printf '%s\\n' " + shellQuoteForTest(resultLine) + "\n" +
		"        exit 0 ;;\n" +
		// The GitHub connectivity preflight probes for a token; reporting
		// NOTOKEN makes it skip cleanly.
		"      *NOTOKEN*) echo NOTOKEN; exit 0 ;;\n" +
		"      *) exit 0 ;;\n" +
		"    esac ;;\n" +
		// `sandbox download <name> <remote> <local>`: create the local
		// destination so SafeDownload's sanitize walk sees a directory —
		// or fail, to drive the extraction-failure retry path.
		"  'sandbox download') " + downloadAction + " ;;\n" +
		"esac\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	orig := sandbox.RetrySleepFn
	sandbox.RetrySleepFn = func(time.Duration) {}
	t.Cleanup(func() { sandbox.RetrySleepFn = orig })

	return logPath
}

// newBudgetHarnessDir builds a minimal fullsend dir whose code harness has
// the given max_cost_usd and an always-failing validation loop, so every
// completed iteration wants a retry.
func newBudgetHarnessDir(t *testing.T, maxCostUSD float64, maxIterations int) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "harness"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "code.md"),
		[]byte("You are a coding agent."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("agents:\n  - harness/code.yaml\n"), 0o644))

	harnessYAML := fmt.Sprintf(
		"agent: agents/code.md\nrole: test\nmax_cost_usd: %v\nvalidation_loop:\n  script: %s\n  max_iterations: %d\n",
		maxCostUSD, writePreScript(t, "exit 1\n"), maxIterations)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte(harnessYAML), 0o644))
	return dir
}

// countStubRuns returns how many claude invocations the openshell stub saw.
func countStubRuns(t *testing.T, logPath string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	runs := 0
	for _, line := range splitLines(string(data)) {
		if line == "RUN" {
			runs++
		}
	}
	return runs
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// readRunMetrics finds the single run directory under outputBase and
// unmarshals its metrics.json.
func readRunMetrics(t *testing.T, outputBase string) aggregateMetrics {
	t.Helper()
	entries, err := os.ReadDir(outputBase)
	require.NoError(t, err)
	require.Len(t, entries, 1, "expected exactly one run dir under %s", outputBase)
	data, err := os.ReadFile(filepath.Join(outputBase, entries[0].Name(), "metrics.json"))
	require.NoError(t, err)
	var m aggregateMetrics
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

// The behavioral half of the max_cost_usd contract: once the aggregate cost
// reaches the cap, the validation loop must not start another iteration,
// and metrics.json must record over_budget because a retry was due (the
// validation script always fails) and the cap suppressed it.
func TestRunAgent_BudgetHaltsValidationRetries(t *testing.T) {
	logPath := useBudgetRunStub(t, 0.06, false) // one iteration exhausts the 0.05 cap
	dir := newBudgetHarnessDir(t, 0.05, 3)
	outputBase := t.TempDir()

	var out strings.Builder
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, outputBase, t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(&out), false, runOverrideFlags{})
	// Validation never passes, so the run reports failure; the assertions
	// below are about what the budget stopped, not the exit status.
	t.Logf("runAgent returned: %v", err)

	assert.Equal(t, 1, countStubRuns(t, logPath),
		"a second iteration started even though iteration 1 exhausted max_cost_usd")
	assert.Contains(t, out.String(), "Skipping remaining retries",
		"a suppressed retry must be announced, not just recorded in metrics.json")

	entries, readErr := os.ReadDir(outputBase)
	require.NoError(t, readErr)
	require.Len(t, entries, 1)
	_, statErr := os.Stat(filepath.Join(outputBase, entries[0].Name(), "iteration-2"))
	assert.True(t, os.IsNotExist(statErr), "iteration-2 directory should never be created")

	m := readRunMetrics(t, outputBase)
	assert.True(t, m.OverBudget, "over_budget must record the suppressed retry")
	assert.InDelta(t, 0.06, m.TotalCostUSD, 1e-9)
	assert.Equal(t, 1, m.Iterations)
}

// The flip side: when the loop ends for its own reasons (iterations
// exhausted), crossing the cap on that final iteration suppresses nothing
// and must NOT be recorded as over_budget. The run log must not claim
// otherwise either — the cap-reached warning fires before the loop knows
// whether a retry is coming, so it may only speak about retries that
// remain, never about halting the run.
func TestRunAgent_BudgetCrossedOnFinalIterationIsNotMarked(t *testing.T) {
	logPath := useBudgetRunStub(t, 0.06, false)
	dir := newBudgetHarnessDir(t, 0.05, 1)
	outputBase := t.TempDir()

	var out strings.Builder
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, outputBase, t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(&out), false, runOverrideFlags{})
	t.Logf("runAgent returned: %v", err)

	assert.Equal(t, 1, countStubRuns(t, logPath))
	m := readRunMetrics(t, outputBase)
	assert.False(t, m.OverBudget, "a run that was ending anyway must not carry the over_budget marker")
	assert.InDelta(t, 0.06, m.TotalCostUSD, 1e-9)

	assert.Contains(t, out.String(), "any remaining retries will be skipped",
		"crossing the cap is still worth reporting")
	assert.NotContains(t, out.String(), "Skipping remaining retries",
		"nothing was suppressed, so the log must not say retries were skipped")
	assert.NotContains(t, out.String(), "no further iterations will start",
		"the warning must not claim a halt while over_budget stays false")
}

// Under the cap, the loop must keep retrying: the budget guard must not eat
// iterations that are still affordable.
func TestRunAgent_UnderBudgetStillRetries(t *testing.T) {
	logPath := useBudgetRunStub(t, 0.01, false) // 3 iterations cost 0.03, well under the cap
	dir := newBudgetHarnessDir(t, 5.00, 3)
	outputBase := t.TempDir()

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, outputBase, t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(io.Discard), false, runOverrideFlags{})
	t.Logf("runAgent returned: %v", err)

	assert.Equal(t, 3, countStubRuns(t, logPath), "all iterations should run while under budget")
	m := readRunMetrics(t, outputBase)
	assert.False(t, m.OverBudget)
	assert.Equal(t, 3, m.Iterations)
}

// The extraction-failure half of the contract: a failed repo extraction
// `continue`s past the bottom-of-loop retry check, so only the top-of-loop
// guard can suppress the retry and record over_budget. With the budget
// exhausted on iteration 1 and every download failing, iteration 2 must
// never start and the marker must still be written.
func TestRunAgent_BudgetHaltsExtractionFailureRetries(t *testing.T) {
	logPath := useBudgetRunStub(t, 0.06, true) // downloads fail; 0.06 exhausts the 0.05 cap
	dir := newBudgetHarnessDir(t, 0.05, 3)
	outputBase := t.TempDir()

	var out strings.Builder
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, outputBase, t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(&out), false, runOverrideFlags{})
	t.Logf("runAgent returned: %v", err)

	assert.Equal(t, 1, countStubRuns(t, logPath),
		"a second iteration started even though the budget was exhausted before the extraction-failure retry")
	assert.Contains(t, out.String(), "Skipping remaining retries",
		"the top-of-loop guard must announce the suppression it records")
	m := readRunMetrics(t, outputBase)
	assert.True(t, m.OverBudget, "the top-of-loop guard must record the suppressed extraction-failure retry")
	assert.InDelta(t, 0.06, m.TotalCostUSD, 1e-9)
	assert.Equal(t, 1, m.Iterations)
}

// Enforcement relies on runtime-reported cost: an iteration reporting $0
// while a cap is set must produce a warning, because the cap silently
// cannot account for it.
func TestRunAgent_ZeroCostIterationWarnsWhenCapSet(t *testing.T) {
	useBudgetRunStub(t, 0, false) // runtime reports no cost
	dir := newBudgetHarnessDir(t, 5.00, 1)
	outputBase := t.TempDir()

	var out strings.Builder
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, outputBase, t.TempDir(), "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(&out), false, runOverrideFlags{})
	t.Logf("runAgent returned: %v", err)

	assert.Contains(t, out.String(), "no cost data",
		"a zero-cost iteration under a cap must be called out in the run log")
	m := readRunMetrics(t, outputBase)
	assert.False(t, m.OverBudget)
	assert.Zero(t, m.TotalCostUSD)
}
