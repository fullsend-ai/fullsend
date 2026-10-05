package repos

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
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

// LockGitLabRoleOperation serializes role credential operations for one repo.
func LockGitLabRoleOperation(owner, repo string) func() {
	lock := gitlabRoleOperationLock(owner, repo)
	lock.Lock()
	return lock.Unlock
}

// gitlabMaskablePattern matches GitLab's allowed charset for a maskable
// CI/CD variable value. A value outside this charset, shorter than 8
// characters, or spanning multiple lines cannot be masked; GitLab would
// otherwise silently fall back to storing it unmasked.
var gitlabMaskablePattern = regexp.MustCompile(`^[a-zA-Z0-9@:.+/=_~-]+$`)

// canMaskGitLabValue reports whether value meets GitLab's masking
// constraints for a CI/CD variable (protected + masked secret).
func canMaskGitLabValue(value string) bool {
	return len(value) >= 8 && gitlabMaskablePattern.MatchString(value)
}

// ProjectAccessToken is the subset of a GitLab project access token
// needed to store a role credential. Token is the secret value and is
// present only at creation time; it must never appear in logs, status,
// or Error strings.
type ProjectAccessToken struct {
	ID        int
	Name      string
	Token     string
	Active    bool
	ExpiresAt string
	Revoked   bool
	UserID    int
}

// ProjectAccessTokenClient creates, lists, and revokes GitLab project
// access tokens. Implementations must not log token values. List results
// typically omit Token (GitLab returns the secret only at creation).
type ProjectAccessTokenClient interface {
	CreateProjectAccessToken(ctx context.Context, owner, repo, name string, scopes []string, accessLevel int, expiresAt string) (*ProjectAccessToken, error)
	ListProjectAccessTokens(ctx context.Context, owner, repo string) ([]ProjectAccessToken, error)
	RevokeProjectAccessToken(ctx context.Context, owner, repo string, tokenID int) error
}

// RoleProvisionConfig is the input to ProvisionGitLabRoleCredentials.
type RoleProvisionConfig struct {
	Owner    string
	Repo     string
	Client   forge.Client
	Tokens   ProjectAccessTokenClient
	Registry gitlabroles.Registry
	// RegistryProvided is true when the operator explicitly supplied
	// --gitlab-role-registry this run, as opposed to Registry being
	// populated from a previously stored registry variable. Used only
	// to decide whether to surface a diagnostic when the supplied
	// registry is not persisted.
	RegistryProvided bool
	// ProvidedTokens maps a role name to an administrator-supplied
	// PAT (free-tier enrollment or a custom own credential). Values
	// must never be logged.
	ProvidedTokens map[gitlabroles.Role]string
	Now            time.Time
	DryRun         bool
}

// RoleProvisionFailure is a per-role error. Reason and Secret are
// names and messages only — never token values.
type RoleProvisionFailure struct {
	Role   gitlabroles.Role
	Secret string
	Reason string
}

// RoleProvisionResult is the observable outcome of a provision run.
// Token values are not included.
type RoleProvisionResult struct {
	Report          gitlabroles.Report
	Created         []gitlabroles.Role
	Enrolled        []gitlabroles.Role
	Skipped         []gitlabroles.Role
	Reused          []gitlabroles.Role
	Failed          []RoleProvisionFailure
	RegistryWritten bool
	DryRun          bool
	Diagnostics     []string
}

// GitLabPATExpiresAt returns the YYYY-MM-DD expiry GitLab expects for a
// project access token. GitLab evaluates expires_at in UTC.
func GitLabPATExpiresAt(now time.Time) string {
	return now.UTC().AddDate(1, 0, 0).Format("2006-01-02")
}

// IsGitLabRoleManagedVar reports whether a FULLSEND_* CI/CD variable is
// a GitLab role-credential artifact (registry, built-in or custom role
// secret), or the legacy shared token. This is an orphan-detection
// classification only: it tells drift/orphan scans that the name is
// recognized role-identity state, not an unmanaged leftover. It does not
// mean the variable is removed on uninstall — the legacy FULLSEND_FORGE_TOKEN
// shared token is deliberately excluded from automatic uninstall cleanup
// (see gitLabRoleUninstallVars and extraGitLabRoleUninstallVars) and
// requires manual cleanup.
func IsGitLabRoleManagedVar(name string) bool {
	switch name {
	case forge.SecretForgeToken, forge.VarGitLabRoleRegistry,
		forge.VarGitLabRoleRotation,
		forge.SecretGitLabPollerToken, forge.SecretGitLabAnalystToken,
		forge.SecretGitLabCoderToken:
		return true
	}
	return strings.HasPrefix(name, "FULLSEND_GITLAB_ROLE_") && strings.HasSuffix(name, "_TOKEN")
}

// appendBuiltinRoleReadiness adds Poller/Analyst/Coder verification
// lines from CheckBuiltinReadiness. Names only; never token values.
func appendBuiltinRoleReadiness(status *RepoStatus, present map[string]bool, reg gitlabroles.Registry, lifecycle map[gitlabroles.Role]gitlabroles.LifecycleState) gitlabroles.BuiltinReadiness {
	if status == nil {
		return gitlabroles.BuiltinReadiness{}
	}
	check := gitlabroles.CheckBuiltinReadiness(present, reg).WithLifecycle(lifecycle)
	status.GitLabRoleDiagnostics = append(status.GitLabRoleDiagnostics, check.Diagnostics...)
	return check
}

// appendRegisteredRoleReadiness adds readiness diagnostics for every role in
// the trusted registry, including custom roles. This keeps repos status honest
// about the same mapping checks job routing enforces.
func appendRegisteredRoleReadiness(status *RepoStatus, present map[string]bool, reg gitlabroles.Registry, lifecycle map[gitlabroles.Role]gitlabroles.LifecycleState) gitlabroles.RegisteredReadiness {
	if status == nil {
		return gitlabroles.RegisteredReadiness{}
	}
	check := gitlabroles.CheckRegisteredReadiness(present, reg).WithLifecycle(lifecycle)
	status.GitLabRoleDiagnostics = append(status.GitLabRoleDiagnostics, check.Diagnostics...)
	return check
}

// gitLabRoleUninstallVars is the static role-credential variable set
// deleted on uninstall. Custom FULLSEND_GITLAB_ROLE_*_TOKEN names are
// discovered at uninstall time from ListRepoVariables. This does not
// include the legacy FULLSEND_FORGE_TOKEN shared secret: uninstall no
// longer removes it automatically, so a repository installed before
// the role-only rollout may require manual cleanup of that secret and
// its matching fullsend-bot project access token.
var gitLabRoleUninstallVars = []string{
	forge.VarGitLabRoleRegistry,
	forge.VarGitLabRoleRotation,
	forge.SecretGitLabPollerToken,
	forge.SecretGitLabAnalystToken,
	forge.SecretGitLabCoderToken,
}

// ProvisionGitLabRoleCredentials creates or enrolls credentials for
// every registered role (built-in and custom), stores them as
// protected masked CI/CD variables, writes the registry, and reports
// which roles are ready.
//
// It never revokes or overwrites FULLSEND_FORGE_TOKEN. Existing role
// secrets are left in place (reinstall / retry). A failed role does
// not roll back roles that already succeeded. A repository installed
// before the role-only rollout may still carry FULLSEND_FORGE_TOKEN;
// provisioning does not retire it, and no automated path does — that
// leftover secret and its matching fullsend-bot project access token
// require manual cleanup.
func ProvisionGitLabRoleCredentials(ctx context.Context, cfg RoleProvisionConfig) (RoleProvisionResult, error) {
	result := RoleProvisionResult{DryRun: cfg.DryRun}
	if cfg.Client == nil {
		return result, fmt.Errorf("GitLab role provisioning requires a forge client")
	}
	operationLock := gitlabRoleOperationLock(cfg.Owner, cfg.Repo)
	operationLock.Lock()
	defer operationLock.Unlock()
	reg := cfg.Registry
	if len(reg.Registrations()) == 0 {
		reg = gitlabroles.BuiltinRegistry()
	}

	present, presErr := gitLabRolePresence(ctx, cfg.Client, cfg.Owner, cfg.Repo, reg)
	if presErr != nil {
		return result, fmt.Errorf("reading GitLab role credential presence: %w", presErr)
	}
	if err := writeGitLabRoleRegistry(ctx, cfg, &result); err != nil {
		return result, err
	}

	provisionOwnRoles(ctx, cfg, reg, present, &result)

	present, presErr = gitLabRolePresence(ctx, cfg.Client, cfg.Owner, cfg.Repo, reg)
	if presErr != nil {
		return result, fmt.Errorf("reading GitLab role credential presence: %w", presErr)
	}
	result.Report = gitlabroles.Diagnose(present, reg)
	result.Diagnostics = result.Report.Diagnostics
	if secretLeak(result) != "" {
		return RoleProvisionResult{}, fmt.Errorf("internal error: provision result leaked a secret value")
	}
	return result, nil
}

// validateProvidedTokenRoles records a failure for every
// --gitlab-role-token key that does not match a registered role name, so
// a misspelled or unregistered role name is never silently ignored.
func validateProvidedTokenRoles(cfg RoleProvisionConfig, reg gitlabroles.Registry, result *RoleProvisionResult) {
	if len(cfg.ProvidedTokens) == 0 {
		return
	}
	roles := make([]gitlabroles.Role, 0, len(cfg.ProvidedTokens))
	for role := range cfg.ProvidedTokens {
		roles = append(roles, role)
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i] < roles[j] })
	for _, role := range roles {
		if _, ok := reg.Lookup(role); !ok {
			result.Failed = append(result.Failed, RoleProvisionFailure{
				Role:   role,
				Reason: "administrator-provided token does not match a registered role",
			})
		}
	}
}

func provisionOwnRoles(ctx context.Context, cfg RoleProvisionConfig, reg gitlabroles.Registry, present map[string]bool, result *RoleProvisionResult) {
	validateProvidedTokenRoles(cfg, reg, result)

	now := cfg.Now
	if now.IsZero() {
		now = time.Now()
	}
	expiresAt := GitLabPATExpiresAt(now)

	for _, rec := range reg.Registrations() {
		if rec.Credential.Kind == gitlabroles.CredentialReuse {
			result.Reused = append(result.Reused, rec.Name)
			continue
		}
		secret := rec.Credential.SecretName
		if present[secret] {
			result.Skipped = append(result.Skipped, rec.Name)
			if !cfg.DryRun {
				backfillInitialDistributionProof(ctx, cfg, rec, now, result)
			}
			continue
		}
		if provided := strings.TrimSpace(cfg.ProvidedTokens[rec.Name]); provided != "" {
			if !canMaskGitLabValue(provided) {
				result.Failed = append(result.Failed, RoleProvisionFailure{
					Role:   rec.Name,
					Secret: secret,
					Reason: "administrator-provided credential cannot be masked (must be a single line of at least 8 characters using GitLab's allowed charset)",
				})
				continue
			}
			if cfg.DryRun {
				result.Enrolled = append(result.Enrolled, rec.Name)
				continue
			}
			if err := cfg.Client.CreateRepoSecret(ctx, cfg.Owner, cfg.Repo, secret, provided); err != nil {
				result.Failed = append(result.Failed, RoleProvisionFailure{
					Role:   rec.Name,
					Secret: secret,
					Reason: "storing administrator-provided credential failed",
				})
				continue
			}
			present[secret] = true
			result.Enrolled = append(result.Enrolled, rec.Name)
			// Record rotation-state proof of this administrator-provided
			// enrollment, mirroring the freshly-minted-PAT path below, so
			// a later RotateGitLabRoleCredentials run does not treat this
			// healthy provided credential as an unproven orphan and
			// immediately re-mint a replacement for it. There is no
			// GitLab token ID to record here (only the secret value was
			// supplied); tokenID=0 with phase=idle and DistributedAt set
			// is the same not-due proof rotateProvided records for a
			// later administrator-provided replacement.
			if err := recordInitialDistribution(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, 0, "", now); err != nil {
				result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
					"%s: recording rotation-state distribution proof failed; a future rotation run will treat this credential as unproven and replace it", rec.Name))
			}
			continue
		}
		if cfg.DryRun {
			result.Created = append(result.Created, rec.Name)
			continue
		}
		if cfg.Tokens == nil {
			result.Failed = append(result.Failed, RoleProvisionFailure{
				Role:   rec.Name,
				Secret: secret,
				Reason: "no GitLab token client and no administrator-provided credential",
			})
			continue
		}
		tokenName := rec.Credential.TokenName
		if tokenName == "" {
			tokenName = gitlabroles.CustomTokenName(rec.Name)
		}
		tok, err := cfg.Tokens.CreateProjectAccessToken(ctx, cfg.Owner, cfg.Repo, tokenName,
			gitlabroles.TokenScopes(), gitlabroles.DeveloperAccessLevel, expiresAt)
		if err != nil {
			result.Failed = append(result.Failed, RoleProvisionFailure{
				Role:   rec.Name,
				Secret: secret,
				Reason: "project access token creation failed",
			})
			continue
		}
		if tok == nil || strings.TrimSpace(tok.Token) == "" {
			if tok != nil && tok.ID != 0 {
				_ = cfg.Tokens.RevokeProjectAccessToken(ctx, cfg.Owner, cfg.Repo, tok.ID)
			}
			result.Failed = append(result.Failed, RoleProvisionFailure{
				Role:   rec.Name,
				Secret: secret,
				Reason: "project access token creation returned no value",
			})
			continue
		}
		if err := cfg.Client.CreateRepoSecret(ctx, cfg.Owner, cfg.Repo, secret, tok.Token); err != nil {
			if tok.ID != 0 {
				_ = cfg.Tokens.RevokeProjectAccessToken(ctx, cfg.Owner, cfg.Repo, tok.ID)
			}
			result.Failed = append(result.Failed, RoleProvisionFailure{
				Role:   rec.Name,
				Secret: secret,
				Reason: "storing role credential failed",
			})
			continue
		}
		present[secret] = true
		result.Created = append(result.Created, rec.Name)
		// Record rotation-state proof of this initial distribution so a
		// later RotateGitLabRoleCredentials run does not treat this
		// healthy, just-provisioned PAT as an unproven orphan and
		// immediately mint a replacement for it.
		if err := recordInitialDistribution(ctx, cfg.Client, cfg.Owner, cfg.Repo, rec.Name, tok.ID, expiresAt, now); err != nil {
			result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
				"%s: recording rotation-state distribution proof failed; a future rotation run will treat this credential as unproven and replace it", rec.Name))
		}
	}
}

func writeGitLabRoleRegistry(ctx context.Context, cfg RoleProvisionConfig, result *RoleProvisionResult) error {
	if cfg.DryRun {
		result.RegistryWritten = true
		return nil
	}
	raw, err := gitlabroles.MarshalCustomRoles(cfg.Registry)
	if err != nil {
		return fmt.Errorf("encoding GitLab role registry: %w", err)
	}
	if err := cfg.Client.UpdateCIVariable(ctx, cfg.Owner, cfg.Repo, forge.VarGitLabRoleRegistry, raw, true); err != nil {
		return fmt.Errorf("writing %s: %w", forge.VarGitLabRoleRegistry, err)
	}
	result.RegistryWritten = true
	return nil
}

// LoadGitLabRoleState reads the registered role policy and per-secret
// presence from a repository. Migration state is deliberately not read.
func LoadGitLabRoleState(ctx context.Context, client forge.Client, owner, repo string) (gitlabroles.Registry, map[string]bool, error) {
	regRaw, _, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleRegistry)
	if err != nil {
		return gitlabroles.Registry{}, nil, fmt.Errorf("reading %s: %w", forge.VarGitLabRoleRegistry, err)
	}
	reg, err := gitlabroles.ParseRegistry(regRaw)
	if err != nil {
		return gitlabroles.Registry{}, nil, err
	}
	present, err := gitLabRolePresence(ctx, client, owner, repo, reg)
	if err != nil {
		return gitlabroles.Registry{}, nil, err
	}
	return reg, present, nil
}

func gitLabRolePresence(ctx context.Context, client forge.Client, owner, repo string, reg gitlabroles.Registry) (map[string]bool, error) {
	names := make(map[string]struct{})
	for _, rec := range reg.Registrations() {
		if rec.Credential.SecretName != "" {
			names[rec.Credential.SecretName] = struct{}{}
		}
	}
	present := make(map[string]bool, len(names))
	for name := range names {
		exists, err := client.RepoSecretExists(ctx, owner, repo, name)
		if err != nil {
			return nil, fmt.Errorf("checking secret %s: %w", name, err)
		}
		present[name] = exists
	}
	return present, nil
}

func extraGitLabRoleUninstallVars(ctx context.Context, client forge.Client, owner, repo string, already []string) []string {
	seen := make(map[string]struct{}, len(already)+8)
	for _, n := range already {
		seen[n] = struct{}{}
	}
	var extra []string
	add := func(name string) {
		if name == "" {
			return
		}
		// FULLSEND_FORGE_TOKEN is the legacy shared credential, not
		// role-identity state: uninstall no longer retires it
		// automatically, so it must not be auto-discovered here even
		// though IsGitLabRoleManagedVar still recognizes the name for
		// orphan-detection purposes elsewhere.
		if name == forge.SecretForgeToken {
			return
		}
		if !IsGitLabRoleManagedVar(name) {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		extra = append(extra, name)
	}
	// ListRepoVariables is best-effort: a list error must not hide names we
	// can still recover from the stored registry. Built-in secrets stay on
	// the static uninstall list either way.
	if vars, err := client.ListRepoVariables(ctx, owner, repo); err == nil {
		for name := range vars {
			add(name)
		}
	}
	if raw, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleRegistry); err == nil && exists {
		if reg, perr := gitlabroles.ParseRegistry(raw); perr == nil {
			for _, rec := range reg.Registrations() {
				add(rec.Credential.SecretName)
			}
		}
	}
	sort.Strings(extra)
	return extra
}

// gitlabRoleIdentityVarNames returns the GitLab role-identity variables
// and secrets that uninstall removes: the registry, rotation document,
// and every built-in or custom role secret. It deliberately excludes
// the legacy FULLSEND_FORGE_TOKEN shared credential — a repository
// installed before the role-only rollout may require manual cleanup of
// that secret and its matching fullsend-bot project access token.
func gitlabRoleIdentityVarNames(ctx context.Context, client forge.Client, owner, repo string) []string {
	already := make([]string, 0, len(gitLabRoleUninstallVars))
	already = append(already, gitLabRoleUninstallVars...)
	return append(already, extraGitLabRoleUninstallVars(ctx, client, owner, repo, already)...)
}

func isGitLabIdentityUninstallVar(name string) bool {
	return IsGitLabRoleManagedVar(name)
}

func isGitLabRoleSecretName(name string) bool {
	switch name {
	case forge.SecretGitLabPollerToken, forge.SecretGitLabAnalystToken, forge.SecretGitLabCoderToken:
		return true
	}
	return strings.HasPrefix(name, "FULLSEND_GITLAB_ROLE_") && strings.HasSuffix(name, "_TOKEN")
}

func secretLeak(result RoleProvisionResult) string {
	needles := []string{"glpat-", "glptt-", "gldt-"}
	check := func(s string) string {
		lower := strings.ToLower(s)
		for _, n := range needles {
			if strings.Contains(lower, n) {
				return n
			}
		}
		return ""
	}
	for _, d := range result.Diagnostics {
		if n := check(d); n != "" {
			return n
		}
	}
	for _, f := range result.Failed {
		if n := check(f.Reason); n != "" {
			return n
		}
		if n := check(string(f.Role)); n != "" {
			return n
		}
		if n := check(f.Secret); n != "" {
			return n
		}
	}
	for _, d := range result.Report.Diagnostics {
		if n := check(d); n != "" {
			return n
		}
	}
	return ""
}
