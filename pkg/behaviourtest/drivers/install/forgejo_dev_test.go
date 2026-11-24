package install

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers"
)

// fakeForgejoStack records teardowns of a forgejo-dev stack.
type fakeForgejoStack struct {
	teardowns   int
	teardownErr error
}

func (s *fakeForgejoStack) URL() string { return "http://127.0.0.1:3000/" }

func (s *fakeForgejoStack) Teardown(context.Context) error {
	s.teardowns++
	return s.teardownErr
}

func startedWith(stack forgejoStack, err error) forgejoStackStarter {
	return func(context.Context, func(string, ...any)) (forgejoStack, error) {
		return stack, err
	}
}

func TestNewForgejoDev(t *testing.T) {
	t.Parallel()

	stack := &fakeForgejoStack{}
	var logged []string
	pool := PoolConfig{Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }}
	d, err := newForgejoDev(pool, func(ctx context.Context, logf func(string, ...any)) (forgejoStack, error) {
		_, hasDeadline := ctx.Deadline()
		assert.True(t, hasDeadline, "stack startup is bounded")
		logf("starting")
		return stack, nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"starting"}, logged, "the stack logs through the suite logger")
	ctx := context.Background()

	assert.NotImplements(t, (*LeaseDriver)(nil), d, "the driver must not act as the GitHub slot pool driver")
	assert.NotImplements(t, (*MintURLReporter)(nil), d)
	assert.Equal(t, ForgejoDevCapacity, d.Capacity())
	assert.Equal(t, 2, ForgejoDevCapacity)

	repo, err := d.AllocateRepo(ctx)
	assert.Empty(t, repo)
	assertNotImplemented(t, err, "AllocateRepo")
	assertNotImplemented(t, d.DeallocateRepo(ctx, "test-repo-01"), "DeallocateRepo")

	require.NoError(t, d.Finalize(ctx))
	require.NoError(t, d.Finalize(ctx))
	assert.Equal(t, 1, stack.teardowns, "Finalize tears the stack down once")
}

func TestNewForgejoDev_StartFailureIsInfrastructure(t *testing.T) {
	t.Parallel()

	d, err := newForgejoDev(PoolConfig{}, startedWith(nil, errors.New("forgejo stack x: readiness: runner container exited")))
	require.Error(t, err)
	assert.Nil(t, d)
	assert.True(t, behaviourfail.Is(err), "startup and readiness failures are infrastructure failures")
	assert.ErrorContains(t, err, "forgejo-dev: starting the Forgejo stack: forgejo stack x: readiness: runner container exited")
}

func TestForgejoDev_FinalizeFailureIsInfrastructure(t *testing.T) {
	t.Parallel()

	stack := &fakeForgejoStack{teardownErr: errors.New("removing container: device busy")}
	d, err := newForgejoDev(PoolConfig{}, startedWith(stack, nil))
	require.NoError(t, err)

	err = d.Finalize(context.Background())
	require.Error(t, err)
	assert.True(t, behaviourfail.Is(err), "teardown failures are infrastructure failures")
	assert.ErrorContains(t, err, "forgejo-dev: tearing down the Forgejo stack: removing container: device busy")
	assert.Equal(t, err, d.Finalize(context.Background()), "later calls return the first result")
	assert.Equal(t, 1, stack.teardowns)
}

func TestNewForgejoDev_NoEngineSocket(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")

	d, err := NewForgejoDev(PoolConfig{})
	require.Error(t, err)
	assert.Nil(t, d)
	assert.True(t, behaviourfail.Is(err))
	assert.ErrorContains(t, err, "needs a unix:// engine socket")
}

func TestNewForgejoDev_StartsOnResolvedSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "fjdev")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "engine.sock")
	l, err := net.Listen("unix", socket)
	require.NoError(t, err)
	var pulled atomic.Bool
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/info" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if r.URL.Path == "/images/create" {
			pulled.Store(true)
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"engine unavailable"}`))
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("DOCKER_HOST", "unix://"+socket)

	var logged []string
	d, err := NewForgejoDev(PoolConfig{Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }})
	require.Error(t, err)
	assert.Nil(t, d)
	assert.True(t, pulled.Load(), "the stack talks to the engine on the resolved socket")
	assert.True(t, behaviourfail.Is(err))
	assert.ErrorContains(t, err, "engine unavailable")
	assert.Contains(t, logged, "forgejo-dev: using container engine socket "+socket)
}

func assertNotImplemented(t *testing.T, err error, operation string) {
	t.Helper()
	require.ErrorIs(t, err, drivers.ErrNotImplemented)
	var nie *drivers.NotImplementedError
	require.True(t, errors.As(err, &nie))
	assert.Equal(t, "forgejo-dev install", nie.Driver)
	assert.Equal(t, operation, nie.Operation)
}
