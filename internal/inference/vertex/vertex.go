// Package vertex implements the inference.Provider interface for Google Cloud
// Vertex AI using Workload Identity Federation with GitHub OIDC.
package vertex

import (
	"context"
	"fmt"
)

const (
	// SecretProjectID is the repo secret name for the GCP project ID.
	SecretProjectID = "FULLSEND_GCP_PROJECT_ID"

	// VariableRegion is the repo variable name for the GCP region.
	VariableRegion = "FULLSEND_GCP_REGION"

	// SecretWIFProvider is the repo secret for the full WIF provider resource name.
	SecretWIFProvider = "FULLSEND_GCP_WIF_PROVIDER"
)

// Config holds the inputs for Vertex credential provisioning.
type Config struct {
	ProjectID   string // required
	Region      string // required: GCP region (e.g. global)
	WIFProvider string // full WIF provider resource name
}

// Provider implements inference.Provider for Vertex AI.
type Provider struct {
	cfg Config
}

// New creates a Vertex Provider with the given config.
func New(cfg Config) *Provider {
	return &Provider{cfg: cfg}
}

// NewAnalyzeOnly creates a Provider that only supports SecretNames() and Name().
// Calling Provision() on this provider returns an error.
func NewAnalyzeOnly() *Provider {
	return &Provider{}
}

// Name returns "vertex".
func (p *Provider) Name() string {
	return "vertex"
}

// SecretNames returns the secret names this provider manages.
func (p *Provider) SecretNames() []string {
	return []string{SecretWIFProvider, SecretProjectID}
}

// Variables returns non-secret name/value pairs to store as repo variables.
func (p *Provider) Variables() map[string]string {
	vars := map[string]string{}
	if p.cfg.Region != "" {
		vars[VariableRegion] = p.cfg.Region
	}
	return vars
}

// Provision acquires GCP credentials and returns them as secrets.
func (p *Provider) Provision(ctx context.Context) (map[string]string, error) {
	if p.cfg.ProjectID == "" {
		return nil, fmt.Errorf("GCP project ID is required")
	}
	if p.cfg.WIFProvider == "" {
		return nil, fmt.Errorf("WIF provider resource name is required")
	}
	return map[string]string{
		SecretWIFProvider: p.cfg.WIFProvider,
		SecretProjectID:   p.cfg.ProjectID,
	}, nil
}
