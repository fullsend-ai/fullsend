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

// steerReceiptTimeout bounds the receipt post. It runs after the agent is
// done, on a context detached from the run's, so it needs its own bound.
const steerReceiptTimeout = 15 * time.Second

// steerActionsReaderFn builds the forge client the watcher lists follow-up
// runs with, steerItemReaderFn the one it reads the work item with, and
// steerMarkerClientFn and steerReceiptClientFn the ones the skip check and
// the receipt writer use. Production gives each the token its call site is
// entitled to. Override in tests to return a stub instead of a live client.
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
	// steerMarkerClientFn reads the timeline for the skip check, and
	// steerReceiptClientFn writes the receipt. Both take the JOB token: the
	// receipt is authenticated by its author, and the job token's identity
	// is the one nothing inside the sandbox can post as.
	steerMarkerClientFn  = func(token string) steerMarkerReader { return newGitHubLiveClient(token, "") }
	steerReceiptClientFn = func(token string) steerReceiptWriter { return newGitHubLiveClient(token, "") }
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
	// runParams carries what a SteerDecliner reads: the sandbox, the model,
	// its fallbacks and the config's aliases.
	runParams agentruntime.RunParams
	// forgePlatform gates the watcher to GitHub: GitLab pipelines queue
	// rather than cancel, so the same wiring there is a later step.
	forgePlatform string
	statusRepo    string
	statusNum     int
	// jobToken is the GH_TOKEN the action passed in, captured before the
	// runner swapped in the minted role token. It reads the Actions API.
	jobToken string
	// receiptToken is the credential the receipt is written and read under:
	// jobToken, but only when minting swapped GH_TOKEN for a role token, and
	// empty otherwise (steerReceiptToken).
	//
	// Separate from jobToken because reading the Actions API with a
	// credential the post-script also holds costs nothing — those reads
	// prove no authorship. Only the receipt's identity has to be unreachable.
	receiptToken string
	// selfLogins are the logins this run's own output is posted under,
	// resolved before bootstrap by resolveSteerSelfLogins. Empty means the
	// run could not name its own output, which is an eligibility defect.
	selfLogins []string
	// roleToken is the minted role token; it reads the work item.
	roleToken string
	// providersDir is the .fullsend providers/ directory this run provisions
	// provider credentials from. The skip check scans every definition in it
	// (steerReceiptsHonoured).
	providersDir string
	runStart     time.Time
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
	// oidcURL and oidcAuth are the GHA OIDC endpoint and its credential when
	// the run refreshes a WIF token (empty otherwise). A codex steer
	// refreshes the token before interrupting the turn (see steerDeliver).
	oidcURL  string
	oidcAuth string
	printer  *ui.Printer
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
//
// A runtime's own refusal is announced too, without being a defect: it
// follows from the run's configuration, such as a pi model chain that can
// fall back, and the operator should learn why that run was not steered.
type steerDecline struct {
	reason   string
	defect   bool
	announce bool
}

// announced reports whether the operator hears about this decline. An
// ordinary decline reaches only a harness that named steering itself; an
// environment defect or a runtime's own refusal reaches everyone.
func (d steerDecline) announced(h *harness.Harness) bool {
	return d.defect || d.announce || h.SteerExplicitlyEnabled()
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
	// Asked here rather than left to Steer, so a run the runtime would
	// refuse never starts a watcher that polls and spends a steer slot to
	// find out.
	if d, ok := o.runtime.(agentruntime.SteerDecliner); ok {
		if reason, declined := d.SteerDeclineReason(o.runParams); declined {
			return steerDecline{reason: reason, announce: true}
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

// exportIterationEnv tells the sandbox this iteration's budget and whether it
// is watched, and settles what the rest of the iteration runs under: the
// session (nil once dropped), whether the run is now steered, and the
// deadline to enforce.
//
// The deadline is advisory, and a stale one from the previous iteration is
// the only harmful state, so a failed write is cleared and the run goes on.
// FULLSEND_STEER_ACTIVE goes out in that same write, so after a failure the
// sandbox no longer advertises it; the agent definitions treat the
// envelope's opening line as an injection attempt while it is unset, so a
// steer delivered now would be distrusted on arrival and still spend one of
// max_steers. This iteration's watcher is stopped instead, and the run
// counts as steered only if an earlier iteration was, with the deadline to
// match.
//
// An error means even the clear failed. The watcher is stopped by then, so
// it is not left polling for a run that never starts.
func exportIterationEnv(exec sandboxExecFunc, sandboxName string, sess *steerSession, steeredBefore bool,
	harnessMinutes int, runStartedAt, agentStart time.Time, timeout time.Duration, traceparent string, printer *ui.Printer,
) (*steerSession, bool, time.Time, error) {
	steered := steeredBefore || sess != nil
	envTimeout, envDeadline := iterationEnvBudget(steered, harnessMinutes, runStartedAt, agentStart, timeout)
	err := writeIterationEnv(exec, sandboxName, envTimeout, envDeadline, traceparent, sess != nil)
	if err == nil {
		return sess, steered, envDeadline, nil
	}
	printer.StepWarn("Could not export the iteration deadline: " + err.Error())
	if rmErr := clearIterationEnv(exec, sandboxName); rmErr != nil {
		sess.stop()
		return nil, steered, envDeadline, rmErr
	}
	if sess == nil {
		return nil, steered, envDeadline, nil
	}
	printer.StepWarn("Steering disabled for this iteration: the sandbox could not be told it is being watched")
	sess.stop()
	_, envDeadline = iterationEnvBudget(steeredBefore, harnessMinutes, runStartedAt, agentStart, timeout)
	return nil, steeredBefore, envDeadline, nil
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
// (a mailbox append, or on Codex the sandbox stop and start that interrupt
// the turn) and would otherwise race the credential refreshers run.go
// already serializes through that lock. The lock lives here, in the CLI layer, which
// is why the runtime cannot take it itself.
func startSteerWatcher(ctx context.Context, o steerOpts) *steerSession {
	if !o.harness.SteerEnabled() {
		return nil
	}
	if d := steerEligible(o); !d.ok() {
		if d.announced(o.harness) {
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

	deliver := steerDeliver(o, steerer)
	settle := func(ctx context.Context) error {
		return withSandboxLock(ctx, nil, func() error {
			return steerer.Settle(ctx, o.sandboxName)
		})
	}

	w := steerwatch.New(steerWatcherConfig(o), steerActionsReaderFn(o.jobToken), steerItemReaderFn(o.roleToken), deliver, settle)

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

// steerWatcherConfig is the watcher's configuration for this run.
func steerWatcherConfig(o steerOpts) steerwatch.Config {
	return steerwatch.Config{
		Repo:            o.statusRepo,
		RunID:           steerRunID(),
		RunName:         steerRunName(o.statusRepo, o.statusNum),
		SelfLogins:      o.selfLogins,
		StartedAt:       o.runStart,
		Deadline:        steerDeadline(o.runStart, o.timeout),
		MaxSteers:       o.harness.SteerMaxSteers(),
		PollInterval:    o.harness.SteerPollInterval(),
		MinRemaining:    steerMinRemaining(o.runtime),
		DeltaBaseline:   o.baseline,
		AlreadySeen:     o.seen,
		AlreadyObserved: o.observed,
		PriorSteers:     o.priorSteers,
		Item: steerwatch.WorkItem{
			Number:  o.statusNum,
			HeadSHA: o.headSHA,
		},
	}
}

// steerInterruptsTheTurn reports whether a steer on rt interrupts the running
// turn rather than reaching it live. Codex exec has no live channel: its
// steer stops and starts the sandbox, then resumes the session.
func steerInterruptsTheTurn(rt agentruntime.Runtime) bool {
	return rt != nil && rt.Name() == "codex"
}

// steerMinRemaining is the run budget a steer needs left before it is sent;
// below it the watcher settles instead. An interrupting steer spends
// agentruntime.CodexSteerInterruptCost of that budget before the resumed
// turn starts, so its floor adds the cost, and the resumed turn keeps the
// same working time a live steer would.
func steerMinRemaining(rt agentruntime.Runtime) time.Duration {
	if steerInterruptsTheTurn(rt) {
		return steerwatch.DefaultMinRemaining + agentruntime.CodexSteerInterruptCost
	}
	return steerwatch.DefaultMinRemaining
}

// steerFetchOIDCToken is fetchOIDCToken; a variable so tests can count
// fetches.
var steerFetchOIDCToken = fetchOIDCToken

// steerDeliver returns the watcher's deliver callback: Steer under sandboxMu.
//
// On an interrupting runtime with a WIF token to keep fresh, it first
// fetches a new OIDC token (outside the lock: the fetch is network only)
// and uploads it inside the same hold, immediately before Steer. The
// interrupt holds the lock for up to sandbox.StopTimeout +
// sandbox.StartTimeout, longer than the token's slack against the 4-minute
// refresher (see sandboxMu), so it must start on a fresh token. The upload
// is uploadOIDCToken, never refreshOIDCToken: the latter takes sandboxMu
// itself and would deadlock here. A failed refresh is a warning and the
// steer still goes out, as the background refresher does: dropping the
// update over a transient token error would be the worse outcome.
func steerDeliver(o steerOpts, steerer agentruntime.Steerer) func(context.Context, agentruntime.SteerMessage) error {
	refresh := steerInterruptsTheTurn(o.runtime) && o.oidcURL != ""
	return func(ctx context.Context, msg agentruntime.SteerMessage) error {
		var token []byte
		if refresh {
			t, err := steerFetchOIDCToken(ctx, o.oidcURL, o.oidcAuth)
			if err != nil {
				o.printer.StepWarn("OIDC token refresh before steering failed; steering anyway: " + err.Error())
			}
			token = t
		}
		return withSandboxLock(ctx, func(waited time.Duration) {
			o.printer.StepInfo(fmt.Sprintf(
				"Waiting %s for a credential refresh to finish before steering the agent", waited))
		}, func() error {
			if token != nil {
				if err := uploadOIDCToken(o.sandboxName, token); err != nil {
					o.printer.StepWarn("OIDC token refresh before steering failed; steering anyway: " + err.Error())
				}
			}
			return steerer.Steer(ctx, o.sandboxName, msg)
		})
	}
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

// steerReceiptWriter posts the receipt comment. It is the narrow surface of
// the forge client the writer needs, so a test can capture what was posted
// and under which token.
type steerReceiptWriter interface {
	CreateIssueComment(ctx context.Context, owner, repo string, number int, body string) (*forge.IssueComment, error)
}

// steerMarkerReader is the forge read surface the skip check needs: the
// login a credential posts under, and the work item's timeline to look for a
// receipt in. github.LiveClient satisfies it.
type steerMarkerReader interface {
	steerSelfLoginReader
	ListIssueComments(ctx context.Context, owner, repo string, number int) ([]forge.IssueComment, error)
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
//
// The watcher is loss-free only because of this intersection: a Steer that
// returns nil is accepted, not delivered, so treating the watcher's handed-off
// set as consumed would receipt an update the agent never saw and the queued
// run would skip it. TestSteerMarkerFrom_OnlyAcknowledgedDeliveriesCount and
// TestSteerMarkerFrom_NoAcksMeansNoMarkerEntries pin that coupling.
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

// steerAlreadyHandled reports whether an earlier run already absorbed the
// follow-up run that dispatched me (ADR 0113).
//
// Dispatch is never suppressed while a run is in flight: a route arm that
// skipped whenever something was running would lose a steer that lands after
// the in-flight run's last check. Instead the in-flight run posts a receipt
// comment under the job token, and this check reads that. A missed receipt
// costs one redundant run, and for review that is a full review of the same
// head.
//
// The receipt is a SEPARATE comment, and the copy of the marker on the
// terminal status comment is informational only: that comment is App-authored,
// the identity the agent's own output goes out under, so honouring it would
// let an injection compose its own receipt (ADR 0120).
//
// It fails open in every direction — no receipt, an unreadable timeline, an
// unresolvable login — because a false "already handled" silently drops the
// work, while a false "not handled" costs one run.
func steerAlreadyHandled(ctx context.Context, receiptReader, roleReader steerMarkerReader, repo string, number int, myRunID int64) (bool, error) {
	if receiptReader == nil || roleReader == nil || myRunID == 0 || number <= 0 {
		return false, nil
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return false, fmt.Errorf("status repo %q is not in owner/repo form", repo)
	}

	// The marker means nothing unless the runner's own receipt credential
	// wrote it. Any user can paste the HTML into a comment of their own, and
	// so can an agent — whose output posts under the role token's identity.
	// Both logins are resolved from their tokens rather than hardcoded: they
	// differ between github.com and GHES.
	receiptLogin, err := receiptReader.GetAuthenticatedUser(ctx)
	if err != nil {
		return false, fmt.Errorf("resolving the receipt login: %w", err)
	}
	roleLogin, err := roleReader.GetAuthenticatedUser(ctx)
	if err != nil {
		return false, fmt.Errorf("resolving the role login: %w", err)
	}
	// The whole control is that the agent cannot post as the receipt author.
	// If the two credentials resolve to the SAME login that is simply untrue
	// — the agent's own comments would pass — and no amount of checking the
	// author can tell them apart. It is not hypothetical: the action's
	// github_token input is a caller-supplied default, so a consumer can
	// hand the runner the same App installation token the role resolves to.
	// Refuse to skip, which costs one redundant run; honouring it would drop
	// the update instead.
	if strings.EqualFold(receiptLogin, roleLogin) {
		return false, fmt.Errorf(
			"the receipt credential and the agent's own credential are the same identity (%s), "+
				"so a receipt proves nothing about who wrote it", receiptLogin)
	}

	comments, err := receiptReader.ListIssueComments(ctx, owner, name, number)
	if err != nil {
		return false, fmt.Errorf("listing comments on %s#%d: %w", repo, number, err)
	}

	tcomments := make([]tracker.Comment, 0, len(comments))
	for _, cm := range comments {
		// An edited comment is not a receipt. Editing keeps the author, and
		// any identity with write access can edit another's comment, so the
		// body now on it is not the one the job token wrote. The runner
		// never edits a receipt. Dropping it can only make the queued run
		// do the work (ADR 0120).
		if cm.UpdatedAt != cm.CreatedAt {
			continue
		}
		tcomments = append(tcomments, tracker.Comment{
			ID:        strconv.Itoa(cm.ID),
			Body:      tracker.Body(cm.Body),
			Author:    cm.Author,
			CreatedAt: cm.CreatedAt,
		})
	}

	marker, found := statuscomment.LatestSteerMarker(tcomments, receiptLogin)
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
	// GitLab has no equivalent: its job token cannot post or read notes, so
	// there is no receipt to authenticate and the check stays fail-open —
	// the queued pipeline does the work, exactly as it does today.
	if o.forgePlatform == repos.ForgeGitLab || o.statusRepo == "" || o.statusNum <= 0 || o.receiptToken == "" || o.roleToken == "" {
		return false
	}
	// A manual re-run keeps its run id, so a receipt that consumed the first
	// attempt would skip it again. A person asked for the work; it runs.
	if a := os.Getenv("GITHUB_RUN_ATTEMPT"); a != "" && a != "1" {
		o.printer.StepInfo("Not skipping on a steer receipt: run attempt " + a + " is a manual re-run")
		return false
	}
	if ok, reason := steerReceiptsHonoured(o.providersDir); !ok {
		o.printer.StepInfo("Not skipping on a steer receipt: " + reason)
		return false
	}
	handled, err := steerAlreadyHandled(ctx, steerMarkerClientFn(o.receiptToken), steerMarkerClientFn(o.roleToken),
		o.statusRepo, o.statusNum, steerRunID())
	if err != nil {
		o.printer.StepWarn("Could not check whether this update was already handled: " + err.Error())
		return false
	}
	return handled
}

// steerReceiptsHonoured reports whether a receipt can be trusted in this
// repository, and why not when it cannot.
//
// A receipt is authenticated only by its author being the job token's login.
// A provider credential that expands GH_WORKFLOW_TOKEN hands that token to a
// sandbox, where the agent can recover it (ADR 0114) and post a receipt of
// its own. Every job in the repository posts as the same login, so a
// definition any harness uses is enough, not only this run's: the scan covers
// every file in providersDir, not the ones this harness declares.
//
// A missing directory holds no definitions. Anything unreadable refuses,
// because a false skip drops work and a false run costs one run.
func steerReceiptsHonoured(providersDir string) (bool, string) {
	if providersDir == "" {
		return false, "provider definitions location unknown"
	}
	defs, err := harness.LoadProviderDefs(providersDir)
	if err != nil {
		return false, "provider definitions unreadable: " + err.Error()
	}
	for _, def := range defs {
		for _, v := range def.Credentials {
			if expandsWorkflowToken(v) {
				return false, fmt.Sprintf("provider %q delivers %s to a sandbox (ADR 0114)", def.Name, workflowTokenEnv)
			}
		}
	}
	return true, ""
}

// expandsWorkflowToken reports whether a provider credential value reads the
// workflow token under either ${VAR} or $VAR form, the way expansion does.
func expandsWorkflowToken(v string) bool {
	found := false
	os.Expand(v, func(k string) string {
		if k == workflowTokenEnv {
			found = true
		}
		return ""
	})
	return found
}

// steerMarkerForStatus returns the marker to write on the terminal status
// comment for a run that ended with the given status.
//
// This marker is informational — the skip check reads the job-token receipt,
// never an App-authored status comment (ADR 0120). It still rides only on a
// successful run, because it reports what the run's published output covered:
// a run that absorbed an update and then failed, timed out, was cancelled or
// was skipped produced no such output, and saying otherwise would misinform
// the person reading the comment. Validation failure arrives here as a
// "failure" status, because an unvalidated run returns an error, so it is
// covered by the same rule.
func steerMarkerForStatus(status string, m statuscomment.SteerMarker) statuscomment.SteerMarker {
	if status != "success" {
		return statuscomment.SteerMarker{}
	}
	return m
}

// steerReceiptToken returns the credential the receipt may be written and
// read under: the job token, but only when minting actually swapped it out.
//
// The swap is what puts the credential beyond the sandbox: minting replaces
// GH_TOKEN with the role token before the sandbox exists. With no mint URL
// or no role there is no swap, the same token stays in os.Environ() for
// childScriptEnv to hand the post-script, and a post-script shelling out to
// `gh` could sign a receipt.
//
// roleToken is read from GH_TOKEN after minting, so comparing the two
// catches an absent swap directly rather than trusting `minted`.
//
// Empty otherwise, which turns the writer and the skip check off and leaves
// the queued run to do the work.
func steerReceiptToken(jobToken, roleToken string, minted bool) string {
	if !minted || jobToken == "" || jobToken == roleToken {
		return ""
	}
	return jobToken
}

// shouldPostSteerReceipt reports whether a finished run may claim a receipt.
//
// Only an outright success may. A receipt tells the run queued behind this
// one that the work is done, so claiming it for a run that failed, was
// cancelled, or was skipped would silently drop the update rather than
// merely waste it — the one direction the skip check must never fail in.
//
// A receipt has to mean the output was published, so both remaining
// arguments are ways a successful run can publish nothing.
// agentReportedError is where this is stricter than the terminal status
// comment: a run whose agent exited 0 with an error in its transcript has its
// post-script skipped, and without a validation loop nothing turns that into
// a non-nil runErr, so the status comment reports success.
// postScriptWithheld is `--no-post-script` on a harness that configures one.
// The flag is deliberately not a workflow input, so this cannot happen on a
// run that mints — but the receipt's correctness must not rest on that
// staying true.
func shouldPostSteerReceipt(runErr, ctxErr error, runSkipped, agentReportedError, postScriptWithheld bool) bool {
	return runErr == nil && ctxErr == nil && !runSkipped && !agentReportedError && !postScriptWithheld
}

// postSteerReceipt writes the run's receipt as its own comment on the work
// item, under the receipt credential.
//
// It is a separate comment rather than the status comment's marker because
// the status comment is posted by the App, the identity the agent's own
// output goes out under, so a marker there authenticates two public strings
// rather than the code path that wrote them.
//
// Best-effort by design: a failure here costs one queued run that redoes
// work already done, where failing the run would throw away work that
// succeeded.
//
// Called only for a successful run, and once per run rather than once per
// steer — the marker is the shipped iteration's (see shippedSteerMarker).
func postSteerReceipt(ctx context.Context, o steerOpts, m statuscomment.SteerMarker) {
	if o.forgePlatform == repos.ForgeGitLab || o.statusRepo == "" || o.statusNum <= 0 || o.receiptToken == "" {
		return
	}
	// At least one absorbed run, not merely a renderable marker. The marker
	// is non-empty for a head-only run too — the head is worth recording on
	// the status comment — but a receipt asserts that a queued run's work is
	// already done, and a run that absorbed nothing has nothing to assert.
	// Without this every successful run on a steering-enabled repository
	// posts a comment saying so.
	ids := m.ConsumedRuns()
	if len(ids) == 0 {
		return
	}
	marker := statuscomment.BuildSteerMarker(m)
	if marker == "" {
		return
	}
	owner, name, ok := strings.Cut(o.statusRepo, "/")
	if !ok || owner == "" || name == "" {
		o.printer.StepWarn(fmt.Sprintf("Not posting the steer receipt: status repo %q is not in owner/repo form", o.statusRepo))
		return
	}

	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), steerReceiptTimeout)
	defer cancel()
	created, err := steerReceiptClientFn(o.receiptToken).CreateIssueComment(
		postCtx, owner, name, o.statusNum, marker+"\n"+steerReceiptLine(ids))
	if err != nil {
		o.printer.StepWarn("Failed to post the steer receipt; the queued run will redo this work: " + err.Error())
		return
	}
	// The author is logged because it is the string the next run's skip
	// check has to match: that check resolves the same token's login through
	// the API, and if the two ever disagree every receipt is silently
	// ignored — safe, but useless, and invisible without this line.
	// created is non-nil from the live client whenever err is nil, but the
	// interface does not promise it and this path is documented
	// best-effort: a nil dereference here would panic out of a defer and
	// take down a run that had already succeeded.
	if created == nil {
		o.printer.StepDone("Posted the steer receipt")
		return
	}
	o.printer.StepDone(fmt.Sprintf("Posted the steer receipt as %s", created.Author))
}

// steerReceiptLine is the human half of the receipt. The marker above it is
// what the queued run reads; this is what a person scrolling the item sees.
// Never called with an empty list: a run that absorbed nothing posts no
// receipt at all.
func steerReceiptLine(consumed []int64) string {
	ids := make([]string, 0, len(consumed))
	for _, id := range consumed {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	return fmt.Sprintf(
		"_The run already working on this item absorbed follow-up run(s) %s, so a run queued for those events exits without repeating the work._",
		strings.Join(ids, ", "))
}

// shippedSteerMarker returns the receipts of the iteration whose output the
// run ships: the validated iteration when there is a validation loop, the
// last one otherwise — the same choice postScriptRepoEnv makes.
//
// Receipts are not unioned across iterations. A retry starts from a fresh
// prompt and transcript, and the watcher does not re-deliver runs an earlier
// iteration judged, so an update absorbed by an iteration that failed
// validation never reaches the output that ships. Its receipt would tell the
// run queued behind this one to skip that update. Without it, the queued run
// does the work again, which costs a run but loses nothing.
func shippedSteerMarker(byIteration map[int]statuscomment.SteerMarker, validationLoop bool, validatedIter, lastIter int) statuscomment.SteerMarker {
	if validationLoop {
		return byIteration[validatedIter]
	}
	return byIteration[lastIter]
}
