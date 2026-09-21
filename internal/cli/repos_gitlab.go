package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/poll"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	gitlabBotTokenName         = "fullsend-bot"
	gitlabAccessLevelDeveloper = 30
)

// gitlabBotPATExpiresAt returns the YYYY-MM-DD expiry GitLab expects for a
// project access token. GitLab evaluates expires_at in UTC, so a local-time
// date can already be in the past when the token is created and the PAT is
// born active:false. Compute against UTC.
func gitlabBotPATExpiresAt(now time.Time) string {
	return now.UTC().AddDate(1, 0, 0).Format("2006-01-02")
}

// setupGitLabBotToken creates a project access token for the fullsend bot
// identity and stores it as a protected CI/CD variable (FULLSEND_FORGE_TOKEN).
//
// If project access tokens are not available (free tier), it falls back
// to the provided fallbackToken (from --gitlab-bot-token). Returns the
// token value.
func setupGitLabBotToken(ctx context.Context, client forge.Client, glClient *gitlab.LiveClient, printer *ui.Printer, owner, repo, fallbackToken string) (string, error) {
	printer.StepStart("Creating project access token")
	var botPAT string
	if glClient != nil {
		// Revoke any existing fullsend-bot tokens to avoid duplicates on re-install.
		existing, listErr := glClient.ListProjectAccessTokens(ctx, owner, repo)
		if listErr != nil {
			printer.StepWarn(fmt.Sprintf("Could not list existing tokens — duplicate cleanup skipped: %v", listErr))
		} else {
			for _, t := range existing {
				if t.Name == gitlabBotTokenName && t.Active {
					if err := glClient.RevokeProjectAccessToken(ctx, owner, repo, t.ID); err != nil {
						printer.StepWarn(fmt.Sprintf("Failed to revoke existing token %q (ID %d): %v", t.Name, t.ID, err))
					} else {
						printer.StepInfo(fmt.Sprintf("Revoked existing token %q (ID %d)", t.Name, t.ID))
					}
				}
			}
		}

		expiresAt := gitlabBotPATExpiresAt(time.Now())
		// "api" scope is required for REST and GraphQL (MR operations, poll-state
		// branch writes). Developer (30) is sufficient now that poller state lives
		// on unprotected branches rather than Maintainer-only CI/CD variables.
		//
		// Residual dependency: the poller also creates pipelines via
		// CreatePipeline on the protected default branch (ADR 0067), which
		// requires merge or push access. Developer (30) satisfies that under
		// GitLab's default "Protected" preset, but a repo whose branch
		// protection restricts merge and push to Maintainers will get a 403
		// on pipeline creation. This is not verified or granted here; see
		// docs/cli/repos.md "GitLab bot token".
		token, err := glClient.CreateProjectAccessToken(ctx, owner, repo, gitlabBotTokenName,
			[]string{"api"}, gitlabAccessLevelDeveloper, expiresAt)
		if err != nil {
			printer.StepWarn(fmt.Sprintf("Project access token creation failed: %v", err))
			if fallbackToken != "" {
				printer.StepInfo("Using token from --gitlab-bot-token flag")
				botPAT = fallbackToken
			} else {
				return "", fmt.Errorf("project access token creation failed (%v); on free-tier instances, pass --gitlab-bot-token with a PAT that has 'api' scope", err)
			}
		} else {
			botPAT = token.Token
			printer.StepDone(fmt.Sprintf("Created project access token %q (ID: %d)", gitlabBotTokenName, token.ID))
		}
	} else if fallbackToken != "" {
		botPAT = fallbackToken
	} else {
		return "", fmt.Errorf("no GitLab client available and no --gitlab-bot-token provided")
	}

	if botPAT != "" {
		// Always store bot PAT as a protected CI/CD variable.
		printer.StepStart("Storing bot credentials")
		if err := client.CreateRepoSecret(ctx, owner, repo, forge.SecretForgeToken, botPAT); err != nil {
			printer.StepFail("Failed to store bot credentials")
			return "", fmt.Errorf("storing bot PAT: %w", err)
		}
		printer.StepDone("Bot credentials stored as protected CI/CD variable")
	}

	return botPAT, nil
}

// provisionGitLabPollState ensures FULLSEND_DISPATCH_SECRET exists so
// poll-state HMAC signing is on by default, then creates both poll-state
// branches with an initial signed document. Legacy CI/CD variables are
// folded in when present (*Fast → slash, *Full + LabelState → events);
// otherwise each branch is seeded with an empty signed baseline.
//
// Only called for already-enrolled (converged / already-current) repos —
// fresh installs are handled by repos.Install() itself. Safe to run on
// live repos: it never creates or revokes the bot PAT, so it does not
// disturb live pipelines. Returns an error if either step fails so the
// caller can count the repo as failed, matching how fresh installs treat
// the identical failure as fatal; failures are still logged via StepWarn
// for operator visibility.
func provisionGitLabPollState(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo string) error {
	repoFullName := owner + "/" + repo
	dispatchSecret, created, secretErr := poll.EnsureDispatchSecret(ctx, client, owner, repo)
	if secretErr != nil {
		printer.StepWarn(fmt.Sprintf("[%s] Could not provision dispatch secret: %v", repoFullName, secretErr))
		return secretErr
	}
	if created {
		printer.StepDone(fmt.Sprintf("[%s] Provisioned FULLSEND_DISPATCH_SECRET (protected, masked)", repoFullName))
	}
	seeded, seedErr := poll.SeedGitLabPollStateBranches(ctx, client, owner, repo, dispatchSecret)
	if seedErr != nil {
		printer.StepWarn(fmt.Sprintf("[%s] Could not seed poll-state branches: %v", repoFullName, seedErr))
		return seedErr
	}
	if seeded {
		printer.StepDone(fmt.Sprintf("[%s] Seeded poll-state branches from legacy vars (or empty baseline)", repoFullName))
	}
	return nil
}

// setupGitLabPipelineSchedules creates two independent pipeline schedules
// for polling: a fast slash-command poll (every 5 min) and an offset
// event-discovery poll (at minutes 2,17,32,47). Each schedule has its own
// resource group so they never cancel each other.
func setupGitLabPipelineSchedules(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo, defaultBranch string) error {
	// Delete existing fullsend schedules to avoid duplicates on re-install.
	existing, listErr := client.ListPipelineSchedules(ctx, owner, repo)
	if listErr != nil {
		printer.StepWarn(fmt.Sprintf("Could not list existing schedules — duplicate cleanup skipped: %v", listErr))
	} else {
		for _, s := range existing {
			if strings.HasPrefix(s.Description, "fullsend") {
				if err := client.DeletePipelineSchedule(ctx, owner, repo, s.ID); err != nil {
					printer.StepWarn(fmt.Sprintf("Failed to delete existing schedule %q (ID %d): %v", s.Description, s.ID, err))
				} else {
					printer.StepInfo(fmt.Sprintf("Removed existing schedule %q (ID %d)", s.Description, s.ID))
				}
			}
		}
	}

	printer.StepStart("Creating pipeline schedules")
	var createdIDs []int64
	for _, spec := range repos.PipelineScheduleSpecs() {
		id, err := client.CreatePipelineSchedule(ctx, owner, repo, defaultBranch,
			spec.Description, spec.Cron, spec.Variables)
		if err != nil {
			// Roll back any schedules created in this call.
			for _, prevID := range createdIDs {
				if delErr := client.DeletePipelineSchedule(ctx, owner, repo, prevID); delErr != nil {
					printer.StepWarn(fmt.Sprintf("Failed to clean up schedule (ID %d): %v", prevID, delErr))
				}
			}
			printer.StepFail(fmt.Sprintf("Failed to create %s schedule", spec.Description))
			return fmt.Errorf("creating %s schedule: %w", spec.Description, err)
		}
		createdIDs = append(createdIDs, id)
		printer.StepDone(fmt.Sprintf("Created %s schedule (ID %d)", spec.Description, id))
	}
	return nil
}

// cleanupGitLabPipelineSchedules removes all fullsend-prefixed pipeline
// schedules from a GitLab project.
func cleanupGitLabPipelineSchedules(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo string) error {
	printer.StepStart("Removing pipeline schedules")
	schedules, err := client.ListPipelineSchedules(ctx, owner, repo)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("Could not list pipeline schedules: %v", err))
		return nil
	}
	var toDelete []int64
	for _, s := range schedules {
		if strings.HasPrefix(s.Description, "fullsend") {
			toDelete = append(toDelete, s.ID)
		}
	}
	var deleted int
	for _, id := range toDelete {
		if err := client.DeletePipelineSchedule(ctx, owner, repo, id); err != nil {
			printer.StepWarn(fmt.Sprintf("Failed to delete schedule ID %d: %v", id, err))
		} else {
			deleted++
		}
	}
	printer.StepDone(fmt.Sprintf("Removed %d pipeline schedule(s)", deleted))
	return nil
}

// healGitLabResourceGroups toggles process_mode on all fullsend-prefixed
// resource groups to break stale locks left by cancelled or deleted pipelines.
// This complements the per-job self-heal in the scaffold templates: the
// self-heal cannot fix stale locks on first run because the job is blocked
// before it starts. Running this during install breaks those locks.
//
// The toggle sequence (unordered → target mode) forces GitLab to
// re-evaluate the lock state and release stale locks.
func healGitLabResourceGroups(ctx context.Context, glClient *gitlab.LiveClient, printer *ui.Printer, owner, repo string) {
	printer.StepStart("Healing resource group locks")
	groups, err := glClient.ListResourceGroups(ctx, owner, repo)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("Could not list resource groups: %v", err))
		return
	}

	var healed int
	for _, g := range groups {
		if !strings.HasPrefix(g.Key, "fullsend-") {
			continue
		}
		// Toggle to unordered first to break any stale lock, then set
		// the desired production mode per resource group type.
		targetMode := "newest_first"
		if g.Key == "fullsend-poll-events" {
			targetMode = "oldest_first"
		}
		if err := glClient.UpdateResourceGroupProcessMode(ctx, owner, repo, g.Key, "unordered"); err != nil {
			printer.StepWarn(fmt.Sprintf("Failed to toggle resource group %q to unordered: %v", g.Key, err))
			continue
		}
		if err := glClient.UpdateResourceGroupProcessMode(ctx, owner, repo, g.Key, targetMode); err != nil {
			printer.StepWarn(fmt.Sprintf("Failed to set resource group %q to %s: %v", g.Key, targetMode, err))
			continue
		}
		healed++
	}
	printer.StepDone(fmt.Sprintf("Healed %d resource group(s)", healed))
}

func annotateGitLabRoleLifecycle(ctx context.Context, clients repos.ForgeClientFactory, result *repos.StatusResult) {
	if result == nil || clients == nil {
		return
	}
	fc, err := clients.ConfigFor(repos.ForgeGitLab)
	if err != nil || fc.Client == nil {
		return
	}
	glClient, ok := fc.Client.(*gitlab.LiveClient)
	if !ok {
		return
	}
	adapter := gitlabTokenAdapter{c: glClient}
	now := time.Now()
	for i := range result.Repos {
		st := &result.Repos[i]
		if st.GitLabRoleMode == "" && len(st.GitLabRoleDiagnostics) == 0 {
			continue
		}
		toks, listErr := adapter.ListProjectAccessTokens(ctx, st.Owner, st.Repo)
		if listErr != nil {
			continue
		}
		// repos.Status already counted this repo once in Summary.Drifted
		// if it had any drift. Only count the no-drift -> drift
		// transition here, or a repo with pre-existing drift that also
		// gains a GitLab-role lifecycle drift gets double-counted.
		wasDrifted := len(st.Drifts) > 0
		if repos.EnrichGitLabRoleStatus(ctx, fc.Client, st.Owner, st.Repo, toks, now, st) && !wasDrifted {
			result.Summary.Drifted++
		}
	}
}

type gitlabTokenAdapter struct {
	c *gitlab.LiveClient
}

func (a gitlabTokenAdapter) CreateProjectAccessToken(ctx context.Context, owner, repo, name string, scopes []string, accessLevel int, expiresAt string) (*repos.ProjectAccessToken, error) {
	tok, err := a.c.CreateProjectAccessToken(ctx, owner, repo, name, scopes, accessLevel, expiresAt)
	if err != nil {
		return nil, err
	}
	return &repos.ProjectAccessToken{ID: tok.ID, Name: tok.Name, Token: tok.Token}, nil
}

func (a gitlabTokenAdapter) RevokeProjectAccessToken(ctx context.Context, owner, repo string, tokenID int) error {
	return a.c.RevokeProjectAccessToken(ctx, owner, repo, tokenID)
}

func (a gitlabTokenAdapter) ListProjectAccessTokens(ctx context.Context, owner, repo string) ([]repos.ProjectAccessToken, error) {
	toks, err := a.c.ListProjectAccessTokens(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	out := make([]repos.ProjectAccessToken, len(toks))
	for i, t := range toks {
		out[i] = repos.ProjectAccessToken{
			ID: t.ID, Name: t.Name, Active: t.Active, ExpiresAt: t.ExpiresAt, Revoked: t.Revoked,
		}
	}
	return out, nil
}

func prepareGitLabRoleFlags(opts *reposInstallConfig) error {
	s := strings.ToLower(strings.TrimSpace(opts.gitlabRoleMigration))
	if s == "enforced" {
		return fmt.Errorf("--gitlab-role-migration=enforced is reserved for verification (see #7501); use migrating, rollback, or disabled")
	}
	if s != "" {
		mode, err := gitlabroles.ParseMode(s)
		if err != nil {
			return fmt.Errorf("--gitlab-role-migration: %w", err)
		}
		opts.gitlabRoleModeFlag = mode
	}
	if opts.gitlabRoleRegistry != "" {
		raw, err := os.ReadFile(opts.gitlabRoleRegistry)
		if err != nil {
			return fmt.Errorf("reading --gitlab-role-registry: %w", err)
		}
		if _, err := gitlabroles.ParseRegistry(string(raw)); err != nil {
			return fmt.Errorf("parsing --gitlab-role-registry: %w", err)
		}
		opts.gitlabRoleRegistryJSON = string(raw)
	}
	provided, err := parseGitLabRoleTokens(opts.gitlabRoleTokens)
	if err != nil {
		return err
	}
	opts.gitlabRoleProvided = provided
	for _, raw := range opts.rotateGitLabRoleNames {
		name := gitlabroles.Role(strings.ToLower(strings.TrimSpace(raw)))
		if name == "" {
			return fmt.Errorf("invalid --rotate-gitlab-role: empty name")
		}
		opts.rotateGitLabRoleFilter = append(opts.rotateGitLabRoleFilter, name)
	}
	return nil
}

func parseGitLabRoleTokens(flags []string) (map[gitlabroles.Role]string, error) {
	out := make(map[gitlabroles.Role]string, len(flags))
	for _, raw := range flags {
		role, tok, ok := strings.Cut(raw, "=")
		role = strings.ToLower(strings.TrimSpace(role))
		if !ok || role == "" || tok == "" {
			return nil, fmt.Errorf("invalid --gitlab-role-token: expected role=token")
		}
		if strings.HasPrefix(role, "glpat-") || strings.HasPrefix(role, "glptt-") || strings.HasPrefix(role, "gldt-") {
			return nil, fmt.Errorf("invalid --gitlab-role-token: role name is not a token value")
		}
		out[gitlabroles.Role(role)] = tok
	}
	return out, nil
}

func maybeProvisionGitLabRoles(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string, fresh bool) error {
	needed, current, err := gitLabRoleWorkNeeded(ctx, client, opts, owner, repo, fresh)
	if err != nil {
		return err
	}
	if !needed {
		return nil
	}
	mode := opts.gitlabRoleModeFlag
	if mode == "" {
		// Preserve the live gate's mode (migrating, enforced, rollback, or
		// disabled) unless the operator explicitly passes
		// --gitlab-role-migration. Without this, supplying only
		// --gitlab-role-registry or --gitlab-role-token on an install that
		// was explicitly rolled back or disabled would silently re-enable
		// migration.
		mode = current
	}
	return setupGitLabRoleCredentials(ctx, opts, client, printer, owner, repo, mode)
}

func gitLabRoleWorkNeeded(ctx context.Context, client forge.Client, opts *reposInstallConfig, owner, repo string, fresh bool) (bool, gitlabroles.Mode, error) {
	if opts.gitlabRoleModeFlag != "" {
		return true, opts.gitlabRoleModeFlag, nil
	}
	if fresh {
		// If FULLSEND_GITLAB_ROLE_MIGRATION fails to write on this fresh
		// install, the failure is reported on this run, but the repo is
		// no longer "fresh" on a later unflagged `repos install` (the
		// workflow is now on the default branch). That later run falls
		// through to the live-gate read below, finds the gate still
		// missing, treats it as ModeDisabled, and reports "no work
		// needed" instead of retrying. Recovery requires the operator to
		// explicitly re-run with --gitlab-role-migration=migrating.
		return true, gitlabroles.ModeMigrating, nil
	}
	// The mode flag is empty: always read the live gate so an operator who
	// only passes --gitlab-role-registry or --gitlab-role-token (without
	// --gitlab-role-migration) gets their existing rollback/disabled/
	// enforced decision back as current, rather than an empty mode that
	// the caller would otherwise default to migrating.
	raw, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleMigration)
	if err != nil {
		return false, "", fmt.Errorf("reading %s: %w", forge.VarGitLabRoleMigration, err)
	}
	current := gitlabroles.ModeDisabled
	if exists {
		mode, err := gitlabroles.ParseMode(raw)
		if err != nil {
			return false, "", err
		}
		current = mode
	}
	if opts.gitlabRoleRegistryJSON != "" || len(opts.gitlabRoleProvided) > 0 {
		return true, current, nil
	}
	return current == gitlabroles.ModeMigrating || current == gitlabroles.ModeEnforced, current, nil
}

func setupGitLabRoleCredentials(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string, mode gitlabroles.Mode) error {
	repoFullName := owner + "/" + repo
	registryJSON := opts.gitlabRoleRegistryJSON
	if registryJSON == "" {
		raw, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleRegistry)
		if err != nil {
			return fmt.Errorf("reading %s: %w", forge.VarGitLabRoleRegistry, err)
		}
		if exists {
			registryJSON = raw
		}
	}
	reg, err := gitlabroles.ParseRegistry(registryJSON)
	if err != nil {
		return err
	}
	var tokens repos.ProjectAccessTokenClient
	if glClient, ok := client.(*gitlab.LiveClient); ok {
		tokens = gitlabTokenAdapter{c: glClient}
	}
	printer.StepStart(fmt.Sprintf("[%s] Provisioning GitLab role credentials", repoFullName))
	result, err := repos.ProvisionGitLabRoleCredentials(ctx, repos.RoleProvisionConfig{
		Owner:            owner,
		Repo:             repo,
		Client:           client,
		Tokens:           tokens,
		Registry:         reg,
		RegistryProvided: opts.gitlabRoleRegistryJSON != "",
		DesiredMode:      mode,
		ProvidedTokens:   opts.gitlabRoleProvided,
		DryRun:           opts.dryRun,
	})
	if err != nil {
		printer.StepFail(fmt.Sprintf("[%s] GitLab role provisioning failed", repoFullName))
		return err
	}
	printGitLabRoleProvision(printer, repoFullName, result)
	return nil
}

func maybeRotateGitLabRoles(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string) error {
	force := opts.rotateGitLabRoles
	raw, exists, err := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleMigration)
	if err != nil {
		return fmt.Errorf("reading %s: %w", forge.VarGitLabRoleMigration, err)
	}
	mode := gitlabroles.ModeDisabled
	if exists {
		parsed, perr := gitlabroles.ParseMode(raw)
		if perr != nil {
			return perr
		}
		mode = parsed
	}
	if mode.UsesSharedOnly() && !force {
		return nil
	}
	if !force && mode != gitlabroles.ModeMigrating && mode != gitlabroles.ModeEnforced {
		return nil
	}
	registryJSON := opts.gitlabRoleRegistryJSON
	if registryJSON == "" {
		live, liveExists, liveErr := client.GetRepoVariable(ctx, owner, repo, forge.VarGitLabRoleRegistry)
		if liveErr != nil {
			return fmt.Errorf("reading %s: %w", forge.VarGitLabRoleRegistry, liveErr)
		}
		if liveExists {
			registryJSON = live
		}
	}
	reg, err := gitlabroles.ParseRegistry(registryJSON)
	if err != nil {
		return err
	}
	var tokens repos.ProjectAccessTokenClient
	if glClient, ok := client.(*gitlab.LiveClient); ok {
		tokens = gitlabTokenAdapter{c: glClient}
	}
	repoFullName := owner + "/" + repo
	printer.StepStart(fmt.Sprintf("[%s] Rotating GitLab role credentials", repoFullName))
	result, err := repos.RotateGitLabRoleCredentials(ctx, repos.RoleRotateConfig{
		Owner:          owner,
		Repo:           repo,
		Client:         client,
		Tokens:         tokens,
		Registry:       reg,
		Mode:           mode,
		Roles:          opts.rotateGitLabRoleFilter,
		Force:          opts.rotateGitLabRoles,
		ProvidedTokens: opts.gitlabRoleProvided,
		DryRun:         opts.dryRun,
	})
	if err != nil {
		printer.StepFail(fmt.Sprintf("[%s] GitLab role rotation failed", repoFullName))
		return err
	}
	printGitLabRoleRotate(printer, repoFullName, result)
	return nil
}

func printGitLabRoleRotate(printer *ui.Printer, repoFullName string, result repos.RoleRotateResult) {
	verb := "Rotated"
	if result.DryRun {
		verb = "Would rotate"
	}
	for _, role := range result.Rotated {
		printer.StepDone(fmt.Sprintf("[%s] %s %s role credential", repoFullName, verb, role))
	}
	for _, role := range result.Skipped {
		printer.StepInfo(fmt.Sprintf("[%s] %s role credential not due for rotation", repoFullName, role))
	}
	for _, role := range result.Reused {
		printer.StepInfo(fmt.Sprintf("[%s] %s reuses another registered role credential; rotation follows the target", repoFullName, role))
	}
	for _, role := range result.Overlapping {
		printer.StepInfo(fmt.Sprintf("[%s] %s previous credential remains usable for in-flight jobs", repoFullName, role))
	}
	for _, role := range result.Cleaned {
		printer.StepDone(fmt.Sprintf("[%s] Revoked previous %s role credential after grace period", repoFullName, role))
	}
	for _, role := range result.RolledBack {
		printer.StepWarn(fmt.Sprintf("[%s] %s rotation rolled back; previous credential left in place", repoFullName, role))
	}
	for _, role := range result.InProgress {
		printer.StepInfo(fmt.Sprintf("[%s] %s rotation already in progress", repoFullName, role))
	}
	for _, f := range result.Failed {
		printer.StepWarn(fmt.Sprintf("[%s] %s role rotation pending (%s): %s", repoFullName, f.Role, f.Secret, f.Reason))
	}
	for _, d := range result.Diagnostics {
		printer.StepInfo(fmt.Sprintf("[%s] %s", repoFullName, d))
	}
}

func printGitLabRoleProvision(printer *ui.Printer, repoFullName string, result repos.RoleProvisionResult) {
	createVerb := "Created"
	enrollVerb := "Enrolled"
	if result.DryRun {
		createVerb = "Would create"
		enrollVerb = "Would enroll"
	}
	for _, role := range result.Created {
		printer.StepDone(fmt.Sprintf("[%s] %s %s role credential", repoFullName, createVerb, role))
	}
	for _, role := range result.Enrolled {
		printer.StepDone(fmt.Sprintf("[%s] %s %s role credential", repoFullName, enrollVerb, role))
	}
	for _, role := range result.Skipped {
		printer.StepInfo(fmt.Sprintf("[%s] %s role credential already present", repoFullName, role))
	}
	for _, role := range result.Reused {
		printer.StepInfo(fmt.Sprintf("[%s] %s reuses another registered role credential", repoFullName, role))
	}
	for _, f := range result.Failed {
		printer.StepWarn(fmt.Sprintf("[%s] %s role credential pending (%s): %s", repoFullName, f.Role, f.Secret, f.Reason))
	}
	if result.GateWritten {
		if result.DryRun {
			printer.StepDone(fmt.Sprintf("[%s] Would set GitLab role migration gate=%s (shared credential preserved)", repoFullName, result.Mode))
		} else {
			printer.StepDone(fmt.Sprintf("[%s] GitLab role migration gate=%s (shared credential preserved)", repoFullName, result.Mode))
		}
	}
	for _, d := range result.Diagnostics {
		printer.StepInfo(fmt.Sprintf("[%s] %s", repoFullName, d))
	}
}

// cleanupGitLabRoleTokens revokes active built-in and custom role
// project access tokens. The shared fullsend-bot token is left to
// cleanupGitLabBotToken.
func cleanupGitLabRoleTokens(ctx context.Context, glClient *gitlab.LiveClient, printer *ui.Printer, owner, repo string) error {
	if glClient == nil {
		return nil
	}
	printer.StepStart("Revoking GitLab role access tokens")
	tokens, err := glClient.ListProjectAccessTokens(ctx, owner, repo)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("Could not list project access tokens: %v", err))
		return nil
	}
	var revoked int
	for _, tok := range tokens {
		if !tok.Active || !gitlabroles.IsRoleProjectTokenName(tok.Name) {
			continue
		}
		if err := glClient.RevokeProjectAccessToken(ctx, owner, repo, tok.ID); err != nil {
			printer.StepWarn(fmt.Sprintf("Failed to revoke token %q (ID %d): %v", tok.Name, tok.ID, err))
			continue
		}
		revoked++
	}
	printer.StepDone(fmt.Sprintf("Revoked %d GitLab role access token(s)", revoked))
	return nil
}

// cleanupGitLabBotToken revokes any active fullsend bot project access
// tokens from a GitLab project.
func cleanupGitLabBotToken(ctx context.Context, glClient *gitlab.LiveClient, printer *ui.Printer, owner, repo string) error {
	if glClient == nil {
		return nil
	}
	printer.StepStart("Revoking bot access token")
	tokens, err := glClient.ListProjectAccessTokens(ctx, owner, repo)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("Could not list project access tokens: %v", err))
		return nil
	}
	revoked := false
	for _, t := range tokens {
		if t.Name == gitlabBotTokenName && t.Active {
			if err := glClient.RevokeProjectAccessToken(ctx, owner, repo, t.ID); err != nil {
				printer.StepWarn(fmt.Sprintf("Failed to revoke token %q (ID %d): %v", t.Name, t.ID, err))
			} else {
				revoked = true
			}
		}
	}
	if revoked {
		printer.StepDone("Revoked bot access token")
	} else {
		printer.StepDone("No active bot access token found")
	}
	return nil
}
