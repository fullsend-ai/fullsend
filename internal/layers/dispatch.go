package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const dispatchTokenName = "FULLSEND_DISPATCH_TOKEN"

// DispatchTokenLayer manages the org-level dispatch token that enrolled
// repos use to trigger workflow_dispatch events on the .fullsend repo.
//
// The dispatch token is a fine-grained PAT scoped to the .fullsend repo
// with actions:write permission. It is stored as an org-level Actions
// secret with visibility "selected", scoped to enrolled repos only.
// This way, enrolled repos can trigger dispatches but never access the
// App private keys (which are repo-level secrets on .fullsend).
type DispatchTokenLayer struct {
	org             string
	client          forge.Client
	dispatchToken   string  // the PAT value to store (empty if reusing existing)
	enrolledRepoIDs []int64 // repo IDs that should have access to this secret
	ui              *ui.Printer
}

var _ Layer = (*DispatchTokenLayer)(nil)

// NewDispatchTokenLayer creates a new DispatchTokenLayer.
func NewDispatchTokenLayer(org string, client forge.Client, token string, repoIDs []int64, printer *ui.Printer) *DispatchTokenLayer {
	return &DispatchTokenLayer{
		org:             org,
		client:          client,
		dispatchToken:   token,
		enrolledRepoIDs: repoIDs,
		ui:              printer,
	}
}

// Name returns the layer name.
func (l *DispatchTokenLayer) Name() string {
	return "dispatch-token"
}

// RequiredScopes returns the scopes needed for the given operation.
func (l *DispatchTokenLayer) RequiredScopes(op Operation) []string {
	switch op {
	case OpInstall, OpUninstall, OpAnalyze:
		return []string{"admin:org"}
	default:
		return nil
	}
}

// Install creates or updates the org-level dispatch token secret.
// If dispatchToken is empty, the secret value is reused (not recreated),
// but the repo access list is still updated if enrolledRepoIDs is set.
func (l *DispatchTokenLayer) Install(ctx context.Context) error {
	if l.dispatchToken == "" {
		l.ui.StepInfo("reusing existing dispatch token")

		if len(l.enrolledRepoIDs) > 0 {
			l.ui.StepStart("updating dispatch token repo access list")
			if err := l.client.SetOrgSecretRepos(ctx, l.org, dispatchTokenName, l.enrolledRepoIDs); err != nil {
				l.ui.StepFail("failed to update dispatch token repo access")
				return fmt.Errorf("updating org secret repo access: %w", err)
			}
			l.ui.StepDone("updated dispatch token repo access list")
		}
		return nil
	}

	l.ui.StepStart("creating org secret " + dispatchTokenName)
	if err := l.client.CreateOrgSecret(ctx, l.org, dispatchTokenName, l.dispatchToken, l.enrolledRepoIDs); err != nil {
		l.ui.StepFail("failed to create org secret " + dispatchTokenName)
		return fmt.Errorf("creating org secret %s: %w", dispatchTokenName, err)
	}
	l.ui.StepDone("created org secret " + dispatchTokenName)
	return nil
}

// Uninstall removes the org-level dispatch token secret if it exists.
func (l *DispatchTokenLayer) Uninstall(ctx context.Context) error {
	exists, err := l.client.OrgSecretExists(ctx, l.org, dispatchTokenName)
	if err != nil {
		return fmt.Errorf("checking org secret %s: %w", dispatchTokenName, err)
	}

	if !exists {
		l.ui.StepInfo(dispatchTokenName + " already deleted")
		return nil
	}

	l.ui.StepStart("deleting org secret " + dispatchTokenName)
	if err := l.client.DeleteOrgSecret(ctx, l.org, dispatchTokenName); err != nil {
		l.ui.StepFail("failed to delete org secret " + dispatchTokenName)
		return fmt.Errorf("deleting org secret %s: %w", dispatchTokenName, err)
	}
	l.ui.StepDone("deleted org secret " + dispatchTokenName)
	return nil
}

// Analyze checks whether the dispatch token org secret exists.
func (l *DispatchTokenLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{Name: l.Name()}

	exists, err := l.client.OrgSecretExists(ctx, l.org, dispatchTokenName)
	if err != nil {
		return nil, fmt.Errorf("checking org secret %s: %w", dispatchTokenName, err)
	}

	if exists {
		report.Status = StatusInstalled
		report.Details = append(report.Details, dispatchTokenName+" org secret exists")
	} else {
		report.Status = StatusNotInstalled
		report.WouldInstall = append(report.WouldInstall, "create "+dispatchTokenName+" org secret")
	}

	return report, nil
}
