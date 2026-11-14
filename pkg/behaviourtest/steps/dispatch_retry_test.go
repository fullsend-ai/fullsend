package steps

import (
	"context"
	"errors"
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
