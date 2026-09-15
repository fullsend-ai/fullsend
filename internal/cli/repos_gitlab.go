package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/poll"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	gitlabBotTokenName         = "fullsend-bot"
	gitlabAccessLevelDeveloper = 30
)

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

		expiresAt := time.Now().AddDate(1, 0, 0).Format("2006-01-02")
		// "api" scope is required because the bot token is used for package
		// registry state, pipeline creation, and merge request operations.
		// Developer (30) is sufficient now that poller state lives in the
		// Generic Package Registry instead of CI/CD variables (#7313).
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

	provisionGitLabDispatchSecret(ctx, client, printer, owner, repo)

	return botPAT, nil
}

// provisionGitLabDispatchSecret ensures FULLSEND_DISPATCH_SECRET exists
// (so poll-state and dispatch HMAC signing is on by default instead of
// silently unsigned), discards any pre-#7317 *unsigned* state.json, and
// migrates any pre-#7313 poller CI/CD variables into the package
// registry — all while the caller's Maintainer-or-higher client can
// still read the legacy variables, which the Developer-level bot PAT
// cannot.
//
// The discard step runs before the migration so that an untrusted
// unsigned document (any Developer-level token can write it) is never
// laundered into a signed one; the only trustworthy migration source is
// the Maintainer-only legacy CI/CD variables. If a repo has neither, its
// next poll starts fresh (a one-time, at-least-once re-dispatch) rather
// than trusting unauthenticated state. See poll.DiscardUnsignedGitLabPollState.
//
// It is safe to run on both fresh installs and already-enrolled
// (converged / already-current) repos: it never creates or revokes the
// bot PAT, so it does not disturb live pipelines. Best-effort — failures
// warn rather than fail the operation, matching the other GitLab setup
// steps (duplicate-token cleanup, etc.).
func provisionGitLabDispatchSecret(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo string) {
	dispatchSecret, secretErr := ensureGitLabDispatchSecret(ctx, client, printer, owner, repo)
	if secretErr != nil {
		printer.StepWarn(fmt.Sprintf("Could not provision dispatch secret: %v", secretErr))
		return
	}
	if discarded, discardErr := poll.DiscardUnsignedGitLabPollState(ctx, client, owner, repo, dispatchSecret); discardErr != nil {
		printer.StepWarn(fmt.Sprintf("Could not discard unsigned poll state: %v", discardErr))
	} else if discarded {
		printer.StepInfo("Discarded untrusted unsigned poll state document")
	}
	if seeded, seedErr := poll.SeedGitLabPollStateFromLegacyVars(ctx, client, owner, repo, dispatchSecret); seedErr != nil {
		printer.StepWarn(fmt.Sprintf("Could not migrate legacy poller state: %v", seedErr))
	} else if seeded {
		printer.StepDone("Migrated legacy poller state to package registry")
	}
}

// ensureGitLabDispatchSecret returns the project's existing
// FULLSEND_DISPATCH_SECRET CI/CD variable value, or generates and
// stores a new one (masked, protected, like the bot PAT) if none is
// set. Without this, dispatch-variable and poll-state HMAC signing
// (see ADR 0067 and PR #7317) require a manual operator step and
// default to unsigned.
func ensureGitLabDispatchSecret(ctx context.Context, client forge.Client, printer *ui.Printer, owner, repo string) (string, error) {
	vars, err := client.ListRepoVariables(ctx, owner, repo)
	if err != nil {
		return "", fmt.Errorf("listing repo variables: %w", err)
	}
	if existing, ok := vars[forge.SecretDispatch]; ok && existing != "" {
		return existing, nil
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating dispatch secret: %w", err)
	}
	secret := hex.EncodeToString(buf)
	if err := client.CreateRepoSecret(ctx, owner, repo, forge.SecretDispatch, secret); err != nil {
		return "", fmt.Errorf("storing dispatch secret: %w", err)
	}
	printer.StepDone("Provisioned FULLSEND_DISPATCH_SECRET (protected, masked)")
	return secret, nil
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
