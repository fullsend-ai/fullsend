package repos

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

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
	// to decide whether to surface a diagnostic when the gate is
	// rollback/disabled and the supplied registry is not persisted.
	RegistryProvided bool
	// DesiredMode is written to FULLSEND_GITLAB_ROLE_MIGRATION.
	// Empty means ModeMigrating. ModeDisabled and ModeRollback write
	// the gate without creating or revoking tokens.
	DesiredMode gitlabroles.Mode
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
	Mode            gitlabroles.Mode
	Report          gitlabroles.Report
	Created         []gitlabroles.Role
	Enrolled        []gitlabroles.Role
	Skipped         []gitlabroles.Role
	Reused          []gitlabroles.Role
	Failed          []RoleProvisionFailure
	SharedPreserved bool
	GateWritten     bool
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
// a GitLab role-credential artifact (gate, registry, built-in or custom
// role secret). These are managed, not orphans, and are removed on
// uninstall.
func IsGitLabRoleManagedVar(name string) bool {
	switch name {
	case forge.VarGitLabRoleMigration, forge.VarGitLabRoleRegistry,
		forge.VarGitLabRoleRotation,
		forge.SecretGitLabPollerToken, forge.SecretGitLabAnalystToken,
		forge.SecretGitLabCoderToken:
		return true
	}
	return strings.HasPrefix(name, "FULLSEND_GITLAB_ROLE_") && strings.HasSuffix(name, "_TOKEN")
}

// gitLabRoleUninstallVars is the static role-credential variable set
// deleted on uninstall. Custom FULLSEND_GITLAB_ROLE_*_TOKEN names are
// discovered at uninstall time from ListRepoVariables.
var gitLabRoleUninstallVars = []string{
	forge.VarGitLabRoleMigration,
	forge.VarGitLabRoleRegistry,
	forge.VarGitLabRoleRotation,
	forge.SecretGitLabPollerToken,
	forge.SecretGitLabAnalystToken,
	forge.SecretGitLabCoderToken,
}

// ProvisionGitLabRoleCredentials creates or enrolls credentials for
// every registered role (built-in and custom), stores them as
// protected masked CI/CD variables, writes the registry and migration
// gate, and reports which roles are ready.
//
// It never revokes or overwrites FULLSEND_FORGE_TOKEN. Existing role
// secrets are left in place (reinstall / retry). A failed role does
// not roll back roles that already succeeded. SharedPreserved is
// always true on a successful return.
func ProvisionGitLabRoleCredentials(ctx context.Context, cfg RoleProvisionConfig) (RoleProvisionResult, error) {
	result := RoleProvisionResult{SharedPreserved: true, DryRun: cfg.DryRun}
	if cfg.Client == nil {
		return result, fmt.Errorf("GitLab role provisioning requires a forge client")
	}
	mode := cfg.DesiredMode
	if mode == "" {
		mode = gitlabroles.ModeMigrating
	}
	if !mode.Valid() {
		return result, fmt.Errorf("%w: %q", gitlabroles.ErrInvalidMode, mode)
	}
	result.Mode = mode
	reg := cfg.Registry
	if len(reg.Registrations()) == 0 {
		reg = gitlabroles.BuiltinRegistry()
	}

	present, presErr := gitLabRolePresence(ctx, cfg.Client, cfg.Owner, cfg.Repo, reg)
	if presErr != nil {
		return result, fmt.Errorf("reading GitLab role credential presence: %w", presErr)
	}

	// Write the gate (and registry) before creating any role tokens. If the
	// gate write fails, no tokens are created and nothing is orphaned. If
	// token creation subsequently fails partway through, the gate already
	// reflects the desired mode, so a follow-up unflagged `repos install`
	// sees the live gate as migrating/enforced and retries the missing
	// roles instead of treating the repo as still on the legacy path.
	skipTokens := mode.UsesSharedOnly()
	if err := writeGitLabRoleGate(ctx, cfg, mode, skipTokens, &result); err != nil {
		return result, err
	}

	if !skipTokens {
		provisionOwnRoles(ctx, cfg, reg, present, &result)
	}

	present, presErr = gitLabRolePresence(ctx, cfg.Client, cfg.Owner, cfg.Repo, reg)
	if presErr != nil {
		return result, fmt.Errorf("reading GitLab role credential presence: %w", presErr)
	}
	result.Report = gitlabroles.Diagnose(mode, present, reg)
	result.Diagnostics = result.Report.Diagnostics
	if skipTokens && (cfg.RegistryProvided || len(cfg.ProvidedTokens) > 0) {
		result.Diagnostics = append(result.Diagnostics, fmt.Sprintf(
			"--gitlab-role-registry/--gitlab-role-token input was ignored: GitLab role migration gate is %q, so role credentials are not minted or persisted", mode))
	}
	if secretLeak(result) != "" {
		return RoleProvisionResult{SharedPreserved: true}, fmt.Errorf("internal error: provision result leaked a secret value")
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

func writeGitLabRoleGate(ctx context.Context, cfg RoleProvisionConfig, mode gitlabroles.Mode, skipTokens bool, result *RoleProvisionResult) error {
	if cfg.DryRun {
		result.GateWritten = true
		if !skipTokens {
			result.RegistryWritten = true
		}
		return nil
	}
	if err := cfg.Client.UpdateCIVariable(ctx, cfg.Owner, cfg.Repo, forge.VarGitLabRoleMigration, string(mode), true); err != nil {
		return fmt.Errorf("writing %s: %w", forge.VarGitLabRoleMigration, err)
	}
	result.GateWritten = true
	if skipTokens {
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

// LoadGitLabRoleState reads the migration gate, registry, and per-secret
// presence from a repository. Secret values are never retained.
func LoadGitLabRoleState(ctx context.Context, client forge.Client, owner, repo string) (gitlabroles.Mode, gitlabroles.Registry, map[string]bool, error) {
	modeRaw, _, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleMigration)
	if err != nil {
		return "", gitlabroles.Registry{}, nil, fmt.Errorf("reading %s: %w", forge.VarGitLabRoleMigration, err)
	}
	mode, err := gitlabroles.ParseMode(modeRaw)
	if err != nil {
		return "", gitlabroles.Registry{}, nil, err
	}
	regRaw, _, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleRegistry)
	if err != nil {
		return mode, gitlabroles.Registry{}, nil, fmt.Errorf("reading %s: %w", forge.VarGitLabRoleRegistry, err)
	}
	reg, err := gitlabroles.ParseRegistry(regRaw)
	if err != nil {
		return mode, gitlabroles.Registry{}, nil, err
	}
	present, err := gitLabRolePresence(ctx, client, owner, repo, reg)
	if err != nil {
		return mode, reg, nil, err
	}
	return mode, reg, present, nil
}

func gitLabRolePresence(ctx context.Context, client forge.Client, owner, repo string, reg gitlabroles.Registry) (map[string]bool, error) {
	names := make(map[string]struct{})
	names[forge.SecretForgeToken] = struct{}{}
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
	vars, err := client.ListRepoVariables(ctx, owner, repo)
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{}, len(already))
	for _, n := range already {
		seen[n] = struct{}{}
	}
	var extra []string
	for name := range vars {
		if _, ok := seen[name]; ok {
			continue
		}
		if IsGitLabRoleManagedVar(name) {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return extra
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
	if n := check(string(result.Mode)); n != "" {
		return n
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
