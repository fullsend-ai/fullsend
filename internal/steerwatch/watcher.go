package steerwatch

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
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
	// RunName is the display title a follow-up run for my work item
	// carries ("<owner/repo>#<number>"), used to bind issue_comment
	// follow-ups, which carry no pull_requests[]. The runner supplies it
	// from the shim's `run-name`; a shim that declares none leaves it
	// empty, and then only pull-request events bind.
	RunName string
	// SelfLogins are the forge logins this run's own output is posted
	// under, matched exactly and never by pattern: the login the role
	// token resolves to (GetAuthenticatedUser) and ReviewBotLogins for the
	// repository owner, in the REST form with the `[bot]` suffix. Required:
	// Start refuses an empty list, because the post-fix and post-code
	// comments carry no marker and only the login keeps them out of the
	// delta.
	SelfLogins []string
	// StartedAt is the run's start; nothing created at or before it is a
	// follow-up, and it is the baseline for the first delta.
	StartedAt time.Time
	// Deadline bounds the whole watch. The runner sets it from
	// min(stage timeout, forge token life − margin): the App installation
	// token the stage minted lives one hour and is not refreshed, so a run
	// that keeps absorbing must settle before it expires.
	Deadline time.Time
	// MaxSteers caps how many updates this run absorbs. The cap is
	// per-run, not per-watcher: see PriorSteers.
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
	// AlreadySeen seeds the judged set. The validation loop runs one watcher
	// per iteration; without this, a retry would re-examine and re-steer on
	// follow-up runs the previous iteration already absorbed.
	AlreadySeen []int64
	// AlreadyObserved seeds the observed issue_comment runs, from an
	// earlier iteration's ObservedRuns. Ids alone are not enough: a run an
	// earlier watcher judged still occupies its slot when comments are
	// paired with runs, and a run that has left the listing window would
	// otherwise be invisible to the new watcher — its comment then free
	// for the next accepted run to claim.
	AlreadyObserved []forge.WorkflowRun
	// PriorSteers is how many steers earlier watchers of this run already
	// spent. MaxSteers is a per-run cap (ADR 0113) and the validation loop
	// builds a fresh watcher per iteration, so without this the counter
	// resets each iteration and a three-iteration run absorbs three times
	// the cap. Seeding it leaves each watcher MaxSteers − PriorSteers of
	// budget, which is what "per run" means.
	PriorSteers int
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

	myRun forge.WorkflowRun
	// selfLogins is Config.SelfLogins lower-cased for exact lookup.
	selfLogins map[string]bool
	// freshAfter is the instant a candidate must have been created after to
	// count as a follow-up. Set by Start from my own run's record.
	freshAfter time.Time
	stageJob   string

	mu sync.Mutex
	// seen is every follow-up run this watcher has already judged, so a
	// candidate is examined once however many times it appears in a poll.
	seen map[int64]bool
	// observed is every issue_comment run this watcher has examined,
	// accepted or not, keyed by run id. Pairing a comment with the run it
	// triggered has to see the rejected runs too: a command whose run the
	// Route job refused still consumed its comment, and without the run in
	// view the next accepted run would claim that comment as its own.
	observed map[int64]forge.WorkflowRun
	// delivered is one entry per Steer call that returned without error, in
	// order. It is not yet proof the agent saw anything: the runtime
	// acknowledges a delivery later, and only an acknowledged one may be
	// counted as consumed. See DeliveredSteer.
	delivered []DeliveredSteer
	// steers is the run's cumulative count, seeded from Config.PriorSteers,
	// so it is not the length of delivered: earlier iterations' steers
	// count against the cap but were delivered by other watchers.
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
	seen := make(map[int64]bool, len(cfg.AlreadySeen))
	for _, id := range cfg.AlreadySeen {
		seen[id] = true
	}
	baseline := cfg.DeltaBaseline
	if baseline.IsZero() {
		baseline = cfg.StartedAt
	}
	steers := max(0, cfg.PriorSteers)
	observed := make(map[int64]forge.WorkflowRun, len(cfg.AlreadyObserved))
	for _, r := range cfg.AlreadyObserved {
		if r.Event == "issue_comment" {
			observed[int64(r.ID)] = r
		}
	}
	self := make(map[string]bool, len(cfg.SelfLogins))
	for _, login := range cfg.SelfLogins {
		if login = strings.ToLower(strings.TrimSpace(login)); login != "" {
			self[login] = true
		}
	}
	return &Watcher{
		cfg:        cfg,
		actions:    actions,
		items:      items,
		deliver:    deliver,
		settle:     settle,
		logf:       func(string, ...any) {},
		warnf:      func(string, ...any) {},
		seen:       seen,
		observed:   observed,
		selfLogins: self,
		steers:     steers,
		lastHead:   cfg.Item.HeadSHA,
		baseline:   baseline,
	}
}

// SetLogFunc sets the informational logger (steers delivered, candidates
// rejected). Defaults to a no-op.
func (w *Watcher) SetLogFunc(f func(string, ...any)) { w.logf = f }

// SetWarnFunc sets the logger for recoverable failures (a poll that could
// not reach the API). Defaults to a no-op.
func (w *Watcher) SetWarnFunc(f func(string, ...any)) { w.warnf = f }

// DeliveredSteer is one Steer call: the id the SteerMessage carried, and
// every follow-up run whose content that one message folded in.
//
// The two are not the same thing. A poll that accepts several candidates
// sends one message naming the newest of them, so the runtime's
// acknowledgement — which is per message — vouches for the whole batch.
type DeliveredSteer struct {
	// MessageID is SteerMessage.FollowUpRunID, the key the runtime reports
	// back in RunMetrics.Steers once the agent has actually received it.
	MessageID int64
	// RunIDs are the follow-up runs this message carried.
	RunIDs []int64
}

// Delivered returns one entry per Steer call that returned without error,
// in order.
//
// Steer returning is not proof the agent saw the message: the live runtimes
// acknowledge a delivery afterwards (Claude's replay echo, pi's response)
// and Codex when the resumed process starts. A delivery counts as consumed
// only once the runtime has acknowledged it, so the caller intersects these
// with the acknowledgements in RunMetrics.Steers; what it then does with the
// consumed set is decided above this package. Claiming an unacknowledged
// delivery would lose the update outright.
func (w *Watcher) Delivered() []DeliveredSteer {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]DeliveredSteer(nil), w.delivered...)
}

// ObservedRuns returns every issue_comment run this watcher has seen bound
// to its work item, judged or not, for the next iteration's
// Config.AlreadyObserved.
func (w *Watcher) ObservedRuns() []forge.WorkflowRun {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]forge.WorkflowRun, 0, len(w.observed))
	for _, r := range w.observed {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SeenRunIDs returns every follow-up run this watcher has judged, so the
// next iteration's watcher does not re-examine them.
func (w *Watcher) SeenRunIDs() []int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]int64, 0, len(w.seen))
	for id := range w.seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Steers returns how many steers this run has spent, including the ones
// earlier validation-loop iterations spent. The runner carries it into the
// next iteration's watcher so MaxSteers stays a per-run cap.
func (w *Watcher) Steers() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.steers
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
// must succeed before the provenance checks can run — without my own
// referenced_workflows there is no dispatch chain to compare against, and
// without my stage job name there is no way to tell whether a candidate's
// route job selected me.
//
// stageHints pick my job when several are in progress, as when Harness
// dispatch is still running beside a stage job or a matrix fans out. Each is
// matched case-insensitively as a substring of the job name; the first that
// matches exactly one job wins.
func (w *Watcher) Start(ctx context.Context, stageHints ...string) error {
	owner, repo, err := splitRepo(w.cfg.Repo)
	if err != nil {
		return err
	}
	if len(w.selfLogins) == 0 {
		return errors.New("no self logins resolved; the run cannot tell its own output from an update")
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

	// The freshness baseline is my own run's server-side creation time, not
	// the runner's clock. The runner starts when the job picks up a machine,
	// which can be minutes after the run was created; anything created in
	// that gap is a genuine follow-up that the runner's own start time would
	// reject and strand with the pending run. Falls back to the runner's
	// start when the record has no usable timestamp.
	w.freshAfter = w.cfg.StartedAt
	if created := runCreatedAt(*run); !created.IsZero() {
		w.freshAfter = created
	}

	jobs, err := w.actions.ListWorkflowRunJobs(ctx, owner, repo, int(w.cfg.RunID))
	if err != nil {
		return fmt.Errorf("reading my own run's jobs: %w", err)
	}
	name, err := resolveStageJob(jobs, stageHints...)
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
// in-progress job is me; when several are in progress the hints decide,
// in order, and a hint counts when it matches exactly one of them. Hints
// and names are compared with `-` and `_` folded to spaces and case
// ignored: a job id is lower-kebab (`harness-run`) while the API reports
// the display name (`dispatch / Harness run (reviewer)`). An unresolvable
// job name fails closed — no steering at all beats steering on another
// stage's authorization.
func resolveStageJob(jobs []forge.WorkflowJob, hints ...string) (string, error) {
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
	for _, hint := range hints {
		if hint == "" {
			continue
		}
		var matched []string
		for _, n := range running {
			if strings.Contains(foldJobName(n), foldJobName(hint)) {
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

// foldJobName lower-cases s and folds `-` and `_` to spaces, so a job id
// and its display name compare equal.
func foldJobName(s string) string {
	return strings.NewReplacer("-", " ", "_", " ").Replace(strings.ToLower(s))
}

// shimFile is the basename of the workflow file my run came from — the shim,
// which is also the file every follow-up run comes from.
func (w *Watcher) shimFile() string { return path.Base(w.myRun.Path) }

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

// markSteered records that one steer carrying these runs' content was
// handed to the runtime, and advances the delta window.
//
// The baseline advances only here: moving it for a run that produced no
// steer would push the window past content the agent never saw, and that
// content would then never reach it.
func (w *Watcher) markSteered(messageID int64, runs []forge.WorkflowRun, d delta, snapshot time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	batch := DeliveredSteer{MessageID: messageID}
	for _, r := range runs {
		batch.RunIDs = append(batch.RunIDs, int64(r.ID))
		w.seen[int64(r.ID)] = true
	}
	w.delivered = append(w.delivered, batch)
	// A batch whose every amendment was dropped for size carried no
	// authorized update to the agent — the runs are left to the queued run
	// and none is counted as consumed — so it must not spend one of
	// MaxSteers. runs is empty only in that case: a context-only batch, a
	// push with no amendment at all, excludes nothing and still counts.
	if len(runs) > 0 {
		w.steers++
	}
	// The boundary the delta actually covered, not "now": delivery takes
	// time, and anything that arrived during it belongs to the next delta.
	w.baseline = snapshot

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
