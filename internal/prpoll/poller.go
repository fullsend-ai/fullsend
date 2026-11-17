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
// comment and status comments) and from the poll workflow's own jobs, never
// from comment text anyone can post.
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
	// "Review (#7@<sha>) / Route" and "Review (#7@<sha>) / Review". The
	// poll reads them back to tell running, finished and failed attempts
	// apart; the workflow must render the same name (see ReviewJobName).
	ReviewJobNameFormat = "Review (#%d@%s)"

	// routeJob and reviewJob are the names of reusable-dispatch.yml's
	// route job and of the built-in review job it runs.
	routeJob  = "Route"
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
)

// failedRunLookback bounds the completed (failed or cancelled) poll
// workflow runs whose jobs loadJobs lists: only runs created in the last
// failedRunLookback, or since Since when that is later. Without it the
// listing grows with every failed run since the poll was enabled. A failed
// review job older than the window no longer counts against
// MaxAttempts, so a head whose attempts ran out is retried at most once
// per window.
const failedRunLookback = 72 * time.Hour

// runningStatuses are the workflow run statuses whose jobs may still run.
var runningStatuses = []string{"queued", "in_progress", "waiting", "requested", "pending"}

// failedConclusions are the run conclusions that can contain a review job
// that did not succeed. A successful run has no failed job.
var failedConclusions = []string{"failure", "cancelled", "timed_out", "startup_failure"}

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
	// MaxAttempts caps the failed review jobs for one head commit.
	MaxAttempts int
	// ReviewBotLogins are the logins of fullsend's review App.
	ReviewBotLogins []string
	// ReviewAppClientID is the client ID of fullsend's review App. Empty
	// disables the comment-based checks, which cannot be verified then.
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

// runJob is a poll workflow job with the ID of the run that holds it.
type runJob struct {
	forge.WorkflowJob
	runID int
}

// Poller runs fork PR review poll cycles.
type Poller struct {
	client Client
	opts   Options

	runsLoaded bool
	jobs       []runJob
	// comments are the issue comments of the pull request being
	// evaluated, listed by finished and reused by statusCommentRunning.
	comments []forge.IssueComment
}

// New returns a Poller over client.
func New(client Client, opts Options) *Poller {
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Poller{client: client, opts: opts}
}

// decision is the outcome for one pull request.
type decision struct {
	pr     forge.ChangeProposal
	reason string
	detail string
}

// Run executes one poll cycle and returns the dispatches to start, oldest
// updated_at first.
func (p *Poller) Run(ctx context.Context) ([]harnessdispatch.ExecutionRef, error) {
	start := p.opts.Now()
	var callsBefore int64
	if p.opts.APICalls != nil {
		callsBefore = p.opts.APICalls()
	}
	if p.opts.ReviewAppClientID == "" {
		fmt.Fprintf(p.opts.Log, "warning: the review App client ID is not set; comment-based finished and running checks are disabled\n")
	}

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

	for _, d := range decisions {
		line := fmt.Sprintf("PR #%d head %s: %s", d.pr.Number, shortSHA(d.pr.HeadSHA), d.reason)
		if d.detail != "" {
			line += " (" + d.detail + ")"
		}
		fmt.Fprintln(p.opts.Log, line)
	}
	summary := fmt.Sprintf("fork PR review poll: %d open PRs, %d dispatched", len(prs), len(refs))
	if p.opts.APICalls != nil {
		summary += fmt.Sprintf(", %d GitHub API calls", p.opts.APICalls()-callsBefore)
	}
	summary += fmt.Sprintf(", wall time %s", p.opts.Now().Sub(start).Round(time.Millisecond))
	fmt.Fprintln(p.opts.Log, summary)
	return refs, nil
}

// evaluate returns the decision reason for one pull request and an
// optional detail. Cheap checks on the listing run before any API call.
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
	if ok, detail := p.authorized(ctx, pr); !ok {
		return ReasonDenied, detail
	}

	p.comments = nil
	finished, err := p.finished(ctx, pr)
	if err != nil {
		return ReasonLookupFailed, err.Error()
	}
	if finished {
		return ReasonFinished, ""
	}

	state, attempts, err := p.jobState(ctx, pr)
	if err != nil {
		return ReasonLookupFailed, err.Error()
	}
	if state != "" {
		return state, ""
	}
	if attempts >= p.opts.MaxAttempts {
		return ReasonAttemptsExhausted, fmt.Sprintf("%d failed attempts", attempts)
	}
	return ReasonDispatch, ""
}

// authorized asks the dispatch core whether pr's author may request a
// review. It builds the event the GitHub event adapter builds for a
// pull_request (see harnessdispatch/input/ghaevent.go): the actor is the
// PR author as the API lists it, with the collaborator role read on every
// cycle. A lookup that is not a 404 leaves the role unverified, which the
// core denies; a bot author is decided by the core's bot rules (ADR
// 0107). The detail names the role.
func (p *Poller) authorized(ctx context.Context, pr forge.ChangeProposal) (bool, string) {
	kind := normevent.ActorHuman
	if strings.EqualFold(pr.AuthorType, "Bot") || strings.HasSuffix(strings.ToLower(pr.Author), "[bot]") {
		kind = normevent.ActorBot
	}
	role := normevent.RoleNone
	verified := true
	detail := ""
	perm, err := p.client.GetCollaboratorPermission(ctx, p.opts.Owner, p.opts.Repo, pr.Author)
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
// comment whose current section names it, or an App-authored review-skip
// note for it (the route found the diff unchanged since the last review).
// A running review status comment for the head commit is reported through
// jobState.
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
	if p.opts.ReviewAppClientID == "" {
		return false, nil
	}
	comments, err := p.client.ListIssueComments(ctx, p.opts.Owner, p.opts.Repo, pr.Number)
	if err != nil {
		return false, fmt.Errorf("list comments: %w", err)
	}
	p.comments = comments
	for _, c := range comments {
		if !p.isReviewAppComment(c) || !strings.Contains(c.Body, reviewMarker) {
			continue
		}
		current, _, _ := strings.Cut(c.Body, stickyHistoryStart)
		if m := headSHARe.FindStringSubmatch(current); m != nil && shaMatches(pr.HeadSHA, m[1]) {
			return true, nil
		}
	}
	for _, c := range comments {
		if !p.isReviewAppComment(c) || !strings.Contains(c.Body, reviewSkipMarker) {
			continue
		}
		for _, m := range reviewSkipHeadRe.FindAllStringSubmatch(c.Body, -1) {
			if shaMatches(pr.HeadSHA, m[1]) {
				return true, nil
			}
		}
	}
	return false, nil
}

// jobState returns ReasonRunning when a job of the PR head's dispatch is
// running, ReasonFinished when its review job succeeded, or "" with the
// number of failed attempts: the runs in which the dispatch's review or
// route job completed without success, one per run.
func (p *Poller) jobState(ctx context.Context, pr forge.ChangeProposal) (string, int, error) {
	if err := p.loadJobs(ctx); err != nil {
		return "", 0, err
	}
	prefix := ReviewJobName(pr.Number, pr.HeadSHA) + " / "
	finished := false
	failedRuns := map[int]bool{}
	for _, j := range p.jobs {
		called, ok := strings.CutPrefix(j.Name, prefix)
		if !ok {
			continue
		}
		if j.Status != "completed" {
			return ReasonRunning, 0, nil
		}
		switch {
		case called == reviewJob && j.Conclusion == "success":
			// A successful review job is a finished review even when its
			// comment is not visible yet; starting another would review
			// the same head twice.
			finished = true
		case (called == reviewJob || called == routeJob) && isFailedConclusion(j.Conclusion):
			failedRuns[j.runID] = true
		}
	}
	if finished {
		return ReasonFinished, 0, nil
	}

	running, err := p.statusCommentRunning(ctx, pr)
	if err != nil {
		return "", 0, err
	}
	if running {
		return ReasonRunning, 0, nil
	}
	return "", len(failedRuns), nil
}

// isFailedConclusion reports whether conclusion is one of
// failedConclusions.
func isFailedConclusion(conclusion string) bool {
	for _, c := range failedConclusions {
		if c == conclusion {
			return true
		}
	}
	return false
}

// statusCommentRunning reports whether an App-authored status comment for
// the PR's head commit belongs to a run that is still in progress. It uses
// the comments finished already listed.
func (p *Poller) statusCommentRunning(ctx context.Context, pr forge.ChangeProposal) (bool, error) {
	for _, c := range p.comments {
		if !p.isReviewAppComment(c) {
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
// that may hold a running or failed review job: runs still in progress
// created since the poll was enabled, and completed runs that did not
// succeed created within failedRunLookback (and since the poll was
// enabled).
func (p *Poller) loadJobs(ctx context.Context) error {
	if p.runsLoaded {
		return nil
	}
	failedSince := p.opts.Now().Add(-failedRunLookback)
	if p.opts.Since.After(failedSince) {
		failedSince = p.opts.Since
	}
	type listing struct {
		status string
		since  time.Time
	}
	var listings []listing
	for _, status := range runningStatuses {
		listings = append(listings, listing{status, p.opts.Since})
	}
	for _, status := range failedConclusions {
		listings = append(listings, listing{status, failedSince})
	}
	seen := map[int]bool{}
	var runs []forge.WorkflowRun
	for _, l := range listings {
		status := l.status
		list, err := p.client.ListWorkflowRunsByStatus(ctx, p.opts.Owner, p.opts.Repo, WorkflowFile, status, l.since)
		if err != nil {
			return fmt.Errorf("list %s poll workflow runs: %w", status, err)
		}
		for _, r := range list {
			if !seen[r.ID] {
				seen[r.ID] = true
				runs = append(runs, r)
			}
		}
	}
	var jobs []runJob
	for _, r := range runs {
		list, err := p.client.ListWorkflowRunJobs(ctx, p.opts.Owner, p.opts.Repo, r.ID)
		if err != nil {
			if r.Status != "completed" && errors.Is(err, forge.ErrJobsListingNotFoundFirstPage) {
				// A run that has not started may 404 its job listing; it
				// has no review job yet.
				continue
			}
			return fmt.Errorf("list jobs of poll workflow run %d: %w", r.ID, err)
		}
		for _, j := range list {
			jobs = append(jobs, runJob{WorkflowJob: j, runID: r.ID})
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
