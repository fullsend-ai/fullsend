package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/inference"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// InferenceLayer manages inference provider credentials in the .fullsend repo.
type InferenceLayer struct {
	org      string
	client   forge.Client
	provider inference.Provider
	ui       *ui.Printer
}

var _ Layer = (*InferenceLayer)(nil)

// NewInferenceLayer creates a new InferenceLayer.
func NewInferenceLayer(org string, client forge.Client, provider inference.Provider, printer *ui.Printer) *InferenceLayer {
	return &InferenceLayer{
		org:      org,
		client:   client,
		provider: provider,
		ui:       printer,
	}
}

// Name returns the layer name.
func (l *InferenceLayer) Name() string {
	return "inference"
}

// RequiredScopes returns the scopes needed for the given operation.
func (l *InferenceLayer) RequiredScopes(op Operation) []string {
	switch op {
	case OpInstall, OpAnalyze:
		return []string{"repo"}
	default:
		return nil
	}
}

// Install provisions inference credentials and stores them as repo secrets.
// If all expected secrets already exist, provisioning is skipped to maintain
// idempotency (avoids accumulating SA keys against GCP's 10-key limit).
func (l *InferenceLayer) Install(ctx context.Context) error {
	if l.provider == nil {
		l.ui.StepInfo("no inference provider configured, skipping")
		return nil
	}

	// Check if secrets already exist to avoid re-provisioning.
	allExist := true
	for _, name := range l.provider.SecretNames() {
		exists, err := l.client.RepoSecretExists(ctx, l.org, forge.ConfigRepoName, name)
		if err != nil {
			// Can't verify — proceed with provisioning.
			allExist = false
			break
		}
		if !exists {
			allExist = false
			break
		}
	}
	if allExist {
		l.ui.StepDone(fmt.Sprintf("%s credentials already provisioned, skipping", l.provider.Name()))
		return nil
	}

	l.ui.StepStart(fmt.Sprintf("provisioning %s credentials", l.provider.Name()))

	secrets, err := l.provider.Provision(ctx)
	if err != nil {
		l.ui.StepFail(fmt.Sprintf("failed to provision %s credentials", l.provider.Name()))
		return fmt.Errorf("provisioning %s: %w", l.provider.Name(), err)
	}

	for name, value := range secrets {
		l.ui.StepStart(fmt.Sprintf("storing %s", name))
		if err := l.client.CreateRepoSecret(ctx, l.org, forge.ConfigRepoName, name, value); err != nil {
			l.ui.StepFail(fmt.Sprintf("failed to store %s", name))
			return fmt.Errorf("creating secret %s: %w", name, err)
		}
		l.ui.StepDone(fmt.Sprintf("stored %s", name))
	}

	l.ui.StepDone(fmt.Sprintf("%s credentials provisioned", l.provider.Name()))
	return nil
}

// Uninstall is a no-op. Secrets are removed when the .fullsend repo is deleted.
func (l *InferenceLayer) Uninstall(_ context.Context) error {
	return nil
}

// Analyze checks whether inference credentials exist in the .fullsend repo.
func (l *InferenceLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{Name: l.Name()}

	if l.provider == nil {
		report.Status = StatusInstalled
		report.Details = append(report.Details, "no inference provider configured")
		return report, nil
	}

	secretNames := l.provider.SecretNames()
	var present, missing []string

	for _, name := range secretNames {
		exists, err := l.client.RepoSecretExists(ctx, l.org, forge.ConfigRepoName, name)
		if err != nil {
			return nil, fmt.Errorf("checking secret %s: %w", name, err)
		}
		if exists {
			present = append(present, name)
		} else {
			missing = append(missing, name)
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
