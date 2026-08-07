package cli

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	gitlabBotTokenName          = "fullsend-bot"
	gitlabAccessLevelMaintainer = 40
)

// botTokenWIFConfig provides GCP parameters for storing the bot token in
// Secret Manager when WIF mode is active. When passed to
// setupGitLabBotToken, the bot PAT is stored in Secret Manager instead
// of as a CI/CD variable, and FULLSEND_BOT_TOKEN_SECRET is set as a
// protected CI/CD variable pointing to the secret name.
type botTokenWIFConfig struct {
	GCPClient gcf.GCFClient
	ProjectID string
}

// secretIDSanitizer replaces characters invalid in Secret Manager IDs with hyphens.
// GCP Secret Manager IDs allow [a-zA-Z0-9_-] only.
var secretIDSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_\-]`)

const secretIDMaxLen = 255

// botTokenSecretID returns the Secret Manager secret ID for a repo's bot token.
// Slashes in GitLab subgroup paths are mapped to double underscores and dots
// are mapped to "_dot_" so that "group/sub", "group-sub", "my.group", and
// "my-group" all produce distinct IDs. Note: a literal "_dot_" in a name would
// collide with a dot-mapped name; this is accepted as extremely unlikely.
func botTokenSecretID(owner, repo string) (string, error) {
	combined := strings.ReplaceAll(owner, "/", "__") + "--" + repo
	combined = strings.ReplaceAll(combined, ".", "_dot_")
	sanitized := secretIDSanitizer.ReplaceAllString(combined, "-")
	id := "fullsend-bot-token-" + sanitized
	if len(id) > secretIDMaxLen {
		return "", fmt.Errorf("secret ID %q exceeds %d character limit", id, secretIDMaxLen)
	}
	return id, nil
}

// legacyBotTokenSecretID returns the pre-_dot_ secret ID for migration.
// Before the _dot_ mapping was added, dots were mapped to hyphens by the
// sanitizer. This is used during cleanup to delete secrets created under
// the old naming scheme.
func legacyBotTokenSecretID(owner, repo string) string {
	combined := strings.ReplaceAll(owner, "/", "__") + "--" + repo
	return "fullsend-bot-token-" + secretIDSanitizer.ReplaceAllString(combined, "-")
}

// setupGitLabBotToken creates a project access token for the fullsend bot
// identity and stores it appropriately based on the credential mode.
//
// When wifCfg is nil (variable mode), the PAT is stored as a protected
// CI/CD variable (FULLSEND_FORGE_TOKEN).
//
// When wifCfg is non-nil (WIF mode), the PAT is stored in GCP Secret
// Manager and FULLSEND_BOT_TOKEN_SECRET is set as a protected CI/CD
// variable pointing to the secret name. The FULLSEND_FORGE_TOKEN CI/CD
// variable is not written — the scaffold retrieves the PAT from Secret
// Manager at runtime via OIDC/WIF.
//
// If project access tokens are not available (free tier), it falls back
// to the provided fallbackToken (from --gitlab-bot-token). Returns the
// token value.
func setupGitLabBotToken(ctx context.Context, client forge.Client, glClient *gitlab.LiveClient, printer *ui.Printer, owner, repo, fallbackToken string, wifCfg *botTokenWIFConfig) (string, error) {
	printer.StepStart("Creating project access token")
	var botPAT string
	var botTokenID int
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
			botTokenID = token.ID
			printer.StepDone(fmt.Sprintf("Created project access token %q (ID: %d)", gitlabBotTokenName, token.ID))
		}
	} else if fallbackToken != "" {
		botPAT = fallbackToken
	} else {
		return "", fmt.Errorf("no GitLab client available and no --gitlab-bot-token provided")
	}

	if botPAT != "" {
		if wifCfg != nil {
			// WIF mode: store bot PAT in Secret Manager and set
			// FULLSEND_BOT_TOKEN_SECRET as a protected CI/CD variable.
			printer.StepStart("Storing bot credentials in Secret Manager")
			secretID, err := botTokenSecretID(owner, repo)
			if err != nil {
				printer.StepFail("Invalid secret ID")
				return "", err
			}

			if err := storeSecretManagerToken(ctx, wifCfg.GCPClient, printer, wifCfg.ProjectID, secretID, []byte(botPAT)); err != nil {
				printer.StepFail("Failed to store bot credentials in Secret Manager")
				return "", fmt.Errorf("storing bot PAT in Secret Manager: %w", err)
			}

			// Grant the WIF service account access to read the secret.
			saEmail := gcf.MintServiceAccountEmail(wifCfg.ProjectID)
			secretResource := fmt.Sprintf("projects/%s/secrets/%s", wifCfg.ProjectID, secretID)
			if err := wifCfg.GCPClient.ReplaceSecretIAMBinding(ctx, secretResource,
				"serviceAccount:"+saEmail, "roles/secretmanager.secretAccessor"); err != nil {
				// Best-effort cleanup: delete the orphaned secret and revoke the PAT.
				if delErr := wifCfg.GCPClient.DeleteSecret(ctx, wifCfg.ProjectID, secretID); delErr != nil {
					printer.StepWarn(fmt.Sprintf("Failed to clean up secret %s: %v", secretID, delErr))
				}
				if botTokenID != 0 && glClient != nil {
					if revErr := glClient.RevokeProjectAccessToken(ctx, owner, repo, botTokenID); revErr != nil {
						printer.StepWarn(fmt.Sprintf("Failed to revoke bot PAT (ID %d): %v", botTokenID, revErr))
					}
				}
				printer.StepFail("Failed to grant secret access")
				return "", fmt.Errorf("granting secret access for %s: %w", secretID, err)
			}

			// Set FULLSEND_BOT_TOKEN_SECRET as a protected CI/CD variable
			// so the scaffold knows which secret to read from Secret Manager.
			if err := client.CreateProtectedCIVariable(ctx, owner, repo, "FULLSEND_BOT_TOKEN_SECRET", secretID); err != nil {
				// Best-effort cleanup: delete the orphaned secret and revoke the PAT.
				if delErr := wifCfg.GCPClient.DeleteSecret(ctx, wifCfg.ProjectID, secretID); delErr != nil {
					printer.StepWarn(fmt.Sprintf("Failed to clean up secret %s: %v", secretID, delErr))
				}
				if botTokenID != 0 && glClient != nil {
					if revErr := glClient.RevokeProjectAccessToken(ctx, owner, repo, botTokenID); revErr != nil {
						printer.StepWarn(fmt.Sprintf("Failed to revoke bot PAT (ID %d): %v", botTokenID, revErr))
					}
				}
				printer.StepFail("Failed to set FULLSEND_BOT_TOKEN_SECRET")
				return "", fmt.Errorf("setting FULLSEND_BOT_TOKEN_SECRET: %w", err)
			}
			printer.StepDone("Bot credentials stored in Secret Manager")

			// Best-effort: delete any legacy-named secret left by
			// a previous install that used dot-to-hyphen mapping.
			if legacyID := legacyBotTokenSecretID(owner, repo); legacyID != secretID {
				if err := wifCfg.GCPClient.DeleteSecret(ctx, wifCfg.ProjectID, legacyID); err == nil {
					printer.StepDone(fmt.Sprintf("Deleted legacy secret %s", legacyID))
				}
			}
		} else {
			// Variable mode: store bot PAT directly as a protected CI/CD variable.
			printer.StepStart("Storing bot credentials")
			if err := client.CreateRepoSecret(ctx, owner, repo, "FULLSEND_FORGE_TOKEN", botPAT); err != nil {
				printer.StepFail("Failed to store bot credentials")
				return "", fmt.Errorf("storing bot PAT: %w", err)
			}
			printer.StepDone("Bot credentials stored as protected CI/CD variable")
		}
	}

	return botPAT, nil
}

// storeSecretManagerToken creates a Secret Manager secret (if it doesn't
// exist), disables any existing latest version, and stores the provided
// data as a new version.
func storeSecretManagerToken(ctx context.Context, gcpClient gcf.GCFClient, printer *ui.Printer, projectID, secretID string, data []byte) error {
	secretErr := gcpClient.GetSecret(ctx, projectID, secretID)
	if secretErr != nil {
		if !errors.Is(secretErr, gcf.ErrSecretNotFound) {
			return fmt.Errorf("checking secret %s: %w", secretID, secretErr)
		}
		if err := gcpClient.CreateSecret(ctx, projectID, secretID); err != nil {
			return fmt.Errorf("creating secret %s: %w", secretID, err)
		}
	} else {
		// Secret already exists — disable the current latest version so
		// stale PATs don't accumulate as enabled versions.
		if err := gcpClient.DisableSecretVersion(ctx, projectID, secretID); err != nil {
			printer.StepWarn(fmt.Sprintf("Could not disable previous secret version for %s: %v", secretID, err))
		}
	}
	if err := gcpClient.AddSecretVersion(ctx, projectID, secretID, data); err != nil {
		return fmt.Errorf("adding secret version for %s: %w", secretID, err)
	}
	return nil
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
// The toggle sequence (unordered → newest_first) forces GitLab to
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
		// Toggle to unordered first to break any stale lock, then set to
		// newest_first which is the desired production mode.
		if err := glClient.UpdateResourceGroupProcessMode(ctx, owner, repo, g.Key, "unordered"); err != nil {
			printer.StepWarn(fmt.Sprintf("Failed to toggle resource group %q to unordered: %v", g.Key, err))
			continue
		}
		if err := glClient.UpdateResourceGroupProcessMode(ctx, owner, repo, g.Key, "newest_first"); err != nil {
			printer.StepWarn(fmt.Sprintf("Failed to set resource group %q to newest_first: %v", g.Key, err))
			continue
		}
		healed++
	}
	printer.StepDone(fmt.Sprintf("Healed %d resource group(s)", healed))
}

// cleanupGitLabBotTokenSecret deletes the bot token Secret Manager secret
// and is a best-effort operation — errors are logged but not returned.
// This handles the GCP side of cleanup; the GitLab side (CI/CD variables,
// PAT revocation) is handled by the main uninstall path and
// cleanupGitLabBotToken.
//
// Tries both the current naming scheme (_dot_ for dots) and the legacy
// scheme (dots mapped to hyphens by the sanitizer) to handle secrets
// created before the _dot_ mapping was introduced.
func cleanupGitLabBotTokenSecret(ctx context.Context, gcpClient gcf.GCFClient, printer *ui.Printer, projectID, owner, repo string) {
	secretID, err := botTokenSecretID(owner, repo)
	if err != nil {
		printer.StepWarn(fmt.Sprintf("Failed to derive secret ID for %s/%s: %v", owner, repo, err))
		return
	}
	if err := gcpClient.DeleteSecret(ctx, projectID, secretID); err != nil {
		printer.StepWarn(fmt.Sprintf("Failed to delete Secret Manager secret %s: %v", secretID, err))
	} else {
		printer.StepDone(fmt.Sprintf("Deleted Secret Manager secret %s", secretID))
	}

	legacyID := legacyBotTokenSecretID(owner, repo)
	if legacyID != secretID {
		if err := gcpClient.DeleteSecret(ctx, projectID, legacyID); err == nil {
			printer.StepDone(fmt.Sprintf("Deleted legacy Secret Manager secret %s", legacyID))
		}
	}
}

// projectIDFromSAEmail extracts the GCP project ID from a service account
// email in the standard format: name@{projectID}.iam.gserviceaccount.com.
// Returns an empty string if the email doesn't match the expected format.
func projectIDFromSAEmail(email string) string {
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return ""
	}
	const suffix = ".iam.gserviceaccount.com"
	if !strings.HasSuffix(parts[1], suffix) {
		return ""
	}
	return strings.TrimSuffix(parts[1], suffix)
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
