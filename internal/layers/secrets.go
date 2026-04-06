package layers

import (
	"context"
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// AgentCredentials extends AgentEntry with app credentials.
type AgentCredentials struct {
	config.AgentEntry
	PEM   string
	AppID int
}

// SecretsLayer manages agent app secrets and variables in the .fullsend repo.
type SecretsLayer struct {
	org    string
	client forge.Client
	agents []AgentCredentials
	ui     *ui.Printer
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

// Name returns the layer name.
func (s *SecretsLayer) Name() string {
	return "secrets"
}

// Install stores agent app private keys as repo secrets and app IDs as
// repo variables in the .fullsend config repo.
func (s *SecretsLayer) Install(ctx context.Context) error {
	for _, agent := range s.agents {
		if agent.PEM == "" {
			s.ui.StepInfo(fmt.Sprintf("skipping %s (reusing existing app credentials)", agent.Role))
			continue
		}

		sName := secretName(agent.Role)
		s.ui.StepStart(fmt.Sprintf("storing private key for %s", agent.Role))
		if err := s.client.CreateRepoSecret(ctx, s.org, forge.ConfigRepoName, sName, agent.PEM); err != nil {
			s.ui.StepFail(fmt.Sprintf("failed to store secret %s", sName))
			return fmt.Errorf("creating secret %s: %w", sName, err)
		}
		s.ui.StepDone(fmt.Sprintf("stored secret %s", sName))

		vName := variableName(agent.Role)
		s.ui.StepStart(fmt.Sprintf("storing app ID for %s", agent.Role))
		if err := s.client.CreateOrUpdateRepoVariable(ctx, s.org, forge.ConfigRepoName, vName, fmt.Sprintf("%d", agent.AppID)); err != nil {
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
	return fmt.Sprintf("FULLSEND_%s_APP_ID", strings.ToUpper(role))
}
