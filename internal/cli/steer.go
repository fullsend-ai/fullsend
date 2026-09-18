package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/repos"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/steerwatch"
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

// steerActionsReaderFn builds the forge client the watcher lists follow-up
// runs with, and steerItemReaderFn the one it reads the work item with.
// Production gives the first the job token and the second the minted role
// token. Override in tests to return a stub instead of a live client.
var (
	// steerActionsReaderFn reads the execution platform's run records with the
	// JOB token — the GH_TOKEN the action passed in, which is the token
	// every stage job already grants `actions: write`.
	steerActionsReaderFn = func(token string) steerwatch.ActionsReader { return newGitHubLiveClient(token, "") }
	// steerItemReaderFn reads the work item with the minted role token.
	steerItemReaderFn = func(token string) steerwatch.ItemReader { return newGitHubLiveClient(token, "") }
	// steerSelfLoginFn resolves the login a token posts under; the watcher
	// needs this run's own logins to tell its own output from an update.
	// Override in tests to answer without a forge.
	steerSelfLoginFn = func(token string) steerSelfLoginReader { return newGitHubLiveClient(token, "") }
)

// steerSelfLoginReader resolves the login a credential posts under. Narrow
// so a test can answer it without a forge.
type steerSelfLoginReader interface {
	GetAuthenticatedUser(ctx context.Context) (string, error)
}

// resolveSteerSelfLogins returns every login this run's own output can appear
// under: the one the role token posts as, plus the review Apps that comment on
// the same work item.
//
// Resolved before the run does anything, not while the watcher starts: a run
// that cannot name its own output must not steer at all, since the post-fix
// and post-code comments carry no marker and it would steer itself with them.
// An error is an environment defect, not a reason to carry on with a shorter
// list.
func resolveSteerSelfLogins(ctx context.Context, roleToken, repo string) ([]string, error) {
	owner, _, ok := strings.Cut(repo, "/")
	if !ok || owner == "" {
		return nil, fmt.Errorf("cannot read the owner out of %q", repo)
	}
	login, err := steerSelfLoginFn(roleToken).GetAuthenticatedUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving the login this run posts under: %w", err)
	}
	if login == "" {
		return nil, errors.New("the forge reported no login for the role token")
	}
	return append([]string{login}, steerwatch.ReviewBotLogins(owner)...), nil
}

// steerSession is the watcher wiring for one agent iteration: the watcher
// itself, the channel the runtime's turn ends arrive on, and the goroutine
// running the loop.
type steerSession struct {
	watcher *steerwatch.Watcher
	turnEnd chan time.Time
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
	// selfLogins are the logins this run's own output is posted under,
	// resolved before bootstrap by resolveSteerSelfLogins. Empty means the
	// run could not name its own output, which is an eligibility defect.
	selfLogins []string
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
	observed []forge.WorkflowRun
	baseline time.Time
	// priorSteers is how many steers earlier iterations of this run already
	// spent. max_steers is a per-run cap (ADR 0113) and each iteration
	// builds its own watcher, so without carrying this the cap would reset
	// every iteration.
	priorSteers int
	printer     *ui.Printer
}

// steerDecline is why steering cannot run on this run, and whether an
// operator needs to hear about it.
//
// Most declines are ordinary: a local run is not in GitHub Actions, a GitLab
// run queues instead of steering, a runtime that cannot take a message never
// could, and a run dispatched against no work item has nothing to watch.
// Announcing those once per iteration would be noise.
//
// An incomplete environment is not ordinary: a job token that failed to reach
// the step, a run id that never got exported, or a login the run cannot
// resolve. Those are announced whatever the harness says, because suppressing
// them would hide a plumbing regression behind silence.
type steerDecline struct {
	reason string
	defect bool
}

// ok reports whether steering may run. The zero steerDecline means eligible,
// so callers ask this rather than comparing reason against the empty string.
func (d steerDecline) ok() bool { return d.reason == "" }

// steerEligible reports why steering cannot run even though the harness has
// it on, or a zero steerDecline when it can. Callers check SteerEnabled first:
// a harness that opted out is not "blocked", it is off.
//
// Steering needs a runtime that can take a message into a running session
// and a GitHub Actions job to watch follow-up runs in.
func steerEligible(o steerOpts) steerDecline {
	if d := steerPreflight(o); !d.ok() {
		return d
	}
	if len(o.selfLogins) == 0 {
		return steerDecline{
			reason: "this run cannot resolve the login its own output is posted under",
			defect: true,
		}
	}
	return steerDecline{}
}

// steerPreflight is the half of the ladder that reads only the environment,
// so the runner can ask whether steering is worth resolving an identity for
// before it spends an API call doing so. Kept separate from steerEligible
// because the identity check needs that call's result.
func steerPreflight(o steerOpts) steerDecline {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return steerDecline{reason: "not running in GitHub Actions"}
	}
	if o.forgePlatform == repos.ForgeGitLab {
		return steerDecline{reason: "GitLab pipelines queue rather than cancel; the watcher is GitHub-only for now"}
	}
	// Guarded because the runner asks this before the runtime is resolved,
	// to decide whether steering is worth an identity lookup: o.runtime is a
	// nil interface there, and building the decline message would dereference
	// it. steerEligible asks again per iteration, where the runtime is set,
	// and startSteerWatcher asserts Steerer before using it.
	if o.runtime != nil {
		if _, ok := o.runtime.(agentruntime.Steerer); !ok {
			return steerDecline{reason: fmt.Sprintf("runtime %q cannot take a message into a running session", o.runtime.Name())}
		}
	}
	if o.statusRepo == "" || o.statusNum <= 0 {
		return steerDecline{reason: "no work item to watch"}
	}
	if o.jobToken == "" {
		return steerDecline{reason: "no job token to read the Actions API with", defect: true}
	}
	if steerRunID() == 0 {
		return steerDecline{reason: "GITHUB_RUN_ID is not set", defect: true}
	}
	return steerDecline{}
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
// instant the watcher and the run context stop at, and steerAwareBudget
// hands it to the timeout detection, so a change to the cap reaches both
// without a matching edit somewhere else.
func steerBudget(timeout time.Duration) time.Duration {
	if tokenBudget := steerTokenLife - steerTokenMargin; timeout > tokenBudget {
		return tokenBudget
	}
	return timeout
}

// iterationEnvBudget returns the timeout and deadline the iteration env
// advertises to the sandbox: what the agent is told it has.
//
// It must be the bound the run is actually killed at. A steered run is
// killed at steerDeadline anchored at the START of the run, and steerBudget
// clips the harness timeout to what is left of the forge token's life — so
// the unsteered pair would promise time the run will not get. Both values
// come off the same helpers steerDeadline gives the run context and
// steerAwareBudget gives the timeout detection, so the three cannot drift.
//
// The unsteered pair is the harness minutes and agentStart+timeout,
// unchanged: that is every run in production today.
func iterationEnvBudget(steered bool, harnessMinutes int, runStartedAt, agentStart time.Time, timeout time.Duration) (int, time.Time) {
	if !steered {
		return harnessMinutes, agentStart.Add(timeout)
	}
	return int(steerBudget(timeout).Minutes()), steerDeadline(runStartedAt, timeout)
}

// heartbeatBudget is the countdown the console heartbeat reports: the
// distance from the agent's start to the deadline the sandbox was just told
// and the run context is bounded by.
//
// It takes the deadline rather than the harness timeout for the same reason
// iterationEnvBudget returns one — a steered iteration is killed at
// steerDeadline, anchored at the RUN's start and clipped to the forge
// token's life, so counting down to agentStart+timeout reports time the run
// will not get. Unsteered, iterationEnvBudget's deadline is exactly
// agentStart.Add(timeout), so this is the harness timeout unchanged.
//
// iterationEnvBudget's minutes are the wrong value to pass: they are whole
// minutes measured from the run's start, while the heartbeat measures
// elapsed from agentStart, so the setup time would be counted twice.
func heartbeatBudget(agentStart, deadline time.Time) time.Duration {
	return deadline.Sub(agentStart)
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
	if d := steerEligible(o); !d.ok() {
		// An ordinary decline is announced only to a harness that named
		// steering itself; an environment defect is announced to everyone.
		// See steerDecline for which is which and why.
		if d.defect || o.harness.SteerExplicitlyEnabled() {
			o.printer.StepWarn("Steering disabled: " + d.reason)
		}
		return nil
	}

	// steerPreflight makes the same assertion, but only when o.runtime is
	// set: it is called before the runtime is resolved, so it skips the check
	// rather than dereferencing a nil interface. This one therefore still
	// stands between a nil or non-steerable runtime and the Steer calls
	// below, and it announces rather than returning a silent nil, because an
	// eligible run reaching here without a Steerer is a wiring fault and not
	// an ordinary decline.
	steerer, ok := o.runtime.(agentruntime.Steerer)
	if !ok {
		o.printer.StepWarn("Steering disabled: the resolved runtime cannot take a message into a running session")
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
		Repo:            o.statusRepo,
		RunID:           steerRunID(),
		RunName:         steerRunName(o.statusRepo, o.statusNum),
		SelfLogins:      o.selfLogins,
		StartedAt:       o.runStart,
		Deadline:        steerDeadline(o.runStart, o.timeout),
		MaxSteers:       o.harness.SteerMaxSteers(),
		PollInterval:    o.harness.SteerPollInterval(),
		DeltaBaseline:   o.baseline,
		AlreadySeen:     o.seen,
		AlreadyObserved: o.observed,
		PriorSteers:     o.priorSteers,
		Item: steerwatch.WorkItem{
			Number:  o.statusNum,
			HeadSHA: o.headSHA,
		},
	}, steerActionsReaderFn(o.jobToken), steerItemReaderFn(o.roleToken), deliver, settle)

	w.SetLogFunc(func(format string, args ...any) {
		o.printer.StepInfo(fmt.Sprintf(format, args...))
	})
	w.SetWarnFunc(func(format string, args ...any) {
		o.printer.StepWarn(fmt.Sprintf(format, args...))
	})

	// GITHUB_JOB (review, fix, ...) names a stage job; the slug names a
	// matrix agent's job.
	if err := w.Start(ctx, os.Getenv("GITHUB_JOB"), o.harness.Slug); err != nil {
		o.printer.StepWarn("Steering disabled: " + err.Error())
		return nil
	}

	sess := &steerSession{
		watcher: w,
		turnEnd: make(chan time.Time, steerTurnEndBuffer),
		done:    make(chan struct{}),
	}
	go func() {
		defer close(sess.done)
		w.Watch(ctx, sess.turnEnd)
	}()
	remaining := max(0, o.harness.SteerMaxSteers()-o.priorSteers)
	o.printer.StepDone(fmt.Sprintf("Watching for work-item updates (%d of %d steers left, polling every %s)",
		remaining, o.harness.SteerMaxSteers(), o.harness.SteerPollInterval()))
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
	case s.turnEnd <- time.Now():
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

// seenRunIDs returns every follow-up run the watcher judged, for the next
// validation-loop iteration.
// observedRuns carries the previous iteration's observed runs into the next
// watcher, alongside seenRunIDs. See Config.AlreadyObserved.
func (s *steerSession) observedRuns() []forge.WorkflowRun {
	if s == nil {
		return nil
	}
	return s.watcher.ObservedRuns()
}

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

// steers returns the run's cumulative steer count, which the next
// iteration's watcher is seeded with so max_steers caps the run rather
// than each iteration of it.
func (s *steerSession) steers() int {
	if s == nil {
		return 0
	}
	return s.watcher.Steers()
}

// steerTurnEndHandler wraps an event handler so agent turn ends reach the
// watcher. ResultEvent is the runtime-neutral turn end: Claude's `result`,
// pi's `agent_settled` (on a steerable run) and Codex's `turn.completed` all
// normalize to it.
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
