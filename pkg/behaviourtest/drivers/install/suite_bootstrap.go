package install

import (
	"context"
	"errors"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// OrgAccess is the org, installation token and forge client the suite's
// template World uses before a scenario leases a slot. Every field is
// empty or nil for a driver that has no default org.
type OrgAccess struct {
	Org    string
	Token  string
	Client forge.Client
}

// SuiteBootstrap is an optional Driver interface. It supplies the details
// the suite runner needs before scenarios start, so the runner never reads
// a driver's pool layout or calls its token source itself. It is separate
// from Driver so external Driver implementations keep compiling. Use
// BootstrapFor to get it for any Driver.
type SuiteBootstrap interface {
	// DefaultConcurrency is the godog worker count when GODOG_CONCURRENCY
	// is unset. The runner caps it at Capacity.
	DefaultConcurrency() int

	// Describe is a one-line description of what the driver allocates
	// from, for the suite log.
	Describe() string

	// DefaultOrg returns the org, token and client the template World
	// points at until a scenario leases a slot. An error means the run
	// cannot start.
	DefaultOrg(ctx context.Context) (OrgAccess, error)
}

// BootstrapFor returns d's SuiteBootstrap. A Driver that does not
// implement it gets a fallback: the default concurrency is Capacity, the
// description names the driver type, and there is no default org (an
// empty OrgAccess). The fallback never mints a token or builds a client.
func BootstrapFor(d Driver) SuiteBootstrap {
	if b, ok := d.(SuiteBootstrap); ok {
		return b
	}
	return driverBootstrap{d: d}
}

// driverBootstrap is BootstrapFor's fallback for a Driver that does not
// implement SuiteBootstrap.
type driverBootstrap struct {
	d Driver
}

func (b driverBootstrap) DefaultConcurrency() int { return b.d.Capacity() }

func (b driverBootstrap) Describe() string {
	return fmt.Sprintf("allocating repos through %T (%d slot(s))", b.d, b.d.Capacity())
}

func (b driverBootstrap) DefaultOrg(context.Context) (OrgAccess, error) {
	return OrgAccess{}, nil
}

// DefaultConcurrency returns the slots per org: aggregate concurrency is
// controlled independently of Capacity, which spans every pool org.
func (d *composedDriver) DefaultConcurrency() int {
	return d.alloc.layout.SlotsPerOrg
}

// Describe names the pool orgs and the slot count.
func (d *composedDriver) Describe() string {
	layout := d.alloc.layout
	return fmt.Sprintf("leasing repo slots across %d org(s) %v (%d slot(s))", layout.OrgCount, layout.Orgs(), d.Capacity())
}

// DefaultOrg returns the first pool org whose credential can be minted.
// One unhealthy org does not fail the run while the allocator can still
// lease slots from the others. The credential is cached (orgCredentials),
// so a later lease in that org reuses the token and client. The error
// joins every org's mint failure when no org's credential can be minted.
func (d *composedDriver) DefaultOrg(ctx context.Context) (OrgAccess, error) {
	var mintErrs []error
	for _, org := range d.alloc.layout.Orgs() {
		cred, err := d.creds.get(ctx, org)
		if err != nil {
			// creds.get names the org in its error.
			d.logf("[driver] %v (trying the next org)", err)
			mintErrs = append(mintErrs, err)
			continue
		}
		return OrgAccess{Org: org, Token: cred.token, Client: cred.client}, nil
	}
	return OrgAccess{}, fmt.Errorf("minting a token for any pool org: %w", errors.Join(mintErrs...))
}

// DefaultConcurrency delegates to the wrapped Driver's SuiteBootstrap.
func (d *PlaybackDriver) DefaultConcurrency() int {
	return BootstrapFor(d.base).DefaultConcurrency()
}

// Describe delegates to the wrapped Driver's SuiteBootstrap.
func (d *PlaybackDriver) Describe() string {
	return BootstrapFor(d.base).Describe()
}

// DefaultOrg delegates to the wrapped Driver's SuiteBootstrap.
func (d *PlaybackDriver) DefaultOrg(ctx context.Context) (OrgAccess, error) {
	return BootstrapFor(d.base).DefaultOrg(ctx)
}

// Compile-time checks.
var _ SuiteBootstrap = (*composedDriver)(nil)
var _ SuiteBootstrap = (*PlaybackDriver)(nil)
