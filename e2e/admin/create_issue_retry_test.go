//go:build e2e

package admin

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
)

func TestCreateIssueWithRetry_RetriesUnauthorizedThenSucceeds(t *testing.T) {
	t.Parallel()

	attempts := 0
	waits := make([]time.Duration, 0, 2)
	want := &forge.Issue{Number: 42}

	issue, err := createIssueWithRetry(
		context.Background(),
		func() (*forge.Issue, error) {
			attempts++
			if attempts < 3 {
				return nil, &gh.APIError{StatusCode: http.StatusUnauthorized, Message: "Bad credentials"}
			}
			return want, nil
		},
		func(delay time.Duration) <-chan time.Time {
			waits = append(waits, delay)
			ready := make(chan time.Time, 1)
			ready <- time.Time{}
			return ready
		},
	)

	require.NoError(t, err)
	require.Same(t, want, issue)
	require.Equal(t, 3, attempts)
	require.Equal(t, []time.Duration{10 * time.Second, 20 * time.Second}, waits)
}

func TestCreateIssueWithRetry_DoesNotRetryOtherErrors(t *testing.T) {
	t.Parallel()

	wantErr := &gh.APIError{StatusCode: http.StatusForbidden, Message: "Resource not accessible"}
	attempts := 0

	issue, err := createIssueWithRetry(
		context.Background(),
		func() (*forge.Issue, error) {
			attempts++
			return nil, wantErr
		},
		func(time.Duration) <-chan time.Time {
			t.Fatal("unexpected retry delay")
			return nil
		},
	)

	require.Nil(t, issue)
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 1, attempts)
}

func TestCreateIssueWithRetry_StopsAfterThreeUnauthorizedErrors(t *testing.T) {
	t.Parallel()

	wantErr := &gh.APIError{StatusCode: http.StatusUnauthorized, Message: "Bad credentials"}
	attempts := 0

	issue, err := createIssueWithRetry(
		context.Background(),
		func() (*forge.Issue, error) {
			attempts++
			return nil, wantErr
		},
		func(time.Duration) <-chan time.Time {
			ready := make(chan time.Time, 1)
			ready <- time.Time{}
			return ready
		},
	)

	require.Nil(t, issue)
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 3, attempts)
}
