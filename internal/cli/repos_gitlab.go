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
	fullsendBotTokenName        = "fullsend-bot"
	gitlabAccessLevelMaintainer = 40
)

// setupGitLabBotToken creates a project access token for the fullsend bot
// identity and stores it as a protected CI/CD variable (FULLSEND_FORGE_TOKEN).
// If project access tokens are not available (free tier), it falls back to
// the provided fallbackToken (from --gitlab-bot-token). Returns the token value.
func setupGitLabBotToken(ctx context.Context, client forge.Client, glClient *gitlab.LiveClient, printer *ui.Printer, owner, repo, fallbackToken string) (string, error) {
	printer.StepStart("Creating project access token")
	var botPAT string
	if glClient != nil {
		expiresAt := time.Now().AddDate(1, 0, 0).Format("2006-01-02")
		token, err := glClient.CreateProjectAccessToken(ctx, owner, repo, fullsendBotTokenName,
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
			printer.StepDone(fmt.Sprintf("Created project access token %q (ID: %d)", fullsendBotTokenName, token.ID))
		}
	} else if fallbackToken != "" {
		botPAT = fallbackToken
	}

	if botPAT != "" {
		printer.StepStart("Storing bot credentials")
		if err := client.CreateRepoSecret(ctx, owner, repo, "FULLSEND_FORGE_TOKEN", botPAT); err != nil {
			printer.StepFail("Failed to store bot credentials")
			return "", fmt.Errorf("storing bot PAT: %w", err)
		}
		printer.StepDone("Bot credentials stored as protected CI/CD variable")
	}

	return botPAT, nil
}

// setupGitLabPipelineSchedules creates pipeline schedules for polling.
// Enterprise instances get dual fast/full poll schedules; free/CE instances
// get a single hourly schedule.
func setupGitLabPipelineSchedules(ctx context.Context, client forge.Client, glClient *gitlab.LiveClient, printer *ui.Printer, owner, repo, defaultBranch string) (bool, error) {
	printer.StepStart("Detecting GitLab tier")
	isEnterprise := glClient != nil && glClient.IsEnterprise(ctx)
	if isEnterprise {
		printer.StepDone("Detected tier: enterprise")
	} else {
		printer.StepDone("Detected tier: free")
	}

	printer.StepStart("Creating pipeline schedules")
	if isEnterprise {
		fastID, err := client.CreatePipelineSchedule(ctx, owner, repo, defaultBranch,
			"fullsend fast poll", "*/5 * * * *",
			map[string]string{"FULLSEND_POLL_MODE": "fast"})
		if err != nil {
			printer.StepFail("Failed to create fast poll schedule")
			return isEnterprise, fmt.Errorf("creating fast poll schedule: %w", err)
		}
		fullID, err := client.CreatePipelineSchedule(ctx, owner, repo, defaultBranch,
			"fullsend full poll", "*/15 * * * *",
			map[string]string{"FULLSEND_POLL_MODE": "full"})
		if err != nil {
			printer.StepFail("Failed to create full poll schedule")
			return isEnterprise, fmt.Errorf("creating full poll schedule: %w", err)
		}
		printer.StepDone(fmt.Sprintf("Created dual poll schedules (fast: ID %d, full: ID %d)", fastID, fullID))
	} else {
		scheduleID, err := client.CreatePipelineSchedule(ctx, owner, repo, defaultBranch,
			"fullsend poll", "0 * * * *", nil)
		if err != nil {
			printer.StepFail("Failed to create poll schedule")
			return isEnterprise, fmt.Errorf("creating poll schedule: %w", err)
		}
		printer.StepDone(fmt.Sprintf("Created hourly poll schedule (ID %d)", scheduleID))
	}

	return isEnterprise, nil
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
	for _, id := range toDelete {
		if err := client.DeletePipelineSchedule(ctx, owner, repo, id); err != nil {
			printer.StepWarn(fmt.Sprintf("Failed to delete schedule ID %d: %v", id, err))
		}
	}
	printer.StepDone(fmt.Sprintf("Removed %d pipeline schedule(s)", len(toDelete)))
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
		if t.Name == fullsendBotTokenName && t.Active {
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
