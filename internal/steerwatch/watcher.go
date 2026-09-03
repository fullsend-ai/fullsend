package steerwatch

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

// Deliver hands one steer to the running session. The runner supplies a
// closure that takes sandboxMu and calls runtime.Steerer.Steer: Steer writes
// into the sandbox (mailbox append, or on Codex the stray-process sweep that
// interrupts the turn) and races the credential refreshers run.go already
// serializes through that lock, and the lock lives in internal/cli so the
// runtime cannot take it itself.
type Deliver func(ctx context.Context, msg agentruntime.SteerMessage) error

// Settle tells the runtime no further steers will arrive, so Run returns
// after the agent finishes the turn it is on. Like Deliver, the runner's
// closure takes sandboxMu. Settling twice is a no-op.
type Settle func(ctx context.Context) error

// defaultMaxSteers is the per-run steer cap when the caller passes none. It
// matches harness.DefaultSteerMaxSteers, which is where the number is
// decided; this is only the floor for a caller that supplied nothing.
const defaultMaxSteers = 2

// settleTimeout bounds the final Settle when the watcher is exiting on a
// cancelled context. Without its own deadline the settle inherits a dead
// context and the runtime never learns the run is over.
const settleTimeout = 30 * time.Second

// defaultMinRemaining is Config.MinRemaining when unset: a steered turn on
// a large diff re-reads the delta and re-runs tools, so a few minutes is the
// least it can need before the exec timeout would cut it off.
const defaultMinRemaining = 5 * time.Minute

// Config is everything the watcher needs that it cannot discover itself.
type Config struct {
	// Repo is "owner/repo" — the consumer repository, which is also where
	// every follow-up run lives, because a reusable workflow executes
	// inside the caller's run.
	Repo string
	// RunID is my own GITHUB_RUN_ID, excluded from every candidate list.
	RunID int64
	// RunName is the shim's run-name for my work item
	// ("<owner/repo>#<number>"), used to bind issue_comment follow-ups that
	// carry no pull_requests[]. Empty disables that binding.
	RunName string
	// StartedAt is the run's start; nothing created at or before it is a
	// follow-up, and it is the baseline for the first delta.
	StartedAt time.Time
	// Deadline bounds the whole watch. The runner sets it from
	// min(stage timeout, forge token life − margin): the App installation
	// token the stage minted lives one hour and is not refreshed, so a run
	// that keeps absorbing must settle before it expires.
	Deadline time.Time
	// MaxSteers caps how many updates this run absorbs.
	MaxSteers int
	// MinRemaining is the floor of run budget a steer needs. Below it the
	// watcher settles instead of steering: the exec that hosts a live
	// session cannot be extended once running, so a steer taken with less
	// time than a turn needs would push the whole run into its timeout and
	// lose everything, the opposite of what steering is for. The update is
	// left to the run queued behind this one. Zero means the default.
	MinRemaining time.Duration
	// PollInterval is how often follow-up runs are listed. A turn end
	// triggers an immediate poll regardless.
	PollInterval time.Duration
	// Item is the work item. Only Number is required: Start resolves
	// whether it is a pull request and fills in the baseline the delta is
	// computed against, because the run's environment cannot tell the
	// watcher either reliably.
	Item WorkItem

	// DeltaBaseline seeds the delta window. Zero means StartedAt. The
	// validation loop runs one watcher per iteration; without this, a retry
	// would rebuild its first delta from the run's start and re-send
	// content the previous iteration already steered on.
	DeltaBaseline time.Time
	// AlreadyConsumed seeds the consumed set. The validation loop runs one
	// watcher per iteration; without this, a retry would re-examine and
	// re-steer on follow-up runs the previous iteration already absorbed.
	AlreadyConsumed []int64
}

// Watcher turns follow-up workflow runs into steers. One watcher serves one
// agent run; it is not reusable.
type Watcher struct {
	cfg     Config
	actions ActionsReader
	items   ItemReader
	deliver Deliver
	settle  Settle
	logf    func(string, ...any)
	warnf   func(string, ...any)

	myRun    forge.WorkflowRun
	stageJob string

	mu sync.Mutex
	// seen is every follow-up run this watcher has already judged, so a
	// candidate is examined once however many times it appears in a poll.
	seen map[int64]bool
	// consumed is the subset whose content actually reached the agent, in
	// delivery order. Only these go in the marker: the queued run reads it
	// to decide whether to skip its own work, so a run that was seen and
	// dropped must not look handled.
	consumed []int64
	steers   int
	lastHead string
	baseline time.Time

	settleOnce sync.Once
}

// New builds a watcher. Nothing is fetched yet — call Start.
func New(cfg Config, actions ActionsReader, items ItemReader, deliver Deliver, settle Settle) *Watcher {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.MaxSteers <= 0 {
		cfg.MaxSteers = defaultMaxSteers
	}
	if cfg.MinRemaining <= 0 {
		cfg.MinRemaining = defaultMinRemaining
	}
	seen := make(map[int64]bool, len(cfg.AlreadyConsumed))
	consumed := make([]int64, 0, len(cfg.AlreadyConsumed))
	for _, id := range cfg.AlreadyConsumed {
		if !seen[id] {
			seen[id] = true
			consumed = append(consumed, id)
		}
	}
	baseline := cfg.DeltaBaseline
	if baseline.IsZero() {
		baseline = cfg.StartedAt
	}
	return &Watcher{
		cfg:      cfg,
		actions:  actions,
		items:    items,
		deliver:  deliver,
		settle:   settle,
		logf:     func(string, ...any) {},
		warnf:    func(string, ...any) {},
		seen:     seen,
		consumed: consumed,
		lastHead: cfg.Item.HeadSHA,
		baseline: baseline,
	}
}

// SetLogFunc sets the informational logger (steers delivered, candidates
// rejected). Defaults to a no-op.
func (w *Watcher) SetLogFunc(f func(string, ...any)) { w.logf = f }

// SetWarnFunc sets the logger for recoverable failures (a poll that could
// not reach the API). Defaults to a no-op.
func (w *Watcher) SetWarnFunc(f func(string, ...any)) { w.warnf = f }

// Consumed returns the follow-up run ids this watcher absorbed, in the order
// it took them. The runner writes them into the steer marker on the terminal
// status comment so the run queued behind it can skip work already covered.
func (w *Watcher) Consumed() []int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]int64(nil), w.consumed...)
}

// Baseline returns the instant the next delta would be computed against. The
// runner carries it into the next validation-loop iteration's watcher.
func (w *Watcher) Baseline() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.baseline
}

// Head returns the work item head the run settled on, which is the head at
// run start when nothing moved it.
func (w *Watcher) Head() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastHead
}

// Start reads my own run record and resolves which stage job is mine. It
// must succeed before Watch can apply the provenance checks — without my own
// referenced_workflows there is no dispatch chain to compare against, and
// without my stage job name there is no way to tell whether a candidate's
// route job selected me.
//
// stageHint disambiguates when more than one job of my run is in progress
// (the harness fan-out runs one job per matrix agent); it is matched
// case-insensitively as a substring of the job name.
func (w *Watcher) Start(ctx context.Context, stageHint string) error {
	owner, repo, err := splitRepo(w.cfg.Repo)
	if err != nil {
		return err
	}
	run, err := w.actions.GetWorkflowRun(ctx, owner, repo, int(w.cfg.RunID))
	if err != nil {
		return fmt.Errorf("reading my own run %d: %w", w.cfg.RunID, err)
	}
	if run.Path == "" {
		return fmt.Errorf("my own run %d reports no workflow path", w.cfg.RunID)
	}
	if len(run.ReferencedWorkflows) == 0 {
		return fmt.Errorf("my own run %d references no reusable workflow; "+
			"the dispatch-chain check has nothing to compare against", w.cfg.RunID)
	}
	w.myRun = *run

	jobs, err := w.actions.ListWorkflowRunJobs(ctx, owner, repo, int(w.cfg.RunID))
	if err != nil {
		return fmt.Errorf("reading my own run's jobs: %w", err)
	}
	name, err := resolveStageJob(jobs, stageHint)
	if err != nil {
		return err
	}
	w.stageJob = name

	return w.resolveItem(ctx)
}

// resolveItem asks the forge what the work item is and what it looked like
// when the run started.
//
// The run's environment cannot answer either question: PR_HEAD_SHA is set
// only on the deprecated per-org dispatch path, so a per-repo run has no
// head SHA in its environment and no way to tell a pull request from an
// issue. Guessing wrong is not cosmetic — an issue-shaped baseline of empty
// title, body and labels makes every delta report the whole body as edited
// and every label as added, forever, so the run never settles and the agent
// is handed the same "update" on each steer.
//
// A caller-supplied HeadSHA still wins: it is the head at run start, which
// is a beat earlier than this call and is the baseline a head move must be
// measured against.
func (w *Watcher) resolveItem(ctx context.Context) error {
	owner, repo, err := splitRepo(w.cfg.Repo)
	if err != nil {
		return err
	}

	head, err := w.items.GetPullRequestHeadSHA(ctx, owner, repo, w.cfg.Item.Number)
	switch {
	case err == nil:
		w.cfg.Item.IsPullRequest = true
		if w.cfg.Item.HeadSHA == "" {
			w.cfg.Item.HeadSHA = head
		}
		w.mu.Lock()
		w.lastHead = w.cfg.Item.HeadSHA
		w.mu.Unlock()
		return nil
	case forge.IsNotFound(err):
		// Not a pull request; fall through to the issue snapshot.
	default:
		return fmt.Errorf("resolving whether %s#%d is a pull request: %w",
			w.cfg.Repo, w.cfg.Item.Number, err)
	}

	issue, err := w.items.GetIssue(ctx, owner, repo, w.cfg.Item.Number)
	if err != nil {
		return fmt.Errorf("reading issue %s#%d: %w", w.cfg.Repo, w.cfg.Item.Number, err)
	}
	w.cfg.Item.IsPullRequest = false
	w.cfg.Item.HeadSHA = ""
	w.cfg.Item.Title = issue.Title
	w.cfg.Item.Body = issue.Body
	w.cfg.Item.Labels = issue.Labels
	w.mu.Lock()
	w.lastHead = ""
	w.mu.Unlock()
	return nil
}

// resolveStageJob picks my own stage job out of my run's job list. The
// in-progress job is me; when several are in progress the hint decides.
// An unresolvable job name fails closed — no steering at all beats steering
// on another stage's authorization.
func resolveStageJob(jobs []forge.WorkflowJob, hint string) (string, error) {
	var running []string
	for _, j := range jobs {
		if j.Status == "in_progress" && !routeJobName(j.Name) {
			running = append(running, j.Name)
		}
	}
	switch len(running) {
	case 0:
		return "", errors.New("no in-progress stage job found in my own run")
	case 1:
		return running[0], nil
	}
	if hint != "" {
		var matched []string
		for _, n := range running {
			if strings.Contains(strings.ToLower(n), strings.ToLower(hint)) {
				matched = append(matched, n)
			}
		}
		if len(matched) == 1 {
			return matched[0], nil
		}
	}
	return "", fmt.Errorf("cannot tell which of %d in-progress jobs is mine (%s)",
		len(running), strings.Join(running, ", "))
}

// shimFile is the basename of the workflow file my run came from — the shim,
// which is also the file every follow-up run comes from.
func (w *Watcher) shimFile() string { return path.Base(w.myRun.Path) }

// Watch runs the poll/steer/settle loop until the run is settled, the
// deadline passes, or ctx is done. It always settles before returning, so
// the runtime is never left holding a session open for a watcher that has
// stopped watching.
//
// turnEnd carries one value per agent turn end (runtime.ResultEvent). On a
// turn end the watcher polls immediately: if something new arrived it steers
// and the agent takes another turn, otherwise it settles and the run ends.
// A steer consumed mid-turn produces no turn end of its own, so turn ends
// are never counted against the steer budget.
func (w *Watcher) Watch(ctx context.Context, turnEnd <-chan struct{}) {
	defer w.doSettle(ctx)

	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	var deadline <-chan time.Time
	if !w.cfg.Deadline.IsZero() {
		t := time.NewTimer(time.Until(w.cfg.Deadline))
		defer t.Stop()
		deadline = t.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			w.logf("Steer deadline reached; settling the run")
			return
		case <-ticker.C:
			if w.lowOnTime() {
				w.logf("Less than %s of run budget left; settling instead of steering", w.cfg.MinRemaining)
				return
			}
			w.pollAndSteer(ctx)
			if w.capReached() {
				w.logf("Steer cap of %d reached; settling the run", w.cfg.MaxSteers)
				return
			}
		case _, ok := <-turnEnd:
			if !ok {
				return
			}
			if w.lowOnTime() {
				w.logf("Less than %s of run budget left; settling instead of steering", w.cfg.MinRemaining)
				return
			}
			if !w.pollAndSteer(ctx) {
				return
			}
			if w.capReached() {
				w.logf("Steer cap of %d reached; settling the run", w.cfg.MaxSteers)
				return
			}
		}
	}
}

// lowOnTime reports whether the run budget left is below MinRemaining. A
// zero Deadline never runs low.
func (w *Watcher) lowOnTime() bool {
	return !w.cfg.Deadline.IsZero() && time.Until(w.cfg.Deadline) < w.cfg.MinRemaining
}

func (w *Watcher) capReached() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.steers >= w.cfg.MaxSteers
}

// doSettle calls Settle exactly once, on a context that outlives a cancelled
// run context so the runtime still learns the run is over.
func (w *Watcher) doSettle(ctx context.Context) {
	w.settleOnce.Do(func() {
		if w.settle == nil {
			return
		}
		settleCtx := ctx
		if ctx.Err() != nil {
			settleCtx = context.Background()
		}
		settleCtx, cancel := context.WithTimeout(settleCtx, settleTimeout)
		defer cancel()
		if err := w.settle(settleCtx); err != nil {
			w.warnf("Settling the agent session failed: %v", err)
		}
	})
}

// pollAndSteer runs one poll. It returns true when a steer was delivered —
// the caller uses that to decide whether a turn end settles the run.
//
// Every accepted candidate in one poll folds into a single steer: the delta
// is the work item's current state against the baseline, so two comments
// that arrive together cost one turn, not two, and both run ids are recorded
// as consumed.
func (w *Watcher) pollAndSteer(ctx context.Context) bool {
	if w.capReached() {
		return false
	}
	accepted := w.poll(ctx)
	if len(accepted) == 0 {
		return false
	}

	w.mu.Lock()
	baseline := w.baseline
	w.mu.Unlock()

	d, err := w.buildDelta(ctx, baseline)
	if err != nil {
		w.warnf("Building the work-item delta failed: %v", err)
		return false
	}
	if d.empty() {
		// The follow-up run was authorized and bound to my item but the
		// item's visible state did not change (a label event the agent does
		// not read, an edit that reverted). Consume the ids anyway so the
		// same runs are not re-examined every poll.
		w.markSeen(accepted...)
		w.logf("Follow-up run(s) %s carried no visible change; not steering", runIDs(accepted))
		return false
	}

	text, findings := w.buildText(accepted, d)
	if findings > 0 {
		w.warnf("Unicode sanitization altered the steer text (%d finding(s) stripped)", findings)
	}

	newest := accepted[len(accepted)-1]
	msg := agentruntime.SteerMessage{
		FollowUpRunID: int64(newest.ID),
		Event:         newest.Event,
		Actor:         actorLogin(newest),
		CreatedAt:     runCreatedAt(newest),
		HeadSHA:       d.newHead,
		Text:          text,
	}
	if err := w.deliver(ctx, msg); err != nil {
		if errors.Is(err, agentruntime.ErrSteerUnsupported) {
			// Logged by the runner when it set up the watcher; nothing to
			// retry, and the queued follow-up run does the work.
			w.warnf("Runtime cannot steer; leaving the update to the queued run")
			return false
		}
		w.warnf("Delivering the steer failed: %v", err)
		return false
	}

	w.markSteered(accepted, d)
	w.logf("Steered the agent with follow-up run(s) %s", runIDs(accepted))
	return true
}

// markSeen records that these runs have been judged, so they are not
// re-examined on every poll. It says nothing about whether the agent saw
// their content.
func (w *Watcher) markSeen(runs ...forge.WorkflowRun) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range runs {
		w.seen[int64(r.ID)] = true
	}
}

// markSteered records that one steer carrying these runs' content reached
// the agent, and advances the delta window.
//
// The baseline advances only here: moving it for a run that produced no
// steer would push the window past content the agent never saw, and that
// content would then never reach it.
func (w *Watcher) markSteered(runs []forge.WorkflowRun, d delta) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range runs {
		if !containsID(w.consumed, int64(r.ID)) {
			w.consumed = append(w.consumed, int64(r.ID))
		}
		w.seen[int64(r.ID)] = true
	}
	w.steers++
	w.baseline = time.Now().UTC()

	// Advance the content baseline to the state the agent was actually
	// told about. Without this an issue's title, body and labels stay
	// pinned to run start, so every later steer repeats changes the agent
	// has already seen — and a field edited back to its original value
	// reads as unchanged and is never reported at all.
	if d.issue != nil {
		w.cfg.Item.Title = d.issue.Title
		w.cfg.Item.Body = d.issue.Body
		w.cfg.Item.Labels = append([]string(nil), d.issue.Labels...)
	}
	if d.headMoved {
		w.lastHead = d.newHead
	}
}

func containsID(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// poll lists follow-up runs and returns the ones that pass every provenance
// check, oldest first.
func (w *Watcher) poll(ctx context.Context) []forge.WorkflowRun {
	runs, err := w.runsSince(ctx, w.shimFile(), w.cfg.StartedAt)
	if err != nil {
		w.warnf("Listing follow-up runs failed: %v", err)
		return nil
	}

	var accepted []forge.WorkflowRun
	for _, run := range runs {
		if rej := w.candidateChecks(run); rej != nil {
			w.logf("Follow-up run %d rejected (%s)", run.ID, rej)
			continue
		}
		rej, err := w.jobChecks(ctx, run)
		if err != nil {
			w.warnf("Reading follow-up run %d's jobs failed: %v", run.ID, err)
			continue
		}
		if rej != nil {
			w.logf("Follow-up run %d rejected (%s)", run.ID, rej)
			// A rejected-on-authorization run must not be retried every
			// poll: its verdict cannot change.
			w.markSeen(run)
			continue
		}
		accepted = append(accepted, run)
	}
	return accepted
}

func runIDs(runs []forge.WorkflowRun) string {
	ids := make([]string, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, fmt.Sprintf("%d", r.ID))
	}
	return strings.Join(ids, ", ")
}
