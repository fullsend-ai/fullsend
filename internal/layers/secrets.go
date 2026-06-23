package layers

import (
	"context"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// AgentCredentials holds agent identity (role, name, slug) and app credentials for layer operations.
type AgentCredentials struct {
	Role     string
	Name     string
	Slug     string
	PEM      string
	ClientID string
	AppID    int
}

// SecretsLayer manages agent app secrets and variables in the .fullsend repo.
// When oidcMode is true, Install is a no-op because agent workflows use the
// OIDC token mint instead of create-github-app-token, so repo secrets
// (FULLSEND_{ROLE}_APP_PRIVATE_KEY) and variables (FULLSEND_{ROLE}_CLIENT_ID)
// are not needed.
type SecretsLayer struct {
	org      string
	client   forge.Client
	agents   []AgentCredentials
	ui       *ui.Printer
	oidcMode bool
}

var _ Layer = (*SecretsLayer)(nil)

// NewSecretsLayer creates a new SecretsLayer.
func NewSecretsLayer(org string, client forge.Client, agents []AgentCredentials, printer *ui.Printer) *SecretsLayer {
	return &SecretsLayer{
		org:    org,
		client: client,
		agents: agents,
		ui:     printer,
	}
}

// WithOIDCMode enables OIDC mode and returns the receiver for chaining.
// In OIDC mode, Install is a no-op because PEMs are stored in GCP Secret
// Manager and workflows use the OIDC token mint.
func (s *SecretsLayer) WithOIDCMode() *SecretsLayer {
	s.oidcMode = true
	return s
}

// Name returns the layer name.
func (s *SecretsLayer) Name() string {
	return "secrets"
}

// RequiredScopes returns the scopes needed for the given operation.
// In OIDC mode, Install needs no scopes (it's a no-op) but Analyze still
// needs "repo" to check for stale secrets via RepoSecretExists.
func (s *SecretsLayer) RequiredScopes(op Operation) []string {
	switch op {
	case OpInstall:
		if s.oidcMode {
			return nil
		}
		return []string{"repo"}
	case OpUninstall:
		return nil // no-op
	case OpAnalyze:
		return []string{"repo"}
	default:
		return nil
	}
}

// Install stores agent app private keys as repo secrets and client IDs as
// repo variables in the .fullsend config repo. In OIDC mode this is a no-op
// because PEMs are stored in GCP Secret Manager and workflows use the mint.
func (s *SecretsLayer) Install(ctx context.Context) error {
	if s.oidcMode {
		s.ui.StepDone("skipping repo secrets (OIDC mint mode)")
		return nil
	}
	for _, agent := range s.agents {
		if agent.PEM != "" {
			sName := secretName(agent.Role)
			s.ui.StepStart(fmt.Sprintf("storing private key for %s", agent.Role))
			if err := s.client.CreateRepoSecret(ctx, s.org, forge.ConfigRepoName, sName, agent.PEM); err != nil {
				s.ui.StepFail(fmt.Sprintf("failed to store secret %s", sName))
				return fmt.Errorf("creating secret %s: %w", sName, err)
			}
			s.ui.StepDone(fmt.Sprintf("stored secret %s", sName))
		}

		vName := variableName(agent.Role)
		s.ui.StepStart(fmt.Sprintf("storing client ID for %s", agent.Role))
		if err := s.client.CreateOrUpdateRepoVariable(ctx, s.org, forge.ConfigRepoName, vName, agent.ClientID); err != nil {
			s.ui.StepFail(fmt.Sprintf("failed to store variable %s", vName))
			return fmt.Errorf("creating variable %s: %w", vName, err)
		}
		s.ui.StepDone(fmt.Sprintf("stored variable %s", vName))
	}
	return nil
}

// Uninstall is a no-op. Secrets are removed when the .fullsend repo is deleted.
func (s *SecretsLayer) Uninstall(_ context.Context) error {
	return nil
}

// Analyze checks whether all expected agent secrets and variables exist in the .fullsend repo.
// In OIDC mode, repo secrets are not required — any present are flagged as stale.
func (s *SecretsLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{Name: s.Name()}

	var present []string
	var missing []string

	for _, agent := range s.agents {
		sName := secretName(agent.Role)
		exists, err := s.client.RepoSecretExists(ctx, s.org, forge.ConfigRepoName, sName)
		if err != nil {
			return nil, fmt.Errorf("checking secret %s: %w", sName, err)
		}
		if exists {
			present = append(present, sName)
		} else {
			missing = append(missing, sName)
		}

		vName := variableName(agent.Role)
		varExists, err := s.client.RepoVariableExists(ctx, s.org, forge.ConfigRepoName, vName)
		if err != nil {
			return nil, fmt.Errorf("checking variable %s: %w", vName, err)
		}
		if varExists {
			present = append(present, vName)
		} else {
			missing = append(missing, vName)
		}
	}

	if s.oidcMode {
		report.Status = StatusInstalled
		if len(present) > 0 {
			report.Status = StatusDegraded
			for _, name := range present {
				report.WouldFix = append(report.WouldFix, fmt.Sprintf("remove stale %s (not needed in OIDC mode)", name))
			}
		} else {
			report.Details = append(report.Details, "no repo secrets (OIDC mint mode)")
		}
		return report, nil
	}

	switch {
	case len(missing) == 0:
		report.Status = StatusInstalled
		for _, name := range present {
			report.Details = append(report.Details, fmt.Sprintf("%s exists", name))
		}
	case len(present) == 0:
		report.Status = StatusNotInstalled
		for _, name := range missing {
			report.WouldInstall = append(report.WouldInstall, fmt.Sprintf("create %s", name))
		}
	default:
		report.Status = StatusDegraded
		for _, name := range present {
			report.Details = append(report.Details, fmt.Sprintf("%s exists", name))
		}
		for _, name := range missing {
			report.WouldFix = append(report.WouldFix, fmt.Sprintf("create missing %s", name))
		}
	}

	return report, nil
}

func secretName(role string) string {
	return fmt.Sprintf("FULLSEND_%s_APP_PRIVATE_KEY", strings.ToUpper(role))
}

func variableName(role string) string {
	return fmt.Sprintf("FULLSEND_%s_CLIENT_ID", strings.ToUpper(role))
}
