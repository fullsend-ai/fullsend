package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/harness"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/statuscomment"
	"github.com/fullsend-ai/fullsend/internal/steerwatch"
	"github.com/fullsend-ai/fullsend/internal/tracker"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// steerTokenMargin is how long before the forge token expires the watcher
// must stop absorbing updates. The stage mints a GitHub App installation
// token at job start; those live one hour and the runner has no refresher
// for them, so a run that keeps steering past this point would finish
// holding a token it can no longer post with.
const steerTokenMargin = 10 * time.Minute

// steerTokenLife is the life of the App installation token the stage minted.
const steerTokenLife = time.Hour

// steerTurnEndBuffer is the depth of the turn-end channel. The runtime's
// stream-parser goroutine must never block on the watcher, so sends are
// non-blocking; a depth this far above the steer cap means a turn end is
// only ever dropped in situations the deadline already covers.
const steerTurnEndBuffer = 16

// steerItemReader builds the forge client the watcher reads the work item
// with, and steerMarkerClient the one the skip check reads the timeline
// with. Both are variables so tests can substitute a stub; production always
// gets the live GitHub client holding the minted role token.
var (
	// steerActionsReader reads the execution platform's run records with the
	// JOB token — the GH_TOKEN the action passed in, which is the token
	// every stage job already grants `actions: write`.
	steerActionsReader = func(token string) steerwatch.ActionsReader { return newSteerGitHubClient(token) }
	// steerItemReader and steerMarkerClient read the work item with the
	// minted role token.
	steerItemReader   = func(token string) steerwatch.ItemReader { return newSteerGitHubClient(token) }
	steerMarkerClient = func(token string) steerMarkerReader { return newSteerGitHubClient(token) }
)

// newSteerGitHubClient builds the forge client the steer paths use. It
// honours GITHUB_API_URL, which every Actions runner sets and which differs
// on GitHub Enterprise Server, so the root is read rather than assumed.
func newSteerGitHubClient(token string) *github.LiveClient {
	c := github.New(token)
	if base := os.Getenv("GITHUB_API_URL"); base != "" {
		c = c.WithBaseURL(base)
	}
	return c
}

// steerSession is the watcher wiring for one agent iteration: the watcher
// itself, the channel the runtime's turn ends arrive on, and the goroutine
// running the loop.
type steerSession struct {
	watcher *steerwatch.Watcher
	turnEnd chan struct{}
	done    chan struct{}
}

// steerOpts is everything the runner knows that the watcher needs.
type steerOpts struct {
	harness *harness.Harness
	// runtime is the selected runtime; steering happens only when it
	// implements agentruntime.Steerer.
	runtime     agentruntime.Runtime
	sandboxName string
	// forgePlatform gates the watcher to GitHub: GitLab pipelines queue
	// rather than cancel, so the same wiring there is a later step.
	forgePlatform string
	statusRepo    string
	statusNum     int
	// jobToken is the GH_TOKEN the action passed in, captured before the
	// runner swapped in the minted role token. It reads the Actions API.
	jobToken string
	// roleToken is the minted role token; it reads the work item.
	roleToken string
	runStart  time.Time
	// headSHA is the work item's head at run start when the environment
	// knows it. The watcher resolves it from the forge when it is empty,
	// along with whether the item is a pull request at all.
	headSHA string
	// timeout is the agent's own budget, which bounds the watch alongside
	// the forge token's life.
	timeout time.Duration
	// seen and baseline seed the watcher on a validation-loop retry, so a
	// later iteration neither re-examines runs the previous one judged nor
	// rebuilds its first delta from the run's start.
	seen     []int64
	baseline time.Time
	printer  *ui.Printer
}

// steerEligible reports why steering cannot run even though the harness
// asked for it, or "" when it can. Callers check SteerEnabled first: a
// harness that never opted in is not "blocked", it is off.
//
// Steering needs a runtime that can take a message into a running session
// and a GitHub Actions job to watch follow-up runs in.
func steerEligible(o steerOpts) string {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return "not running in GitHub Actions"
	}
	if o.forgePlatform == "gitlab" {
		return "GitLab pipelines queue rather than cancel; the watcher is GitHub-only for now"
	}
	if _, ok := o.runtime.(agentruntime.Steerer); !ok {
		return fmt.Sprintf("runtime %q cannot take a message into a running session", o.runtime.Name())
	}
	if o.statusRepo == "" || o.statusNum <= 0 {
		return "no work item to watch"
	}
	if o.jobToken == "" {
		return "no job token to read the Actions API with"
	}
	if steerRunID() == 0 {
		return "GITHUB_RUN_ID is not set"
	}
	if !preserveRunsEnabled() {
		// harness-reference.md documents this as one of the three
		// conditions steering needs, and promises the runner prints why it
		// declined. Steering a run that is about to be cancelled is the
		// mixed state ADR 0101 calls worse than today: the run absorbs an
		// update, is cancelled anyway, and the queued run repeats the work
		// with no receipt to skip on.
		return "FULLSEND_PRESERVE_RUNS is not \"true\" on this repository, so this run would be cancelled rather than steered"
	}
	return ""
}

// preserveRunsEnabled reports whether the repository set the variable that
// stops stage jobs cancelling a run in progress. The workflow passes it
// into the runner's own step: `vars` is not otherwise visible here, and
// FULLSEND_REPO_VARS reaches the sandbox environment rather than this
// process.
func preserveRunsEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("FULLSEND_PRESERVE_RUNS")), "true")
}

// steerRunID returns this job's workflow run id, or 0 when it is unset or
// unparseable.
func steerRunID() int64 {
	id, err := strconv.ParseInt(os.Getenv("GITHUB_RUN_ID"), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// steerRunName is the shim's run-name for a work item. It binds follow-up
// runs whose event carries no pull_requests[] (issue_comment, issues).
func steerRunName(repo string, number int) string {
	if repo == "" || number <= 0 {
		return ""
	}
	return fmt.Sprintf("%s#%d", repo, number)
}

// steerDeadline bounds the watch at the earlier of the agent's own budget
// and the point where the forge token is about to expire.
func steerDeadline(runStart time.Time, timeout time.Duration) time.Time {
	return runStart.Add(steerBudget(timeout))
}

// steerBudget is the wall clock a steered run actually gets: the agent's own
// budget, clipped to what is left of the forge token's life.
//
// This is the single source for the bound. steerDeadline turns it into the
// instant the watcher and the run context stop at, and steerAwareTimeout
// hands it to the timeout detection, so a change to the cap reaches both
// without a matching edit somewhere else.
func steerBudget(timeout time.Duration) time.Duration {
	if tokenBudget := steerTokenLife - steerTokenMargin; timeout > tokenBudget {
		return tokenBudget
	}
	return timeout
}

// steerAwareBudget returns the pair the timeout detection must compare: how
// long the run had been going, and the budget that would have killed it.
//
// The two are returned together because they have to share a clock. A
// steered run is killed at steerDeadline(runStartedAt, timeout) — anchored
// at the START OF THE RUN, because the bound comes from the forge token's
// life and kills the whole run rather than an iteration. Measuring a
// per-iteration elapsed against that whole-run budget compares two different
// clocks: with a 90 minute timeout, a 50 minute budget and 10 minutes of
// setup, the agent is killed having itself run only 40 minutes, 40 does not
// reach nine tenths of 50, and the run reports success with the work
// unfinished. Later iterations are worse, their anchor being later still.
//
// The unsteered pair is per-iteration elapsed against the harness timeout,
// unchanged: that is every run in production.
func steerAwareBudget(steered bool, runStartedAt, now time.Time, iterElapsed, timeout time.Duration) (elapsed, budget time.Duration) {
	if !steered {
		return iterElapsed, timeout
	}
	return now.Sub(runStartedAt), steerBudget(timeout)
}

// steerAwareTimeout returns the budget a finished run was bounded by, which
// is what "did it run out of clock" has to be measured against.
//
// For an unsteered run that is the harness timeout, unchanged. For a steered
// one it is steerBudget, which can be shorter — and when it is, a run killed
// at the budget does not reach nine tenths of the harness timeout, so the
// detection reads it as "finished early" rather than "ran out of clock".
// With a validation loop that surfaces as "validation failed"; without one
// the run returns nil and reports success, having been cut off mid-work.
//
// Not a regression from #7042: the divergence is steerDeadline capping below
// the harness timeout, and #7042 only made it visible by giving the timeout
// branch precedence over the validation-failed branch.
func steerAwareTimeout(timeout time.Duration, steered bool) time.Duration {
	if !steered {
		return timeout
	}
	return steerBudget(timeout)
}

// startSteerWatcher builds and starts the follow-up run watcher. It returns
// nil when steering is off or unavailable — the caller then leaves
// RunParams.Steerable false and today's single-turn Run is unchanged.
//
// Steer and Settle are called under sandboxMu: both write into the sandbox
// (a mailbox append, or on Codex the stray-process sweep that interrupts the
// turn) and would otherwise race the credential refreshers run.go already
// serializes through that lock. The lock lives here, in the CLI layer, which
// is why the runtime cannot take it itself.
func startSteerWatcher(ctx context.Context, o steerOpts) *steerSession {
	if !o.harness.SteerEnabled() {
		return nil
	}
	if reason := steerEligible(o); reason != "" {
		o.printer.StepWarn("Steering disabled: " + reason)
		return nil
	}

	steerer, ok := o.runtime.(agentruntime.Steerer)
	if !ok {
		return nil
	}

	deliver := func(ctx context.Context, msg agentruntime.SteerMessage) error {
		return withSandboxLock(ctx, func(waited time.Duration) {
			o.printer.StepInfo(fmt.Sprintf(
				"Waiting %s for a credential refresh to finish before steering the agent", waited))
		}, func() error {
			return steerer.Steer(ctx, o.sandboxName, msg)
		})
	}
	settle := func(ctx context.Context) error {
		return withSandboxLock(ctx, nil, func() error {
			return steerer.Settle(ctx, o.sandboxName)
		})
	}

	w := steerwatch.New(steerwatch.Config{
		Repo:          o.statusRepo,
		RunID:         steerRunID(),
		RunName:       steerRunName(o.statusRepo, o.statusNum),
		StartedAt:     o.runStart,
		Deadline:      steerDeadline(o.runStart, o.timeout),
		MaxSteers:     o.harness.SteerMaxSteers(),
		PollInterval:  o.harness.SteerPollInterval(),
		DeltaBaseline: o.baseline,
		AlreadySeen:   o.seen,
		Item: steerwatch.WorkItem{
			Number:  o.statusNum,
			HeadSHA: o.headSHA,
		},
	}, steerActionsReader(o.jobToken), steerItemReader(o.roleToken), deliver, settle)

	w.SetLogFunc(func(format string, args ...any) {
		o.printer.StepInfo(fmt.Sprintf(format, args...))
	})
	w.SetWarnFunc(func(format string, args ...any) {
		o.printer.StepWarn(fmt.Sprintf(format, args...))
	})

	if err := w.Start(ctx, o.harness.Slug); err != nil {
		o.printer.StepWarn("Steering disabled: " + err.Error())
		return nil
	}

	sess := &steerSession{
		watcher: w,
		turnEnd: make(chan struct{}, steerTurnEndBuffer),
		done:    make(chan struct{}),
	}
	go func() {
		defer close(sess.done)
		w.Watch(ctx, sess.turnEnd)
	}()
	o.printer.StepDone(fmt.Sprintf("Watching for work-item updates (max %d steers, polling every %s)",
		o.harness.SteerMaxSteers(), o.harness.SteerPollInterval()))
	return sess
}

// notifyTurnEnd tells the watcher the agent finished a turn. It never
// blocks: it runs on the runtime's stream-parser goroutine.
//
// A steer consumed mid-turn produces no turn end of its own — Claude absorbs
// it after the tool result and before the turn's result event — so turn ends
// are a settle signal, never a steer count.
func (s *steerSession) notifyTurnEnd() {
	if s == nil {
		return
	}
	select {
	case s.turnEnd <- struct{}{}:
	default:
	}
}

// stop ends the watch and waits for the loop to settle the session.
func (s *steerSession) stop() {
	if s == nil {
		return
	}
	close(s.turnEnd)
	<-s.done
}

// marker returns what the run absorbed, for the terminal status comment.
//
// Only steers the runtime acknowledged count. Steer returning means the
// message was handed over, not that the agent received it: the live runtimes
// ack afterwards (Claude's replay echo, pi's response) and Codex when the
// resumed process starts, and the runtime records each ack in
// RunMetrics.Steers. If the runtime dies between the hand-off and the ack, a
// marker built from attempts would tell the queued run its work was already
// done and the update would be lost outright — so an unacknowledged delivery
// is left out and the queued run does the work.
func (s *steerSession) marker(acked []agentruntime.SteerResult) statuscomment.SteerMarker {
	if s == nil {
		return statuscomment.SteerMarker{}
	}
	return steerMarkerFrom(s.watcher.Delivered(), s.watcher.Head(), acked)
}

// steerMarkerFrom intersects what the watcher handed to the runtime with what
// the runtime acknowledged. One message can carry several follow-up runs,
// since a poll folds simultaneous candidates together, so the ack for a
// message id vouches for its whole batch.
func steerMarkerFrom(delivered []steerwatch.DeliveredSteer, head string, acked []agentruntime.SteerResult) statuscomment.SteerMarker {
	ackedIDs := make(map[int64]bool, len(acked))
	for _, r := range acked {
		ackedIDs[r.FollowUpRunID] = true
	}

	var consumed []int64
	for _, batch := range delivered {
		if !ackedIDs[batch.MessageID] {
			continue
		}
		consumed = append(consumed, batch.RunIDs...)
	}
	return statuscomment.SteerMarker{ConsumedRunIDs: consumed, HeadSHA: head}
}

// seenRunIDs returns every follow-up run the watcher judged, for the next
// validation-loop iteration.
func (s *steerSession) seenRunIDs() []int64 {
	if s == nil {
		return nil
	}
	return s.watcher.SeenRunIDs()
}

// baseline returns the window the next iteration's watcher should compute
// its first delta from.
func (s *steerSession) baseline() time.Time {
	if s == nil {
		return time.Time{}
	}
	return s.watcher.Baseline()
}

// steerTurnEndHandler wraps an event handler so agent turn ends reach the
// watcher. ResultEvent is the runtime-neutral turn end: Claude's `result`,
// pi's `agent_end` and Codex's `turn.completed` all normalize to it.
func steerTurnEndHandler(inner func(agentruntime.AgentEvent), sess *steerSession) func(agentruntime.AgentEvent) {
	if sess == nil {
		return inner
	}
	return func(evt agentruntime.AgentEvent) {
		if inner != nil {
			inner(evt)
		}
		switch evt.(type) {
		case agentruntime.ResultEvent, *agentruntime.ResultEvent:
			sess.notifyTurnEnd()
		}
	}
}

// steerMarkerReader is the forge read surface the skip check needs.
// github.LiveClient satisfies it.
type steerMarkerReader interface {
	GetAuthenticatedUser(ctx context.Context) (string, error)
	ListIssueComments(ctx context.Context, owner, repo string, number int) ([]forge.IssueComment, error)
}

// steerAlreadyHandled reports whether an earlier run already absorbed the
// follow-up run that dispatched me (ADR 0101).
//
// Dispatch is never suppressed while a run is in flight: a route arm that
// skipped whenever something was running would lose a steer that lands after
// the in-flight run's last check. Instead the in-flight run records what it
// consumed on its terminal status comment, and this check reads it. Worst
// case is one short redundant run.
//
// It fails open in every direction — no marker, an unreadable timeline, an
// unresolvable App login — because a false "already handled" silently drops
// the work, while a false "not handled" costs one run.
func steerAlreadyHandled(ctx context.Context, c steerMarkerReader, repo string, number int, myRunID int64) (bool, error) {
	if c == nil || myRunID == 0 || number <= 0 {
		return false, nil
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return false, fmt.Errorf("status repo %q is not in owner/repo form", repo)
	}

	// The marker means nothing unless the App wrote it: any user can paste
	// the HTML into a comment of their own.
	appLogin, err := c.GetAuthenticatedUser(ctx)
	if err != nil {
		return false, fmt.Errorf("resolving the app login: %w", err)
	}

	comments, err := c.ListIssueComments(ctx, owner, name, number)
	if err != nil {
		return false, fmt.Errorf("listing comments on %s#%d: %w", repo, number, err)
	}

	tcomments := make([]tracker.Comment, 0, len(comments))
	for _, cm := range comments {
		tcomments = append(tcomments, tracker.Comment{
			ID:        strconv.Itoa(cm.ID),
			Body:      tracker.Body(cm.Body),
			Author:    cm.Author,
			CreatedAt: cm.CreatedAt,
		})
	}

	marker, found := statuscomment.LatestSteerMarker(tcomments, appLogin)
	if !found {
		return false, nil
	}
	return marker.Consumed(myRunID), nil
}

// checkSteerAlreadyHandled runs the skip check and reports whether this run
// should exit without starting the agent. A failure is warned about and
// treated as "not handled".
func checkSteerAlreadyHandled(ctx context.Context, o steerOpts) bool {
	if !o.harness.SteerEnabled() || os.Getenv("GITHUB_ACTIONS") != "true" {
		return false
	}
	if o.forgePlatform == "gitlab" || o.statusRepo == "" || o.statusNum <= 0 || o.roleToken == "" {
		return false
	}
	handled, err := steerAlreadyHandled(ctx, steerMarkerClient(o.roleToken), o.statusRepo, o.statusNum, steerRunID())
	if err != nil {
		o.printer.StepWarn("Could not check whether this update was already handled: " + err.Error())
		return false
	}
	return handled
}

// steerMarkerForStatus returns the marker to write on the terminal status
// comment for a run that ended with the given status.
//
// The marker is a receipt for work the agent finished, so it rides only on a
// successful run. A run that absorbed an update and then failed, timed out,
// was cancelled, or was skipped produced no output for it — and a marker
// there would tell the run queued behind it to skip work nobody did, losing
// the update. Validation failure arrives here as a "failure" status, because
// an unvalidated run returns an error, so it is covered by the same rule.
func steerMarkerForStatus(status string, m statuscomment.SteerMarker) statuscomment.SteerMarker {
	if status != "success" {
		return statuscomment.SteerMarker{}
	}
	return m
}

// mergeSteerMarkers unions two markers, keeping the later head.
//
// The validation loop runs one watcher per iteration, and each reports only
// what it absorbed. Replacing the marker per iteration would drop the
// receipts an earlier iteration earned the moment a later one absorbed
// nothing, and the run queued behind would redo work already done.
func mergeSteerMarkers(prev, next statuscomment.SteerMarker) statuscomment.SteerMarker {
	out := statuscomment.SteerMarker{HeadSHA: prev.HeadSHA}
	if next.HeadSHA != "" {
		out.HeadSHA = next.HeadSHA
	}
	seen := make(map[int64]bool, len(prev.ConsumedRunIDs)+len(next.ConsumedRunIDs))
	for _, ids := range [][]int64{prev.ConsumedRunIDs, next.ConsumedRunIDs} {
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			out.ConsumedRunIDs = append(out.ConsumedRunIDs, id)
		}
	}
	return out
}
