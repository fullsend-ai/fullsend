// Package vertex implements the inference.Provider interface for Google Cloud
// Vertex AI. It supports three modes of credential provisioning:
//
//  1. GCP project ID only → create service account + key
//  2. GCP project ID + SA name → verify SA exists, create key
//  3. GCP project ID + credential JSON → use key directly
package vertex

import (
	"context"
	"fmt"
)

const (
	// SecretCredentials is the repo secret name for the GCP service account key JSON.
	// Uses the FULLSEND_ prefix to avoid confusion with the GCP SDK env var
	// GOOGLE_APPLICATION_CREDENTIALS, which expects a file path, not JSON content.
	SecretCredentials = "FULLSEND_GCP_SA_KEY_JSON"

	// SecretProjectID is the repo secret name for the GCP project ID.
	SecretProjectID = "GCP_PROJECT_ID"

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
	ProjectID          string // required
	ServiceAccountName string // optional: existing SA name (mode 2)
	CredentialJSON     string // optional: pre-made key JSON (mode 3)
}

// Provider implements inference.Provider for Vertex AI.
type Provider struct {
	cfg    Config
	gcpAPI GCPClient
}

// New creates a Vertex Provider with the given config and GCP client.
func New(cfg Config, gcpAPI GCPClient) *Provider {
	return &Provider{cfg: cfg, gcpAPI: gcpAPI}
}

// Name returns "vertex".
func (p *Provider) Name() string {
	return "vertex"
}

// SecretNames returns the secret names this provider manages.
func (p *Provider) SecretNames() []string {
	return []string{SecretCredentials, SecretProjectID}
}

// Provision acquires GCP credentials and returns them as secrets.
func (p *Provider) Provision(ctx context.Context) (map[string]string, error) {
	if p.cfg.ProjectID == "" {
		return nil, fmt.Errorf("GCP project ID is required")
	}

	// Mode 3: credential JSON provided directly.
	if p.cfg.CredentialJSON != "" {
		return map[string]string{
			SecretCredentials: p.cfg.CredentialJSON,
			SecretProjectID:   p.cfg.ProjectID,
		}, nil
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
