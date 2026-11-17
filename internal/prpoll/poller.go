// Package prpoll implements the github-pr-poll input driver (ADR 0171): a
// scheduled poll that starts the built-in review for fork pull requests
// from authorized authors. Since ADR 0164 the pull_request shim skips every
// fork PR, so without this poll those PRs are reviewed only on /fs-review.
//
// One cycle lists the open pull requests, keeps the fork PRs whose author
// owns the fork and whom the shared dispatch core authorizes for the
// review harness (Options.Authorize), and emits a review dispatch for each
// head commit that fullsend's review App has neither finished nor started,
// within per-cycle and per-head bounds. Decisions
// come only from records the review App writes (formal reviews, its sticky
// comment and status comments), from the jobs of the poll workflow's own
// schedule and workflow_dispatch runs on the default branch, and from the
// poll's State, never from comment text anyone can post.
//
// The State carries per-head dispatch counts and finished or exhausted
// heads across cycles. It bounds every head to MaxAttempts dispatches,
// even when a review ends without a record the poll can read, and lets a
// cycle skip finished or exhausted heads without any API call.
package prpoll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

const (
	// WorkflowFile is the file name of the poll caller workflow that
	// `fullsend repos install` writes under .github/workflows/.
	WorkflowFile = "fullsend-poll-prs.yml"

	// ReviewJobNameFormat is the name of the poll caller workflow's
	// dispatch job for one pull request number and head SHA. That job
	// calls reusable-dispatch.yml, so Actions names the called jobs
	// "<dispatch job> / <called job>", for example
	// "Review (#7@<sha>) / Route" and "Review (#7@<sha>) / Review"; before
	// the called workflow expands, the job is listed under the bare
	// dispatch job name. The poll reads them back to tell running and
	// finished reviews apart; the workflow must render the same name (see
	// ReviewJobName).
	ReviewJobNameFormat = "Review (#%d@%s)"

	// reviewJob is the name of the built-in review job reusable-dispatch.yml
	// runs.
	reviewJob = "Review"

	// TriggerSource is the trigger_source of every dispatch the poll
	// emits.
	TriggerSource = "fork-pr-poll"

	// reviewAgent is the built-in agent and harness role the poll starts.
	reviewAgent = "review"

	// reviewMarker marks the review agent's sticky comment (see
	// postreview.go).
	reviewMarker = "<!-- fullsend:review-agent -->"
	// stickyHistoryStart separates the sticky comment's current section
	// from its collapsed history.
	stickyHistoryStart = "<!-- sticky:history-start -->"
	// reviewSkipMarker marks the note the route posts when the
	// review-diff-hash guard finds the diff unchanged; the note names the
	// head it skipped with a reviewSkipHeadRe marker.
	reviewSkipMarker = "<!-- fullsend:review-skip -->"
)

// Decision reasons logged for each pull request. The test-repo validation
// reads them.
const (
	ReasonDispatch          = "dispatch"
	ReasonSameRepo          = "skip: same-repo"
	ReasonForkNotOwned      = "skip: fork not owned by author"
	ReasonDenied            = "skip: denied by dispatch core"
	ReasonFinished          = "skip: finished"
	ReasonRunning           = "skip: running"
	ReasonAttemptsExhausted = "skip: attempts exhausted"
	ReasonBeforeEnable      = "skip: before enable"
	ReasonStartCap          = "skip: start cap"
	ReasonLookupFailed      = "skip: lookup failed"
)

var (
	// headSHARe extracts the reviewed head SHA from the sticky comment's
	// current section, as pre-fetch-prior-review.sh does.
	headSHARe = regexp.MustCompile(`\*\*Head SHA:\*\* ([0-9a-f]{7,64})`)
	// statusMarkerRe extracts the run ID of a status comment.
	statusMarkerRe = regexp.MustCompile(`<!-- fullsend:agent-status:([0-9]+) -->`)
	// reviewSkipHeadRe extracts the head SHA a review-skip note names.
	reviewSkipHeadRe = regexp.MustCompile(`<!-- fullsend:review-skip-head:([0-9a-fA-F]{7,64}) -->`)
	// statusCommitRe extracts the short commit SHA of a status comment.
	statusCommitRe = regexp.MustCompile("Commit: `([0-9a-f]{7,64})`")
	// statusSuccessRe matches the GitHub body of a status comment whose
	// run finished with success, as statuscomment's buildCompletionBody
	// renders it: the run marker and the terminal marker on lines of their
	// own at the start, the "Finished <description> · ✅ Success · Started"
	// line (a success has no detail, and the description is the runner's
	// agent name, which holds no "·"), then the Commit line. Group 1 is the
	// short commit SHA. A failure renders "❌ Failure (<detail>)" right
	// after the description, so its detail cannot reach this position.
	statusSuccessRe = regexp.MustCompile("\\A<!-- fullsend:agent-status:[0-9]+ -->\n" +
		regexp.QuoteMeta(statusTerminalMarker) + "\n" +
		"🤖 Finished [^·\n]+ · ✅ Success · Started [^\n]*\n\n" +
		"Commit: `([0-9a-f]{7,64})`")
	// statusSkippedRe matches the GitHub body of a status comment whose run
	// ended skipped, in the two forms statuscomment renders, with the same
	// anchoring as statusSuccessRe: buildCompletionBody's "Finished
	// <description> · ⏭ Skipped[ (<detail>)] · Started" line, and
	// buildInterruptedBody's "<heading> · ⏭ Skipped (no completion
	// comment)" line. A detail is sanitized to one line, so it cannot
	// supply the Commit line that follows. Group 1 is the short commit SHA.
	statusSkippedRe = regexp.MustCompile("\\A<!-- fullsend:agent-status:[0-9]+ -->\n" +
		regexp.QuoteMeta(statusTerminalMarker) + "\n" +
		"🤖 (?:Finished [^·\n]+ · " + skippedLabel + "(?: \\([^\n]*\\))? · Started|[^·\n]+ · " + skippedLabel + " \\(no completion comment\\) ·) [^\n]*\n\n" +
		"Commit: `([0-9a-f]{7,64})`")
)

// statusTerminalMarker is the marker statuscomment writes on a status
// comment once its run has finished.
const statusTerminalMarker = "<!-- fullsend:status:terminal -->"

// skippedLabel is statuscomment's skipped status label: the ⏭ emoji with
// its emoji variation selector (U+FE0F), then "Skipped".
const skippedLabel = "\u23ed\ufe0f Skipped"

// runningStatuses are the workflow run statuses whose jobs may still run.
var runningStatuses = []string{"queued", "in_progress", "waiting", "requested", "pending"}

// pollRunEvents are the events of the poll workflow runs whose jobs the
// poll reads. A pull_request run executes the workflow file of the PR's
// head, so a fork could fake review jobs in it.
var pollRunEvents = map[string]bool{"schedule": true, "workflow_dispatch": true}

// ReviewJobName returns the review job name for pull request number and
// head SHA sha.
func ReviewJobName(number int, sha string) string {
	return fmt.Sprintf(ReviewJobNameFormat, number, sha)
}

// Client is the subset of forge.Client the poll uses.
type Client interface {
	ListRepoPullRequests(ctx context.Context, owner, repo string) ([]forge.ChangeProposal, error)
	GetCollaboratorPermission(ctx context.Context, owner, repo, username string) (string, error)
	ListPullRequestReviews(ctx context.Context, owner, repo string, number int) ([]forge.PullRequestReview, error)
	ListIssueComments(ctx context.Context, owner, repo string, number int) ([]forge.IssueComment, error)
	ListWorkflowRunsByStatus(ctx context.Context, owner, repo, workflowFile, status string, createdSince time.Time) ([]forge.WorkflowRun, error)
	ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int) ([]forge.WorkflowJob, error)
	GetWorkflowRun(ctx context.Context, owner, repo string, runID int) (*forge.WorkflowRun, error)
	GetRepo(ctx context.Context, owner, repo string) (*forge.Repository, error)
}

// Options configure one poll cycle.
type Options struct {
	// Owner and Repo name the repository whose pull requests are polled.
	Owner, Repo string
	// Since is the time the poll was enabled. A pull request last updated
	// before it is skipped until its next push.
	Since time.Time
	// MaxStarts caps the dispatches of one cycle.
	MaxStarts int
	// MaxAttempts caps the review dispatches the poll emits for one head
	// commit, as counted in State.
	MaxAttempts int
	// DefaultBranch is the repository's default branch: only poll
	// workflow runs on it are read. Empty reads it from the repository;
	// when that fails Run fails, with no dispatches.
	DefaultBranch string
	// State is the per-head state across cycles. Run updates it in place;
	// nil uses an empty state that is discarded after the cycle.
	State *State
	// ReviewBotLogins are the logins of fullsend's review App.
	ReviewBotLogins []string
	// ReviewAppClientID is the client ID of fullsend's review App. It is
	// required: without it the poll cannot tell the App's comments from
	// anyone else's, so Run fails rather than risk duplicate reviews.
	ReviewAppClientID string
	// Authorize is the shared dispatch core's authorization gate (ADR
	// 0054, authorization contract v1): it reports whether the event's
	// actor, the PR author, may start the review harness. nil denies
	// every pull request (fail closed).
	Authorize func(ev *normevent.Event) bool
	// Log receives one line per pull request and a summary line.
	Log io.Writer
	// Now returns the current time; nil uses time.Now.
	Now func() time.Time
	// APICalls returns the number of API calls the client has made so
	// far; nil omits the count from the summary.
	APICalls func() int64
}

// Poller runs fork PR review poll cycles.
type Poller struct {
	client Client
	opts   Options

	state *State
	// now is the time of the cycle, used for state entries.
	now time.Time

	runsLoaded bool
	jobs       []forge.WorkflowJob
	// comments are the issue comments of the pull request being
	// evaluated, listed once and read by statusCommentRunning and
	// finished.
	comments []forge.IssueComment
	// perms caches collaborator permission lookups by lowercased login
	// for the cycle.
	perms map[string]permLookup
	// cacheSkips counts the heads skipped from State without API calls.
	cacheSkips int
}

// permLookup is the result of one collaborator permission lookup.
type permLookup struct {
	perm string
	err  error
}

// New returns a Poller over client.
func New(client Client, opts Options) *Poller {
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	state := opts.State
	if state == nil {
		state = NewState()
	}
	return &Poller{client: client, opts: opts, state: state, perms: map[string]permLookup{}}
}

// ErrNoDefaultBranch reports that the repository's default branch is not
// known, so the poll cannot tell its own runs from runs a pull request
// could fake.
var ErrNoDefaultBranch = errors.New("the repository's default branch is unknown; the poll cannot tell its own runs from pull request runs, so it starts no review")

// decision is the outcome for one pull request.
type decision struct {
	pr     forge.ChangeProposal
	reason string
	detail string
}

// ErrNoReviewAppClientID reports that Options.ReviewAppClientID is empty.
var ErrNoReviewAppClientID = errors.New("the review App client ID (FULLSEND_REVIEW_CLIENT_ID) is not set; the poll cannot verify which comments the review App wrote, so it starts no review")

// Run executes one poll cycle and returns the dispatches to start, oldest
// updated_at first, and records them in the state. It fails, with no
// dispatches, when the review App client ID is not set or the default
// branch is unknown.
func (p *Poller) Run(ctx context.Context) ([]harnessdispatch.ExecutionRef, error) {
	if p.opts.ReviewAppClientID == "" {
		return nil, ErrNoReviewAppClientID
	}
	start := p.opts.Now()
	p.now = start
	var callsBefore int64
	if p.opts.APICalls != nil {
		callsBefore = p.opts.APICalls()
	}

	if p.opts.DefaultBranch == "" {
		info, err := p.client.GetRepo(ctx, p.opts.Owner, p.opts.Repo)
		if err != nil {
			return nil, fmt.Errorf("%w: get repository: %w", ErrNoDefaultBranch, err)
		}
		if info == nil || info.DefaultBranch == "" {
			return nil, ErrNoDefaultBranch
		}
		p.opts.DefaultBranch = info.DefaultBranch
	}
	p.state.Prune(start)

	prs, err := p.client.ListRepoPullRequests(ctx, p.opts.Owner, p.opts.Repo)
	if err != nil {
		return nil, fmt.Errorf("list open pull requests: %w", err)
	}

	decisions := make([]decision, 0, len(prs))
	var candidates []int
	for _, pr := range prs {
		reason, detail := p.evaluate(ctx, pr)
		decisions = append(decisions, decision{pr: pr, reason: reason, detail: detail})
		if reason == ReasonDispatch {
			candidates = append(candidates, len(decisions)-1)
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return updatedAt(decisions[candidates[i]].pr).Before(updatedAt(decisions[candidates[j]].pr))
	})
	var refs []harnessdispatch.ExecutionRef
	for _, idx := range candidates {
		d := &decisions[idx]
		if len(refs) >= p.opts.MaxStarts {
			d.reason = ReasonStartCap
			d.detail = fmt.Sprintf("max_starts_per_cycle=%d reached", p.opts.MaxStarts)
			continue
		}
		ref, err := p.executionRef(d.pr)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	// Count the dispatches only once every ref is built, so an error above
	// records none.
	for _, idx := range candidates {
		if d := decisions[idx]; d.reason == ReasonDispatch {
			p.state.recordDispatch(headKey(d.pr.Number, d.pr.HeadSHA), start)
		}
	}
	p.state.Prune(start)

	for _, d := range decisions {
		line := fmt.Sprintf("PR #%d head %s: %s", d.pr.Number, shortSHA(d.pr.HeadSHA), d.reason)
		if d.detail != "" {
			line += " (" + d.detail + ")"
		}
		fmt.Fprintln(p.opts.Log, line)
	}
	summary := fmt.Sprintf("fork PR review poll: %d open PRs, %d dispatched, %d heads skipped from cache", len(prs), len(refs), p.cacheSkips)
	if p.opts.APICalls != nil {
		summary += fmt.Sprintf(", %d GitHub API calls", p.opts.APICalls()-callsBefore)
	}
	summary += fmt.Sprintf(", wall time %s", p.opts.Now().Sub(start).Round(time.Millisecond))
	fmt.Fprintln(p.opts.Log, summary)
	return refs, nil
}

// evaluate returns the decision reason for one pull request and an
// optional detail. Cheap checks on the listing and the state run before
// any API call; then authorization, the running check, the finished check
// and last the dispatch count.
func (p *Poller) evaluate(ctx context.Context, pr forge.ChangeProposal) (string, string) {
	if pr.HeadRepo != "" && strings.EqualFold(pr.HeadRepo, pr.BaseRepo) {
		return ReasonSameRepo, ""
	}
	if pr.HeadRepo == "" || pr.HeadOwner == "" || pr.Author == "" || !strings.EqualFold(pr.HeadOwner, pr.Author) {
		return ReasonForkNotOwned, fmt.Sprintf("head repo %q owned by %q, author %q", pr.HeadRepo, pr.HeadOwner, pr.Author)
	}
	if pr.HeadSHA == "" {
		return ReasonLookupFailed, "no head SHA"
	}
	if t := updatedAt(pr); t.IsZero() || t.Before(p.opts.Since) {
		return ReasonBeforeEnable, "updated_at " + pr.UpdatedAt
	}
	key := headKey(pr.Number, pr.HeadSHA)
	if h := p.state.head(key); h != nil {
		switch h.Status {
		case HeadFinished:
			p.cacheSkips++
			return ReasonFinished, "cached"
		case HeadExhausted:
			p.cacheSkips++
			return ReasonAttemptsExhausted, fmt.Sprintf("cached, %d dispatches", h.Dispatches)
		}
	}
	if ok, detail := p.authorized(ctx, pr); !ok {
		return ReasonDenied, detail
	}

	p.comments = nil
	jobs, err := p.jobState(ctx, pr)
	if err != nil {
		return ReasonLookupFailed, err.Error()
	}
	if jobs == ReasonRunning {
		return ReasonRunning, ""
	}
	comments, err := p.client.ListIssueComments(ctx, p.opts.Owner, p.opts.Repo, pr.Number)
	if err != nil {
		return ReasonLookupFailed, fmt.Errorf("list comments: %w", err).Error()
	}
	p.comments = comments
	running, err := p.statusCommentRunning(ctx, pr)
	if err != nil {
		return ReasonLookupFailed, err.Error()
	}
	if running {
		return ReasonRunning, ""
	}

	finished := jobs == ReasonFinished
	if !finished {
		if finished, err = p.finished(ctx, pr); err != nil {
			return ReasonLookupFailed, err.Error()
		}
	}
	if finished {
		p.state.setStatus(key, HeadFinished, p.now)
		return ReasonFinished, ""
	}

	if n := p.state.dispatches(key); n >= p.opts.MaxAttempts {
		p.state.setStatus(key, HeadExhausted, p.now)
		return ReasonAttemptsExhausted, fmt.Sprintf("%d dispatches", n)
	}
	return ReasonDispatch, ""
}

// authorized asks the dispatch core whether pr's author may request a
// review. It builds the event the GitHub event adapter builds for a
// pull_request (see harnessdispatch/input/ghaevent.go): the actor is the
// PR author as the API lists it, with the collaborator role read once per
// author on every cycle. A lookup that is not a 404 leaves the role
// unverified, which the core denies; a bot author is decided by the core's
// bot rules (ADR 0107). The detail names the role.
func (p *Poller) authorized(ctx context.Context, pr forge.ChangeProposal) (bool, string) {
	kind := normevent.ActorHuman
	if strings.EqualFold(pr.AuthorType, "Bot") || strings.HasSuffix(strings.ToLower(pr.Author), "[bot]") {
		kind = normevent.ActorBot
	}
	role := normevent.RoleNone
	verified := true
	detail := ""
	lookup, ok := p.perms[strings.ToLower(pr.Author)]
	if !ok {
		lookup.perm, lookup.err = p.client.GetCollaboratorPermission(ctx, p.opts.Owner, p.opts.Repo, pr.Author)
		p.perms[strings.ToLower(pr.Author)] = lookup
	}
	perm, err := lookup.perm, lookup.err
	switch {
	case err == nil:
		role = normevent.MapGitHubPermission(perm)
	case errors.Is(err, forge.ErrNotFound):
		// Not a collaborator; OWNERS may still list the author.
	default:
		verified = false
		detail = "; role lookup failed: " + err.Error()
	}
	repo := p.opts.Owner + "/" + p.opts.Repo
	ev := &normevent.Event{
		Repo: repo,
		Actor: normevent.Actor{
			ID:             pr.Author,
			Kind:           kind,
			Role:           role,
			RoleVerified:   &verified,
			IsEntityAuthor: true,
		},
		Source: normevent.Source{
			System:  normevent.SystemGitHub,
			RawType: "pull_request",
		},
		State: normevent.State{
			Labels: []string{},
			ChangeProposal: &normevent.ChangeProposal{
				ID:       pr.Number,
				HeadRepo: pr.HeadRepo,
				BaseRepo: repo,
				HeadRef:  pr.Head,
				BaseRef:  pr.Base,
				HeadSHA:  pr.HeadSHA,
				AuthorID: pr.Author,
				IsFork:   true,
			},
		},
	}
	detail = "role " + string(role) + detail
	if kind == normevent.ActorBot {
		detail = "bot " + detail
	}
	if p.opts.Authorize == nil {
		return false, detail + "; no authorization gate"
	}
	return p.opts.Authorize(ev), detail
}

// isReviewBot reports whether login is one of the review App's logins.
func (p *Poller) isReviewBot(login string) bool {
	for _, bot := range p.opts.ReviewBotLogins {
		if bot != "" && strings.EqualFold(bot, login) {
			return true
		}
	}
	return false
}

// isReviewAppComment reports whether c was created by the review App: its
// author is a review bot login and the App client ID matches.
func (p *Poller) isReviewAppComment(c forge.IssueComment) bool {
	return p.opts.ReviewAppClientID != "" && c.AppClientID == p.opts.ReviewAppClientID && p.isReviewBot(c.Author)
}

// finished reports whether the review App already reviewed the PR's head
// commit: a submitted formal review against it, an App-authored sticky
// comment whose current section names it, an App-authored review-skip
// note for it (the route found the diff unchanged since the last review),
// or an App-authored status comment whose run finished with success or
// ended skipped for it. The status comment covers a review that ended
// without leaving a head-keyed review record (a failure notice, or a
// pre-script skip). It reads the comments evaluate listed.
func (p *Poller) finished(ctx context.Context, pr forge.ChangeProposal) (bool, error) {
	reviews, err := p.client.ListPullRequestReviews(ctx, p.opts.Owner, p.opts.Repo, pr.Number)
	if err != nil {
		return false, fmt.Errorf("list reviews: %w", err)
	}
	for _, r := range reviews {
		if p.isReviewBot(r.User) && r.SubmittedAt != "" && r.CommitID == pr.HeadSHA {
			return true, nil
		}
	}
	for _, c := range p.comments {
		if !p.isReviewAppComment(c) || !strings.Contains(c.Body, reviewMarker) {
			continue
		}
		current, _, _ := strings.Cut(c.Body, stickyHistoryStart)
		if m := headSHARe.FindStringSubmatch(current); m != nil && shaMatches(pr.HeadSHA, m[1]) {
			return true, nil
		}
	}
	for _, c := range p.comments {
		if !p.isReviewAppComment(c) || !strings.Contains(c.Body, reviewSkipMarker) {
			continue
		}
		for _, m := range reviewSkipHeadRe.FindAllStringSubmatch(c.Body, -1) {
			if shaMatches(pr.HeadSHA, m[1]) {
				return true, nil
			}
		}
	}
	for _, c := range p.comments {
		if !p.isReviewAppComment(c) {
			continue
		}
		for _, re := range []*regexp.Regexp{statusSuccessRe, statusSkippedRe} {
			if m := re.FindStringSubmatch(c.Body); m != nil && shaMatches(pr.HeadSHA, m[1]) {
				return true, nil
			}
		}
	}
	return false, nil
}

// jobState returns ReasonRunning when a job of the PR head's dispatch is
// not completed in a poll workflow run that loadJobs lists, ReasonFinished
// when its review job succeeded there, or "". A dispatch job is listed
// under its bare name until the called workflow expands it, and as
// "<name> / <called job>" after.
func (p *Poller) jobState(ctx context.Context, pr forge.ChangeProposal) (string, error) {
	if err := p.loadJobs(ctx); err != nil {
		return "", err
	}
	name := ReviewJobName(pr.Number, pr.HeadSHA)
	prefix := name + " / "
	finished := false
	for _, j := range p.jobs {
		called, ok := strings.CutPrefix(j.Name, prefix)
		if !ok && j.Name != name {
			continue
		}
		if j.Status != "completed" {
			return ReasonRunning, nil
		}
		if ok && called == reviewJob && j.Conclusion == "success" {
			// A successful review job is a finished review even when its
			// comment is not visible yet; starting another would review
			// the same head twice.
			finished = true
		}
	}
	if finished {
		return ReasonFinished, nil
	}
	return "", nil
}

// statusCommentRunning reports whether an App-authored status comment for
// the PR's head commit belongs to a run that is still in progress. A
// terminal status comment's run has finished its review and is not read.
// A run the forge reports as not found (deleted, or older than the run
// retention) is not running; any other lookup error fails closed.
func (p *Poller) statusCommentRunning(ctx context.Context, pr forge.ChangeProposal) (bool, error) {
	for _, c := range p.comments {
		if !p.isReviewAppComment(c) || strings.Contains(c.Body, statusTerminalMarker) {
			continue
		}
		m := statusMarkerRe.FindStringSubmatch(c.Body)
		if m == nil {
			continue
		}
		cm := statusCommitRe.FindStringSubmatch(c.Body)
		if cm == nil || !shaMatches(pr.HeadSHA, cm[1]) {
			continue
		}
		runID, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		run, err := p.client.GetWorkflowRun(ctx, p.opts.Owner, p.opts.Repo, runID)
		if errors.Is(err, forge.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("get status comment run %d: %w", runID, err)
		}
		if run != nil && run.Status != "completed" {
			return true, nil
		}
	}
	return false, nil
}

// loadJobs lists, once per cycle, the jobs of the poll workflow's runs
// still in progress created since the poll was enabled. Only schedule and
// workflow_dispatch runs on the default branch count: they run the
// workflow file of the default branch, so a pull request cannot fake their
// jobs.
func (p *Poller) loadJobs(ctx context.Context) error {
	if p.runsLoaded {
		return nil
	}
	seen := map[int]bool{}
	var runs []forge.WorkflowRun
	for _, status := range runningStatuses {
		list, err := p.client.ListWorkflowRunsByStatus(ctx, p.opts.Owner, p.opts.Repo, WorkflowFile, status, p.opts.Since)
		if err != nil {
			return fmt.Errorf("list %s poll workflow runs: %w", status, err)
		}
		for _, r := range list {
			if seen[r.ID] || !pollRunEvents[r.Event] || r.HeadBranch != p.opts.DefaultBranch {
				continue
			}
			seen[r.ID] = true
			runs = append(runs, r)
		}
	}
	var jobs []forge.WorkflowJob
	for _, r := range runs {
		list, err := p.client.ListWorkflowRunJobs(ctx, p.opts.Owner, p.opts.Repo, r.ID)
		if err != nil {
			if errors.Is(err, forge.ErrJobsListingNotFoundFirstPage) {
				// A run that has not started may 404 its job listing; it
				// has no review job yet.
				continue
			}
			return fmt.Errorf("list jobs of poll workflow run %d: %w", r.ID, err)
		}
		for _, j := range list {
			jobs = append(jobs, j)
		}
	}
	p.jobs = jobs
	p.runsLoaded = true
	return nil
}

// shaMatches reports whether prefix is a prefix (at least 7 characters)
// of the full SHA full.
func shaMatches(full, prefix string) bool {
	return len(prefix) >= 7 && strings.HasPrefix(strings.ToLower(full), strings.ToLower(prefix))
}

// updatedAt parses the PR's updated_at, or returns the zero time.
func updatedAt(pr forge.ChangeProposal) time.Time {
	t, err := time.Parse(time.RFC3339, pr.UpdatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// payloadPR is the pull_request object of a dispatch's event_payload.
type payloadPR struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		Repo struct {
			FullName string `json:"full_name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

// executionRef builds the review dispatch for pr.
func (p *Poller) executionRef(pr forge.ChangeProposal) (harnessdispatch.ExecutionRef, error) {
	var body payloadPR
	body.Number = pr.Number
	body.HTMLURL = pr.URL
	body.User.Login = pr.Author
	body.Head.SHA = pr.HeadSHA
	body.Head.Ref = pr.Head
	body.Head.Repo.FullName = pr.HeadRepo
	body.Head.Repo.Owner.Login = pr.HeadOwner
	body.Base.SHA = pr.BaseSHA
	body.Base.Ref = pr.Base
	body.Base.Repo.FullName = pr.BaseRepo
	payload, err := json.Marshal(struct {
		PullRequest payloadPR `json:"pull_request"`
	}{body})
	if err != nil {
		return harnessdispatch.ExecutionRef{}, fmt.Errorf("marshal event payload for PR #%d: %w", pr.Number, err)
	}
	repo := p.opts.Owner + "/" + p.opts.Repo
	return harnessdispatch.ExecutionRef{
		Agent:         reviewAgent,
		Role:          reviewAgent,
		SourceRepo:    repo,
		EventType:     "pull_request",
		EventPayload:  payload,
		TriggerSource: TriggerSource,
		StatusRepo:    repo,
		StatusNumber:  strconv.Itoa(pr.Number),
	}, nil
}
