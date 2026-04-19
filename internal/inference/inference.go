// Package inference defines the interface for provisioning inference provider
// credentials during fullsend installation.
package inference

import "context"

// Provider provisions inference credentials during install.
type Provider interface {
	// Name returns the provider identifier (e.g. "vertex").
	Name() string

	// Provision acquires credentials and returns secrets to store.
	// Returns a map of secret-name → secret-value pairs to store
	// as repo secrets on .fullsend.
	Provision(ctx context.Context) (map[string]string, error)

	// SecretNames returns the names of secrets this provider manages,
	// used by Analyze to check whether credentials are present.
	SecretNames() []string

	// Variables returns non-secret name/value pairs to store as repo
	// variables (e.g. region). May return nil.
	Variables() map[string]string
}
