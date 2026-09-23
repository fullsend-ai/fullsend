package repos

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// GitLabRoleCleanupConfig controls removal of migration-era GitLab
// identity state: the gate, registry, rotation document, built-in and
// custom role secrets, leftover shared token, and matching project
// access tokens.
type GitLabRoleCleanupConfig struct {
	Owner  string
	Repo   string
	Client forge.Client
	// Tokens, when set, revokes active fullsend-bot and role project
	// access tokens. Nil skips PAT revocation; CI/CD variables and
	// secrets are still deleted.
	Tokens ProjectAccessTokenClient
	DryRun bool
}

// GitLabRoleCleanupResult is the non-secret outcome of identity cleanup.
type GitLabRoleCleanupResult struct {
	VarsDeleted   int
	TokensRevoked int
	DryRun        bool
	Diagnostics   []string
}

// CleanupGitLabRoleIdentity removes GitLab role-identity installation
// state without recreating any of it. Missing variables, secrets, or
// tokens are not errors, so retries and uninstall of partial installs
// stay idempotent. A token-inventory failure after variables are
// deleted is still returned so uninstall can fail closed and leave the
// manifest entry for retry.
//
// This function never reads or returns secret values.
func CleanupGitLabRoleIdentity(ctx context.Context, cfg GitLabRoleCleanupConfig) (GitLabRoleCleanupResult, error) {
	result := GitLabRoleCleanupResult{DryRun: cfg.DryRun}
	if cfg.Client == nil {
		return result, fmt.Errorf("GitLab role identity cleanup requires a forge client")
	}
	defer LockGitLabRoleOperation(cfg.Owner, cfg.Repo)()

	names := gitlabRoleIdentityVarNames(ctx, cfg.Client, cfg.Owner, cfg.Repo)
	if cfg.DryRun {
		result.VarsDeleted = len(names)
		result.Diagnostics = append(result.Diagnostics, "dry-run: would delete GitLab role identity variables, secrets, and project access tokens")
		return result, nil
	}

	var errs []error
	for _, name := range names {
		if err := cfg.Client.DeleteRepoVariable(ctx, cfg.Owner, cfg.Repo, name); err != nil {
			errs = append(errs, fmt.Errorf("deleting variable %s: %w", name, err))
		} else {
			result.VarsDeleted++
		}
		if !isGitLabRoleSecretName(name) {
			continue
		}
		if err := cfg.Client.DeleteRepoSecret(ctx, cfg.Owner, cfg.Repo, name); err != nil && !forge.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("deleting secret %s: %w", name, err))
		}
	}

	revoked, diag, err := revokeGitLabIdentityTokens(ctx, cfg.Tokens, cfg.Owner, cfg.Repo)
	result.TokensRevoked = revoked
	if diag != "" {
		result.Diagnostics = append(result.Diagnostics, diag)
	}
	if err != nil {
		errs = append(errs, err)
	}
	return result, errors.Join(errs...)
}

// revokeGitLabIdentityTokens lists and revokes active fullsend identity
// project access tokens. Only a confirmed-gone project (404) is reported
// as a diagnostic and treated as "nothing to revoke" rather than a hard
// failure, since deletion can never converge there. Every other listing
// failure — including 403/Forbidden, which GitLab returns identically for
// plan-tier feature gating, group-level PAT disablement, and insufficient
// token permissions — fails the call closed so the manifest entry is
// retried rather than silently reporting uninstall success while tokens
// stay live. Operators stuck on a genuinely unsupported plan use the
// documented manual `--manifest-only` recovery path after confirming
// manual token revocation.
func revokeGitLabIdentityTokens(ctx context.Context, tokens ProjectAccessTokenClient, owner, repo string) (int, string, error) {
	if tokens == nil {
		return 0, "", nil
	}
	listed, err := tokens.ListProjectAccessTokens(ctx, owner, repo)
	if err != nil {
		if forge.IsNotFound(err) {
			return 0, fmt.Sprintf("Warning: GitLab project access token inventory unavailable (%v); treating as nothing to revoke — verify manually if the project no longer exists", err), nil
		}
		return 0, "", fmt.Errorf("listing GitLab project tokens for uninstall: %w", err)
	}
	// Stable order so joined errors and TokensRevoked are deterministic.
	sort.Slice(listed, func(i, j int) bool {
		if listed[i].Name == listed[j].Name {
			return listed[i].ID < listed[j].ID
		}
		return listed[i].Name < listed[j].Name
	})
	var errs []error
	revoked := 0
	for _, tok := range listed {
		if !tok.Active || tok.Revoked {
			continue
		}
		if tok.Name != gitlabroles.SharedTokenName && !gitlabroles.IsRoleProjectTokenName(tok.Name) {
			continue
		}
		if err := tokens.RevokeProjectAccessToken(ctx, owner, repo, tok.ID); err != nil {
			errs = append(errs, fmt.Errorf("revoking GitLab identity token %s (id %d): %w", tok.Name, tok.ID, err))
			continue
		}
		revoked++
	}
	return revoked, "", errors.Join(errs...)
}
