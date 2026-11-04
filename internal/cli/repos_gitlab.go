package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlablifecycle"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/poll"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// setupGitLabBotToken and ensureGitLabSharedCredentialAllowed were
// removed: registered role credentials are now the only supported
// runtime path (#7782 PR3), so `repos install` never provisions the
// shared fullsend-bot PAT (fresh or existing install).

// provisionGitLabPollState ensures FULLSEND_DISPATCH_SECRET exists so
// poll-state HMAC signing is on by default, then creates both poll-state
// branches with an empty signed baseline document.
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
		printer.StepDone(fmt.Sprintf("[%s] Seeded poll-state branches", repoFullName))
	}
	return nil
}

// setupGitLabPipelineSchedules creates two independent pipeline schedules
// for polling: a fast slash-command poll (every 5 min) and an offset
// event-discovery poll (at minutes 2,17,32,47). Each schedule has its own
// resource group so they never cancel each other. Schedules are
// variable-free: the typed templates select poll mode from the schedule
// description.
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
	adapter := newGitLabRoleTokenClient(glClient)
	now := time.Now()
	for i := range result.Repos {
		st := &result.Repos[i]
		// A manifest can mix GitHub and GitLab entries. RepoStatus.Forge is
		// populated by repos.Status() for every real status result; an
		// empty value only occurs in hand-built test fixtures that predate
		// this field, which are exercising GitLab-only scenarios. Skip
		// anything explicitly resolved to a non-GitLab forge so GitHub
		// owner/repo paths are never sent to the GitLab client (mirrors
		// the forge gate in repos install's equivalent path).
		if st.Forge != "" && st.Forge != repos.ForgeGitLab {
			continue
		}
		// Rows rejected for configuration (e.g. missing inference.auth) were
		// never evaluated; do not inspect their project or append drift.
		if st.ConfigRejected {
			continue
		}
		toks, listErr := adapter.ListProjectAccessTokens(ctx, st.Owner, st.Repo)
		if listErr != nil {
			toks = nil
		}
		// repos.Status already counted this repo once in Summary.Drifted
		// if it had any drift. Only count the no-drift -> drift
		// transition here, or a repo with pre-existing drift that also
		// gains a GitLab-role lifecycle drift gets double-counted.
		wasDrifted := len(st.Drifts) > 0
		newlyDrifted := false
		if listErr != nil {
			// Credential health cannot be established without the inventory, and
			// the service-account check below does not read it: never leave
			// roles ready and drift-free on an unverified inventory.
			newlyDrifted = repos.AppendGitLabTokenInventoryUnverified(st)
		} else {
			newlyDrifted = repos.EnrichGitLabRoleStatus(ctx, fc.Client, st.Owner, st.Repo, toks, now, st)
			// A supplied credential outside the inventories has no snapshot, so
			// the enrichment above trusts its enrollment proof. Authenticate it
			// the way rotation does so status and rotation agree on its health.
			if adapter.AppendGitLabSuppliedCredentialStatus(ctx, fc.Client, st.Owner, st.Repo, toks, now, st) {
				newlyDrifted = true
			}
		}
		userIDs := repos.WithSuppliedPollerUserID(ctx, fc.Client, st.Owner, st.Repo, repos.PollerPipelineUserIDs(toks))
		if repos.AppendGitLabPipelineRefStatus(ctx, fc.Client, st.Owner, st.Repo, userIDs, st) {
			newlyDrifted = true
		}
		if adapter.AppendGitLabServiceAccountStatus(ctx, fc.Client, st.Owner, st.Repo, st) {
			newlyDrifted = true
		}
		// The ledger-only diagnosis from repos.Status becomes a live one.
		if appendGitLabTriggerAccountStatus(ctx, fc.Client, st) {
			newlyDrifted = true
		}
		// The live trigger re-diagnosis can add drift to a ready ledger-only
		// row or remove ledger-only drift that GitLab disproves, so the repo's
		// final state decides the count in both directions.
		nowDrifted := newlyDrifted || len(st.Drifts) > 0
		result.Summary.Drifted = summaryDriftedAfterLive(result.Summary.Drifted, wasDrifted, nowDrifted)
	}
}

// summaryDriftedAfterLive adjusts the drifted-repo count when a live
// re-diagnosis changes whether a repo is drifted: a repo is counted once, on
// its no-drift to drift transition, and uncounted when drift is disproved.
func summaryDriftedAfterLive(drifted int, was, now bool) int {
	switch {
	case now && !was:
		return drifted + 1
	case !now && was && drifted > 0:
		return drifted - 1
	}
	return drifted
}

func gitLabTokenInventory(opts *reposInstallConfig, client forge.Client) repos.ProjectAccessTokenClient {
	if opts != nil && opts.testGitLabTokenInventory != nil {
		return opts.testGitLabTokenInventory
	}
	glClient, ok := client.(*gitlab.LiveClient)
	if !ok {
		return nil
	}
	return newGitLabRoleTokenClient(glClient)
}

func ensureGitLabPollerPipelineAccess(ctx context.Context, client forge.Client, tokens repos.ProjectAccessTokenClient, printer *ui.Printer, owner, repo string, dryRun bool) error {
	repoFullName := owner + "/" + repo
	var toks []repos.ProjectAccessToken
	var listErr error
	if tokens != nil {
		listed, err := tokens.ListProjectAccessTokens(ctx, owner, repo)
		if err != nil {
			listErr = fmt.Errorf("listing project access tokens for protected-ref pipeline access: %w", err)
		} else {
			toks = listed
		}
	}
	// Proceed even when the token list failed: EnsureGitLabPollerPipelineAccess
	// no-ops when the branch is unprotected or Developer-class merge/push is
	// already allowed, neither of which needs the token list. Only surface
	// listErr if a grant actually turns out to be required.
	userIDs := repos.WithSuppliedPollerUserID(ctx, client, owner, repo, repos.PollerPipelineUserIDs(toks))
	res, err := repos.EnsureGitLabPollerPipelineAccess(ctx, client, owner, repo, userIDs, dryRun)
	if err != nil {
		if listErr != nil {
			err = fmt.Errorf("%w (also failed to list project access tokens: %v)", err, listErr)
		}
		printer.StepFail(fmt.Sprintf("[%s] GitLab poller protected-ref pipeline access: %v", repoFullName, err))
		return err
	}
	if res.Detail != "" {
		printer.StepDone(fmt.Sprintf("[%s] %s", repoFullName, res.Detail))
	}
	return nil
}

// ensureGitLabPipelineVariableOverrideRole converges a GitLab project's
// ci_pipeline_variables_minimum_override_role toward no_one_allowed
// (#7850, superseding the owner-role direction #7769 had left open). It
// is called for both fresh installs and converge/repair of
// already-installed repos, and is idempotent: an already-no_one_allowed
// project reports Action "none" every time. Automatic setting changes
// default to off, but drift is an activation error, not report-only success.
// Variable-free managed schedules and ADR 0125 live validation remain
// rollout gates.
func ensureGitLabPipelineVariableOverrideRole(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo string, dryRun bool) error {
	repoFullName := owner + "/" + repo
	res, err := repos.ActivateGitLabTypedDispatch(ctx, client, owner, repo, dryRun)
	if err != nil {
		printer.StepFail(fmt.Sprintf("[%s] GitLab pipeline-variable override role: %v", repoFullName, err))
		return err
	}
	if res.Detail != "" {
		msg := fmt.Sprintf("[%s] %s", repoFullName, res.Detail)
		printer.StepDone(msg)
	}
	return nil
}

// setupGitLabWebhookFastPath provisions or repairs the GitLab webhook
// fast-path (ADR 0125): the pipeline trigger token stored as
// FULLSEND_TRIGGER_TOKEN, the webhook secret stored as
// FULLSEND_WEBHOOK_SECRET, and the project webhook that fires the native
// pipeline trigger pinned to the protected default branch. It is an
// always-managed component (no opt-in flag), idempotent and probe-first
// like setupGitLabPipelineSchedules' callers: an already-provisioned repo
// is a no-op. rotate force-rotates the trigger token. Output names
// variables and numeric IDs only — never the trigger token, webhook
// secret, or webhook URL. While the trigger-account dispatch activation is
// recorded, the webhook uses the trigger account's published trigger token
// (#8509). Otherwise the trigger token is owned by a fresh Poller service
// account generation that is verified at Developer before it is published;
// ineligible repos defer and keep polling (see gitlabTriggerOwnerFor).
func setupGitLabWebhookFastPath(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo string, rotate, dryRun bool) error {
	repoFullName := owner + "/" + repo
	res, err := repos.EnsureGitLabWebhookFastPathWithTriggerOwner(ctx, client, gitlabTriggerOwnerFor(client), repos.GitLabBaseURL(client), owner, repo, rotate, dryRun)
	for _, d := range res.Details {
		if res.Action == "deferred" {
			printer.StepInfo(fmt.Sprintf("[%s] %s", repoFullName, d))
		} else {
			printer.StepDone(fmt.Sprintf("[%s] %s", repoFullName, d))
		}
	}
	if err != nil {
		printer.StepFail(fmt.Sprintf("[%s] GitLab webhook fast-path: %v", repoFullName, err))
		return err
	}
	return nil
}

// refreshGitLabWebhookTriggerRoute converges the webhook fast path again after
// the trigger-account stage when the webhook's trigger credential no longer
// matches the dispatch route (#8509): the route was activated or the account
// rotated, so the webhook must carry the account's current trigger token, or
// the route was withdrawn, so the webhook returns to a Poller-owned trigger.
// Dry runs change nothing and skip it.
func refreshGitLabWebhookTriggerRoute(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo string, dryRun bool) error {
	if dryRun {
		return nil
	}
	stale, err := repos.GitLabWebhookNeedsProvisioning(ctx, client, repos.GitLabBaseURL(client), owner, repo)
	if err != nil {
		return fmt.Errorf("checking the GitLab webhook trigger route: %w", err)
	}
	if !stale {
		return nil
	}
	return setupGitLabWebhookFastPath(ctx, client, printer, owner, repo, false, false)
}

// refreshGitLabWebhookAfterPollerFailure skips Poller-route provisioning after
// a failed Poller stage, even if an old account hook appears stale after
// activation was withdrawn. It may repair or
// create a hook only if account convergence restored the active account route.
func refreshGitLabWebhookAfterPollerFailure(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo string, dryRun bool) error {
	if dryRun {
		return nil
	}
	repoFullName := owner + "/" + repo
	res, err := repos.EnsureGitLabWebhookAccountRouteAfterPollerFailure(ctx, client, gitlabTriggerOwnerFor(client), repos.GitLabBaseURL(client), owner, repo)
	for _, d := range res.Details {
		if res.Action == "deferred" {
			printer.StepInfo(fmt.Sprintf("[%s] %s", repoFullName, d))
		} else {
			printer.StepDone(fmt.Sprintf("[%s] %s", repoFullName, d))
		}
	}
	if err != nil {
		printer.StepFail(fmt.Sprintf("[%s] GitLab webhook fast-path: %v", repoFullName, err))
	}
	return err
}

// reconcileGitLabWebhookSafety revokes the managed webhook and trigger
// credential when the trigger-token safety invariants no longer hold, and
// never provisions. It runs for repositories whose installation or
// convergence failed, where setupGitLabWebhookFastPath is not reached but
// an existing credential must still not outlive a weakened restriction.
func reconcileGitLabWebhookSafety(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string) error {
	repoFullName := owner + "/" + repo
	dryRun := opts != nil && opts.dryRun
	// A Poller left elevated by an interrupted install is restored, or its
	// credential contained, even when the install failed before webhook setup.
	res, err := repos.ReconcileGitLabWebhookSafetyWithTriggerOwner(ctx, client, gitlabTriggerOwnerFor(client), owner, repo, dryRun)
	for _, d := range res.Details {
		printer.StepInfo(fmt.Sprintf("[%s] %s", repoFullName, d))
	}
	if err != nil {
		printer.StepWarn(fmt.Sprintf("[%s] GitLab webhook safety reconciliation: %v", repoFullName, err))
		// Removing the managed webhook does not invalidate the trigger
		// account's bearer, so an account-side safety failure also contains the
		// account's trigger and published token.
		if repos.GitLabWebhookTriggerOwnerUnsafe(err) && !dryRun {
			err = errors.Join(err, containGitLabTriggerAccountAfterWebhookFailure(ctx, opts, client, printer, owner, repo))
		}
		return err
	}
	return nil
}

// reconcileGitLabPollerElevation returns a managed Poller left above
// Developer by an interrupted install or rotation to Developer access (or
// contains its credential) for every GitLab repository, before any role
// lifecycle operation and independently of webhook work detection. It never
// provisions.
func reconcileGitLabPollerElevation(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string) error {
	repoFullName := owner + "/" + repo
	to := gitlabTriggerOwnerFor(client)
	if opts != nil && opts.testGitLabTriggerOwner != nil {
		to = opts.testGitLabTriggerOwner
	}
	dryRun := opts != nil && opts.dryRun
	res, err := repos.ReconcileGitLabPollerElevation(ctx, client, to, owner, repo, dryRun)
	for _, d := range res.Details {
		printer.StepInfo(fmt.Sprintf("[%s] %s", repoFullName, d))
	}
	return err
}

type gitlabTokenAdapter struct {
	c *gitlab.LiveClient
}

func (a gitlabTokenAdapter) CreateProjectAccessToken(ctx context.Context, owner, repo, name string, scopes []string, accessLevel int, expiresAt string) (*repos.ProjectAccessToken, error) {
	tok, err := a.c.CreateProjectAccessToken(ctx, owner, repo, name, scopes, accessLevel, expiresAt)
	if err != nil {
		return nil, err
	}
	return &repos.ProjectAccessToken{ID: tok.ID, Name: tok.Name, Token: tok.Token, UserID: tok.UserID}, nil
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
			ID: t.ID, Name: t.Name, Active: t.Active, ExpiresAt: t.ExpiresAt, Revoked: t.Revoked, UserID: t.UserID,
		}
	}
	return out, nil
}

func prepareGitLabRoleFlags(opts *reposInstallConfig) error {
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

func maybeProvisionGitLabRoles(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string) error {
	return setupGitLabRoleCredentials(ctx, opts, client, printer, owner, repo)
}

func setupGitLabRoleCredentials(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string) error {
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
		tokens = newGitLabRoleTokenClient(glClient)
	}
	printer.StepStart(fmt.Sprintf("[%s] Provisioning GitLab role credentials", repoFullName))
	result, err := repos.ProvisionGitLabRoleCredentials(ctx, repos.RoleProvisionConfig{
		Owner:            owner,
		Repo:             repo,
		Client:           client,
		Tokens:           tokens,
		Registry:         reg,
		RegistryProvided: opts.gitlabRoleRegistryJSON != "",
		ProvidedTokens:   opts.gitlabRoleProvided,
		ForceRotation:    opts.rotateGitLabRoles,
		ForceRoles:       opts.rotateGitLabRoleFilter,
		DryRun:           opts.dryRun,
	})
	if err != nil {
		printer.StepFail(fmt.Sprintf("[%s] GitLab role provisioning failed", repoFullName))
		return err
	}
	printGitLabRoleProvision(printer, repoFullName, result)
	if len(result.Failed) > 0 {
		return fmt.Errorf("GitLab role provisioning incomplete: %d role credential(s) pending", len(result.Failed))
	}
	return nil
}

func maybeRotateGitLabRoles(ctx context.Context, opts *reposInstallConfig, client forge.Client, printer *ui.Printer, owner, repo string) error {
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
		tokens = newGitLabRoleTokenClient(glClient)
	}
	repoFullName := owner + "/" + repo
	if tokens != nil {
		if _, inventoryErr := tokens.ListProjectAccessTokens(ctx, owner, repo); inventoryErr != nil {
			if opts.rotateGitLabRoles || len(opts.rotateGitLabRoleFilter) > 0 {
				return fmt.Errorf("listing GitLab project access tokens for forced role rotation: %w", inventoryErr)
			}
			// Provisioning leaves existing secrets alone and delegates supplied
			// replacements to this pass, so deferring would report success while
			// the old credential stays installed.
			if len(opts.gitlabRoleProvided) > 0 {
				return fmt.Errorf("listing GitLab project access tokens for supplied role credential replacement: %w", inventoryErr)
			}
			printer.StepInfo(fmt.Sprintf("[%s] GitLab role rotation deferred: project-token inventory unavailable", repoFullName))
			return nil
		}
	}
	printer.StepStart(fmt.Sprintf("[%s] Rotating GitLab role credentials", repoFullName))
	result, err := repos.RotateGitLabRoleCredentials(ctx, repos.RoleRotateConfig{
		Owner:          owner,
		Repo:           repo,
		Client:         client,
		Tokens:         tokens,
		Registry:       reg,
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
	if len(result.Failed) > 0 {
		return fmt.Errorf("GitLab role rotation incomplete: %d role credential(s) pending", len(result.Failed))
	}
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
	for _, d := range result.Diagnostics {
		printer.StepInfo(fmt.Sprintf("[%s] %s", repoFullName, d))
	}
}

// gitLabUninstallTokens returns the project-token inventory used by
// repos.Uninstall to revoke GitLab identity PATs. Test hooks win; live
// GitLab clients are wrapped when at least one targeted repo is GitLab.
// An unobtainable live client for a GitLab-targeted manifest is a
// silent-success risk (PAT revocation is skipped entirely), so it is
// reported via printer.StepWarn rather than returned without comment.
func gitLabUninstallTokens(opts *reposUninstallConfig, clients repos.ForgeClientFactory, printer *ui.Printer, manifest *repos.Manifest, repoNames []string) repos.ProjectAccessTokenClient {
	if opts != nil && opts.testGitLabTokens != nil {
		return opts.testGitLabTokens
	}
	if clients == nil || manifest == nil {
		return nil
	}
	for _, fullName := range repoNames {
		owner, name, found := strings.Cut(fullName, "/")
		if !found {
			continue
		}
		rc, ok := manifest.ResolveConfigWithGlobs(owner, name)
		if !ok || rc.Forge != repos.ForgeGitLab {
			continue
		}
		fc, err := clients.ConfigFor(repos.ForgeGitLab)
		if err != nil {
			printer.StepWarn(fmt.Sprintf("Could not obtain a GitLab client for project access token revocation: %v — token revocation will be skipped for GitLab repos in this run", err))
			return nil
		}
		glClient, ok := fc.Client.(*gitlab.LiveClient)
		if !ok {
			printer.StepWarn("GitLab client is not a live API client — project access token revocation will be skipped for GitLab repos in this run")
			return nil
		}
		return gitlablifecycle.NewUninstallTokenClient(glClient, newGitLabRoleTokenClient(glClient))
	}
	return nil
}
