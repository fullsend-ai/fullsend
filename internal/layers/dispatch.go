package layers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const dispatchTokenName = "FULLSEND_DISPATCH_TOKEN"

// DispatchTokenLayer manages the org-level dispatch mechanism that enrolled
// repos use to trigger agent workflows on the .fullsend repo via workflow_call.
//
// The mint URL is stored as an org-level variable (FULLSEND_MINT_URL).
// A repo-level copy is also set on the config repo (.fullsend) because GitHub
// org variables with "selected" visibility are not accessible to repos whose
// names start with a dot.
type DispatchTokenLayer struct {
	org             string
	client          forge.Client
	enrolledRepoIDs []int64
	dispatcher      dispatch.Dispatcher
	ui              *ui.Printer
}

var _ Layer = (*DispatchTokenLayer)(nil)

// NewOIDCDispatchLayer creates a new DispatchTokenLayer in OIDC mode.
// It uses a dispatch.Dispatcher to provision cloud infrastructure and store
// the mint URL as an org-level variable.
func NewOIDCDispatchLayer(org string, client forge.Client, enrolledRepoIDs []int64, dispatcher dispatch.Dispatcher, printer *ui.Printer) *DispatchTokenLayer {
	return &DispatchTokenLayer{
		org:             org,
		client:          client,
		enrolledRepoIDs: enrolledRepoIDs,
		dispatcher:      dispatcher,
		ui:              printer,
	}
}

// Name returns the layer name.
func (l *DispatchTokenLayer) Name() string {
	return "dispatch"
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

// Install creates or updates the dispatch mechanism.
func (l *DispatchTokenLayer) Install(ctx context.Context) error {
	return l.installOIDC(ctx)
}

// installOIDC provisions cloud infrastructure and stores the dispatch URL.
// If a stale PAT dispatch secret exists from a previous mode, it is cleaned up.
func (l *DispatchTokenLayer) installOIDC(ctx context.Context) error {
	if l.dispatcher == nil {
		return fmt.Errorf("OIDC dispatcher not configured")
	}

	// Clean up stale PAT secret if migrating from PAT to OIDC mode.
	exists, checkErr := l.client.OrgSecretExists(ctx, l.org, dispatchTokenName)
	if checkErr != nil {
		l.ui.StepWarn("could not check for stale " + dispatchTokenName + ": " + checkErr.Error())
	}
	if checkErr == nil && exists {
		l.ui.StepInfo("detected existing PAT dispatch setup — migrating to OIDC mint")
		l.ui.StepStart("removing stale " + dispatchTokenName + " org secret")
		if delErr := l.client.DeleteOrgSecret(ctx, l.org, dispatchTokenName); delErr != nil {
			l.ui.StepWarn("failed to remove stale " + dispatchTokenName + ": " + delErr.Error())
		} else {
			l.ui.StepDone("removed stale " + dispatchTokenName)
		}
	}

	l.ui.StepStart(fmt.Sprintf("provisioning %s mint function", l.dispatcher.Name()))

	variables, err := l.dispatcher.Provision(ctx)
	if err != nil {
		l.ui.StepFail("failed to provision mint function")
		return fmt.Errorf("provisioning %s mint: %w", l.dispatcher.Name(), err)
	}

	l.ui.StepDone(fmt.Sprintf("provisioned %s mint function", l.dispatcher.Name()))

	repoIDs := l.enrolledRepoIDs
	configRepo, err := l.client.GetRepo(ctx, l.org, forge.ConfigRepoName)
	if err == nil && configRepo != nil {
		seen := make(map[int64]bool, len(repoIDs))
		for _, id := range repoIDs {
			seen[id] = true
		}
		if !seen[configRepo.ID] {
			repoIDs = append(append([]int64(nil), repoIDs...), configRepo.ID)
		}
	}

	varNames := make([]string, 0, len(variables))
	for name := range variables {
		varNames = append(varNames, name)
	}
	sort.Strings(varNames)

	for _, name := range varNames {
		value := variables[name]
		l.ui.StepStart(fmt.Sprintf("storing org variable %s", name))
		if err := l.client.CreateOrUpdateOrgVariable(ctx, l.org, name, value, repoIDs); err != nil {
			l.ui.StepFail(fmt.Sprintf("failed to store org variable %s", name))
			return fmt.Errorf("creating org variable %s: %w", name, err)
		}
		l.ui.StepDone(fmt.Sprintf("stored org variable %s", name))
	}

	// GitHub org variables with "selected" visibility don't resolve in
	// Actions runners for repos whose names start with a dot. Set
	// repo-level copies on any dot-prefixed enrolled repos as a workaround.
	dotRepos := l.dotPrefixedRepos(ctx, repoIDs)
	for _, repo := range dotRepos {
		for _, name := range varNames {
			value := variables[name]
			l.ui.StepStart(fmt.Sprintf("storing repo variable %s on %s", name, repo.Name))
			if err := l.client.CreateOrUpdateRepoVariable(ctx, l.org, repo.Name, name, value); err != nil {
				l.ui.StepFail(fmt.Sprintf("failed to store repo variable %s on %s", name, repo.Name))
				return fmt.Errorf("creating repo variable %s on %s: %w", name, repo.Name, err)
			}
			l.ui.StepDone(fmt.Sprintf("stored repo variable %s on %s", name, repo.Name))
		}
	}

	return nil
}

// dotPrefixedRepos returns enrolled repos whose names start with "." by
// resolving the given repo IDs against the org's repo list. Repos with
// dot-prefixed names cannot read GitHub org variables due to a platform
// bug, so callers use this to set repo-level fallback variables.
func (l *DispatchTokenLayer) dotPrefixedRepos(ctx context.Context, repoIDs []int64) []forge.Repository {
	allRepos, err := l.client.ListOrgRepos(ctx, l.org, false)
	if err != nil {
		l.ui.StepWarn("could not list org repos to detect dot-prefixed names: " + err.Error())
		return nil
	}

	idSet := make(map[int64]bool, len(repoIDs))
	for _, id := range repoIDs {
		idSet[id] = true
	}

	var result []forge.Repository
	for _, r := range allRepos {
		if idSet[r.ID] && strings.HasPrefix(r.Name, ".") {
			result = append(result, r)
		}
	}
	return result
}

// Uninstall removes the dispatch mechanism.
func (l *DispatchTokenLayer) Uninstall(ctx context.Context) error {
	return l.uninstallOIDC(ctx)
}

// uninstallPAT removes the org-level dispatch token secret if it exists.
func (l *DispatchTokenLayer) uninstallPAT(ctx context.Context) error {
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

// uninstallOIDC removes the org-level dispatch URL variable.
func (l *DispatchTokenLayer) uninstallOIDC(ctx context.Context) error {
	if l.dispatcher == nil {
		return nil
	}

	var foundAny bool
	for _, name := range l.dispatcher.OrgVariableNames() {
		exists, err := l.client.OrgVariableExists(ctx, l.org, name)
		if err != nil {
			return fmt.Errorf("checking org variable %s: %w", name, err)
		}

		if !exists {
			l.ui.StepInfo(name + " already deleted")
			continue
		}

		foundAny = true
		l.ui.StepStart("deleting org variable " + name)
		if err := l.client.DeleteOrgVariable(ctx, l.org, name); err != nil {
			l.ui.StepFail("failed to delete org variable " + name)
			return fmt.Errorf("deleting org variable %s: %w", name, err)
		}
		l.ui.StepDone("deleted org variable " + name)
	}

	if foundAny {
		l.ui.StepWarn("GCP resources (Cloud Function, service account, secrets) must be deleted manually")
	}

	return nil
}

// Analyze checks whether the dispatch mechanism is configured.
func (l *DispatchTokenLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	return l.analyzeOIDC(ctx)
}

// analyzePAT checks whether the dispatch token org secret exists.
// Used only by bothModesDispatchLayer for stale secret detection.
func (l *DispatchTokenLayer) analyzePAT(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{Name: l.Name()}

	exists, err := l.client.OrgSecretExists(ctx, l.org, dispatchTokenName)
	if err != nil {
		return nil, fmt.Errorf("checking org secret %s: %w", dispatchTokenName, err)
	}

	if !exists {
		report.Status = StatusNotInstalled
		return report, nil
	}

	report.Status = StatusInstalled
	report.Details = append(report.Details, dispatchTokenName+" org secret exists")
	return report, nil
}

// analyzeOIDC checks whether the dispatch URL org variable exists.
func (l *DispatchTokenLayer) analyzeOIDC(ctx context.Context) (*LayerReport, error) {
	report := &LayerReport{Name: l.Name()}

	if l.dispatcher == nil {
		report.Status = StatusNotInstalled
		report.WouldInstall = append(report.WouldInstall, "configure OIDC dispatch")
		return report, nil
	}

	allExist := true
	for _, name := range l.dispatcher.OrgVariableNames() {
		exists, err := l.client.OrgVariableExists(ctx, l.org, name)
		if err != nil {
			return nil, fmt.Errorf("checking org variable %s: %w", name, err)
		}

		if exists {
			report.Details = append(report.Details, name+" org variable exists")
		} else {
			report.WouldInstall = append(report.WouldInstall, "create "+name+" org variable")
			allExist = false
		}
	}

	if allExist {
		report.Status = StatusInstalled
	} else {
		report.Status = StatusNotInstalled
	}

	return report, nil
}

// bothModesDispatchLayer cleans up both PAT and OIDC dispatch artifacts.
// Used during uninstall when the dispatch mode cannot be determined (e.g.,
// config repo already deleted).
type bothModesDispatchLayer struct {
	pat  *DispatchTokenLayer
	oidc *DispatchTokenLayer
}

// NewBothModesDispatchLayer creates a layer that cleans both dispatch modes.
// The PAT-mode layer is constructed inline for stale secret cleanup only.
func NewBothModesDispatchLayer(org string, client forge.Client, dispatcher dispatch.Dispatcher, printer *ui.Printer) Layer {
	return &bothModesDispatchLayer{
		pat: &DispatchTokenLayer{
			org:    org,
			client: client,
			ui:     printer,
		},
		oidc: NewOIDCDispatchLayer(org, client, nil, dispatcher, printer),
	}
}

func (l *bothModesDispatchLayer) Name() string { return "dispatch" }

func (l *bothModesDispatchLayer) RequiredScopes(op Operation) []string {
	seen := make(map[string]bool)
	var scopes []string
	for _, s := range l.pat.RequiredScopes(op) {
		if !seen[s] {
			seen[s] = true
			scopes = append(scopes, s)
		}
	}
	for _, s := range l.oidc.RequiredScopes(op) {
		if !seen[s] {
			seen[s] = true
			scopes = append(scopes, s)
		}
	}
	return scopes
}

func (l *bothModesDispatchLayer) Install(_ context.Context) error {
	return fmt.Errorf("bothModesDispatchLayer does not support Install")
}

func (l *bothModesDispatchLayer) Uninstall(ctx context.Context) error {
	patErr := l.pat.uninstallPAT(ctx)
	oidcErr := l.oidc.uninstallOIDC(ctx)
	return errors.Join(patErr, oidcErr)
}

func (l *bothModesDispatchLayer) Analyze(ctx context.Context) (*LayerReport, error) {
	patReport, err := l.pat.analyzePAT(ctx)
	if err != nil {
		return nil, err
	}
	oidcReport, err := l.oidc.analyzeOIDC(ctx)
	if err != nil {
		return nil, err
	}

	merged := &LayerReport{Name: l.Name()}
	merged.Details = append(merged.Details, patReport.Details...)
	merged.Details = append(merged.Details, oidcReport.Details...)
	merged.WouldInstall = append(merged.WouldInstall, patReport.WouldInstall...)
	merged.WouldInstall = append(merged.WouldInstall, oidcReport.WouldInstall...)

	if patReport.Status == StatusInstalled || oidcReport.Status == StatusInstalled {
		merged.Status = StatusInstalled
	} else {
		merged.Status = StatusNotInstalled
	}

	return merged, nil
}
