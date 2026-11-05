package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// ciDriverWithAction returns a mockCIDriver whose DownloadNamedArtifactFromRun
// writes an agent-result.json recording the given review action, simulating
// the round's own immutable disposition record that roundDisposition reads.
func ciDriverWithAction(action string) *mockCIDriver {
	return &mockCIDriver{
		downloadNamedArtifactFn: func(_ context.Context, _, _ string, _ int, _, destDir string) error {
			return os.WriteFile(filepath.Join(destDir, "agent-result.json"), []byte(fmt.Sprintf(`{"action":%q}`, action)), 0o644)
		},
	}
}

// fakePlaybackSCM implements scm.Driver for playback step tests. Embedding
// the nil interface (like mockCIDriver in dispatch_count_test.go) means
// only the methods playback.go actually calls need stubs; anything else
// panics on the nil interface, which surfaces an unexpected call
// immediately.
type fakePlaybackSCM struct {
	scm.Driver            // nil; only methods stubbed below are implemented
	createIssueFn         func(ctx context.Context, owner, repo, title, body string, labels ...string) (*forge.Issue, error)
	commitFileFn          func(ctx context.Context, owner, repo, path, message string, content []byte) error
	listPRsFn             func(ctx context.Context, owner, repo string) ([]forge.ChangeProposal, error)
	listReviewsFn         func(ctx context.Context, owner, repo string, number int) ([]forge.PullRequestReview, error)
	getBranchRefFn        func(ctx context.Context, owner, repo, branch string) (string, error)
	listCommitsFn         func(ctx context.Context, owner, repo string, number int) ([]string, error)
	getFileContentAtRefFn func(ctx context.Context, owner, repo, path, ref string) ([]byte, error)
	commitPaths           []string
	commitCallsTotal      int
}

func (f *fakePlaybackSCM) CreateIssue(ctx context.Context, owner, repo, title, body string, labels ...string) (*forge.Issue, error) {
	return f.createIssueFn(ctx, owner, repo, title, body, labels...)
}

func (f *fakePlaybackSCM) CommitFile(ctx context.Context, owner, repo, path, message string, content []byte) error {
	f.commitPaths = append(f.commitPaths, path)
	f.commitCallsTotal++
	if f.commitFileFn != nil {
		return f.commitFileFn(ctx, owner, repo, path, message, content)
	}
	return nil
}

func (f *fakePlaybackSCM) ListOpenChangeProposals(ctx context.Context, owner, repo string) ([]forge.ChangeProposal, error) {
	return f.listPRsFn(ctx, owner, repo)
}

func (f *fakePlaybackSCM) ListPullRequestReviews(ctx context.Context, owner, repo string, number int) ([]forge.PullRequestReview, error) {
	return f.listReviewsFn(ctx, owner, repo, number)
}

func (f *fakePlaybackSCM) GetBranchRef(ctx context.Context, owner, repo, branch string) (string, error) {
	return f.getBranchRefFn(ctx, owner, repo, branch)
}

func (f *fakePlaybackSCM) ListPullRequestCommits(ctx context.Context, owner, repo string, number int) ([]string, error) {
	return f.listCommitsFn(ctx, owner, repo, number)
}

func (f *fakePlaybackSCM) GetFileContentAtRef(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	return f.getFileContentAtRefFn(ctx, owner, repo, path, ref)
}

func playbackWorld() *world.World {
	return &world.World{Driver: install.NewPlaybackDriver(nil)}
}

// --- givenAgentReturns ---

func TestGivenAgentReturns_ErrorsOutsidePlaybackMode(t *testing.T) {
	w := &world.World{}
	err := givenAgentReturns(w, "triage", "triage/bug")
	require.Error(t, err)
	assert.Empty(t, w.PlaybackEntries)
}

func TestGivenAgentReturns_AppendsEntryInPlaybackMode(t *testing.T) {
	w := playbackWorld()
	require.NoError(t, givenAgentReturns(w, "triage", "triage/bug"))
	require.NoError(t, givenAgentReturns(w, "code", "code/bug-fix"))
	require.Equal(t, []runtime.PlaybackEntry{{Result: "triage/bug"}, {Result: "code/bug-fix"}}, w.PlaybackEntries)
}

// --- thenAgentIsTriggered ---

// speedUpDispatchVisibilityRetries shrinks the delay between
// thenAgentIsTriggered's dispatch-visibility retries so a test exercising
// the never-dispatched or eventually-dispatched paths doesn't block for
// the real ~5-minute window.
func speedUpDispatchVisibilityRetries(t *testing.T) {
	t.Helper()
	orig := dispatchVisibilityRetryDelay
	dispatchVisibilityRetryDelay = 0
	t.Cleanup(func() { dispatchVisibilityRetryDelay = orig })
}

func TestThenAgentIsTriggered_RequiresScenarioStart(t *testing.T) {
	err := thenAgentIsTriggered(&world.World{}, "triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trigger time")
}

func TestThenAgentIsTriggered_ErrorsWhenNotDispatched(t *testing.T) {
	speedUpDispatchVisibilityRetries(t)
	calls := 0
	w := &world.World{
		ScenarioStart: time.Now(),
		CI: &mockCIDriver{countFn: func(context.Context, string, string, string, time.Time) (int, error) {
			calls++
			return 0, nil
		}},
	}
	err := thenAgentIsTriggered(w, "triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not dispatched")
	assert.Equal(t, dispatchVisibilityAttempts, calls, "should exhaust the full retry budget before giving up")
}

func TestThenAgentIsTriggered_SucceedsOnDelayedVisibility(t *testing.T) {
	// Regression (#7957 review): CountHarnessDispatches returns an
	// immediate zero when the just-triggered run has not appeared yet in
	// the CI API's workflow-run listing (e.g. delayed webhook delivery).
	// thenAgentIsTriggered must retry instead of failing on that first
	// empty listing.
	speedUpDispatchVisibilityRetries(t)
	calls := 0
	w := &world.World{
		ScenarioStart: time.Now(),
		CI: &mockCIDriver{countFn: func(context.Context, string, string, string, time.Time) (int, error) {
			calls++
			if calls < 3 {
				return 0, nil
			}
			return 1, nil
		}},
	}
	require.NoError(t, thenAgentIsTriggered(w, "triage"))
	assert.Equal(t, 3, calls)
}

func TestThenAgentIsTriggered_SucceedsWhenDispatched(t *testing.T) {
	w := &world.World{
		ScenarioStart: time.Now(),
		CI: &mockCIDriver{countFn: func(context.Context, string, string, string, time.Time) (int, error) {
			return 1, nil
		}},
	}
	require.NoError(t, thenAgentIsTriggered(w, "triage"))
}

func TestThenAgentIsTriggered_PropagatesDriverError(t *testing.T) {
	w := &world.World{
		ScenarioStart: time.Now(),
		CI: &mockCIDriver{countFn: func(context.Context, string, string, string, time.Time) (int, error) {
			return 0, fmt.Errorf("API failure")
		}},
	}
	err := thenAgentIsTriggered(w, "triage")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API failure")
}

func TestThenAgentIsTriggered_RetriesTransientNotFound(t *testing.T) {
	// Regression (#8123): an intermittent jobs-endpoint 404 right after
	// dispatch is retried, not failed.
	speedUpDispatchVisibilityRetries(t)
	calls := 0
	w := &world.World{
		ScenarioStart: time.Now(),
		CI: &mockCIDriver{countFn: func(context.Context, string, string, string, time.Time) (int, error) {
			calls++
			if calls == 1 {
				return 0, fmt.Errorf("list workflow run jobs page 1: %w", forge.ErrNotFound)
			}
			return 1, nil
		}},
	}
	require.NoError(t, thenAgentIsTriggered(w, "triage"))
	assert.Equal(t, 2, calls)
}

func TestThenAgentIsTriggered_RetriesNotFoundThenZero(t *testing.T) {
	speedUpDispatchVisibilityRetries(t)
	calls := 0
	w := &world.World{
		ScenarioStart: time.Now(),
		CI: &mockCIDriver{countFn: func(context.Context, string, string, string, time.Time) (int, error) {
			calls++
			switch calls {
			case 1:
				return 0, fmt.Errorf("list workflow run jobs page 1: %w", forge.ErrNotFound)
			case 2:
				return 0, nil
			default:
				return 1, nil
			}
		}},
	}
	require.NoError(t, thenAgentIsTriggered(w, "triage"))
	assert.Equal(t, 3, calls)
}

func TestThenAgentIsTriggered_PersistentNotFoundExhaustsRetries(t *testing.T) {
	speedUpDispatchVisibilityRetries(t)
	calls := 0
	w := &world.World{
		ScenarioStart: time.Now(),
		CI: &mockCIDriver{countFn: func(context.Context, string, string, string, time.Time) (int, error) {
			calls++
			return 0, fmt.Errorf("list workflow run jobs page 1: %w", forge.ErrNotFound)
		}},
	}
	err := thenAgentIsTriggered(w, "triage")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotFound)
	assert.Contains(t, err.Error(), fmt.Sprintf(`checking "triage" agent dispatch (attempt %d/%d)`, dispatchVisibilityAttempts, dispatchVisibilityAttempts))
	assert.Equal(t, dispatchVisibilityAttempts, calls, "should exhaust the full retry budget before giving up")
}

func TestThenAgentIsTriggered_PersistentListingNotFoundExhaustsRetries(t *testing.T) {
	// A not-found from the run listing (missing workflow or repo) is
	// retried like a jobs-endpoint 404 and surfaces after the full budget.
	speedUpDispatchVisibilityRetries(t)
	calls := 0
	w := &world.World{
		ScenarioStart: time.Now(),
		CI: &mockCIDriver{countFn: func(context.Context, string, string, string, time.Time) (int, error) {
			calls++
			return 0, fmt.Errorf("list workflow runs: %w", forge.ErrNotFound)
		}},
	}
	err := thenAgentIsTriggered(w, "triage")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotFound)
	assert.Contains(t, err.Error(), "list workflow runs")
	assert.Equal(t, dispatchVisibilityAttempts, calls)
}

func TestThenAgentIsTriggered_NotFoundThenZeroKeepsNotFound(t *testing.T) {
	speedUpDispatchVisibilityRetries(t)
	calls := 0
	w := &world.World{
		ScenarioStart: time.Now(),
		CI: &mockCIDriver{countFn: func(context.Context, string, string, string, time.Time) (int, error) {
			calls++
			if calls == 1 {
				return 0, fmt.Errorf("list workflow run jobs page 1: %w", forge.ErrNotFound)
			}
			return 0, nil
		}},
	}
	err := thenAgentIsTriggered(w, "triage")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotFound, "an earlier 404 must stay in the error chain")
	assert.Contains(t, err.Error(), "was not dispatched since")
	assert.Contains(t, err.Error(), fmt.Sprintf("(attempt 1/%d)", dispatchVisibilityAttempts))
	assert.Equal(t, dispatchVisibilityAttempts, calls)
}

func TestThenAgentIsTriggered_NonRetryableErrorFailsImmediately(t *testing.T) {
	speedUpDispatchVisibilityRetries(t)
	calls := 0
	w := &world.World{
		ScenarioStart: time.Now(),
		CI: &mockCIDriver{countFn: func(context.Context, string, string, string, time.Time) (int, error) {
			calls++
			return 0, fmt.Errorf("list workflow run jobs page 1: %w", forge.ErrForbidden)
		}},
	}
	err := thenAgentIsTriggered(w, "triage")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrForbidden)
	assert.Equal(t, 1, calls, "non-not-found errors must not be retried")
}

// --- thenAgentCompletes ---

func TestThenAgentCompletes_RecordsConsumedRunAndAdvancesScenarioStart(t *testing.T) {
	w := &world.World{
		ScenarioStart: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		CI: &mockCIDriver{
			waitForHarnessAgentRoundFn: func(_ context.Context, _, _, _ string, _ time.Time, consumed map[int]bool) (*forge.WorkflowRun, error) {
				assert.Empty(t, consumed, "no round for this agent has completed yet")
				return &forge.WorkflowRun{ID: 100, CreatedAt: "2026-01-02T00:00:00Z"}, nil
			},
			downloadNamedArtifactFn: func(context.Context, string, string, int, string, string) error { return nil },
		},
	}
	require.NoError(t, thenAgentCompletes(w, "review"))
	assert.Equal(t, 100, w.WorkflowRun.ID)
	assert.True(t, w.ConsumedHarnessRunIDs["review"][100], "the matched run must be recorded as consumed")
	// Regression (#7957 review): the new boundary must be inclusive of
	// the just-completed run's own CreatedAt, not CreatedAt+1s — both CI
	// drivers filter with runTime.Before(after), so an exclusive boundary
	// would wrongly exclude a distinct later run sharing this run's
	// second-precision CreatedAt. ConsumedHarnessRunIDs (asserted above)
	// is what actually prevents re-matching this run.
	assert.Equal(t, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), w.ScenarioStart)
}

func TestThenAgentCompletes_SecondRoundExcludesFirstRoundConsumedID(t *testing.T) {
	// Regression (#7957 review): a second "the review agent completes
	// successfully" call for the same agent must pass along the first
	// round's already-consumed run ID so WaitForHarnessAgentRound does
	// not re-match it.
	w := &world.World{
		ScenarioStart:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ConsumedHarnessRunIDs: map[string]map[int]bool{"review": {100: true}},
		CI: &mockCIDriver{
			waitForHarnessAgentRoundFn: func(_ context.Context, _, _, _ string, _ time.Time, consumed map[int]bool) (*forge.WorkflowRun, error) {
				assert.True(t, consumed[100], "the first round's run ID must be passed through as consumed")
				return &forge.WorkflowRun{ID: 200, CreatedAt: "2026-01-02T01:00:00Z"}, nil
			},
			downloadNamedArtifactFn: func(context.Context, string, string, int, string, string) error { return nil },
		},
	}
	require.NoError(t, thenAgentCompletes(w, "review"))
	assert.Equal(t, 200, w.WorkflowRun.ID)
	assert.True(t, w.ConsumedHarnessRunIDs["review"][100])
	assert.True(t, w.ConsumedHarnessRunIDs["review"][200])
}

func TestThenAgentCompletes_SequentialStagesGetDistinctArtifactDirs(t *testing.T) {
	// Regression (#7957 review): ensureHarnessArtifacts (dispatch.go)
	// downloads once per scenario and then no-ops on every later call,
	// which would leave every stage after the first pointed at the first
	// stage's cached directory. thenAgentCompletes must download fresh
	// per run instead, caching by run ID.
	var downloadedRuns []int
	runs := map[string]*forge.WorkflowRun{
		"triage": {ID: 100, CreatedAt: "2026-01-02T00:00:00Z"},
		"code":   {ID: 200, CreatedAt: "2026-01-03T00:00:00Z"},
	}
	w := &world.World{
		ScenarioStart: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		RepoOwner:     "acme",
		RepoName:      "widgets",
		CI: &mockCIDriver{
			waitForHarnessAgentRoundFn: func(_ context.Context, _, _, agent string, _ time.Time, _ map[int]bool) (*forge.WorkflowRun, error) {
				return runs[agent], nil
			},
			downloadNamedArtifactFn: func(_ context.Context, _, _ string, runID int, _, destDir string) error {
				downloadedRuns = append(downloadedRuns, runID)
				return os.WriteFile(filepath.Join(destDir, fmt.Sprintf("run-%d.txt", runID)), []byte("x"), 0o644)
			},
		},
		SCM: &fakePlaybackSCM{
			listPRsFn: func(context.Context, string, string) ([]forge.ChangeProposal, error) {
				return []forge.ChangeProposal{{Number: 1, Head: "feature"}}, nil
			},
			listCommitsFn: func(context.Context, string, string, int) ([]string, error) {
				return []string{"codesha"}, nil
			},
		},
	}
	require.NoError(t, thenAgentCompletes(w, "triage"))
	triageDir := w.ArtifactDir
	require.NoError(t, thenAgentCompletes(w, "code"))
	codeDir := w.ArtifactDir

	assert.Equal(t, []int{100, 200}, downloadedRuns, "each stage must trigger its own download")
	assert.NotEqual(t, triageDir, codeDir, "each run must get its own artifact directory")
	assert.Len(t, w.HarnessRunArtifactDirs, 2)
	assert.Equal(t, triageDir, w.HarnessRunArtifactDirs[100])
	assert.Equal(t, codeDir, w.HarnessRunArtifactDirs[200])
}

func TestThenAgentCompletes_LaterStageDownloadFailurePropagates(t *testing.T) {
	// Regression (#7957 review): a later stage's artifact-download
	// failure must surface as an error, not be hidden behind an earlier
	// stage's already-populated w.ArtifactDir.
	calls := 0
	w := &world.World{
		ScenarioStart: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		RepoOwner:     "acme",
		RepoName:      "widgets",
		CI: &mockCIDriver{
			waitForHarnessAgentRoundFn: func(_ context.Context, _, _, _ string, _ time.Time, _ map[int]bool) (*forge.WorkflowRun, error) {
				calls++
				if calls == 1 {
					return &forge.WorkflowRun{ID: 100, CreatedAt: "2026-01-02T00:00:00Z"}, nil
				}
				return &forge.WorkflowRun{ID: 200, CreatedAt: "2026-01-03T00:00:00Z"}, nil
			},
			downloadNamedArtifactFn: func(_ context.Context, _, _ string, runID int, _, destDir string) error {
				if runID == 200 {
					return fmt.Errorf("artifact download failed")
				}
				return os.WriteFile(filepath.Join(destDir, "run.txt"), []byte("x"), 0o644)
			},
		},
		SCM: &fakePlaybackSCM{
			listPRsFn: func(context.Context, string, string) ([]forge.ChangeProposal, error) {
				return []forge.ChangeProposal{{Number: 1, Head: "feature"}}, nil
			},
			listCommitsFn: func(context.Context, string, string, int) ([]string, error) {
				return []string{"codesha"}, nil
			},
		},
	}
	require.NoError(t, thenAgentCompletes(w, "triage"))
	err := thenAgentCompletes(w, "code")
	require.Error(t, err, "the second stage's download failure must not be hidden by the first stage's cached ArtifactDir")
	assert.Contains(t, err.Error(), "artifact download failed")
}

// TestThenAgentCompletes_PinsCodeStageSHAEvenIfFixAlreadyAdvancedBranch is
// a regression test for the #7957 review: the code stage's published SHA
// must come from an immutable record rather than the head branch's live
// tip, because review and fix — dispatched automatically by forge
// webhooks independently of this scenario's own step pacing — can advance
// the branch before the code round's completion wait even returns. Here
// the fake SCM models a branch that fix has already advanced: the head
// branch resolves to "fixsha", and the pull request's commit list holds
// the code stage's commit first and fix's commit after. thenAgentCompletes
// must pin "codesha" (the first commit), never "fixsha".
func TestThenAgentCompletes_PinsCodeStageSHAEvenIfFixAlreadyAdvancedBranch(t *testing.T) {
	w := &world.World{
		ScenarioStart: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		RepoOwner:     "acme",
		RepoName:      "widgets",
		CI: &mockCIDriver{
			waitForHarnessAgentRoundFn: func(_ context.Context, _, _, _ string, _ time.Time, _ map[int]bool) (*forge.WorkflowRun, error) {
				return &forge.WorkflowRun{ID: 200, CreatedAt: "2026-01-02T00:00:00Z"}, nil
			},
			downloadNamedArtifactFn: func(_ context.Context, _, _ string, _ int, _, destDir string) error {
				return os.WriteFile(filepath.Join(destDir, "agent-result.json"), []byte(`{}`), 0o644)
			},
		},
		SCM: &fakePlaybackSCM{
			listPRsFn: func(context.Context, string, string) ([]forge.ChangeProposal, error) {
				return []forge.ChangeProposal{{Number: 1, Head: "feature"}}, nil
			},
			getBranchRefFn: func(context.Context, string, string, string) (string, error) {
				return "fixsha", nil
			},
			listCommitsFn: func(_ context.Context, _, _ string, number int) ([]string, error) {
				assert.Equal(t, 1, number)
				return []string{"codesha", "fixsha"}, nil
			},
		},
	}

	require.NoError(t, thenAgentCompletes(w, "code"))
	assert.Equal(t, "codesha", w.StagePublishedSHA["code"],
		"must pin the code stage's own commit, not the already-advanced branch tip")

	// "a pull request exists" runs next in the Gherkin scenario; it must
	// not re-resolve and overwrite the already-pinned SHA with whatever
	// the branch has since advanced to.
	require.NoError(t, thenPullRequestExists(w))
	assert.Equal(t, "codesha", w.StagePublishedSHA["code"],
		"thenPullRequestExists must not overwrite an already-pinned code-stage SHA")
}

// --- thenPullRequestExists ---

func TestThenPullRequestExists_RequiresRepo(t *testing.T) {
	err := thenPullRequestExists(&world.World{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestThenPullRequestExists_ErrorsWhenNoOpenPRs(t *testing.T) {
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		SCM: &fakePlaybackSCM{listPRsFn: func(context.Context, string, string) ([]forge.ChangeProposal, error) {
			return nil, nil
		}},
	}
	err := thenPullRequestExists(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no open pull requests")
}

func TestThenPullRequestExists_RecordsFirstOpenPR(t *testing.T) {
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		SCM: &fakePlaybackSCM{
			listPRsFn: func(context.Context, string, string) ([]forge.ChangeProposal, error) {
				return []forge.ChangeProposal{{Number: 7, Head: "agent/code"}, {Number: 8}}, nil
			},
			listCommitsFn: func(_ context.Context, _, _ string, number int) ([]string, error) {
				assert.Equal(t, 7, number, "must list the first PR's own commits")
				return []string{"deadbeef", "laterfix"}, nil
			},
		},
	}
	require.NoError(t, thenPullRequestExists(w))
	assert.Equal(t, 7, w.PRNumber)
	assert.Equal(t, "deadbeef", w.StagePublishedSHA["code"],
		"regression (#7957 review): must pin the code stage's published SHA at PR-discovery time, "+
			"before review/fix can advance the branch further before the later fixture assertion runs")
}

func TestThenPullRequestExists_PropagatesListCommitsError(t *testing.T) {
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		SCM: &fakePlaybackSCM{
			listPRsFn: func(context.Context, string, string) ([]forge.ChangeProposal, error) {
				return []forge.ChangeProposal{{Number: 7, Head: "agent/code"}}, nil
			},
			listCommitsFn: func(context.Context, string, string, int) ([]string, error) {
				return nil, fmt.Errorf("commit list lookup failed")
			},
		},
	}
	err := thenPullRequestExists(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit list lookup failed")
}

func TestThenPullRequestExists_ErrorsWhenPRHasNoCommits(t *testing.T) {
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		SCM: &fakePlaybackSCM{
			listPRsFn: func(context.Context, string, string) ([]forge.ChangeProposal, error) {
				return []forge.ChangeProposal{{Number: 7, Head: "agent/code"}}, nil
			},
			listCommitsFn: func(context.Context, string, string, int) ([]string, error) {
				return nil, nil
			},
		},
	}
	err := thenPullRequestExists(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no commits")
}

// --- thenAgentSubmittedReview ---

func TestThenAgentSubmittedReview_RequiresPRNumber(t *testing.T) {
	err := thenAgentSubmittedReview(&world.World{}, "review", "approved")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pull request recorded")
}

func TestThenAgentSubmittedReview_RequiresWorkflowRun(t *testing.T) {
	w := &world.World{PRNumber: 2}
	err := thenAgentSubmittedReview(w, "review", "approved")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no harness run recorded")
}

func TestThenAgentSubmittedReview_ErrorsWhenNoReviews(t *testing.T) {
	w := &world.World{
		PRNumber:    2,
		WorkflowRun: &forge.WorkflowRun{CreatedAt: "2026-01-01T00:00:00Z"},
		CI:          ciDriverWithAction("approve"),
		SCM: &fakePlaybackSCM{listReviewsFn: func(context.Context, string, string, int) ([]forge.PullRequestReview, error) {
			return nil, nil
		}},
	}
	err := thenAgentSubmittedReview(w, "review", "approved")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no reviews found")
}

func TestThenAgentSubmittedReview_MatchesCaseInsensitively(t *testing.T) {
	w := &world.World{
		PRNumber:    2,
		WorkflowRun: &forge.WorkflowRun{CreatedAt: "2026-01-01T00:00:00Z"},
		CI:          ciDriverWithAction("request-changes"),
		SCM: &fakePlaybackSCM{listReviewsFn: func(context.Context, string, string, int) ([]forge.PullRequestReview, error) {
			return []forge.PullRequestReview{{State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:05:00Z"}}, nil
		}},
	}
	require.NoError(t, thenAgentSubmittedReview(w, "review", "changes_requested"))
}

func TestThenAgentSubmittedReview_SelectsReviewForCurrentRound(t *testing.T) {
	// Regression (#7957 review): fix and a second review round progress
	// independently of this (first) round's assertion, so an APPROVED
	// review from the second round can already exist when this round
	// expects CHANGES_REQUESTED. Picking "the latest review" would pick
	// the wrong one and fail an otherwise-successful pipeline; anchoring
	// to this round's own harness-run CreatedAt must still select this
	// round's review.
	w := &world.World{
		PRNumber:    2,
		WorkflowRun: &forge.WorkflowRun{CreatedAt: "2026-01-01T00:00:00Z"},
		CI:          ciDriverWithAction("request-changes"),
		SCM: &fakePlaybackSCM{listReviewsFn: func(context.Context, string, string, int) ([]forge.PullRequestReview, error) {
			return []forge.PullRequestReview{
				{State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:05:00Z"},
				{State: "APPROVED", SubmittedAt: "2026-01-01T01:00:00Z"}, // second round, already in
			}, nil
		}},
	}
	require.NoError(t, thenAgentSubmittedReview(w, "review", "changes_requested"))
}

func TestThenAgentSubmittedReview_SelectsLaterRoundWhenRunAdvanced(t *testing.T) {
	w := &world.World{
		PRNumber:    2,
		WorkflowRun: &forge.WorkflowRun{CreatedAt: "2026-01-01T00:30:00Z"}, // second round's own run
		CI:          ciDriverWithAction("approve"),
		SCM: &fakePlaybackSCM{listReviewsFn: func(context.Context, string, string, int) ([]forge.PullRequestReview, error) {
			return []forge.PullRequestReview{
				{State: "CHANGES_REQUESTED", SubmittedAt: "2026-01-01T00:05:00Z"}, // first round
				{State: "APPROVED", SubmittedAt: "2026-01-01T01:00:00Z"},          // second round
			}, nil
		}},
	}
	require.NoError(t, thenAgentSubmittedReview(w, "review", "approved"))
}

func TestThenAgentSubmittedReview_MismatchErrors(t *testing.T) {
	w := &world.World{
		PRNumber:    2,
		WorkflowRun: &forge.WorkflowRun{CreatedAt: "2026-01-01T00:00:00Z"},
		CI:          ciDriverWithAction("approve"),
		SCM: &fakePlaybackSCM{listReviewsFn: func(context.Context, string, string, int) ([]forge.PullRequestReview, error) {
			return []forge.PullRequestReview{{State: "COMMENTED", SubmittedAt: "2026-01-01T00:05:00Z"}}, nil
		}},
	}
	err := thenAgentSubmittedReview(w, "review", "approved")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"approved"`)
	assert.Contains(t, err.Error(), `"commented"`)
}

func TestThenAgentSubmittedReview_DismissedRaceStillPasses(t *testing.T) {
	// Regression (#7957 review): postreview dismisses the author's
	// earlier CHANGES_REQUESTED review before submitting a later round's
	// approval. If that dismissal races ahead of this (first round's)
	// assertion, the live review reads DISMISSED instead of
	// CHANGES_REQUESTED. The round's own agent-result artifact still
	// records what this round actually submitted, so the assertion must
	// still pass instead of failing on the now-stale live state.
	w := &world.World{
		PRNumber:    2,
		WorkflowRun: &forge.WorkflowRun{ID: 55, CreatedAt: "2026-01-01T00:00:00Z"},
		CI:          ciDriverWithAction("request-changes"),
		SCM: &fakePlaybackSCM{listReviewsFn: func(context.Context, string, string, int) ([]forge.PullRequestReview, error) {
			return []forge.PullRequestReview{{State: "DISMISSED", SubmittedAt: "2026-01-01T00:05:00Z"}}, nil
		}},
	}
	require.NoError(t, thenAgentSubmittedReview(w, "review", "changes_requested"))
}

func TestThenAgentSubmittedReview_DispositionMismatchErrors(t *testing.T) {
	// A round whose own agent-result artifact implies a different
	// disposition than the scenario expects must fail here, even though
	// the live review's current state happens to equal the artifact's
	// disposition — the dismissed-race tolerance must not also mask a
	// genuine wrong-action bug.
	w := &world.World{
		PRNumber:    2,
		WorkflowRun: &forge.WorkflowRun{ID: 55, CreatedAt: "2026-01-01T00:00:00Z"},
		CI:          ciDriverWithAction("approve"),
		SCM: &fakePlaybackSCM{listReviewsFn: func(context.Context, string, string, int) ([]forge.PullRequestReview, error) {
			return []forge.PullRequestReview{{State: "APPROVED", SubmittedAt: "2026-01-01T00:05:00Z"}}, nil
		}},
	}
	err := thenAgentSubmittedReview(w, "review", "changes_requested")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "implies review state")
}

func TestThenAgentSubmittedReview_DismissedToleranceDoesNotCoverApproval(t *testing.T) {
	// Regression (#7957 review): the DISMISSED tolerance exists only for
	// an earlier CHANGES_REQUESTED round being dismissed by a later
	// round's approval. It must not also let a final "approve" artifact
	// pass when the live review itself reads DISMISSED instead of
	// APPROVED — nothing in this flow dismisses an approval, so a
	// DISMISSED live review here means the round never actually recorded
	// a live approval.
	w := &world.World{
		PRNumber:    2,
		WorkflowRun: &forge.WorkflowRun{ID: 55, CreatedAt: "2026-01-01T00:00:00Z"},
		CI:          ciDriverWithAction("approve"),
		SCM: &fakePlaybackSCM{listReviewsFn: func(context.Context, string, string, int) ([]forge.PullRequestReview, error) {
			return []forge.PullRequestReview{{State: "DISMISSED", SubmittedAt: "2026-01-01T00:05:00Z"}}, nil
		}},
	}
	err := thenAgentSubmittedReview(w, "review", "approved")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"approved"`)
	assert.Contains(t, err.Error(), `"dismissed"`)
}

// --- thenPublishedRepoMatchesFixture (#7957 review: test-inadequate) ---

func TestThenPublishedRepoMatchesFixture_RequiresRepo(t *testing.T) {
	err := thenPublishedRepoMatchesFixture(&world.World{}, "code/bug-fix")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestThenPublishedRepoMatchesFixture_RequiresPRNumber(t *testing.T) {
	w := &world.World{RepoOwner: "org", RepoName: "repo"}
	err := thenPublishedRepoMatchesFixture(w, "code/bug-fix")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pull request recorded")
}

func TestThenPublishedRepoMatchesFixture_PRNotFoundAmongOpenPRs(t *testing.T) {
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		PRNumber:  7,
		SCM: &fakePlaybackSCM{listPRsFn: func(context.Context, string, string) ([]forge.ChangeProposal, error) {
			return []forge.ChangeProposal{{Number: 8, Head: "other-branch"}}, nil
		}},
	}
	err := thenPublishedRepoMatchesFixture(w, "code/bug-fix")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found among open pull requests")
}

func TestThenPublishedRepoMatchesFixture_MatchesRealFixture(t *testing.T) {
	// Reads the real fixture tree at e2e/behaviour/results/code/bug-fix/repo
	// and serves its own content back for every path, so the comparison
	// must succeed.
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		FixturesRoot: "e2e/behaviour",
		PRNumber:     7,
		SCM: &fakePlaybackSCM{
			listPRsFn: func(context.Context, string, string) ([]forge.ChangeProposal, error) {
				return []forge.ChangeProposal{{Number: 7, Head: "agent/fix"}}, nil
			},
			getBranchRefFn: func(_ context.Context, _, _, branch string) (string, error) {
				assert.Equal(t, "agent/fix", branch)
				return "deadbeef", nil
			},
			getFileContentAtRefFn: func(_ context.Context, _, _, path, ref string) ([]byte, error) {
				assert.Equal(t, "deadbeef", ref, "the pinned SHA from GetBranchRef must be used, not the branch name")
				root, err := moduleRootDir()
				require.NoError(t, err)
				return os.ReadFile(filepath.Join(root, "e2e", "behaviour", "results", "code", "bug-fix", "repo", path))
			},
		},
	}
	require.NoError(t, thenPublishedRepoMatchesFixture(w, "code/bug-fix"))
}

func TestThenPublishedRepoMatchesFixture_MismatchErrors(t *testing.T) {
	// Regression (#7957 review): a negative case for the gap the finding
	// describes — the fix-stage PR still contains the code-stage version
	// (e.g. an incorrect fix that never actually applied the TOCTOU
	// correction). Serving the code-stage fixture's content while asserting
	// against the fix-stage fixture must fail instead of silently passing.
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		FixturesRoot: "e2e/behaviour",
		PRNumber:     7,
		SCM: &fakePlaybackSCM{
			listPRsFn: func(context.Context, string, string) ([]forge.ChangeProposal, error) {
				return []forge.ChangeProposal{{Number: 7, Head: "agent/fix"}}, nil
			},
			getBranchRefFn: func(context.Context, string, string, string) (string, error) {
				return "deadbeef", nil
			},
			getFileContentAtRefFn: func(_ context.Context, _, _, path, _ string) ([]byte, error) {
				root, err := moduleRootDir()
				require.NoError(t, err)
				// Still serving the code stage's (pre-fix, TOCTOU-buggy)
				// version, not the fix stage's.
				return os.ReadFile(filepath.Join(root, "e2e", "behaviour", "results", "code", "bug-fix", "repo", path))
			},
		},
	}
	err := thenPublishedRepoMatchesFixture(w, "fix/success")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match fixture")
}

func TestThenPublishedRepoMatchesFixture_UsesPinnedSHAEvenIfFixAdvancesBranch(t *testing.T) {
	// Regression (#7957 review): review and fix can progress automatically
	// (dispatched by forge webhooks) independently of how quickly this
	// scenario's own steps execute. If fix has already pushed its commit
	// onto the same PR branch by the time the code-stage's own content
	// assertion runs, re-resolving the branch's live tip at that point
	// would compare fix's content against the code-stage fixture and fail
	// despite both stages having published correctly. thenPullRequestExists
	// pins the code stage's SHA at PR-discovery time (before fix could have
	// advanced the branch); thenPublishedRepoMatchesFixture must use that
	// pinned SHA instead of GetBranchRef's current (already-advanced)
	// answer.
	w := &world.World{
		RepoOwner:         "org",
		RepoName:          "repo",
		FixturesRoot:      "e2e/behaviour",
		StagePublishedSHA: map[string]string{"code": "codesha"},
		PRNumber:          7,
		SCM: &fakePlaybackSCM{
			getBranchRefFn: func(context.Context, string, string, string) (string, error) {
				// Simulates fix having already advanced the branch: if
				// this is ever consulted for the code-stage assertion,
				// the test must fail rather than silently pass.
				return "fixsha-already-advanced", nil
			},
			getFileContentAtRefFn: func(_ context.Context, _, _, path, ref string) ([]byte, error) {
				assert.Equal(t, "codesha", ref, "must use the SHA pinned at PR-discovery time, not the live branch tip")
				root, err := moduleRootDir()
				require.NoError(t, err)
				return os.ReadFile(filepath.Join(root, "e2e", "behaviour", "results", "code", "bug-fix", "repo", path))
			},
		},
	}
	require.NoError(t, thenPublishedRepoMatchesFixture(w, "code/bug-fix"))
	assert.NotContains(t, w.StagePublishedSHA, "code", "the pinned entry must be consumed, not reused by a later stage's check")
}

func TestThenPublishedRepoMatchesFixture_MissingRepoDirErrors(t *testing.T) {
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		FixturesRoot: "e2e/behaviour",
		PRNumber:     7,
		SCM: &fakePlaybackSCM{
			listPRsFn: func(context.Context, string, string) ([]forge.ChangeProposal, error) {
				return []forge.ChangeProposal{{Number: 7, Head: "agent/fix"}}, nil
			},
			getBranchRefFn: func(context.Context, string, string, string) (string, error) {
				return "deadbeef", nil
			},
		},
	}
	// "review/approve" has a result.json fixture but no repo/ subdirectory.
	err := thenPublishedRepoMatchesFixture(w, "review/approve")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no repo/ directory")
}

// --- givenIssueFromFixture / commitPlaylist (integration over real fixtures) ---

func TestGivenIssueFromFixture_RequiresRepo(t *testing.T) {
	err := givenIssueFromFixture(&world.World{}, "concurrent-map-crash")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo configured")
}

func TestGivenIssueFromFixture_CommitsPlaylistAndRepoFiles(t *testing.T) {
	scm := &fakePlaybackSCM{
		createIssueFn: func(_ context.Context, _, _, title, _ string, _ ...string) (*forge.Issue, error) {
			return &forge.Issue{Number: 42, Title: title}, nil
		},
	}
	var drainedCalls int
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		FixturesRoot: "e2e/behaviour",
		SCM:          scm,
		Driver:       install.NewPlaybackDriver(nil),
		CI: &mockCIDriver{
			waitForWorkflowFn: func(context.Context, string, string, string, time.Time, string) (*forge.WorkflowRun, error) {
				drainedCalls++
				return &forge.WorkflowRun{ID: 1}, nil
			},
		},
	}
	require.NoError(t, givenAgentReturns(w, "triage", "triage/bug"))
	// The same result reused twice must only be committed once.
	require.NoError(t, givenAgentReturns(w, "review", "review/approve"))
	require.NoError(t, givenAgentReturns(w, "review", "review/approve"))

	require.NoError(t, givenIssueFromFixture(w, "concurrent-map-crash"))

	assert.Equal(t, 42, w.IssueNumber)
	assert.Contains(t, w.IssueTitle, "concurrent map writes")
	assert.True(t, w.PlaybackCommitted)
	assert.Equal(t, 1, drainedCalls, "the non-dispatching issue-open workflow run must be drained")
	// Regression (#7957 review): the e2e suite's issue is bot-authored
	// via a minted installation token, so issues.opened never dispatches
	// triage for it (IsAuthorized requires collaborator permission with
	// no bot exception for issue-open). givenIssueFromFixture must not
	// set ScenarioStart itself — the caller's subsequent "the issue is
	// labeled 'ready-for-triage'" step (whenIssueLabeled) is the
	// pipeline's sole working trigger and owns the trigger boundary.
	assert.True(t, w.ScenarioStart.IsZero(), "ScenarioStart must be left unset for the caller's label step to establish the trigger boundary")

	assert.Contains(t, scm.commitPaths, "main.go", "the issue fixture's repo/main.go should be committed to the repo root")
	assert.Contains(t, scm.commitPaths, ".fullsend/results/playlist.yaml")
	assert.Contains(t, scm.commitPaths, ".fullsend/results/triage/bug/result.json")
	assert.Contains(t, scm.commitPaths, ".fullsend/results/review/approve/result.json")

	reviewApproveCommits := 0
	for _, p := range scm.commitPaths {
		if p == ".fullsend/results/review/approve/result.json" {
			reviewApproveCommits++
		}
	}
	assert.Equal(t, 1, reviewApproveCommits, "a result reused twice in the playlist should only be committed once")
}

func TestGivenIssueFromFixture_UnknownFixtureErrors(t *testing.T) {
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		FixturesRoot: "e2e/behaviour",
		SCM:          &fakePlaybackSCM{},
	}
	err := givenIssueFromFixture(w, "does-not-exist")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading issue fixture")
}

// --- fixtureSubpath / commitDirTree containment (#7957 review) ---

func TestFixtureSubpath_RejectsTraversal(t *testing.T) {
	_, err := fixtureSubpath("/fixtures-root", "/fixtures-root", "issues", "../../../../etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes fixtures root")
}

func TestFixtureSubpath_AllowsNestedName(t *testing.T) {
	got, err := fixtureSubpath("/fixtures-root", "/fixtures-root", "results", filepath.Join("review", "approve"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/fixtures-root", "results", "review", "approve"), got)
}

func TestGivenIssueFromFixture_RejectsPathTraversalName(t *testing.T) {
	// Regression (#7957 review): an issue-fixture name must not be able
	// to escape the fixtures tree via "../" segments.
	scm := &fakePlaybackSCM{}
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		FixturesRoot: "e2e/behaviour",
		SCM:          scm,
	}
	err := givenIssueFromFixture(w, "../../../../etc")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes fixtures root")
	assert.Empty(t, scm.commitPaths, "nothing should be committed when the fixture name escapes the fixtures root")
}

func TestGivenIssueFromFixture_RejectsPathTraversalResult(t *testing.T) {
	// Regression (#7957 review): a playback result name must not be able
	// to escape the fixtures tree via "../" segments either.
	scm := &fakePlaybackSCM{
		createIssueFn: func(_ context.Context, _, _, title, _ string, _ ...string) (*forge.Issue, error) {
			return &forge.Issue{Number: 1, Title: title}, nil
		},
	}
	w := &world.World{
		RepoOwner:    "org",
		RepoName:     "repo",
		FixturesRoot: "e2e/behaviour",
		SCM:          scm,
		Driver:       install.NewPlaybackDriver(nil),
	}
	require.NoError(t, givenAgentReturns(w, "triage", "../../../../etc"))
	err := givenIssueFromFixture(w, "concurrent-map-crash")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes fixtures root")
	for _, p := range scm.commitPaths {
		assert.NotContains(t, p, "playlist", "no playlist/result content should be committed when the result name escapes the fixtures root")
	}
}

func TestFixtureSubpath_RejectsSymlinkedAncestor(t *testing.T) {
	// Regression (#7957 review): the lexical containment check alone
	// cannot see a symlink committed inside the fixtures tree itself. A
	// "results/link" symlink pointing outside the tree, followed by a
	// real subdirectory in the requested name, must still be rejected —
	// filepath.WalkDir and os.ReadFile would otherwise resolve through
	// "link" transparently (it is not the final path component, so a
	// bare Lstat on the full path never sees it) and expose the external
	// directory's contents.
	root := t.TempDir()
	resultsDir := filepath.Join(root, "results")
	require.NoError(t, os.MkdirAll(resultsDir, 0o755))

	external := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(external, "subdir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(external, "subdir", "secret.txt"), []byte("super-secret"), 0o600))
	require.NoError(t, os.Symlink(external, filepath.Join(resultsDir, "link")))

	_, err := fixtureSubpath(root, root, "results", filepath.Join("link", "subdir"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
}

func TestFixtureSubpath_RejectsSymlinkedCategoryDirectory(t *testing.T) {
	// Regression (#7957 review): the symlink guard previously started
	// below fixturesRoot/category, leaving the category directory itself
	// (and its ancestors) unchecked. A checked-in "results" symlink
	// targeting an external directory, combined with a result name that
	// identifies a real subdirectory there, must still be rejected.
	root := t.TempDir()

	external := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(external, "triage", "bug"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(external, "triage", "bug", "secret.txt"), []byte("super-secret"), 0o600))
	require.NoError(t, os.Symlink(external, filepath.Join(root, "results")))

	_, err := fixtureSubpath(root, root, "results", filepath.Join("triage", "bug"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
}

func TestFixtureSubpath_RejectsSymlinkedFixturesIssuesAncestor(t *testing.T) {
	// Regression (#7957 review): the same unchecked-ancestor gap applies
	// to the issue fixture path, where category is "fixtures/issues". A
	// checked-in "fixtures" symlink targeting an external directory,
	// combined with an issue name identifying a real subdirectory there,
	// must still be rejected.
	root := t.TempDir()

	external := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(external, "issues", "concurrent-map-crash"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(external, "issues", "concurrent-map-crash", "issue.yaml"), []byte("title: leaked"), 0o600))
	require.NoError(t, os.Symlink(external, filepath.Join(root, "fixtures")))

	_, err := fixtureSubpath(root, root, filepath.Join("fixtures", "issues"), "concurrent-map-crash")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
}

func TestGivenIssueFromFixture_RejectsSymlinkedAncestorInResult(t *testing.T) {
	// Regression (#7957 review): end-to-end through the playback setup
	// path, a playlist result name that resolves through a symlinked
	// ancestor must be rejected before anything is committed, so the
	// external file's contents are never published via SCM.CommitFile.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "fixtures", "issues", "concurrent-map-crash"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "fixtures", "issues", "concurrent-map-crash", "issue.yaml"),
		[]byte("title: crash\nbody: boom\n"), 0o644))
	resultsDir := filepath.Join(root, "results")
	require.NoError(t, os.MkdirAll(resultsDir, 0o755))

	external := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(external, "subdir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(external, "subdir", "secret.txt"), []byte("super-secret"), 0o600))
	require.NoError(t, os.Symlink(external, filepath.Join(resultsDir, "link")))

	scm := &fakePlaybackSCM{
		createIssueFn: func(_ context.Context, _, _, title, _ string, _ ...string) (*forge.Issue, error) {
			return &forge.Issue{Number: 1, Title: title}, nil
		},
	}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		SCM:       scm,
		Driver:    install.NewPlaybackDriver(nil),
	}
	require.NoError(t, givenAgentReturns(w, "triage", filepath.Join("link", "subdir")))

	// givenIssueFromFixture normally resolves root via moduleRootDir and
	// fixturesRoot via findModuleSubdir(w.FixturesRoot); exercise the same
	// containment path it uses (fixtureSubpath -> commitPlaylist) directly
	// against the crafted root above, as commitPlaylist does once the
	// playlist has entries.
	err := commitPlaylist(w, root, root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
	for _, p := range scm.commitPaths {
		assert.NotContains(t, p, "secret", "the symlinked ancestor's external content must never reach SCM.CommitFile")
	}
}

func TestGivenIssueFromFixture_RejectsSymlinkedCategoryDirectory(t *testing.T) {
	// Regression (#7957 review): end-to-end through the playback setup
	// path, a checked-in "results" symlink (rather than a symlink nested
	// inside "results") must also be rejected before anything is
	// committed, so the external directory's content is never published
	// via SCM.CommitFile.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "fixtures", "issues", "concurrent-map-crash"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "fixtures", "issues", "concurrent-map-crash", "issue.yaml"),
		[]byte("title: crash\nbody: boom\n"), 0o644))

	external := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(external, "triage", "bug"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(external, "triage", "bug", "secret.txt"), []byte("super-secret"), 0o600))
	require.NoError(t, os.Symlink(external, filepath.Join(root, "results")))

	scm := &fakePlaybackSCM{
		createIssueFn: func(_ context.Context, _, _, title, _ string, _ ...string) (*forge.Issue, error) {
			return &forge.Issue{Number: 1, Title: title}, nil
		},
	}
	w := &world.World{
		RepoOwner: "org",
		RepoName:  "repo",
		SCM:       scm,
		Driver:    install.NewPlaybackDriver(nil),
	}
	require.NoError(t, givenAgentReturns(w, "triage", filepath.Join("triage", "bug")))

	err := commitPlaylist(w, root, root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
	for _, p := range scm.commitPaths {
		assert.NotContains(t, p, "secret", "the symlinked category directory's external content must never reach SCM.CommitFile")
	}
}

func TestRejectSymlinkComponents_RejectsSymlinkedLeaf(t *testing.T) {
	// Regression (#7957 review): the direct issue.yaml read in
	// givenIssueFromFixture must not follow a symlink either — only
	// files walked by commitDirTree were previously protected.
	dir := t.TempDir()
	secretDir := t.TempDir()
	target := filepath.Join(secretDir, "issue.yaml")
	require.NoError(t, os.WriteFile(target, []byte("title: leaked"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "issue.yaml")))

	err := rejectSymlinkComponents(dir, filepath.Join(dir, "issue.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
}

func TestCommitDirTree_RejectsSymlink(t *testing.T) {
	// Regression (#7957 review): os.ReadFile follows a symlink to
	// wherever it points. Without this rejection, a fixture symlink to a
	// runner-readable credential file would have its target contents
	// committed (published) to the leased repository.
	dir := t.TempDir()
	secretDir := t.TempDir()
	target := filepath.Join(secretDir, "secret.txt")
	require.NoError(t, os.WriteFile(target, []byte("super-secret"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "link.txt")))

	scm := &fakePlaybackSCM{}
	w := &world.World{RepoOwner: "org", RepoName: "repo", SCM: scm}
	err := commitDirTree(w, dir, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
	assert.Empty(t, scm.commitPaths, "a symlink's target contents must never reach SCM.CommitFile")
}
