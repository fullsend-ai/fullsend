package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	gitlabBotTokenName          = "fullsend-bot"
	gitlabAccessLevelMaintainer = 40
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
		// "api" scope is required because the bot token is used for CI/CD variable
		// management, pipeline schedule creation, and merge request operations.
		token, err := glClient.CreateProjectAccessToken(ctx, owner, repo, gitlabBotTokenName,
			[]string{"api"}, gitlabAccessLevelMaintainer, expiresAt)
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
	slashID, err := client.CreatePipelineSchedule(ctx, owner, repo, defaultBranch,
		"fullsend slash poll", "*/5 * * * *", map[string]string{forge.VarPollMode: "slash"})
	if err != nil {
		printer.StepFail("Failed to create slash poll schedule")
		return fmt.Errorf("creating slash poll schedule: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Created slash poll schedule (ID %d)", slashID))

	eventID, err := client.CreatePipelineSchedule(ctx, owner, repo, defaultBranch,
		"fullsend event poll", "2,17,32,47 * * * *", map[string]string{forge.VarPollMode: "events"})
	if err != nil {
		if delErr := client.DeletePipelineSchedule(ctx, owner, repo, slashID); delErr != nil {
			printer.StepWarn(fmt.Sprintf("Failed to clean up slash poll schedule (ID %d): %v", slashID, delErr))
		}
		printer.StepFail("Failed to create event poll schedule")
		return fmt.Errorf("creating event poll schedule: %w", err)
	}
	printer.StepDone(fmt.Sprintf("Created event poll schedule (ID %d)", eventID))
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
