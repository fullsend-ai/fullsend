package repos

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

var gitlabRoleOperationLocks sync.Map // map[string]*sync.Mutex

func gitlabRoleOperationLock(owner, repo string) *sync.Mutex {
	key := owner + "/" + repo
	lock := &sync.Mutex{}
	actual, _ := gitlabRoleOperationLocks.LoadOrStore(key, lock)
	return actual.(*sync.Mutex)
}

// GitLabRoleCutoverConfig controls the explicit verification-and-cutover
// operation. Cutover is intentionally separate from normal provisioning and
// rotation so an ordinary install can never retire the shared credential by
// accident.
type GitLabRoleCutoverConfig struct {
	Owner          string
	Repo           string
	Client         forge.Client
	TokenInventory ProjectAccessTokenClient
	// RequireTokenInventory is retained for source compatibility, but the
	// cutover operation always requires inventory. Callers cannot opt out of
	// lifecycle verification.
	RequireTokenInventory bool
	DrainConfirmed        bool
	Now                   time.Time
	DryRun                bool
}

// GitLabRoleCutoverResult is the non-secret outcome of a cutover attempt.
type GitLabRoleCutoverResult struct {
	Mode          gitlabroles.Mode
	Readiness     gitlabroles.BuiltinReadiness
	Registered    gitlabroles.RegisteredReadiness
	Lifecycle     gitlabroles.Report
	Enforced      bool
	SharedRetired bool
	RolledBack    bool
	DryRun        bool
	Diagnostics   []string
}

// CutoverGitLabRoleCredentials verifies every registered role, enables the
// fail-closed enforced gate, and retires FULLSEND_FORGE_TOKEN. The shared
// secret is deleted only after the enforced gate has been written. If secret
// retirement fails after this call changed the gate, the gate is rolled back
// to migrating so the existing shared-token path remains recoverable.
//
// This function never reads or returns secret values. It does not infer that
// a role is ready from the presence of FULLSEND_FORGE_TOKEN.
func CutoverGitLabRoleCredentials(ctx context.Context, cfg GitLabRoleCutoverConfig) (GitLabRoleCutoverResult, error) {
	result := GitLabRoleCutoverResult{DryRun: cfg.DryRun}
	if cfg.Client == nil {
		return result, fmt.Errorf("GitLab role cutover requires a forge client")
	}
	cutoverLock := gitlabRoleOperationLock(cfg.Owner, cfg.Repo)
	cutoverLock.Lock()
	defer cutoverLock.Unlock()
	if !cfg.DrainConfirmed {
		return result, fmt.Errorf("GitLab role cutover requires confirmation that in-flight shared-token jobs are drained")
	}
	if cfg.TokenInventory == nil {
		return result, fmt.Errorf("GitLab role cutover requires GitLab project-token inventory")
	}

	mode, reg, present, err := LoadGitLabRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo)
	if err != nil {
		return result, fmt.Errorf("loading GitLab role state for cutover: %w", err)
	}
	result.Mode = mode
	result.Readiness = gitlabroles.CheckBuiltinReadiness(present, reg)
	result.Registered = gitlabroles.CheckRegisteredReadiness(present, reg)
	if cfg.TokenInventory != nil {
		tokens, listErr := cfg.TokenInventory.ListProjectAccessTokens(ctx, cfg.Owner, cfg.Repo)
		if listErr != nil {
			return result, fmt.Errorf("listing GitLab project tokens for cutover: %w", listErr)
		}
		result.Lifecycle = gitlabroles.DiagnoseLifecycle(mode, present, reg, snapshotsFrom(tokens), cfg.Now, gitlabroles.DefaultRotationLead)
		lifecycle := make(map[gitlabroles.Role]gitlabroles.LifecycleState, len(result.Lifecycle.Roles))
		for _, role := range result.Lifecycle.Roles {
			lifecycle[role.Name] = role.Lifecycle
		}
		result.Readiness = result.Readiness.WithLifecycle(lifecycle)
		result.Registered = result.Registered.WithLifecycle(lifecycle)
	}
	result.Diagnostics = append(result.Diagnostics, result.Readiness.Diagnostics...)
	result.Diagnostics = append(result.Diagnostics, result.Registered.Diagnostics...)
	if !result.Readiness.Ready || !result.Registered.Ready {
		return result, fmt.Errorf("GitLab role cutover is not ready: %s", cutoverMissingRoles(result))
	}
	if mode != gitlabroles.ModeMigrating && mode != gitlabroles.ModeEnforced {
		return result, fmt.Errorf("GitLab role cutover requires migration mode %q or %q, got %q", gitlabroles.ModeMigrating, gitlabroles.ModeEnforced, mode)
	}
	if cfg.DryRun {
		result.Enforced = true
		result.SharedRetired = secretPresent(present, forge.SecretForgeToken)
		result.Diagnostics = append(result.Diagnostics, "dry-run: would enable enforced mode and retire FULLSEND_FORGE_TOKEN")
		return result, nil
	}

	// Re-read the state immediately before the irreversible transition. This
	// closes the common check-then-cutover window when provisioning, rotation,
	// or an operator changes the registry or role secrets while verification is
	// in progress. GitLab does not expose a repository-scoped CAS for this
	// compound operation, so callers must still serialize concurrent cutovers.
	latestMode, latestReg, latestPresent, err := LoadGitLabRoleState(ctx, cfg.Client, cfg.Owner, cfg.Repo)
	if err != nil {
		return result, fmt.Errorf("revalidating GitLab role state before cutover: %w", err)
	}
	if latestMode != mode || !reflect.DeepEqual(latestReg, reg) || !reflect.DeepEqual(latestPresent, present) {
		return result, fmt.Errorf("GitLab role state changed during cutover verification; rerun cutover")
	}
	latestBuiltin := gitlabroles.CheckBuiltinReadiness(latestPresent, latestReg)
	latestRegistered := gitlabroles.CheckRegisteredReadiness(latestPresent, latestReg)
	if cfg.TokenInventory != nil {
		tokens, listErr := cfg.TokenInventory.ListProjectAccessTokens(ctx, cfg.Owner, cfg.Repo)
		if listErr != nil {
			return result, fmt.Errorf("relisting GitLab project tokens before cutover: %w", listErr)
		}
		latestLifecycle := gitlabroles.DiagnoseLifecycle(latestMode, latestPresent, latestReg, snapshotsFrom(tokens), cfg.Now, gitlabroles.DefaultRotationLead)
		lifecycle := make(map[gitlabroles.Role]gitlabroles.LifecycleState, len(latestLifecycle.Roles))
		for _, role := range latestLifecycle.Roles {
			lifecycle[role.Name] = role.Lifecycle
		}
		latestBuiltin = latestBuiltin.WithLifecycle(lifecycle)
		latestRegistered = latestRegistered.WithLifecycle(lifecycle)
	}
	if !latestBuiltin.Ready || !latestRegistered.Ready {
		return result, fmt.Errorf("GitLab role state is no longer ready for cutover; rerun verification")
	}

	gateChanged := mode != gitlabroles.ModeEnforced
	if gateChanged {
		if err := cfg.Client.UpdateCIVariable(ctx, cfg.Owner, cfg.Repo, forge.VarGitLabRoleMigration, string(gitlabroles.ModeEnforced), true); err != nil {
			return result, fmt.Errorf("enabling enforced GitLab role migration: %w", err)
		}
	}
	result.Enforced = true

	if !secretPresent(present, forge.SecretForgeToken) {
		result.SharedRetired = true
		result.Diagnostics = append(result.Diagnostics, "shared credential already retired")
		return result, nil
	}
	if err := cfg.Client.DeleteRepoSecret(ctx, cfg.Owner, cfg.Repo, forge.SecretForgeToken); err != nil {
		if gateChanged {
			rollbackErr := cfg.Client.UpdateCIVariable(ctx, cfg.Owner, cfg.Repo, forge.VarGitLabRoleMigration, string(gitlabroles.ModeMigrating), true)
			if rollbackErr != nil {
				return result, fmt.Errorf("retiring shared GitLab credential: %v; rollback to migrating also failed: %w", err, rollbackErr)
			}
			result.RolledBack = true
		}
		// If the gate was already enforced, preserve that fact in the
		// observable result. There was no safe rollback to perform.
		if gateChanged {
			result.Enforced = false
		}
		return result, fmt.Errorf("retiring shared GitLab credential: %w", err)
	}
	result.SharedRetired = true
	result.Diagnostics = append(result.Diagnostics, "shared credential retired; enforced mode is active")
	return result, nil
}

func secretPresent(present map[string]bool, name string) bool {
	return present != nil && present[name]
}

func cutoverMissingRoles(result GitLabRoleCutoverResult) string {
	missing := make([]string, 0, len(result.Readiness.Missing)+len(result.Registered.Missing))
	for _, role := range result.Readiness.Missing {
		missing = append(missing, string(role))
	}
	for _, role := range result.Registered.Missing {
		seen := false
		for _, existing := range missing {
			if existing == string(role) {
				seen = true
				break
			}
		}
		if !seen {
			missing = append(missing, string(role))
		}
	}
	if len(missing) == 0 {
		return "unknown readiness failure"
	}
	return fmt.Sprintf("roles not ready: %v", missing)
}
