package steps

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// fakeEmptyMatrixCI implements ci.Driver (through the embedded nil
// interface, never called) and ci.EmptyMatrixWaiter.
type fakeEmptyMatrixCI struct {
	ci.Driver

	err   error
	calls int
	after time.Time
}

func (f *fakeEmptyMatrixCI) WaitForHarnessEmptyMatrix(_ context.Context, _, _ string, after time.Time) (*forge.WorkflowRun, error) {
	f.calls++
	f.after = after
	if f.err != nil {
		return nil, f.err
	}
	return &forge.WorkflowRun{ID: 1}, nil
}

func TestThenHarnessDispatchEmptyMatrix_RequiresScenarioStart(t *testing.T) {
	w := &world.World{CI: &fakeEmptyMatrixCI{}}
	err := thenHarnessDispatchEmptyMatrix(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trigger time")
}

func TestThenHarnessDispatchEmptyMatrix_DriverWithoutWaiter(t *testing.T) {
	w := &world.World{ScenarioStart: time.Now(), CI: &fakeListerCI{}}
	err := thenHarnessDispatchEmptyMatrix(w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ci.EmptyMatrixWaiter")
}

func TestThenHarnessDispatchEmptyMatrix_PassesTriggerTime(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := &fakeEmptyMatrixCI{}
	w := &world.World{Org: "org", RepoName: "repo", ScenarioStart: start, CI: fake}
	require.NoError(t, thenHarnessDispatchEmptyMatrix(w))
	assert.Equal(t, 1, fake.calls)
	assert.Equal(t, start, fake.after)
}

func TestThenHarnessDispatchEmptyMatrix_PropagatesError(t *testing.T) {
	fake := &fakeEmptyMatrixCI{err: errors.New("dispatch job failed")}
	w := &world.World{Org: "org", RepoName: "repo", ScenarioStart: time.Now(), CI: fake}
	err := thenHarnessDispatchEmptyMatrix(w)
	require.EqualError(t, err, "dispatch job failed")
}
