// Package dispatch defines the Dispatcher interface for provisioning
// dispatch infrastructure during install. Implementations (e.g. GCF)
// create the cloud function, OIDC federation, and org-level
// secrets/variables needed by the shim workflow.
package dispatch

import "context"

// Dispatcher provisions dispatch infrastructure during install.
type Dispatcher interface {
	// Name returns a human-readable identifier (e.g. "gcf").
	Name() string

	// Provision creates the dispatch infrastructure and returns
	// org-level variables to store (e.g. mint URL).
	Provision(ctx context.Context) (variables map[string]string, err error)

	// StoreAgentPEM persists a role's PEM key in the mint's secret store.
	// Called once per role during App setup so partial failures are survivable.
	StoreAgentPEM(ctx context.Context, role string, pem []byte) error

	// OrgSecretNames returns the names of org-level secrets this
	// dispatcher manages.
	OrgSecretNames() []string

	// OrgVariableNames returns the names of org-level variables this
	// dispatcher manages.
	OrgVariableNames() []string
}
