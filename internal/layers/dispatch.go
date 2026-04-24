package layers

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const dispatchTokenName = "FULLSEND_DISPATCH_TOKEN"

// PromptTokenFunc is a callback that prompts the user for a dispatch token
// interactively. It is called during Install when no token was provided at
// construction time and the org secret doesn't already exist.
type PromptTokenFunc func(ctx context.Context) (string, error)

// DispatchTokenLayer manages the org-level dispatch token that enrolled
// repos use to trigger workflow_dispatch events on the .fullsend repo.
//
// The dispatch token is a fine-grained PAT scoped to the .fullsend repo
// with actions:write and contents:read permissions. It is stored as an org-level Actions
// secret with visibility "selected", scoped to enrolled repos only.
// This way, enrolled repos can trigger dispatches but never access the
// App private keys (which are repo-level secrets on .fullsend).
type DispatchTokenLayer struct {
	org           string
	client        forge.Client
	dispatchToken string // the PAT value to store (empty if reusing existing)
	ui            *ui.Printer
	promptFn      PromptTokenFunc // optional callback to prompt for token interactively
}

var _ Layer = (*DispatchTokenLayer)(nil)

// NewDispatchTokenLayer creates a new DispatchTokenLayer.
// If promptFn is non-nil, it will be called during Install to obtain a
// dispatch token interactively when none was provided and the org secret
// doesn't already exist.
func NewDispatchTokenLayer(org string, client forge.Client, token string, printer *ui.Printer, promptFn PromptTokenFunc) *DispatchTokenLayer {
	return &DispatchTokenLayer{
		org:           org,
		client:        client,
		dispatchToken: token,
		ui:            printer,
		promptFn:      promptFn,
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
// If dispatchToken is non-empty, the secret is created with that value.
// If dispatchToken is empty, the method checks whether the secret already
// exists. If it does, the existing secret is reused. If it doesn't and
// promptFn is set, promptFn is called to obtain a token interactively.
// If it doesn't exist and promptFn is nil, an error is returned.
func (l *DispatchTokenLayer) Install(ctx context.Context) error {
	if l.dispatchToken != "" {
		return l.createSecret(ctx, l.dispatchToken)
	}

	// No token provided — check if the secret already exists.
	exists, err := l.client.OrgSecretExists(ctx, l.org, dispatchTokenName)
	if err != nil {
		return fmt.Errorf("checking org secret %s: %w", dispatchTokenName, err)
	}

	if exists {
		l.ui.StepInfo("reusing existing dispatch token")
		return nil
	}

	// Secret doesn't exist — prompt for it if we can.
	if l.promptFn == nil {
		return fmt.Errorf("dispatch token not provided and org secret %s does not exist", dispatchTokenName)
	}

	token, err := l.promptFn(ctx)
	if err != nil {
		return fmt.Errorf("prompting for dispatch token: %w", err)
	}

	return l.createSecret(ctx, token)
}

func (l *DispatchTokenLayer) createSecret(ctx context.Context, token string) error {
	l.ui.StepStart("creating org secret " + dispatchTokenName)
	if err := l.client.CreateOrgSecret(ctx, l.org, dispatchTokenName, token, nil); err != nil {
		l.ui.StepFail(fmt.Sprintf("failed to create org secret %s", dispatchTokenName))
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
