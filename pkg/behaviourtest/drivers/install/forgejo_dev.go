package install

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/forgejostack"
)

// ForgejoDevCapacity is the forgejo-dev driver's Capacity: the initial
// concurrent scenario and Forgejo Runner job capacity of the managed
// Forgejo stack.
const ForgejoDevCapacity = forgejostack.RunnerCapacity

// forgejoDevDriverName labels the forgejo-dev driver in not-implemented
// errors.
const forgejoDevDriverName = "forgejo-dev install"

// forgejoDevStartTimeout bounds stack startup: image pulls, Forgejo
// health, and the readiness job.
const forgejoDevStartTimeout = 20 * time.Minute

// forgejoStack is the part of *forgejostack.Stack the driver uses.
type forgejoStack interface {
	URL() string
	Teardown(ctx context.Context) error
}

// forgejoStackStarter starts the managed stack.
type forgejoStackStarter func(ctx context.Context, logf func(string, ...any)) (forgejoStack, error)

// forgejoDevDriver is the install Driver for ENVIRONMENT=forgejo-dev.
//
// Its factory starts an isolated managed Forgejo and Forgejo Runner
// stack (package forgejostack) on the host's Docker or Podman engine and
// returns once the stack is ready; Finalize tears it down. Repository
// allocation and release are not implemented yet: they return an error
// that satisfies errors.Is(err, drivers.ErrNotImplemented) and names the
// operation. It never falls back to the GitHub pool drivers.
type forgejoDevDriver struct {
	stack forgejoStack

	finalizeOnce sync.Once
	finalizeErr  error
}

var _ Driver = (*forgejoDevDriver)(nil)

// NewForgejoDev is the install Factory for ENVIRONMENT=forgejo-dev. It
// starts the managed Forgejo stack and fails, with an
// infrastructure-classified error naming the failed phase and carrying
// the stack's container logs, when the stack cannot be started or does
// not become ready. pool's GitHub layout and credentials do not apply
// to Forgejo; only its Logf is used.
func NewForgejoDev(pool PoolConfig) (Driver, error) {
	return newForgejoDev(pool, startForgejoStack)
}

func newForgejoDev(pool PoolConfig, start forgejoStackStarter) (Driver, error) {
	ctx, cancel := context.WithTimeout(context.Background(), forgejoDevStartTimeout)
	defer cancel()
	stack, err := start(ctx, poolLogf(pool))
	if err != nil {
		return nil, behaviourfail.Mark(fmt.Errorf("forgejo-dev: starting the Forgejo stack: %w", err), "forgejo-dev: stack startup")
	}
	return &forgejoDevDriver{stack: stack}, nil
}

// startForgejoStack starts the stack on the engine socket ResolveSocket
// selects from the environment.
func startForgejoStack(ctx context.Context, logf func(string, ...any)) (forgejoStack, error) {
	socket, err := forgejostack.ResolveSocket(os.Getenv, forgejostack.SocketExists)
	if err != nil {
		return nil, err
	}
	logf("forgejo-dev: using container engine socket %s", socket)
	stack, err := forgejostack.Start(ctx, forgejostack.Config{
		Engine:     forgejostack.NewSocketEngine(socket),
		SocketPath: socket,
		Logf:       logf,
	})
	if err != nil {
		return nil, err
	}
	return stack, nil
}

func (d *forgejoDevDriver) AllocateRepo(context.Context) (string, error) {
	return "", drivers.NotImplemented(forgejoDevDriverName, "AllocateRepo")
}

func (d *forgejoDevDriver) DeallocateRepo(context.Context, string) error {
	return drivers.NotImplemented(forgejoDevDriverName, "DeallocateRepo")
}

// Finalize tears down the managed stack once; later calls return the
// first call's result. A teardown failure is infrastructure-classified.
func (d *forgejoDevDriver) Finalize(ctx context.Context) error {
	d.finalizeOnce.Do(func() {
		tdCtx, cancel := context.WithTimeout(ctx, forgejostack.TeardownTimeout)
		defer cancel()
		if err := d.stack.Teardown(tdCtx); err != nil {
			d.finalizeErr = behaviourfail.Mark(fmt.Errorf("forgejo-dev: tearing down the Forgejo stack: %w", err), "forgejo-dev: stack teardown")
		}
	})
	return d.finalizeErr
}

func (d *forgejoDevDriver) Capacity() int {
	return ForgejoDevCapacity
}
