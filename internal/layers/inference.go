package layers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/inference"
	"github.com/fullsend-ai/fullsend/internal/inference/vertex"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// InferenceLayer manages inference provider credentials. GCP secrets and region
// are stored on the .fullsend repo and duplicated at org scope so enrolled repos
// can pass them through workflow_call dispatch (caller secret context).
type InferenceLayer struct {
	org             string
	client          forge.Client
	provider        inference.Provider
	enrolledRepoIDs []int64
	ui              *ui.Printer
}

var _ Layer = (*InferenceLayer)(nil)

// NewInferenceLayer creates a new InferenceLayer.
func NewInferenceLayer(org string, client forge.Client, provider inference.Provider, enrolledRepoIDs []int64, printer *ui.Printer) *InferenceLayer {
	return &InferenceLayer{
		org:             org,
		client:          client,
		provider:        provider,
		enrolledRepoIDs: enrolledRepoIDs,
		ui:              printer,
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
		if l.provider != nil {
			return []string{"repo", "admin:org"}
		}
		return []string{"repo"}
	default:
		return nil
	}
}

// Install provisions inference credentials and stores them as repo secrets on
// .fullsend and org secrets/variables visible to enrolled repos.
func (l *InferenceLayer) Install(ctx context.Context) error {
	if l.provider == nil {
		l.ui.StepInfo("no inference provider configured, skipping")
		return nil
	}

	l.ui.StepStart(fmt.Sprintf("provisioning %s credentials", l.provider.Name()))

	secrets, err := l.provider.Provision(ctx)
	if err != nil {
		l.ui.StepFail(fmt.Sprintf("failed to provision %s credentials", l.provider.Name()))
		return fmt.Errorf("provisioning %s: %w", l.provider.Name(), err)
	}

	repoIDs, err := l.inferenceRepoIDs(ctx)
	if err != nil {
		return err
	}

	secretNames := sortedStringMapKeys(secrets)
	for _, name := range secretNames {
		value := secrets[name]
		l.ui.StepStart(fmt.Sprintf("storing %s on .fullsend", name))
		if err := l.client.CreateRepoSecret(ctx, l.org, forge.ConfigRepoName, name, value); err != nil {
			l.ui.StepFail(fmt.Sprintf("failed to store %s", name))
			return fmt.Errorf("creating secret %s: %w", name, err)
		}
		l.ui.StepDone(fmt.Sprintf("stored %s on .fullsend", name))

		l.ui.StepStart(fmt.Sprintf("storing org secret %s", name))
		if err := l.client.CreateOrgSecret(ctx, l.org, name, value, repoIDs); err != nil {
			l.ui.StepFail(fmt.Sprintf("failed to store org secret %s", name))
			return fmt.Errorf("creating org secret %s: %w", name, err)
		}
		l.ui.StepDone(fmt.Sprintf("stored org secret %s", name))
	}

	l.ui.StepDone(fmt.Sprintf("%s credentials provisioned", l.provider.Name()))

	variables := l.provider.Variables()
	varNames := sortedStringMapKeys(variables)
	for _, name := range varNames {
		value := variables[name]
		l.ui.StepStart(fmt.Sprintf("storing org variable %s", name))
		if err := l.client.CreateOrUpdateOrgVariable(ctx, l.org, name, value, repoIDs); err != nil {
			l.ui.StepFail(fmt.Sprintf("failed to store org variable %s", name))
			return fmt.Errorf("creating org variable %s: %w", name, err)
		}
		l.ui.StepDone(fmt.Sprintf("stored org variable %s", name))

		l.ui.StepStart(fmt.Sprintf("setting variable %s on .fullsend", name))
		if err := l.client.CreateOrUpdateRepoVariable(ctx, l.org, forge.ConfigRepoName, name, value); err != nil {
			l.ui.StepFail(fmt.Sprintf("failed to set variable %s", name))
			return fmt.Errorf("setting variable %s: %w", name, err)
		}
		l.ui.StepDone(fmt.Sprintf("set variable %s on .fullsend", name))
	}

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

// SyncEnrolledRepoAccess updates org-level inference secret and variable visibility
// for the given enrolled repository IDs. It is best-effort: callers typically log
// warnings and continue when individual updates fail (e.g. admin enable/disable).
func (l *InferenceLayer) SyncEnrolledRepoAccess(ctx context.Context, repoIDs []int64) {
	repoIDs, err := l.inferenceRepoIDsFromList(ctx, repoIDs)
	if err != nil {
		l.ui.StepWarn("could not resolve inference repo access list: " + err.Error())
		return
	}

	for _, name := range inferenceOrgSecretNames(l.provider) {
		exists, checkErr := l.client.OrgSecretExists(ctx, l.org, name)
		if checkErr != nil {
			l.ui.StepWarn(fmt.Sprintf("could not check org secret %s: %v", name, checkErr))
			continue
		}
		if !exists {
			continue
		}

		l.ui.StepStart(fmt.Sprintf("Updating %s visibility for enrolled repos", name))
		if setErr := l.client.SetOrgSecretRepos(ctx, l.org, name, repoIDs); setErr != nil {
			l.ui.StepWarn(fmt.Sprintf("failed to update %s visibility: %v", name, setErr))
		} else {
			l.ui.StepDone(fmt.Sprintf("Updated %s visibility (%d repos)", name, len(repoIDs)))
		}
	}

	for _, name := range inferenceOrgVariableNames(l.provider) {
		exists, checkErr := l.client.OrgVariableExists(ctx, l.org, name)
		if checkErr != nil {
			l.ui.StepWarn(fmt.Sprintf("could not check org variable %s: %v", name, checkErr))
			continue
		}
		if !exists {
			continue
		}

		l.ui.StepStart(fmt.Sprintf("Updating %s visibility for enrolled repos", name))
		if setErr := l.client.SetOrgVariableRepos(ctx, l.org, name, repoIDs); setErr != nil {
			l.ui.StepWarn(fmt.Sprintf("failed to update %s visibility: %v", name, setErr))
		} else {
			l.ui.StepDone(fmt.Sprintf("Updated %s visibility (%d repos)", name, len(repoIDs)))
		}
	}

	l.syncDotPrefixedRepoVariables(ctx, repoIDs)
}

func inferenceOrgSecretNames(provider inference.Provider) []string {
	if provider != nil {
		return provider.SecretNames()
	}
	return []string{vertex.SecretWIFProvider, vertex.SecretProjectID}
}

func inferenceOrgVariableNames(provider inference.Provider) []string {
	if provider != nil {
		return sortedStringMapKeys(provider.Variables())
	}
	return []string{vertex.VariableRegion}
}

// syncDotPrefixedRepoVariables copies inference org variables onto dot-prefixed
// enrolled repos that cannot read org variables (GitHub platform limitation).
func (l *InferenceLayer) syncDotPrefixedRepoVariables(ctx context.Context, repoIDs []int64) {
	dotRepos := l.dotPrefixedRepos(ctx, repoIDs)
	if len(dotRepos) == 0 {
		return
	}

	for _, name := range inferenceOrgVariableNames(l.provider) {
		value, ok, err := l.client.GetRepoVariable(ctx, l.org, forge.ConfigRepoName, name)
		if err != nil {
			l.ui.StepWarn(fmt.Sprintf("could not read %s from .fullsend: %v", name, err))
			continue
		}
		if !ok {
			continue
		}

		for _, repo := range dotRepos {
			l.ui.StepStart(fmt.Sprintf("storing repo variable %s on %s", name, repo.Name))
			if err := l.client.CreateOrUpdateRepoVariable(ctx, l.org, repo.Name, name, value); err != nil {
				l.ui.StepWarn(fmt.Sprintf("failed to store repo variable %s on %s: %v", name, repo.Name, err))
			} else {
				l.ui.StepDone(fmt.Sprintf("stored repo variable %s on %s", name, repo.Name))
			}
		}
	}
}

func (l *InferenceLayer) inferenceRepoIDs(ctx context.Context) ([]int64, error) {
	return l.inferenceRepoIDsFromList(ctx, l.enrolledRepoIDs)
}

func (l *InferenceLayer) inferenceRepoIDsFromList(ctx context.Context, repoIDs []int64) ([]int64, error) {
	return appendConfigRepoID(ctx, l.client, l.org, repoIDs)
}

// appendConfigRepoID ensures the .fullsend config repo is included in repo ID lists.
func appendConfigRepoID(ctx context.Context, client forge.Client, org string, repoIDs []int64) ([]int64, error) {
	repoIDs = append([]int64(nil), repoIDs...)
	configRepo, err := client.GetRepo(ctx, org, forge.ConfigRepoName)
	if err == nil && configRepo != nil {
		seen := make(map[int64]bool, len(repoIDs))
		for _, id := range repoIDs {
			seen[id] = true
		}
		if !seen[configRepo.ID] {
			repoIDs = append(repoIDs, configRepo.ID)
		}
	}
	return repoIDs, nil
}

func (l *InferenceLayer) dotPrefixedRepos(ctx context.Context, repoIDs []int64) []forge.Repository {
	allRepos, err := l.client.ListOrgRepos(ctx, l.org)
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

// Uninstall is a no-op. Secrets are removed when the .fullsend repo is deleted.
func (l *InferenceLayer) Uninstall(_ context.Context) error {
	return nil
}

// Analyze checks whether inference credentials exist in .fullsend and at org scope.
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
		repoExists, err := l.client.RepoSecretExists(ctx, l.org, forge.ConfigRepoName, name)
		if err != nil {
			return nil, fmt.Errorf("checking secret %s: %w", name, err)
		}
		orgExists, err := l.client.OrgSecretExists(ctx, l.org, name)
		if err != nil {
			return nil, fmt.Errorf("checking org secret %s: %w", name, err)
		}
		if repoExists && orgExists {
			present = append(present, name)
		} else {
			missing = append(missing, name)
		}
	}

	for name := range l.provider.Variables() {
		repoExists, err := l.client.RepoVariableExists(ctx, l.org, forge.ConfigRepoName, name)
		if err != nil {
			return nil, fmt.Errorf("checking variable %s: %w", name, err)
		}
		orgExists, err := l.client.OrgVariableExists(ctx, l.org, name)
		if err != nil {
			return nil, fmt.Errorf("checking org variable %s: %w", name, err)
		}
		if repoExists && orgExists {
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

// sortedStringMapKeys returns sorted keys from a string map.
func sortedStringMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
