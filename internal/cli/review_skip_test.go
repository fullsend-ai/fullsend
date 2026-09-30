package cli

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	reviewedHead   = "f3af8c5aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runCreated     = "2026-09-23T16:14:04Z"
	afterRunStart  = "2026-09-23T16:30:00Z"
	beforeRunStart = "2026-09-23T16:10:00Z"
)

type fakeRunReader struct {
	run *forge.WorkflowRun
	err error
}

func (f fakeRunReader) GetWorkflowRun(context.Context, string, string, int) (*forge.WorkflowRun, error) {
	return f.run, f.err
}

type fakePRReader struct {
	head       string
	headErr    error
	reviews    []forge.PullRequestReview
	reviewsErr error
}

func (f fakePRReader) GetPullRequestHeadSHA(context.Context, string, string, int) (string, error) {
	return f.head, f.headErr
}

func (f fakePRReader) ListPullRequestReviews(context.Context, string, string, int) ([]forge.PullRequestReview, error) {
	return f.reviews, f.reviewsErr
}

func appReview(login, commit, submitted string) forge.PullRequestReview {
	return forge.PullRequestReview{User: login, State: "APPROVED", CommitID: commit, SubmittedAt: submitted, AuthorIsApp: true}
}

func headReviewedBase() (headReviewedInput, fakeRunReader, fakePRReader) {
	in := headReviewedInput{
		role:       "review",
		transition: normevent.TransitionLabelChanged,
		runAttempt: "1",
		runRepo:    "org/repo",
		runID:      35752742886,
		prRepo:     "org/repo",
		prNumber:   1480,
	}
	runs := fakeRunReader{run: &forge.WorkflowRun{ID: 35752742886, CreatedAt: runCreated}}
	prs := fakePRReader{head: reviewedHead, reviews: []forge.PullRequestReview{
		appReview("org-review[bot]", reviewedHead, afterRunStart),
	}}
	return in, runs, prs
}

// TestHeadAlreadyReviewed pins when a queued review skips: only when the
// review App reviewed the current head after this run was created, for an
// event that is not a human command, on the first attempt.
func TestHeadAlreadyReviewed(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*headReviewedInput, *fakeRunReader, *fakePRReader)
		skip    bool
		wantErr bool
	}{
		{name: "labelled after the head was reviewed skips", mutate: func(*headReviewedInput, *fakeRunReader, *fakePRReader) {}, skip: true},
		{name: "opened skips", mutate: func(in *headReviewedInput, _ *fakeRunReader, _ *fakePRReader) {
			in.transition = normevent.TransitionOpened
		}, skip: true},
		{name: "synchronize skips", mutate: func(in *headReviewedInput, _ *fakeRunReader, _ *fakePRReader) {
			in.transition = normevent.TransitionSynchronized
		}, skip: true},
		{name: "attempt unset counts as the first", mutate: func(in *headReviewedInput, _ *fakeRunReader, _ *fakePRReader) {
			in.runAttempt = ""
		}, skip: true},
		{name: "shared review App login", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.reviews = []forge.PullRequestReview{appReview("fullsend-ai-review[bot]", reviewedHead, afterRunStart)}
		}, skip: true},
		{name: "login matched without regard to case", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.reviews = []forge.PullRequestReview{appReview("Org-Review[bot]", reviewedHead, afterRunStart)}
		}, skip: true},

		{name: "a human /fs-review comment runs", mutate: func(in *headReviewedInput, _ *fakeRunReader, _ *fakePRReader) {
			in.transition = normevent.TransitionCommentAdded
		}},
		{name: "an unreadable event runs", mutate: func(in *headReviewedInput, _ *fakeRunReader, _ *fakePRReader) {
			in.transition = ""
		}},
		{name: "a manual re-run runs", mutate: func(in *headReviewedInput, _ *fakeRunReader, _ *fakePRReader) {
			in.runAttempt = "2"
		}},
		{name: "another stage runs", mutate: func(in *headReviewedInput, _ *fakeRunReader, _ *fakePRReader) {
			in.role = "triage"
		}},
		{name: "a review of an older head runs", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.head = "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0"
		}},
		{name: "a review older than this run runs", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.reviews = []forge.PullRequestReview{appReview("org-review[bot]", reviewedHead, beforeRunStart)}
		}},
		{name: "a review at the run's own second runs", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.reviews = []forge.PullRequestReview{appReview("org-review[bot]", reviewedHead, runCreated)}
		}},
		{name: "a human reviewer runs", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			r := appReview("maintainer", reviewedHead, afterRunStart)
			r.AuthorIsApp = false
			p.reviews = []forge.PullRequestReview{r}
		}},
		{name: "a look-alike login runs", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.reviews = []forge.PullRequestReview{appReview("org-review-bot", reviewedHead, afterRunStart)}
		}},
		{name: "another App's review runs", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.reviews = []forge.PullRequestReview{appReview("org-coder[bot]", reviewedHead, afterRunStart)}
		}},
		{name: "the review App's login without the forge's App verdict runs", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			r := appReview("org-review[bot]", reviewedHead, afterRunStart)
			r.AuthorIsApp = false
			p.reviews = []forge.PullRequestReview{r}
		}},
		{name: "a later human review does not count, and skips", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			human := appReview("maintainer", reviewedHead, "2026-09-23T16:40:00Z")
			human.AuthorIsApp = false
			p.reviews = append(p.reviews, human)
		}, skip: true},
		{name: "a later App review of an old head does not hide the right one", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.reviews = append(p.reviews, appReview("org-review[bot]", "0101010101010101010101010101010101010101", "2026-09-23T16:40:00Z"))
		}, skip: true},
		{name: "a newer App review of an old head does not stand in for the current head", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.reviews = []forge.PullRequestReview{
				appReview("org-review[bot]", reviewedHead, beforeRunStart),
				appReview("org-review[bot]", "0101010101010101010101010101010101010101", afterRunStart),
			}
		}},
		{name: "a dismissed App review runs", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.reviews[0].State = "DISMISSED"
		}},
		{name: "no reviews runs", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.reviews = nil
		}},
		{name: "a review without a commit runs", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.reviews[0].CommitID = ""
		}},
		{name: "an empty head against a review without a commit runs", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.head = ""
			p.reviews[0].CommitID = ""
		}},
		{name: "an unparseable run time runs", mutate: func(_ *headReviewedInput, r *fakeRunReader, _ *fakePRReader) {
			r.run = &forge.WorkflowRun{CreatedAt: "yesterday"}
		}},
		{name: "a missing run runs", mutate: func(_ *headReviewedInput, r *fakeRunReader, _ *fakePRReader) {
			r.run = nil
		}},
		{name: "a malformed run repo runs", mutate: func(in *headReviewedInput, _ *fakeRunReader, _ *fakePRReader) {
			in.runRepo = "repo"
		}},
		{name: "no run id runs", mutate: func(in *headReviewedInput, _ *fakeRunReader, _ *fakePRReader) {
			in.runID = 0
		}},

		{name: "a run lookup error runs", mutate: func(_ *headReviewedInput, r *fakeRunReader, _ *fakePRReader) {
			r.err = errors.New("403")
		}, wantErr: true},
		{name: "a head lookup error runs", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.headErr = errors.New("404")
		}, wantErr: true},
		{name: "a review listing error runs", mutate: func(_ *headReviewedInput, _ *fakeRunReader, p *fakePRReader) {
			p.reviewsErr = errors.New("502")
		}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, runs, prs := headReviewedBase()
			tt.mutate(&in, &runs, &prs)
			got, why, err := headAlreadyReviewed(context.Background(), in, runs, prs)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, why, "every outcome names its reason, so a live run is diagnosable")
			}
			assert.Equal(t, tt.skip, got)
		})
	}
}

// headReviewedOpts sets up checkHeadAlreadyReviewed with fakes that would
// skip, so each test can take away one input.
func headReviewedOpts(t *testing.T) (steerOpts, map[string]any, *strings.Builder) {
	t.Helper()
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_RUN_ID", "35752742886")
	t.Setenv("GITHUB_RUN_ATTEMPT", "1")
	t.Setenv("GITHUB_REPOSITORY", "org/repo")
	// The CI job running these tests has its own event; never read it.
	t.Setenv("GITHUB_EVENT_NAME", "")
	t.Setenv("GITHUB_EVENT_PATH", "")

	_, runs, prs := headReviewedBase()
	prevRun, prevPR := headReviewedRunClientFn, headReviewedPRClientFn
	t.Cleanup(func() { headReviewedRunClientFn, headReviewedPRClientFn = prevRun, prevPR })
	headReviewedRunClientFn = func(string) headReviewedRunReader { return runs }
	headReviewedPRClientFn = func(string) headReviewedPRReader { return prs }

	var out strings.Builder
	o := steerOpts{
		harness:    steerHarness(false),
		statusRepo: "org/repo",
		statusNum:  1480,
		jobToken:   "job-token",
		roleToken:  "role-token",
		printer:    ui.New(&out),
	}
	o.harness.Role = "review"
	event := map[string]any{"transition": map[string]any{"kind": "label_changed"}}
	return o, event, &out
}

// TestCheckHeadAlreadyReviewed_Wiring pins how the runner feeds the check:
// the event's transition, the run attempt and the run's repository all come
// from the environment the job runs in, and steering being off does not stop
// it.
func TestCheckHeadAlreadyReviewed_Wiring(t *testing.T) {
	o, event, _ := headReviewedOpts(t)
	assert.True(t, checkHeadAlreadyReviewed(context.Background(), o, event),
		"skips with steering explicitly off: the duplicate is not steering's")

	t.Run("comment event runs", func(t *testing.T) {
		o, _, _ := headReviewedOpts(t)
		comment := map[string]any{"transition": map[string]any{"kind": "comment_added"}}
		assert.False(t, checkHeadAlreadyReviewed(context.Background(), o, comment))
	})
	t.Run("no event runs", func(t *testing.T) {
		o, _, _ := headReviewedOpts(t)
		assert.False(t, checkHeadAlreadyReviewed(context.Background(), o, nil))
	})
	t.Run("re-run runs", func(t *testing.T) {
		o, event, _ := headReviewedOpts(t)
		t.Setenv("GITHUB_RUN_ATTEMPT", "2")
		assert.False(t, checkHeadAlreadyReviewed(context.Background(), o, event))
	})
	t.Run("outside Actions runs", func(t *testing.T) {
		o, event, _ := headReviewedOpts(t)
		t.Setenv("GITHUB_ACTIONS", "")
		assert.False(t, checkHeadAlreadyReviewed(context.Background(), o, event))
	})
	t.Run("GitLab runs", func(t *testing.T) {
		o, event, _ := headReviewedOpts(t)
		o.forgePlatform = repos.ForgeGitLab
		assert.False(t, checkHeadAlreadyReviewed(context.Background(), o, event))
	})
	t.Run("no role token runs", func(t *testing.T) {
		o, event, _ := headReviewedOpts(t)
		o.roleToken = ""
		assert.False(t, checkHeadAlreadyReviewed(context.Background(), o, event))
	})
	t.Run("an API error runs and says why", func(t *testing.T) {
		o, event, out := headReviewedOpts(t)
		headReviewedPRClientFn = func(string) headReviewedPRReader { return fakePRReader{headErr: errors.New("boom")} }
		assert.False(t, checkHeadAlreadyReviewed(context.Background(), o, event))
		assert.Contains(t, out.String(), "Could not check whether this head was already reviewed")
	})
}

// writeRawEvent writes the event GitHub delivers to a job and points the
// runner at it.
func writeRawEvent(t *testing.T, name, action string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"action":"`+action+`","pull_request":{"number":1480}}`), 0o600))
	t.Setenv("GITHUB_EVENT_NAME", name)
	t.Setenv("GITHUB_EVENT_PATH", path)
}

// TestCheckHeadAlreadyReviewed_BuiltInReviewStage is the production shape the
// skip first missed: the built-in review stage gets no normalized event, so
// the kind has to come from the event GitHub delivered to the job.
func TestCheckHeadAlreadyReviewed_BuiltInReviewStage(t *testing.T) {
	for _, action := range []string{"opened", "labeled"} {
		t.Run(action+" skips", func(t *testing.T) {
			o, _, out := headReviewedOpts(t)
			writeRawEvent(t, "pull_request_target", action)
			assert.True(t, checkHeadAlreadyReviewed(context.Background(), o, nil),
				"a PR whose head the App already reviewed skips with no normalized event")
			assert.NotContains(t, out.String(), "not applied")
		})
	}
	t.Run("a payload for another pull request runs", func(t *testing.T) {
		o, _, out := headReviewedOpts(t)
		writeRawEvent(t, "pull_request_target", "opened")
		o.statusNum = 1481
		assert.False(t, checkHeadAlreadyReviewed(context.Background(), o, nil))
		assert.Contains(t, out.String(), "Head-reviewed skip not applied")
	})
	t.Run("a comment runs", func(t *testing.T) {
		o, _, out := headReviewedOpts(t)
		writeRawEvent(t, "issue_comment", "created")
		assert.False(t, checkHeadAlreadyReviewed(context.Background(), o, nil))
		assert.Contains(t, out.String(), `event "comment_added"`)
	})
}

func TestHeadReviewedTransition(t *testing.T) {
	dir := t.TempDir()
	raw := func(action string) string {
		p := filepath.Join(dir, action+".json")
		require.NoError(t, os.WriteFile(p, []byte(`{"action":"`+action+`","pull_request":{"number":7}}`), 0o600))
		return p
	}
	normalized := map[string]any{"transition": map[string]any{"kind": "synchronized"}}
	tests := []struct {
		name      string
		eventMap  map[string]any
		eventName string
		path      string
		want      normevent.TransitionKind
	}{
		{"the normalized event wins", normalized, "issue_comment", raw("created"), normevent.TransitionSynchronized},
		{"labeled", nil, "pull_request_target", raw("labeled"), normevent.TransitionLabelChanged},
		{"unlabeled", nil, "pull_request_target", raw("unlabeled"), normevent.TransitionLabelChanged},
		{"opened", nil, "pull_request_target", raw("opened"), normevent.TransitionOpened},
		{"reopened", nil, "pull_request", raw("reopened"), normevent.TransitionReopened},
		{"synchronize", nil, "pull_request_target", raw("synchronize"), normevent.TransitionSynchronized},
		{"ready for review", nil, "pull_request_target", raw("ready_for_review"), normevent.TransitionMarkedReady},
		{"a comment", nil, "issue_comment", raw("created"), normevent.TransitionCommentAdded},
		{"an edited PR", nil, "pull_request_target", raw("edited"), ""},
		{"a workflow_dispatch event", nil, "workflow_dispatch", raw("dispatch"), ""},
		{"no event file", nil, "pull_request_target", "", ""},
		{"another pull request's payload", nil, "pull_request_target", raw("labeled"), ""},
		{"a missing event file", nil, "pull_request_target", filepath.Join(dir, "absent.json"), ""},
	}
	t.Run("no pull request number on either side", func(t *testing.T) {
		p := filepath.Join(dir, "bare.json")
		require.NoError(t, os.WriteFile(p, []byte(`{"action":"labeled"}`), 0o600))
		assert.Equal(t, normevent.TransitionKind(""), headReviewedTransition(nil, "pull_request_target", p, 0))
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			number := 7
			if tt.name == "another pull request's payload" {
				number = 8
			}
			assert.Equal(t, tt.want, headReviewedTransition(tt.eventMap, tt.eventName, tt.path, number))
		})
	}
}

// TestCheckHeadAlreadyReviewed_LogsWhyNot pins the one line a run that does
// not skip prints, naming the guard.
func TestCheckHeadAlreadyReviewed_LogsWhyNot(t *testing.T) {
	o, _, out := headReviewedOpts(t)
	t.Setenv("GITHUB_EVENT_NAME", "workflow_dispatch")
	t.Setenv("GITHUB_EVENT_PATH", "")
	assert.False(t, checkHeadAlreadyReviewed(context.Background(), o, nil))
	assert.Contains(t, out.String(), `Head-reviewed skip not applied: event "" is not one a duplicate review comes from`)

	o, event, out := headReviewedOpts(t)
	o.jobToken = ""
	assert.False(t, checkHeadAlreadyReviewed(context.Background(), o, event))
	assert.Contains(t, out.String(), "Head-reviewed skip not checked")
}

// TestCheckHeadAlreadyReviewed_ReadsWithTheRightTokens pins the credentials:
// the run is read with the job token from the repository the workflow runs
// in, and the pull request with the role token.
func TestCheckHeadAlreadyReviewed_ReadsWithTheRightTokens(t *testing.T) {
	o, event, _ := headReviewedOpts(t)
	var runTok, prTok string
	inner := headReviewedRunClientFn
	innerPR := headReviewedPRClientFn
	headReviewedRunClientFn = func(tok string) headReviewedRunReader { runTok = tok; return inner(tok) }
	headReviewedPRClientFn = func(tok string) headReviewedPRReader { prTok = tok; return innerPR(tok) }
	checkHeadAlreadyReviewed(context.Background(), o, event)
	assert.Equal(t, "job-token", runTok)
	assert.Equal(t, "role-token", prTok)
}

// TestRunPreflightSkips pins both skips behind the one call run makes, and
// the line each prints.
func TestRunPreflightSkips(t *testing.T) {
	t.Run("the receipt skips", func(t *testing.T) {
		var out strings.Builder
		o := baseOpts(t)
		o.printer = ui.New(&out)
		prev := steerMarkerClientFn
		t.Cleanup(func() { steerMarkerClientFn = prev })
		steerMarkerClientFn = func(token string) steerMarkerReader {
			if token == o.roleToken {
				return fakeMarkerReader{login: "fullsend[bot]"}
			}
			return fakeMarkerReader{login: "github-actions[bot]", comments: []forge.IssueComment{
				{Author: "github-actions[bot]", Body: "<!-- fullsend:steer consumed=33740015232 head=abc -->"},
			}}
		}
		assert.True(t, runPreflightSkips(context.Background(), o, nil))
		assert.Contains(t, out.String(), "Update already absorbed by the run in flight")
	})
	t.Run("a reviewed head skips", func(t *testing.T) {
		o, event, out := headReviewedOpts(t)
		assert.True(t, runPreflightSkips(context.Background(), o, event))
		assert.Contains(t, out.String(), "This head was already reviewed")
	})
	t.Run("neither runs", func(t *testing.T) {
		o, _, out := headReviewedOpts(t)
		assert.False(t, runPreflightSkips(context.Background(), o, nil))
		assert.NotContains(t, out.String(), "nothing to do")
	})
}

// TestRunAgentCallsPreflightSkipsFirst pins the call site no unit test can
// reach: runAgent calls runPreflightSkips exactly once, before it posts the
// start comment and before it runs the pre-script.
func TestRunAgentCallsPreflightSkipsFirst(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "run.go", nil, 0)
	require.NoError(t, err)

	var body *ast.BlockStmt
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "runAgent" {
			body = fn.Body
		}
	}
	require.NotNil(t, body, "runAgent not found in run.go")

	first := map[string]token.Pos{}
	count := 0
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name == "runPreflightSkips" {
			count++
		}
		if _, seen := first[id.Name]; !seen {
			first[id.Name] = call.Pos()
		}
		return true
	})
	require.Equal(t, 1, count, "runAgent must call runPreflightSkips exactly once")
	for _, later := range []string{"setupStatusNotifier", "runPreScript"} {
		require.Contains(t, first, later)
		assert.Less(t, first["runPreflightSkips"], first[later],
			"the pre-flight skips must run before %s", later)
	}
}
