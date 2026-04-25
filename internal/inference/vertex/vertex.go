// Package vertex implements the inference.Provider interface for Google Cloud
// Vertex AI. It supports two auth modes:
//
//   - SA key (default): long-lived service account key JSON, with sub-modes
//     for creating, verifying, or using a pre-made key.
//   - WIF: Workload Identity Federation with GitHub OIDC — no stored keys.
package vertex

import (
	"context"
	"fmt"
)

// AuthMode selects between service-account key and Workload Identity Federation.
type AuthMode string

const (
	// AuthModeSAKey uses a long-lived service account key JSON file.
	AuthModeSAKey AuthMode = "sa_key"

	// AuthModeWIF uses Workload Identity Federation with GitHub OIDC.
	AuthModeWIF AuthMode = "wif"
)

const (
	// SecretCredentials is the repo secret name for the GCP service account key JSON.
	// Uses the FULLSEND_ prefix to avoid confusion with the GCP SDK env var
	// GOOGLE_APPLICATION_CREDENTIALS, which expects a file path, not JSON content.
	SecretCredentials = "FULLSEND_GCP_SA_KEY_JSON"

	// SecretProjectID is the repo secret name for the GCP project ID.
	// Uses the FULLSEND_ prefix for consistency with other secrets.
	SecretProjectID = "FULLSEND_GCP_PROJECT_ID"

	// VariableRegion is the repo variable name for the GCP region.
	VariableRegion = "FULLSEND_GCP_REGION"

	// SecretWIFProvider is the repo secret for the full WIF provider resource name.
	// Stored as a secret so the value is masked in GitHub Actions logs.
	SecretWIFProvider = "FULLSEND_GCP_WIF_PROVIDER"

	// SecretWIFServiceAccount is the repo secret for the SA email used with WIF.
	// Stored as a secret so the value is masked in GitHub Actions logs.
	SecretWIFServiceAccount = "FULLSEND_GCP_WIF_SA_EMAIL"

	// defaultSAName is the service account name created in mode 1.
	defaultSAName = "fullsend-agent"
)

// GCPClient abstracts GCP IAM operations for testability.
type GCPClient interface {
	// GetServiceAccount checks that a service account exists.
	GetServiceAccount(ctx context.Context, projectID, saName string) error

	// CreateServiceAccount creates a new service account.
	CreateServiceAccount(ctx context.Context, projectID, saName, displayName string) error

	// CreateServiceAccountKey generates a new JSON key for a service account.
	CreateServiceAccountKey(ctx context.Context, projectID, saEmail string) ([]byte, error)
}

// Config holds the inputs for Vertex credential provisioning.
type Config struct {
	ProjectID          string   // required
	Region             string   // required: GCP region (e.g. global)
	Mode               AuthMode // "sa_key" (default) or "wif"
	ServiceAccountName string   // optional: existing SA name (sa_key mode 2)
	CredentialJSON     []byte   // optional: pre-made key JSON (sa_key mode 3)
	WIFProvider        string   // WIF mode: full provider resource name
	WIFServiceAccount  string   // WIF mode: service account email
}

// Provider implements inference.Provider for Vertex AI.
type Provider struct {
	cfg    Config
	gcpAPI GCPClient
}

// New creates a Vertex Provider with the given config and GCP client.
func New(cfg Config, gcpAPI GCPClient) *Provider {
	if cfg.Mode == "" {
		cfg.Mode = AuthModeSAKey
	}
	return &Provider{cfg: cfg, gcpAPI: gcpAPI}
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
	if p.cfg.Mode == AuthModeWIF {
		return []string{SecretWIFProvider, SecretWIFServiceAccount, SecretProjectID}
	}
	return []string{SecretCredentials, SecretProjectID}
}

// Variables returns non-secret name/value pairs to store as repo variables.
func (p *Provider) Variables() map[string]string {
	if p.cfg.Region == "" {
		return nil
	}
	return map[string]string{VariableRegion: p.cfg.Region}
}

// Provision acquires GCP credentials and returns them as secrets.
func (p *Provider) Provision(ctx context.Context) (map[string]string, error) {
	if p.cfg.ProjectID == "" {
		return nil, fmt.Errorf("GCP project ID is required")
	}

	if p.cfg.Mode == AuthModeWIF {
		return p.provisionWIF()
	}
	return p.provisionSAKey(ctx)
}

func (p *Provider) provisionWIF() (map[string]string, error) {
	if p.cfg.WIFProvider == "" {
		return nil, fmt.Errorf("WIF provider resource name is required")
	}
	if p.cfg.WIFServiceAccount == "" {
		return nil, fmt.Errorf("WIF service account email is required")
	}
	return map[string]string{
		SecretWIFProvider:       p.cfg.WIFProvider,
		SecretWIFServiceAccount: p.cfg.WIFServiceAccount,
		SecretProjectID:         p.cfg.ProjectID,
	}, nil
}

func (p *Provider) provisionSAKey(ctx context.Context) (map[string]string, error) {
	// Mode 3: credential JSON provided directly.
	if len(p.cfg.CredentialJSON) > 0 {
		return map[string]string{
			SecretCredentials: string(p.cfg.CredentialJSON),
			SecretProjectID:   p.cfg.ProjectID,
		}, nil
	}

	if p.gcpAPI == nil {
		return nil, fmt.Errorf("GCP client is required for provisioning")
	}

	saName := p.cfg.ServiceAccountName
	if saName == "" {
		// Mode 1: create a new service account.
		saName = defaultSAName
		if err := p.gcpAPI.CreateServiceAccount(ctx, p.cfg.ProjectID, saName, "Fullsend agent inference"); err != nil {
			return nil, fmt.Errorf("creating service account %s: %w", saName, err)
		}
	} else {
		// Mode 2: verify existing service account.
		if err := p.gcpAPI.GetServiceAccount(ctx, p.cfg.ProjectID, saName); err != nil {
			return nil, fmt.Errorf("verifying service account %s: %w", saName, err)
		}
	}

	// Create key for the service account (modes 1 and 2).
	saEmail := saName + "@" + p.cfg.ProjectID + ".iam.gserviceaccount.com"
	keyJSON, err := p.gcpAPI.CreateServiceAccountKey(ctx, p.cfg.ProjectID, saEmail)
	if err != nil {
		return nil, fmt.Errorf("creating key for %s: %w", saEmail, err)
	}

	return map[string]string{
		SecretCredentials: string(keyJSON),
		SecretProjectID:   p.cfg.ProjectID,
	}, nil
}
