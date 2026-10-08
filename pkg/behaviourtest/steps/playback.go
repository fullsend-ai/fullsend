package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/artifacts"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// playlistRepoPath and resultsRepoDir mirror the paths the dummy-playback
// runtime reads relative to the repo's .fullsend/ directory (see
// internal/runtime/dummy_playback.go: playlistRelPath = "results/playlist.yaml").
// They are duplicated here (rather than exported from internal/runtime)
// because committing the playlist and its result fixtures to the leased
// test repo is scenario setup, not runtime behaviour.
const (
	playlistRepoPath = ".fullsend/results/playlist.yaml"
	resultsRepoDir   = ".fullsend/results"
)

func registerPlaybackSteps(sc *godog.ScenarioContext) {
	sc.Step(`^a (\w+) agent that returns "([^"]+)"$`, func(ctx context.Context, agent, result string) (context.Context, error) {
		return ctx, givenAgentReturns(world.FromContext(ctx), agent, result)
	})
	sc.Step(`^an issue "([^"]+)" is created$`, func(ctx context.Context, name string) (context.Context, error) {
		return ctx, givenIssueFromFixture(world.FromContext(ctx), name)
	})
	sc.Step(`^the (\w+) agent is triggered$`, func(ctx context.Context, agent string) (context.Context, error) {
		return ctx, thenAgentIsTriggered(world.FromContext(ctx), agent)
	})
	sc.Step(`^the (\w+) agent completes successfully$`, func(ctx context.Context, agent string) (context.Context, error) {
		return ctx, thenAgentCompletes(world.FromContext(ctx), agent)
	})
	sc.Step(`^a pull request exists$`, func(ctx context.Context) (context.Context, error) {
		return ctx, thenPullRequestExists(world.FromContext(ctx))
	})
	sc.Step(`^the (\w+) agent submitted "([^"]+)"$`, func(ctx context.Context, agent, state string) (context.Context, error) {
		return ctx, thenAgentSubmittedReview(world.FromContext(ctx), agent, state)
	})
	sc.Step(`^the published repository matches fixture "([^"]+)"$`, func(ctx context.Context, name string) (context.Context, error) {
		return ctx, thenPublishedRepoMatchesFixture(world.FromContext(ctx), name)
	})
}

// givenAgentReturns records that the next time the named agent stage runs
// in this scenario, the dummy-playback runtime should serve the canned
// result directory under .fullsend/results/<result>/. Entries accumulate
// in playlist order; "an issue ... is created" commits the playlist (and
// the result fixtures it references) once all entries for the scenario
// have been declared.
func givenAgentReturns(w *world.World, agent, result string) error {
	if !w.IsPlaybackMode() {
		return fmt.Errorf("%q is a playback-only step; the @playback tag should have skipped this scenario outside the playback suite", "a "+agent+" agent that returns")
	}
	w.PlaybackEntries = append(w.PlaybackEntries, runtime.PlaybackEntry{Result: result})
	return nil
}

// issueFixture is the schema for e2e/behaviour/fixtures/issues/<name>/issue.yaml.
type issueFixture struct {
	Title string `yaml:"title"`
	Body  string `yaml:"body"`
}

// givenIssueFromFixture loads fixtures/issues/<name>/issue.yaml, commits
// its optional repo/ subdirectory into the leased test repo (simulating
// the pre-existing codebase the agent will work against), commits the
// playback playlist built by prior "a <agent> agent that returns" steps,
// creates the issue, and drains the (non-dispatching, bot-authored)
// issue-open workflow run. It does not itself trigger triage or set
// w.ScenarioStart: the caller must follow with "the issue is labeled
// 'ready-for-triage'" (whenIssueLabeled, triage.go), the pipeline's sole
// working trigger for this actor (#7957 review).
func givenIssueFromFixture(w *world.World, name string) error {
	if w.RepoOwner == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before creating an issue")
	}

	root, err := moduleRootDir()
	if err != nil {
		return fmt.Errorf("finding module root: %w", err)
	}
	fixturesRoot, err := findModuleSubdir(w.FixturesRoot)
	if err != nil {
		return fmt.Errorf("finding fixtures root: %w", err)
	}
	fixtureDir, err := fixtureSubpath(root, fixturesRoot, filepath.Join("fixtures", "issues"), name)
	if err != nil {
		return fmt.Errorf("issue fixture %q: %w", name, err)
	}

	issueFile := filepath.Join(fixtureDir, "issue.yaml")
	if err := rejectSymlinkComponents(fixtureDir, issueFile); err != nil {
		return fmt.Errorf("issue fixture %q: %w", name, err)
	}
	data, err := os.ReadFile(issueFile)
	if err != nil {
		return fmt.Errorf("reading issue fixture %q: %w", name, err)
	}
	var fix issueFixture
	if err := yaml.Unmarshal(data, &fix); err != nil {
		return fmt.Errorf("parsing issue fixture %q: %w", name, err)
	}
	if fix.Title == "" {
		return fmt.Errorf("issue fixture %q has no title", name)
	}

	repoDir := filepath.Join(fixtureDir, "repo")
	if info, statErr := os.Stat(repoDir); statErr == nil && info.IsDir() {
		if err := commitDirTree(w, repoDir, ""); err != nil {
			return fmt.Errorf("committing fixture repo files for %q: %w", name, err)
		}
	}

	if w.IsPlaybackMode() && len(w.PlaybackEntries) > 0 && !w.PlaybackCommitted {
		if err := commitPlaylist(w, root, fixturesRoot); err != nil {
			return fmt.Errorf("committing playback playlist: %w", err)
		}
	}

	trigger := time.Now()
	issue, err := w.SCM.CreateIssue(context.Background(), w.RepoOwner, w.RepoName, fix.Title, fix.Body)
	if err != nil {
		return fmt.Errorf("creating issue from fixture %q: %w", name, err)
	}
	w.IssueNumber = issue.Number
	w.IssueTitle = fix.Title

	// This issue is created with the e2e suite's own minted GitHub App
	// installation token (pkg/behaviourtest/drivers/install/doc.go), so
	// it is bot-authored. fullsend.yaml's "issues: opened" route only
	// dispatches triage for an actor holding collaborator write/triage
	// permission (internal/harnessdispatch/auth.go: IsAuthorized), and
	// that permission is never granted to the e2e installation bot
	// (doc.go: "Direct collaborator grants are not applied"). So
	// issue-open cannot be this pipeline's trigger — it never dispatches
	// for this actor, let alone twice, and a prior "issue-open is the
	// sole trigger" fix here was itself the bug (#7957 review). Mirror
	// createIssue's (triage.go) established choreography instead: drain
	// the non-dispatching issue-open workflow run first (so it cannot
	// race the next dispatch), then let the caller apply "ready-for-triage"
	// (shared whenIssueLabeled step) — a label-added event is dispatched
	// unconditionally regardless of actor identity, making it the
	// pipeline's one working trigger.
	return drainIssueOpenWorkflow(w, trigger)
}

// commitPlaylist commits .fullsend/results/playlist.yaml (pointing at the
// ordered list of result directories recorded in w.PlaybackEntries) and
// the result fixtures those entries reference, deduplicated by name so a
// result reused later in the playlist (e.g. two "review/approve" entries)
// is only committed once.
func commitPlaylist(w *world.World, root, fixturesRoot string) error {
	playlist := runtime.Playlist{Current: 1}
	seen := make(map[string]bool, len(w.PlaybackEntries))
	for _, entry := range w.PlaybackEntries {
		playlist.Results = append(playlist.Results, entry.Result)
		if seen[entry.Result] {
			continue
		}
		seen[entry.Result] = true
		resultDir, err := fixtureSubpath(root, fixturesRoot, "results", entry.Result)
		if err != nil {
			return fmt.Errorf("result fixture %q: %w", entry.Result, err)
		}
		if _, err := os.Stat(resultDir); err != nil {
			return fmt.Errorf("result fixture %q: %w", entry.Result, err)
		}
		dest := filepath.Join(resultsRepoDir, entry.Result)
		if err := commitDirTree(w, resultDir, dest); err != nil {
			return fmt.Errorf("committing result fixture %q: %w", entry.Result, err)
		}
	}

	data, err := yaml.Marshal(playlist)
	if err != nil {
		return fmt.Errorf("marshaling playlist: %w", err)
	}
	if err := w.SCM.CommitFile(context.Background(), w.RepoOwner, w.RepoName,
		playlistRepoPath, "behaviour: commit playback playlist", data); err != nil {
		return fmt.Errorf("committing playlist: %w", err)
	}
	w.PlaybackCommitted = true
	return nil
}

// fixtureSubpath joins fixturesRoot/category/name and verifies the result
// stays within fixturesRoot/category, rejecting a name that would escape
// it (e.g. via a ".." segment) and a name that reaches outside it through a
// symlinked path component. name identifies fixture/scenario data from a
// Gherkin step argument ("an issue %q is created", "a ... agent that
// returns %q"); a lexical containment check alone keeps a stray "../" from
// reaching outside the fixtures tree, but cannot see a symlink committed
// inside the fixture tree itself.
//
// root is the trusted boundary the symlink check is anchored to — the
// caller's module root (moduleRootDir), not fixturesRoot/category.
// fixturesRoot, each category directory ("results", "fixtures/issues"),
// and their own ancestors down to root are themselves checked-in fixture
// content, so a checked-in "results" symlink pointing outside the
// repository — combined with a name that happens to match a real
// subdirectory at that external target — would otherwise pass a check
// that only looked below fixturesRoot/category. Checking every component
// from root down catches a symlinked fixturesRoot, category directory, or
// any ancestor in between, not just a symlinked name (#7957 review).
func fixtureSubpath(root, fixturesRoot, category, name string) (string, error) {
	base := filepath.Clean(filepath.Join(fixturesRoot, category))
	full := filepath.Join(base, name)
	if full != base && !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("%q escapes fixtures root %q", name, base)
	}
	if err := rejectSymlinkComponents(root, full); err != nil {
		return "", err
	}
	return full, nil
}

// rejectSymlinkComponents Lstats every path component of full that lies
// strictly beneath base, erroring if any of them is a symlink. base itself
// is not checked: callers pass a trust boundary they have independently
// established (e.g. moduleRootDir, the checked-out repository root) rather
// than any part of the fixture tree. A plain Lstat on full alone is not
// enough: filepath.WalkDir and os.ReadFile both resolve a *non-final*
// symlink component transparently via the OS, so a path like
// base/link/subdir reaches whatever "link" points to even though
// Lstat(base/link/subdir) reports the real subdir at the symlink's target
// rather than the symlink itself. Checking every component from base down
// catches a symlinked ancestor anywhere in between, not just a symlinked
// leaf (#7957 review).
func rejectSymlinkComponents(base, full string) error {
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == "." {
		return nil
	}
	cur := base
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			// A missing path is reported by the caller's own stat/read;
			// nothing more to check once a component doesn't exist.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%q contains symlink component %q; refusing to follow it outside %q", full, cur, base)
		}
	}
	return nil
}

// commitDirTree walks localDir and commits every regular file it contains
// into the leased repo under repoPrefix (repo root when repoPrefix is
// empty), preserving the relative path. Symlinks and other non-regular
// entries are rejected rather than read: localDir comes from
// checked-in fixture trees, but os.ReadFile follows a symlink to
// wherever it points, including outside localDir — a fixture symlink to
// a runner-readable credential file would otherwise have its target
// contents committed (published) to the leased repository (#7957 review).
// WalkDir does not recurse into a symlinked directory (it is reported
// once, as a single non-dir entry), so this also covers a symlinked path
// component partway down the tree.
func commitDirTree(w *world.World, localDir, repoPrefix string) error {
	ctx := context.Background()
	return filepath.WalkDir(localDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("fixture entry %q is a symlink; refusing to read its target", path)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("fixture entry %q is not a regular file (mode %s)", path, d.Type())
		}
		rel, err := filepath.Rel(localDir, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dest := filepath.Join(repoPrefix, rel)
		return w.SCM.CommitFile(ctx, w.RepoOwner, w.RepoName, dest,
			fmt.Sprintf("behaviour: add fixture %s", dest), content)
	})
}

// dispatchVisibilityAttempts bounds how many times thenAgentIsTriggered
// polls CountHarnessDispatches before giving up.
//
// dispatchVisibilityAttempts * dispatchVisibilityRetryDelay matches the
// ~5-minute webhook-delivery tolerance used elsewhere in the CI drivers
// (see dispatchTimeout in the githubactions driver): CountHarnessDispatches
// settles an in-flight dispatch internally once it has a run to settle,
// but returns an immediate zero when the just-triggered run has not yet
// appeared at all in the CI API's workflow-run listing. Retrying here
// covers that initial visibility gap instead of failing on the first
// empty listing. Not-found errors are retried within the same budget.
const dispatchVisibilityAttempts = 20

// dispatchVisibilityRetryDelay is the delay between dispatch-visibility
// retries in thenAgentIsTriggered. Overridden in tests to avoid slow
// retry loops.
var dispatchVisibilityRetryDelay = 15 * time.Second

// thenAgentIsTriggered asserts that the named agent's harness dispatched
// at least once since w.ScenarioStart. CountHarnessDispatches settles
// pending runs before counting, so this also waits out an in-flight
// dispatch rather than racing it; the retry loop here additionally waits
// out a dispatch that has not become visible through the CI API yet.
// Not-found errors are retried too (#8123).
func thenAgentIsTriggered(w *world.World, agent string) error {
	agent = strings.TrimSpace(agent)
	if w.ScenarioStart.IsZero() {
		return fmt.Errorf("no workflow trigger time recorded")
	}
	var lastErr, notFoundErr error
	for attempt := 0; attempt < dispatchVisibilityAttempts; attempt++ {
		count, err := w.CI.CountHarnessDispatches(context.Background(), w.Org, w.RepoName, agent, w.ScenarioStart)
		switch {
		case err != nil && forge.IsNotFound(err):
			// The jobs endpoint has returned intermittent 404s right
			// after dispatch (#8123; cause unconfirmed). Any not-found
			// here is retried, including one from the run listing.
			notFoundErr = fmt.Errorf("checking %q agent dispatch (attempt %d/%d): %w",
				agent, attempt+1, dispatchVisibilityAttempts, err)
			lastErr = notFoundErr
		case err != nil:
			return fmt.Errorf("checking %q agent dispatch: %w", agent, err)
		case count >= 1:
			return nil
		default:
			lastErr = fmt.Errorf("%q agent was not dispatched since %s", agent, w.ScenarioStart.Format(time.RFC3339))
		}
		if attempt < dispatchVisibilityAttempts-1 {
			time.Sleep(dispatchVisibilityRetryDelay)
		}
	}
	if notFoundErr != nil && lastErr != notFoundErr {
		// Keep the last 404 visible when the final attempt was a zero count.
		return errors.Join(lastErr, notFoundErr)
	}
	return lastErr
}

// thenAgentCompletes waits for the named agent's harness run to succeed.
// It uses w.CI.WaitForHarnessAgentRound rather than reusing
// thenHarnessWorkflowCompletes's WaitForHarnessAgent (dispatch.go): the
// playback pipeline can dispatch the same agent's harness more than once
// in one scenario (e.g. review is retried after fix), and
// WaitForHarnessAgent's "latest eligible run wins" selection — designed
// for dual-dispatch resilience when a single occurrence is expected —
// picks a later round's run when an earlier round's own completion is
// asserted only after the later round has also already finished (#7957).
// WaitForHarnessAgentRound instead selects the earliest eligible run not
// already recorded in w.ConsumedHarnessRunIDs, and this step records the
// matched run there so a later call for the same agent cannot re-match
// it.
//
// It then advances w.ScenarioStart so a later wait for the same agent
// name scopes its run listing to after this point; the consumed-run set
// above is what actually prevents re-matching an earlier round now, so
// this is a scoping/diagnostics aid rather than a correctness
// requirement.
//
// The new ScenarioStart is anchored to the just-completed run's own
// CreatedAt, inclusive, rather than time.Now(): the next stage's dispatch
// is triggered by a label the post-script applies partway through this
// run, so it can start before this run reaches "completed" and
// WaitForHarnessAgentRound returns. Using time.Now() here would risk
// setting the window later than the next run's own CreatedAt and missing
// it. The boundary must stay inclusive rather than advancing past
// CreatedAt by a second: both CI drivers filter candidate runs with
// runTime.Before(after), so a distinct later run sharing this run's
// second-precision CreatedAt would otherwise be excluded by an
// exclusive boundary and never considered. w.ConsumedHarnessRunIDs (set
// just above) is what actually keeps this run from being re-matched, not
// the boundary (#7957 review).
//
// For the "code" agent specifically, this step also pins the code stage's
// published commit (pinCodeStagePublishedSHA) immediately once the round
// is observed to have completed — before going on to collect logs and
// download the round's own agent-result artifact. Deferring that pin to
// "a pull request exists" (thenPullRequestExists), the next Gherkin step,
// is not early enough: this step's own log collection and artifact
// download already cost real wall-clock time, during which review and
// fix, dispatched automatically by forge webhooks independently of this
// scenario's own step pacing, can race ahead and advance the same branch
// before that later step gets around to resolving it (#7957 review,
// fourth pass).
func thenAgentCompletes(w *world.World, agent string) error {
	agent = strings.TrimSpace(agent)
	if w.ScenarioStart.IsZero() {
		return fmt.Errorf("no workflow trigger time recorded")
	}
	ctx := context.Background()
	run, err := w.CI.WaitForHarnessAgentRound(ctx, w.Org, w.RepoName, agent, w.ScenarioStart, w.ConsumedHarnessRunIDs[agent])
	if err != nil {
		saveWorkflowRunLogs(ctx, w, agent, run)
		return err
	}
	if agent == "code" {
		if err := pinCodeStagePublishedSHA(ctx, w); err != nil {
			return err
		}
	}
	saveWorkflowRunLogs(ctx, w, agent, run)
	w.WorkflowRun = run
	if err := ensureRoundArtifacts(w, agent, run); err != nil {
		return err
	}

	if w.ConsumedHarnessRunIDs == nil {
		w.ConsumedHarnessRunIDs = make(map[string]map[int]bool)
	}
	if w.ConsumedHarnessRunIDs[agent] == nil {
		w.ConsumedHarnessRunIDs[agent] = make(map[int]bool)
	}
	w.ConsumedHarnessRunIDs[agent][run.ID] = true

	if created, err := time.Parse(time.RFC3339, run.CreatedAt); err == nil {
		w.ScenarioStart = created
		return nil
	}
	w.ScenarioStart = time.Now()
	return nil
}

// ensureRoundArtifacts downloads run's agent-result artifact and points
// w.ArtifactDir at it, caching the result in w.HarnessRunArtifactDirs by
// run ID so a later call for the same run reuses the download. Unlike
// ensureHarnessArtifacts (dispatch.go), which downloads once per scenario
// into a single w.ArtifactDir for the single-dispatch case, a playback
// scenario dispatches several stages (triage, code, review, fix) in one
// scenario: reusing ensureHarnessArtifacts's "already populated, skip"
// check would leave every stage after the first pointed at that first
// stage's artifacts and silently hide a later stage's download failure
// (#7957 review).
func ensureRoundArtifacts(w *world.World, agent string, run *forge.WorkflowRun) error {
	if dir, ok := w.HarnessRunArtifactDirs[run.ID]; ok {
		w.ArtifactDir = dir
		return nil
	}
	ctx := context.Background()
	dest, err := prepareArtifactDir()
	if err != nil {
		return err
	}
	if err := w.CI.DownloadNamedArtifactFromRun(ctx, w.Org, w.RepoName, run.ID, "fullsend-"+agent, dest); err != nil {
		_ = os.RemoveAll(dest)
		return fmt.Errorf("downloading %q agent-result artifact for run %d: %w", agent, run.ID, err)
	}
	if w.HarnessRunArtifactDirs == nil {
		w.HarnessRunArtifactDirs = make(map[int]string)
	}
	w.HarnessRunArtifactDirs[run.ID] = dest
	w.ArtifactDir = dest
	return nil
}

// thenPullRequestExists records the first open pull request on the
// leased repo in w.PRNumber, for later label/review assertions.
//
// It also pins the code stage's published commit SHA into
// w.StagePublishedSHA under the "code" stage key, as a fallback for when
// thenAgentCompletes's own pin (pinCodeStagePublishedSHA, run on
// completion of the "code" round) did not already do so. Both resolve the
// SHA via codeStageCommitSHA, which reads the pull request's own commit
// list rather than the head branch's live tip, so a review/fix push that
// has already advanced the branch cannot change the result (#7957
// review).
func thenPullRequestExists(w *world.World) error {
	if w.RepoOwner == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before PR operations")
	}
	ctx := context.Background()
	prs, err := w.SCM.ListOpenChangeProposals(ctx, w.RepoOwner, w.RepoName)
	if err != nil {
		return fmt.Errorf("listing open pull requests: %w", err)
	}
	if len(prs) == 0 {
		return fmt.Errorf("no open pull requests found on %s/%s", w.RepoOwner, w.RepoName)
	}
	w.PRNumber = prs[0].Number

	if _, pinned := w.StagePublishedSHA["code"]; pinned {
		return nil
	}

	sha, err := codeStageCommitSHA(ctx, w, prs[0].Number)
	if err != nil {
		return err
	}
	if w.StagePublishedSHA == nil {
		w.StagePublishedSHA = make(map[string]string)
	}
	w.StagePublishedSHA["code"] = sha
	return nil
}

// codeStageCommitSHA returns the commit the code stage published to pull
// request number: the first commit on the pull request. This relies on
// the playback scenario's own assumptions, not on a guarantee of the SCM
// interface: the code stage publishes a single commit, and the review and
// fix stages that follow (dispatched automatically by forge webhooks
// independently of this scenario's step pacing) only append further
// commits, never force-push or rebase. Under those assumptions — unlike
// the head branch's live tip — the first commit identifies the code
// round's publication however far the branch has advanced by the time
// this is read (#7957 review).
func codeStageCommitSHA(ctx context.Context, w *world.World, number int) (string, error) {
	commits, err := w.SCM.ListPullRequestCommits(ctx, w.RepoOwner, w.RepoName, number)
	if err != nil {
		return "", fmt.Errorf("listing pull request #%d commits: %w", number, err)
	}
	if len(commits) == 0 {
		return "", fmt.Errorf("pull request #%d has no commits", number)
	}
	return commits[0], nil
}

// pinCodeStagePublishedSHA resolves the code stage's published commit
// (codeStageCommitSHA) for the open pull request and pins it into
// w.StagePublishedSHA under the "code" stage key, unless a pin already
// exists. Called from thenAgentCompletes once the "code" round is
// observed to have completed, rather than deferring to
// thenPullRequestExists, the next Gherkin step.
func pinCodeStagePublishedSHA(ctx context.Context, w *world.World) error {
	if w.RepoOwner == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before PR operations")
	}
	if _, pinned := w.StagePublishedSHA["code"]; pinned {
		return nil
	}
	prs, err := w.SCM.ListOpenChangeProposals(ctx, w.RepoOwner, w.RepoName)
	if err != nil {
		return fmt.Errorf("listing open pull requests: %w", err)
	}
	if len(prs) == 0 {
		return fmt.Errorf("no open pull requests found on %s/%s", w.RepoOwner, w.RepoName)
	}
	sha, err := codeStageCommitSHA(ctx, w, prs[0].Number)
	if err != nil {
		return err
	}
	if w.StagePublishedSHA == nil {
		w.StagePublishedSHA = make(map[string]string)
	}
	w.StagePublishedSHA["code"] = sha
	return nil
}

// thenAgentSubmittedReview asserts that the review submitted during the
// current round on w.PRNumber has the expected state. The current round
// is anchored to w.WorkflowRun (set by thenAgentCompletes for the
// preceding "... agent completes successfully" step): fix and a review
// round progress independently, so an earlier round's review (e.g. an
// already-submitted APPROVED from a later round racing ahead) must not be
// mistaken for this round's. expectedState is matched case-insensitively
// against forge.PullRequestReview.State (which the GitHub/GitLab APIs
// report upper-cased, e.g. "CHANGES_REQUESTED").
//
// The live review's current State is not an immutable record of what
// this round actually submitted: before approving, postreview dismisses
// the author's earlier CHANGES_REQUESTED reviews
// (internal/cli.dismissStaleRequestChanges), so if a later round's
// approval races ahead of this assertion, this round's own
// CHANGES_REQUESTED review can already read DISMISSED by the time this
// runs. roundDisposition reads the round's own agent-result artifact —
// unaffected by a later round's dismissal — to check what this round
// actually intended first; the live review is then checked for a
// matching (or since-dismissed) submission.
func thenAgentSubmittedReview(w *world.World, agent, expectedState string) error {
	if w.PRNumber == 0 {
		return fmt.Errorf("no pull request recorded; call 'a pull request exists' first")
	}
	if w.WorkflowRun == nil {
		return fmt.Errorf("no harness run recorded; call 'the %s agent completes successfully' first", agent)
	}
	ctx := context.Background()

	wantState, err := roundDisposition(ctx, w, agent, w.WorkflowRun)
	if err != nil {
		return fmt.Errorf("determining %q agent's round disposition: %w", agent, err)
	}
	if !strings.EqualFold(wantState, expectedState) {
		return fmt.Errorf("agent-result artifact for %q agent's run %d implies review state %q, but the scenario expects %q",
			agent, w.WorkflowRun.ID, wantState, expectedState)
	}

	roundStart, err := time.Parse(time.RFC3339, w.WorkflowRun.CreatedAt)
	if err != nil {
		return fmt.Errorf("parsing harness run creation time %q: %w", w.WorkflowRun.CreatedAt, err)
	}
	reviews, err := w.SCM.ListPullRequestReviews(ctx, w.RepoOwner, w.RepoName, w.PRNumber)
	if err != nil {
		return fmt.Errorf("listing PR reviews: %w", err)
	}
	if len(reviews) == 0 {
		return fmt.Errorf("no reviews found on PR #%d", w.PRNumber)
	}
	review, err := reviewForRound(reviews, roundStart)
	if err != nil {
		return fmt.Errorf("finding %q agent's review on PR #%d: %w", agent, w.PRNumber, err)
	}
	got := strings.ToLower(review.State)
	want := strings.ToLower(expectedState)
	// A later round's own approval can dismiss this round's
	// CHANGES_REQUESTED review before this assertion runs; the
	// disposition check above already confirmed what this round itself
	// submitted, so DISMISSED here means that race happened, not that
	// the harness failed to submit the review. This tolerance is specific
	// to that earlier-round race: nothing in this flow dismisses an
	// approval, so an "approved" (or other non-changes-requested)
	// expectation must observe the live review actually in that state.
	// Otherwise a DISMISSED live review paired with an "approve" artifact
	// would pass without the round ever having submitted a live approval
	// (#7957 review).
	if got != want && !(want == "changes_requested" && got == "dismissed") {
		return fmt.Errorf("expected %q agent to submit review state %q but PR #%d's review for this round is %q", agent, want, w.PRNumber, got)
	}
	return nil
}

// reviewActionToState maps a review round's agent-result "action" field
// (ReviewResult.Action in internal/cli/postreview.go: "approve",
// "request-changes", "comment", "reject", "failure") to the forge review
// state it causes, for comparison against forge.PullRequestReview.State.
func reviewActionToState(action string) (string, bool) {
	switch strings.ToLower(action) {
	case "approve":
		return "APPROVED", true
	case "request-changes", "reject":
		return "CHANGES_REQUESTED", true
	case "comment":
		return "COMMENTED", true
	default:
		return "", false
	}
}

// roundDisposition returns the forge review state implied by the given
// run's own agent-result artifact — the canned result.json fixture the
// dummy-playback runtime serves, written to output/agent-result.json and
// uploaded as fullsend-<agent> (internal/runtime/dummy_playback.go) — an
// immutable record of what this specific round intended to submit.
//
// It downloads the artifact fresh into a scratch directory scoped to
// run.ID rather than reusing w.ArtifactDir: ensureHarnessArtifacts (used
// by the generic "... agent completes successfully" step) only ever
// downloads once per scenario, so a later round's call would otherwise
// silently reuse an earlier round's cached directory.
func roundDisposition(ctx context.Context, w *world.World, agent string, run *forge.WorkflowRun) (string, error) {
	dest, err := prepareArtifactDir()
	if err != nil {
		return "", fmt.Errorf("preparing scratch dir for %q agent-result artifact: %w", agent, err)
	}
	defer os.RemoveAll(dest)
	if err := w.CI.DownloadNamedArtifactFromRun(ctx, w.Org, w.RepoName, run.ID, "fullsend-"+agent, dest); err != nil {
		return "", fmt.Errorf("downloading %q agent-result artifact for run %d: %w", agent, run.ID, err)
	}
	data, err := artifacts.FindOutputFile(dest, "agent-result.json")
	if err != nil {
		return "", fmt.Errorf("reading agent-result.json for %q agent's run %d: %w", agent, run.ID, err)
	}
	var result struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("parsing agent-result.json for %q agent's run %d: %w", agent, run.ID, err)
	}
	state, ok := reviewActionToState(result.Action)
	if !ok {
		return "", fmt.Errorf("agent-result.json for %q agent's run %d has unrecognized action %q", agent, run.ID, result.Action)
	}
	return state, nil
}

// reviewForRound selects the review submitted during the current review
// round: the earliest review whose SubmittedAt is not before roundStart
// (the just-completed round's own harness-run CreatedAt). A prior round's
// review was necessarily submitted before this round's run even started,
// so anchoring to roundStart excludes it even if a later round's review
// already exists by the time this assertion runs.
//
// This selection strategy assumes a forge.PullRequestReview projection
// shaped like GitHub's, where every review (including an approval) carries
// a real SubmittedAt. It is a portability gap against GitLab's
// ListPullRequestReviews (internal/forge/gitlab/mr.go), which synthesizes
// APPROVED reviews from the approvals endpoint with no SubmittedAt
// (discarded here by the time.Parse error above) and maps every
// non-system MR note to COMMENTED, so an ordinary unrelated note could be
// selected instead. Harmless today because no playback driver wires live
// GitLab execution through this step (install.Driver's Non-GitHub install
// backends are still a TODO, docs/guides/dev/behaviour-drivers.md); must
// be replaced with forge-aware correlation, with coverage for both gaps,
// before a live GitLab playback driver is enabled (#7957 review).
func reviewForRound(reviews []forge.PullRequestReview, roundStart time.Time) (forge.PullRequestReview, error) {
	var best *forge.PullRequestReview
	var bestSubmitted time.Time
	for i := range reviews {
		submitted, err := time.Parse(time.RFC3339, reviews[i].SubmittedAt)
		if err != nil || submitted.Before(roundStart) {
			continue
		}
		if best == nil || submitted.Before(bestSubmitted) {
			best = &reviews[i]
			bestSubmitted = submitted
		}
	}
	if best == nil {
		return forge.PullRequestReview{}, fmt.Errorf("no review submitted at or after %s", roundStart.Format(time.RFC3339))
	}
	return *best, nil
}

// thenPublishedRepoMatchesFixture asserts that every regular file under
// fixtures/results/<name>/repo/ has actually been published, byte-for-byte,
// to w.PRNumber's pull request — not merely that the pipeline produced
// successful jobs, an open PR, and canned reviews, none of which establish
// that the stage published the expected content (#7957 review): because
// final approval is canned, an incorrect code or fix stage can still push
// some commit, trigger review, and satisfy every other assertion while
// leaving the original bug in place.
//
// The comparison is pinned to an immutable commit SHA, resolved once up
// front rather than re-resolved per file or left as a live branch-name
// read threaded through every GetFileContentAtRef call: a downstream
// stage's commit landing on the same branch between this step and (or
// even partway through) its own later call would otherwise be silently
// compared against instead of the revision this specific stage actually
// published (#7957 review).
//
// The pinned SHA preferentially comes from w.StagePublishedSHA, keyed by
// name's leading path segment (e.g. "code/bug-fix" -> "code") —
// populated by thenPullRequestExists at the earliest point the code
// stage's publication can be observed, before review and fix (dispatched
// automatically by forge webhooks, independently of this scenario's own
// step pacing) have a chance to advance the branch further (#7957
// review). A stage with no pinned entry (e.g. "fix", which nothing
// publishes to afterward in this scenario) falls back to resolving the
// pull request's head branch's current tip directly.
func thenPublishedRepoMatchesFixture(w *world.World, name string) error {
	if w.RepoOwner == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before PR operations")
	}
	if w.PRNumber == 0 {
		return fmt.Errorf("no pull request recorded; call 'a pull request exists' first")
	}
	ctx := context.Background()

	stage := strings.SplitN(name, "/", 2)[0]
	sha, pinned := w.StagePublishedSHA[stage]
	if pinned {
		delete(w.StagePublishedSHA, stage)
	} else {
		prs, err := w.SCM.ListOpenChangeProposals(ctx, w.RepoOwner, w.RepoName)
		if err != nil {
			return fmt.Errorf("listing open pull requests: %w", err)
		}
		var head string
		found := false
		for _, pr := range prs {
			if pr.Number == w.PRNumber {
				head = pr.Head
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("pull request #%d not found among open pull requests", w.PRNumber)
		}
		sha, err = w.SCM.GetBranchRef(ctx, w.RepoOwner, w.RepoName, head)
		if err != nil {
			return fmt.Errorf("resolving pull request #%d head branch %q: %w", w.PRNumber, head, err)
		}
	}

	root, err := moduleRootDir()
	if err != nil {
		return fmt.Errorf("finding module root: %w", err)
	}
	fixturesRoot, err := findModuleSubdir(w.FixturesRoot)
	if err != nil {
		return fmt.Errorf("finding fixtures root: %w", err)
	}
	resultDir, err := fixtureSubpath(root, fixturesRoot, "results", name)
	if err != nil {
		return fmt.Errorf("result fixture %q: %w", name, err)
	}
	repoDir := filepath.Join(resultDir, "repo")
	if info, statErr := os.Stat(repoDir); statErr != nil || !info.IsDir() {
		return fmt.Errorf("result fixture %q has no repo/ directory to compare against the published pull request", name)
	}

	if err := compareDirTree(ctx, w, repoDir, sha); err != nil {
		return fmt.Errorf("pull request #%d at %s does not match fixture %q: %w", w.PRNumber, sha, name, err)
	}
	return nil
}

// compareDirTree walks localDir and asserts that every regular file it
// contains is published, byte-for-byte, at the same relative path in the
// leased repo at ref. Symlinks and other non-regular entries are rejected
// rather than read, mirroring commitDirTree's fixture-containment rule.
func compareDirTree(ctx context.Context, w *world.World, localDir, ref string) error {
	return filepath.WalkDir(localDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("fixture entry %q is a symlink; refusing to read its target", path)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("fixture entry %q is not a regular file (mode %s)", path, d.Type())
		}
		rel, err := filepath.Rel(localDir, path)
		if err != nil {
			return err
		}
		want, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		got, err := w.SCM.GetFileContentAtRef(ctx, w.RepoOwner, w.RepoName, rel, ref)
		if err != nil {
			return fmt.Errorf("reading published %q at %s: %w", rel, ref, err)
		}
		if !bytes.Equal(want, got) {
			return fmt.Errorf("published %q at %s does not match fixture content (want %d bytes, got %d bytes)",
				rel, ref, len(want), len(got))
		}
		return nil
	})
}
