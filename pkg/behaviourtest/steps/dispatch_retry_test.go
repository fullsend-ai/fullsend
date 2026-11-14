package steps

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/failclass"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// TestDispatchWaitRetry_ReturnsRetryError checks that when both waits of
// a skew-buffer retry fail, the step returns the retry's error: its wait
// ended last, so its detail (here, a poll-error summary) matches the
// window the dispatch diagnostics measure from. Attempt 1 times out
// cleanly; attempt 2's polls fail.
func TestDispatchWaitRetry_ReturnsRetryError(t *testing.T) {
	t.Parallel()
	const pollDetail = " (failed on 3 of 3 polls during the wait; last: rate limited)"

	tests := map[string]struct {
		event  string
		prefix string
		call   func(w *world.World) error
	}{
		"issue-open drain": {
			event:  issueOpenEvent,
			prefix: "waiting for issue-open workflow: ",
			call: func(w *world.World) error {
				return drainIssueOpenWorkflow(w, time.Date(2026, 11, 14, 0, 5, 58, 0, time.UTC))
			},
		},
		"dispatch run": {
			event:  issueCommentEvent,
			prefix: "waiting for dispatch workflow (issue_comment): ",
			call: func(w *world.World) error {
				_, err := waitForDispatchRun(w)
				return err
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			attempts := 0
			w := &world.World{
				Org: "org", RepoOwner: "org", RepoName: "repo",
				ScenarioStart: time.Date(2026, 11, 14, 0, 5, 58, 0, time.UTC),
				Logf:          func(string, ...any) {},
				CI: &mockCIDriver{waitForWorkflowFn: func(_ context.Context, _, _, file string, _ time.Time, event string) (*forge.WorkflowRun, error) {
					attempts++
					if attempts == 1 {
						return nil, &ci.NotDispatchedError{WorkflowFile: file, Event: event}
					}
					return nil, &ci.NotDispatchedError{WorkflowFile: file, Event: event, Detail: pollDetail}
				}},
			}

			err := tc.call(w)

			require.Error(t, err)
			assert.Equal(t, 2, attempts)
			assert.Equal(t, tc.prefix+"workflow fullsend.yaml ("+tc.event+") was not dispatched"+pollDetail, err.Error())
			assert.ErrorIs(t, err, ci.ErrWorkflowNotDispatched)
			var nd *ci.NotDispatchedError
			require.True(t, errors.As(err, &nd))
			assert.Equal(t, pollDetail, nd.Detail, "the detail comes from the retry, the window that ended last")
			assert.Equal(t, behaviourfail.Scenario, failclass.Classify(err))
		})
	}
}

// TestDispatchWaitRetry_OnlyAfterNotDispatched checks that the skew-buffer
// retry runs only after a timeout with no run. A first wait that ends with
// a run that concluded with a failure, with a cancelled or expired
// context, or with an error that only reads "was not dispatched" without
// wrapping the sentinel returns that error unchanged, with no retry; a
// not-dispatched
// first wait followed by a retry that fails some other way returns the
// retry's error.
func TestDispatchWaitRetry_OnlyAfterNotDispatched(t *testing.T) {
	t.Parallel()
	runFailed := errors.New(`workflow fullsend.yaml run 4 concluded with "failure"`)
	pollsFailed := errors.New("list workflow runs: 502 bad gateway")
	messageOnly := errors.New("workflow fullsend.yaml (issues) was not dispatched")

	calls := map[string]struct {
		prefix string
		call   func(w *world.World) error
	}{
		"issue-open drain": {
			prefix: "waiting for issue-open workflow: ",
			call: func(w *world.World) error {
				return drainIssueOpenWorkflow(w, time.Date(2026, 11, 14, 0, 5, 58, 0, time.UTC))
			},
		},
		"dispatch run": {
			prefix: "waiting for dispatch workflow (issue_comment): ",
			call: func(w *world.World) error {
				_, err := waitForDispatchRun(w)
				return err
			},
		},
	}
	cases := map[string]struct {
		attempts     []error // error returned by each attempt
		wantAttempts int
		wantErr      error
	}{
		"run failure is not retried": {
			attempts:     []error{runFailed, pollsFailed},
			wantAttempts: 1,
			wantErr:      runFailed,
		},
		// Only the sentinel marks a timeout with no run, not its text.
		"message-only not dispatched is not retried": {
			attempts:     []error{messageOnly, pollsFailed},
			wantAttempts: 1,
			wantErr:      messageOnly,
		},
		"cancelled first wait is not retried": {
			attempts:     []error{context.Canceled, pollsFailed},
			wantAttempts: 1,
			wantErr:      context.Canceled,
		},
		"first wait past its deadline is not retried": {
			attempts:     []error{context.DeadlineExceeded, pollsFailed},
			wantAttempts: 1,
			wantErr:      context.DeadlineExceeded,
		},
		"not dispatched then other error": {
			attempts:     []error{&ci.NotDispatchedError{WorkflowFile: "fullsend.yaml"}, pollsFailed},
			wantAttempts: 2,
			wantErr:      pollsFailed,
		},
	}
	for callName, c := range calls {
		for name, tc := range cases {
			t.Run(callName+"/"+name, func(t *testing.T) {
				t.Parallel()
				attempts := 0
				w := &world.World{
					Org: "org", RepoOwner: "org", RepoName: "repo",
					ScenarioStart: time.Date(2026, 11, 14, 0, 5, 58, 0, time.UTC),
					Logf:          func(string, ...any) {},
					CI: &mockCIDriver{waitForWorkflowFn: func(context.Context, string, string, string, time.Time, string) (*forge.WorkflowRun, error) {
						err := tc.attempts[attempts]
						attempts++
						return nil, err
					}},
				}

				err := c.call(w)

				require.Error(t, err)
				assert.Equal(t, tc.wantAttempts, attempts)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Equal(t, c.prefix+tc.wantErr.Error(), err.Error())
				assert.NotErrorIs(t, err, ci.ErrWorkflowNotDispatched, "the dispatch diagnostics must not run for this error")
			})
		}
	}
}

// TestDispatchWaitRetry_GitLabTimeoutIsRetried checks that the retry is
// driver-neutral: a not-dispatched timeout that wraps only the sentinel,
// as the GitLab CI driver returns, is retried like the GitHub Actions
// one. When the retry also times out, the error still wraps the
// sentinel but is not a *ci.NotDispatchedError, so the GitHub Actions
// dispatch diagnostics do not run for it.
func TestDispatchWaitRetry_GitLabTimeoutIsRetried(t *testing.T) {
	t.Parallel()
	gitlabTimeout := func(event string) error {
		return fmt.Errorf("pipeline (%s) %w", event, ci.ErrWorkflowNotDispatched)
	}

	calls := map[string]func(w *world.World) error{
		"issue-open drain": func(w *world.World) error {
			return drainIssueOpenWorkflow(w, time.Date(2026, 11, 14, 0, 5, 58, 0, time.UTC))
		},
		"dispatch run": func(w *world.World) error {
			_, err := waitForDispatchRun(w)
			return err
		},
	}
	for name, call := range calls {
		for _, retrySucceeds := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/retry succeeds %t", name, retrySucceeds), func(t *testing.T) {
				t.Parallel()
				attempts := 0
				w := &world.World{
					Org: "org", RepoOwner: "org", RepoName: "repo",
					ScenarioStart: time.Date(2026, 11, 14, 0, 5, 58, 0, time.UTC),
					Logf:          func(string, ...any) {},
					CI: &mockCIDriver{waitForWorkflowFn: func(_ context.Context, _, _, _ string, _ time.Time, event string) (*forge.WorkflowRun, error) {
						attempts++
						if attempts == 2 && retrySucceeds {
							return &forge.WorkflowRun{ID: 7}, nil
						}
						return nil, gitlabTimeout(event)
					}},
				}

				err := call(w)

				assert.Equal(t, 2, attempts, "the GitLab timeout is retried")
				if retrySucceeds {
					require.NoError(t, err)
					return
				}
				require.Error(t, err)
				assert.Contains(t, err.Error(), ") was not dispatched")
				assert.ErrorIs(t, err, ci.ErrWorkflowNotDispatched)
				var nd *ci.NotDispatchedError
				assert.False(t, errors.As(err, &nd), "no GitHub Actions details, so no dispatch diagnostics")
				assert.Equal(t, behaviourfail.Scenario, failclass.Classify(err))
			})
		}
	}
}
