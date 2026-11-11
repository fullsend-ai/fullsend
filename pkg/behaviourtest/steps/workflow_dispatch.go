package steps

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// workflowDispatchEvent is the event a workflow started through the
// workflow_dispatch API runs for.
const workflowDispatchEvent = "workflow_dispatch"

func registerWorkflowDispatchSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the "([^"]+)" workflow is dispatched with event payload:$`, func(ctx context.Context, file string, payload *godog.DocString) (context.Context, error) {
		return ctx, whenWorkflowDispatchedWithPayload(ctx, world.FromContext(ctx), file, payload.Content)
	})
	sc.Step(`^the "([^"]+)" workflow fails with "([^"]+)" in its logs$`, func(ctx context.Context, file, needle string) (context.Context, error) {
		return ctx, thenWorkflowFailsLogging(ctx, world.FromContext(ctx), file, needle)
	})
}

func workflowDispatcher(w *world.World) (ci.WorkflowDispatcher, error) {
	d, ok := w.CI.(ci.WorkflowDispatcher)
	if !ok {
		return nil, fmt.Errorf("the CI driver does not implement ci.WorkflowDispatcher; it cannot dispatch a workflow")
	}
	return d, nil
}

// errDispatchedWorkflow is the fail-fast error for harness steps that
// would read w.WorkflowRun — or scope a run window from w.ScenarioStart,
// which the dispatch step moves to the dispatch time — after the
// workflow dispatch step or the workflow-failure step ran: those steps
// read the field and the windows with harness semantics, so without the
// guard a wrong-run check, a moved window or a skipped settle could pass
// or fail on the dispatched run instead of the harness run. A harness
// wait step that re-resolves the harness run (such as "the harness …
// workflow completes successfully") clears the flag and makes them legal
// again — but their wait window still opens at the dispatch time
// (ScenarioStart stays moved), so a harness run triggered before the
// dispatch is not matched.
var errDispatchedWorkflow = errors.New("the workflow dispatch step ran in this scenario, so this harness step cannot follow it without re-resolving the harness run: the dispatched workflow's run is not a harness agent run")

// whenWorkflowDispatchedWithPayload starts a workflow of the leased
// repository on its default branch with event_type "issues", source_repo
// the leased repository, and the given event_payload, a caller-written
// dispatch payload. The target workflow must declare those three inputs
// under on.workflow_dispatch.inputs: GitHub is expected to refuse
// undeclared inputs with a 422 — expected, not confirmed against the
// live API — and a refused dispatch reports the client's error
// unchanged.
func whenWorkflowDispatchedWithPayload(ctx context.Context, w *world.World, file, payload string) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before dispatching a workflow")
	}
	d, err := workflowDispatcher(w)
	if err != nil {
		return err
	}
	ref, err := w.SCM.GetDefaultBranch(ctx, w.Org, w.RepoName)
	if err != nil {
		return fmt.Errorf("reading the default branch: %w", err)
	}
	// ScenarioStart is the trigger time, which trigger steps only move
	// forward (world.World); the Then step opens its own wait window a
	// skew buffer earlier. Truncated to second precision like every
	// trigger step: forge run created_at timestamps have second
	// precision, so a fractional boundary excludes same-second runs
	// from every later creation-time filter (#7957).
	w.ScenarioStart = time.Now().Truncate(time.Second)
	// The dispatched workflow's run is not a harness agent run: steps
	// that read w.WorkflowRun and the run windows with harness semantics
	// (log, artifact, negative-assertion, triage, gateway-cleanup,
	// pre-script-skip, submitted-review, dispatch-count, agent-triggered
	// and empty-matrix steps, and the OWNERS dispatch-run wait) fail fast
	// (see errDispatchedWorkflow) instead of silently asserting on it,
	// until a harness wait step re-resolves the harness run.
	w.DispatchedWorkflow = true
	w.DispatchedWorkflowFile = file
	w.DispatchedWorkflowAt = w.ScenarioStart
	floor, err := d.DispatchWorkflow(ctx, w.Org, w.RepoName, file, ref, map[string]string{
		"event_type":    issueOpenEvent,
		"source_repo":   w.Org + "/" + w.RepoName,
		"event_payload": strings.TrimSpace(payload),
	})
	if err != nil {
		return err
	}
	// The run-ID floor (the highest run of the file listed before the
	// dispatch) is recorded beside the file and time, never cleared: the
	// failure step's wait accepts only runs above it, so a run already
	// listed before the dispatch — the org scheduler, a harness, an
	// earlier dispatch in the scenario — can never become the asserted
	// run. A second dispatch of the same file records its own floor —
	// higher once the earlier run is listed — so assert each dispatch
	// before dispatching the same file again.
	w.DispatchedWorkflowRunFloor = floor
	return nil
}

// thenWorkflowFailsLogging waits for the dispatched workflow's run and
// requires that it concluded with a terminal failure — failure,
// timed_out or startup_failure, the conclusions ci.IsTerminalFailure
// names, shared with the drivers' fail-fast rules (cancelled is
// concurrency noise, not the behaviour under test) — and that its logs
// contain needle. A run that failed before any job
// started (startup_failure) has no logs to assert on, so the step fails
// fast saying that instead of spending the log-poll window on them.
// Unusable logs are fetched again until logPollWindow
// passes (fetchUsableSubjectLogs, shared with the harness log steps). When
// the text is missing, the error says whether the logs could not be
// fetched, were empty or were incomplete, so a log-fetch problem does not
// read as the run lacking the text. The fetched logs are saved as a debug
// artifact (saveFetchedWorkflowRunLogs), so even a passing scenario leaves
// a log trail. The run is recorded in w.WorkflowRun as the dispatched
// workflow's run, not a harness agent run, and the step re-arms the
// fail-fast (w.DispatchedWorkflow) when it records it: harness steps
// must not follow it in the same scenario without re-resolving the harness
// run (a harness wait step such as "the harness … workflow completes
// successfully" does that).
func thenWorkflowFailsLogging(ctx context.Context, w *world.World, file, needle string) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before asserting the workflow's failure")
	}
	// The step pairs with the dispatch step: without one there is no
	// dispatched run to wait for, and the whole-scenario window would
	// burn the detection budget blaming GitHub. The recorded file (never
	// cleared, unlike the fail-fast flag) keeps this check correct after
	// a harness wait re-resolved the harness run; a failure assertion
	// naming a different file than the one dispatched is an authoring
	// mistake to fail fast on, not a detection window to burn. Both
	// sides are base-normalized because the dispatch step records the
	// file as written while the driver matches on its base name.
	if w.DispatchedWorkflowFile == "" {
		return fmt.Errorf("no workflow was dispatched in this scenario; dispatch the workflow before asserting its failure")
	}
	if filepath.Base(w.DispatchedWorkflowFile) != filepath.Base(file) {
		return fmt.Errorf("no %q workflow was dispatched in this scenario; %q was", file, w.DispatchedWorkflowFile)
	}
	// The shared guard in checkRunLogsNeedle would catch a blank needle
	// only after this step's wait — the largest budget in the suite —
	// so reject it here, before the run is resolved.
	if err := rejectBlankNeedle(needle); err != nil {
		return err
	}
	d, err := workflowDispatcher(w)
	if err != nil {
		return err
	}
	// The run's created_at comes from GitHub's clock, so the window
	// opens issueOpenDrainSkewBuffer before the dispatch time to
	// tolerate a runner clock ahead of it. The dispatch step records
	// that time beside the file (never cleared, unlike the flag): a
	// trigger step between the dispatch and this assertion moves
	// ScenarioStart forward and must not exclude the dispatched run.
	// The run-ID floor recorded with it accepts only runs above the
	// floor: a run of the file already listed before the dispatch
	// cannot become the asserted run, and a
	// cancelled dispatched run is returned as final rather than followed
	// to a replacement another caller started.
	after := w.DispatchedWorkflowAt.Add(-issueOpenDrainSkewBuffer)
	run, err := d.WaitForWorkflowConclusion(ctx, w.Org, w.RepoName, file, after, workflowDispatchEvent, w.DispatchedWorkflowRunFloor)
	if err != nil {
		return err
	}
	w.WorkflowRun = run
	// The recorded run is the dispatched caller workflow's run, so the
	// step maintains the fail-fast guard itself: a harness step that
	// follows it must re-resolve the harness run again.
	w.DispatchedWorkflow = true
	if !ci.IsTerminalFailure(run.Conclusion) {
		return fmt.Errorf("workflow %q run %d concluded with %q; want a failure (url=%s)", file, run.ID, run.Conclusion, run.HTMLURL)
	}
	if run.Conclusion == "startup_failure" {
		return fmt.Errorf("workflow %q run %d failed before any job started (concluded %q), so it has no logs (url=%s)", file, run.ID, run.Conclusion, run.HTMLURL)
	}
	// The subject label matches the harness log steps' form ("harness %q")
	// so one error-message family reads the same way in both callers. The
	// saved debug artifact keeps the same label: no other step saves this
	// run, so its directory needs no agent-style name.
	label := fmt.Sprintf("workflow %q", file)
	logs, fetchErr, stopped := fetchUsableSubjectLogs(ctx, w, label, label, needle, run)
	_, checkErr := checkRunLogsNeedle(run, label, needle, logs, fetchErr)
	if stopped != nil {
		return fmt.Errorf("%w (stopped waiting for the logs: %w)", checkErr, stopped)
	}
	return checkErr
}
