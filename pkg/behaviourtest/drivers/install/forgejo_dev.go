package install

import (
	"context"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers"
)

// ForgejoDevCapacity is the forgejo-dev driver's Capacity: the initial
// concurrent scenario and Forgejo Runner job capacity of the managed
// Forgejo stack.
const ForgejoDevCapacity = 2

// forgejoDevDriverName labels the forgejo-dev driver in not-implemented
// errors.
const forgejoDevDriverName = "forgejo-dev install"

// forgejoDevDriver is the install Driver for ENVIRONMENT=forgejo-dev.
//
// It is a minimal stub: repository allocation and release return an
// error that satisfies errors.Is(err, drivers.ErrNotImplemented) and
// names the operation. It provisions nothing, so Finalize has nothing
// to tear down. It never falls back to the GitHub pool drivers.
// Provisioning the Forgejo stack is #9646.
type forgejoDevDriver struct{}

var _ Driver = (*forgejoDevDriver)(nil)

// NewForgejoDev is the install Factory for ENVIRONMENT=forgejo-dev. It
// returns the stub driver; pool is ignored because the GitHub pool
// layout and credentials do not apply to Forgejo.
func NewForgejoDev(PoolConfig) (Driver, error) {
	return &forgejoDevDriver{}, nil
}

func (d *forgejoDevDriver) AllocateRepo(context.Context) (string, error) {
	return "", drivers.NotImplemented(forgejoDevDriverName, "AllocateRepo")
}

func (d *forgejoDevDriver) DeallocateRepo(context.Context, string) error {
	return drivers.NotImplemented(forgejoDevDriverName, "DeallocateRepo")
}

// Finalize returns nil: the stub provisions no suite-scoped resources
// and can hold no leases.
func (d *forgejoDevDriver) Finalize(context.Context) error {
	return nil
}

func (d *forgejoDevDriver) Capacity() int {
	return ForgejoDevCapacity
}
