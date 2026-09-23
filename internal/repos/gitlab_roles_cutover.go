package repos

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// ErrGitLabRoleCutoverNotReady indicates a required role credential is
// missing or not yet ready when cutover is attempted. Ordinary unflagged
// install treats this as deferred: leave migrating, do not reopen the
// shared-token fallback.
var ErrGitLabRoleCutoverNotReady = errors.New("GitLab role cutover is not ready")

// ErrGitLabRoleCutoverWrongMode indicates cutover was attempted while the
// GitLab role migration gate is neither migrating nor enforced. Ordinary
// unflagged install treats this as deferred rather than failing the
// whole converge.
var ErrGitLabRoleCutoverWrongMode = errors.New("GitLab role cutover requires migrating or enforced mode")

// ErrGitLabRoleCutoverStateChanged indicates the role registry, credential
// presence, or rotation state changed between the initial readiness check
// and the final pre-cutover revalidation. Ordinary unflagged install
// treats this as deferred rather than failing the whole converge.
var ErrGitLabRoleCutoverStateChanged = errors.New("GitLab role state changed during cutover verification")

// IsGitLabRoleCutoverDeferred reports whether err is a cutover
// precondition failure that ordinary install should leave in place
// instead of failing.
func IsGitLabRoleCutoverDeferred(err error) bool {
	return errors.Is(err, ErrGitLabRoleCutoverNotReady) ||
		errors.Is(err, ErrGitLabRoleCutoverWrongMode) ||
		errors.Is(err, ErrGitLabRoleCutoverStateChanged)
}

var gitlabRoleOperationLocks sync.Map // map[string]*sync.Mutex

func gitlabRoleOperationLock(owner, repo string) *sync.Mutex {
	key := owner + "/" + repo
	lock := &sync.Mutex{}
	actual, _ := gitlabRoleOperationLocks.LoadOrStore(key, lock)
	return actual.(*sync.Mutex)
}

// LockGitLabRoleOperation serializes role credential operations for one repo.
func LockGitLabRoleOperation(owner, repo string) func() {
	lock := gitlabRoleOperationLock(owner, repo)
	lock.Lock()
	return lock.Unlock
}

// GitLabRoleCutoverConfig controls verification-and-cutover. Ordinary
// unflagged `repos install` calls this after provisioning when roles are
// ready. Explicit `--gitlab-role-cutover` still requires DrainConfirmed.
type GitLabRoleCutoverConfig struct {
	Owner          string
	Repo           string
	Client         forge.Client
	TokenInventory ProjectAccessTokenClient
	DrainConfirmed bool
	Now            time.Time
	DryRun         bool
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
// secret is deleted before its project access tokens are revoked. If secret
// deletion fails after this call changed the gate, the gate is rolled back to
// migrating while the shared-token path remains recoverable. Once the secret
// is deleted, the gate stays enforced even if token revocation needs a retry.
//
// This function never reads or returns secret values. It does not infer that
// a role is ready from the presence of FULLSEND_FORGE_TOKEN.
func CutoverGitLabRoleCredentials(ctx context.Context, cfg GitLabRoleCutoverConfig) (GitLabRoleCutoverResult, error) {
	result := GitLabRoleCutoverResult{DryRun: cfg.DryRun}
	if cfg.Client == nil {
		return result, fmt.Errorf("GitLab role cutover requires a forge client")
	}
	defer LockGitLabRoleOperation(cfg.Owner, cfg.Repo)()
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
	rotation, _, err := loadRotationState(ctx, cfg.Client, cfg.Owner, cfg.Repo)
	if err != nil {
		return result, fmt.Errorf("loading GitLab role rotation state for cutover: %w", err)
	}
	result.Mode = mode
	result.Readiness = gitlabroles.CheckBuiltinReadiness(present, reg)
	result.Registered = gitlabroles.CheckRegisteredReadiness(present, reg)
	tokens, listErr := cfg.TokenInventory.ListProjectAccessTokens(ctx, cfg.Owner, cfg.Repo)
	if listErr != nil {
		return result, fmt.Errorf("listing GitLab project tokens for cutover: %w", listErr)
	}
	result.Lifecycle = cutoverLifecycle(mode, present, reg, tokens, rotation, cfg.Now)
	lifecycle := make(map[gitlabroles.Role]gitlabroles.LifecycleState, len(result.Lifecycle.Roles))
	for _, role := range result.Lifecycle.Roles {
		lifecycle[role.Name] = role.Lifecycle
	}
	result.Readiness = result.Readiness.WithLifecycle(lifecycle)
	result.Registered = result.Registered.WithLifecycle(lifecycle)
	result.Diagnostics = append(result.Diagnostics, result.Readiness.Diagnostics...)
	result.Diagnostics = append(result.Diagnostics, result.Registered.Diagnostics...)
	if leak := secretLeakCutover(result); leak != "" {
		return GitLabRoleCutoverResult{}, fmt.Errorf("internal error: cutover result leaked a secret value (%s)", leak)
	}
	if !result.Readiness.Ready || !result.Registered.Ready {
		return result, fmt.Errorf("%w: %s", ErrGitLabRoleCutoverNotReady, cutoverMissingRoles(result))
	}
	if mode != gitlabroles.ModeMigrating && mode != gitlabroles.ModeEnforced {
		return result, fmt.Errorf("%w, got %q", ErrGitLabRoleCutoverWrongMode, mode)
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
	latestRotation, _, rotationErr := loadRotationState(ctx, cfg.Client, cfg.Owner, cfg.Repo)
	if rotationErr != nil {
		return result, fmt.Errorf("revalidating GitLab role rotation state before cutover: %w", rotationErr)
	}
	if latestMode != mode || !reflect.DeepEqual(latestReg, reg) || !reflect.DeepEqual(latestPresent, present) || !reflect.DeepEqual(latestRotation, rotation) {
		return result, fmt.Errorf("%w; rerun cutover", ErrGitLabRoleCutoverStateChanged)
	}
	latestBuiltin := gitlabroles.CheckBuiltinReadiness(latestPresent, latestReg)
	latestRegistered := gitlabroles.CheckRegisteredReadiness(latestPresent, latestReg)
	tokens, listErr = cfg.TokenInventory.ListProjectAccessTokens(ctx, cfg.Owner, cfg.Repo)
	if listErr != nil {
		return result, fmt.Errorf("relisting GitLab project tokens before cutover: %w", listErr)
	}
	latestLifecycle := cutoverLifecycle(latestMode, latestPresent, latestReg, tokens, latestRotation, cfg.Now)
	lifecycle = make(map[gitlabroles.Role]gitlabroles.LifecycleState, len(latestLifecycle.Roles))
	for _, role := range latestLifecycle.Roles {
		lifecycle[role.Name] = role.Lifecycle
	}
	latestBuiltin = latestBuiltin.WithLifecycle(lifecycle)
	latestRegistered = latestRegistered.WithLifecycle(lifecycle)
	if !latestBuiltin.Ready || !latestRegistered.Ready {
		return result, fmt.Errorf("%w: state changed during revalidation; rerun verification", ErrGitLabRoleCutoverNotReady)
	}

	gateChanged := mode != gitlabroles.ModeEnforced
	if gateChanged {
		if err := cfg.Client.UpdateCIVariable(ctx, cfg.Owner, cfg.Repo, forge.VarGitLabRoleMigration, string(gitlabroles.ModeEnforced), true); err != nil {
			return result, fmt.Errorf("enabling enforced GitLab role migration: %w", err)
		}
	}
	result.Enforced = true

	if err := cfg.Client.DeleteRepoSecret(ctx, cfg.Owner, cfg.Repo, forge.SecretForgeToken); err != nil && !forge.IsNotFound(err) {
		if gateChanged {
			rollbackErr := cfg.Client.UpdateCIVariable(ctx, cfg.Owner, cfg.Repo, forge.VarGitLabRoleMigration, string(gitlabroles.ModeMigrating), true)
			if rollbackErr != nil {
				return result, fmt.Errorf("retiring shared GitLab credential: %v; rollback to migrating also failed: %w", err, rollbackErr)
			}
			result.RolledBack = true
			result.Enforced = false
		}
		return result, fmt.Errorf("retiring shared GitLab credential: %w", err)
	}
	result.SharedRetired = true
	freshTokens, err := cfg.TokenInventory.ListProjectAccessTokens(ctx, cfg.Owner, cfg.Repo)
	if err != nil {
		return result, fmt.Errorf("relisting GitLab project tokens after secret retirement: %w", err)
	}
	revoked, err := revokeSharedTokens(ctx, cfg, freshTokens)
	if err != nil {
		return result, err
	}
	if revoked == 0 {
		result.Diagnostics = append(result.Diagnostics, "shared CI credential retired; no fullsend-bot project access token was listed, so any personal or group PAT used for --gitlab-bot-token must be revoked manually")
	} else {
		result.Diagnostics = append(result.Diagnostics, "shared credential retired; enforced mode is active")
	}
	if leak := secretLeakCutover(result); leak != "" {
		return GitLabRoleCutoverResult{}, fmt.Errorf("internal error: cutover result leaked a secret value (%s)", leak)
	}
	return result, nil
}

func cutoverLifecycle(mode gitlabroles.Mode, present map[string]bool, reg gitlabroles.Registry, tokens []ProjectAccessToken, rotation rotationStateFile, now time.Time) gitlabroles.Report {
	report := gitlabroles.DiagnoseLifecycle(mode, present, reg, snapshotsFrom(tokens), now, gitlabroles.DefaultRotationLead)
	applyAdministratorEnrollmentProof(&report, reg, rotation)
	return report
}

func applyAdministratorEnrollmentProof(report *gitlabroles.Report, reg gitlabroles.Registry, rotation rotationStateFile) {
	if report == nil {
		return
	}
	oldLifecycleDiagnostics := 0
	for _, role := range report.Roles {
		if role.Lifecycle != "" && role.Lifecycle != gitlabroles.LifecycleUnconfigured && role.Lifecycle != gitlabroles.LifecycleOK {
			oldLifecycleDiagnostics++
		}
	}
	for i := range report.Roles {
		if report.Roles[i].Lifecycle != gitlabroles.LifecycleUnverified {
			continue
		}
		proofRole := report.Roles[i].Name
		seen := map[gitlabroles.Role]bool{}
		for !seen[proofRole] {
			seen[proofRole] = true
			registration, ok := reg.Lookup(proofRole)
			if !ok || registration.Credential.ReuseOf == "" {
				break
			}
			proofRole = registration.Credential.ReuseOf
		}
		state, ok := rotation.Roles[string(proofRole)]
		if ok && state.IncomingID == 0 && state.Phase == rotationPhaseIdle && state.DistributedAt != "" {
			report.Roles[i].Lifecycle = gitlabroles.LifecycleOK
		}
	}
	gitlabroles.RefreshLifecycleDiagnostics(report, oldLifecycleDiagnostics)
}

func secretLeakCutover(result GitLabRoleCutoverResult) string {
	for _, diagnostic := range result.Diagnostics {
		lower := strings.ToLower(diagnostic)
		for _, needle := range []string{"glpat-", "glptt-", "gldt-"} {
			if strings.Contains(lower, needle) {
				return needle
			}
		}
	}
	return ""
}

func revokeSharedTokens(ctx context.Context, cfg GitLabRoleCutoverConfig, tokens []ProjectAccessToken) (int, error) {
	revoked := 0
	for _, token := range tokens {
		if token.Name != gitlabroles.SharedTokenName || !token.Active || token.Revoked {
			continue
		}
		if err := cfg.TokenInventory.RevokeProjectAccessToken(ctx, cfg.Owner, cfg.Repo, token.ID); err != nil {
			return revoked, fmt.Errorf("revoking shared GitLab credential after secret retirement: %w", err)
		}
		revoked++
	}
	return revoked, nil
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
