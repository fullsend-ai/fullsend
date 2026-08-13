package steps

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// fakeRepoDriver is a minimal install.Driver for unit testing step-level
// repo allocation. It tracks how many times AllocateRepo is called.
type fakeRepoDriver struct {
	allocateCalls int
	nextName      string
}

func (f *fakeRepoDriver) AllocateRepo(_ context.Context) (string, error) {
	f.allocateCalls++
	return f.nextName, nil
}

func (f *fakeRepoDriver) DeallocateRepo(_ context.Context, _ string) error { return nil }
func (f *fakeRepoDriver) Finalize(_ context.Context) error                 { return nil }
func (f *fakeRepoDriver) Capacity() int                                    { return 3 }

var _ install.Driver = (*fakeRepoDriver)(nil)

func TestGivenEnrolledTestRepository_DoubleCallGuard(t *testing.T) {
	drv := &fakeRepoDriver{nextName: "test-repo-01"}
	w := &world.World{
		Org:        "test-org",
		RepoDriver: drv,
	}

	// First call should allocate.
	err := givenEnrolledTestRepository(context.Background(), w)
	require.NoError(t, err)
	assert.Equal(t, 1, drv.allocateCalls)
	assert.Equal(t, "test-repo-01", w.LeasedRepoName)

	// Second call is a no-op — should not allocate again.
	err = givenEnrolledTestRepository(context.Background(), w)
	require.NoError(t, err)
	assert.Equal(t, 1, drv.allocateCalls, "second call should not allocate another slot")
}

func TestGivenEnrolledTestRepository_NilDriver(t *testing.T) {
	w := &world.World{Org: "test-org"}

	err := givenEnrolledTestRepository(context.Background(), w)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no repo driver configured")
}
