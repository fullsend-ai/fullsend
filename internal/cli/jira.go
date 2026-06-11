package cli

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// jiraProjectKeyPattern validates Jira project keys.
// Jira project keys start with an uppercase letter followed by up to 9
// uppercase letters, digits, or underscores.
var jiraProjectKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,9}$`)

func newJiraCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jira",
		Short: "Manage Jira project enrollment in fullsend",
		Long:  "Commands for enrolling and unenrolling Jira projects in fullsend.",
	}
	cmd.AddCommand(newJiraEnrollCmd())
	cmd.AddCommand(newJiraUnenrollCmd())
	return cmd
}

func newJiraEnrollCmd() *cobra.Command {
	var org string
	var host string
	var linkedRepos []string

	cmd := &cobra.Command{
		Use:   "enroll <PROJECT-KEY>",
		Short: "Enroll a Jira project in fullsend",
		Long:  "Enrolls a Jira project in fullsend by updating config.yaml, storing Jira credentials as GitHub secrets, and committing the jira-dispatch workflow.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectKey := args[0]

			if !jiraProjectKeyPattern.MatchString(projectKey) {
				return fmt.Errorf("invalid Jira project key %q: must match ^[A-Z][A-Z0-9_]{0,9}$", projectKey)
			}

			if len(host) < 3 {
				return fmt.Errorf("--host must be a valid hostname (e.g. yourorg.atlassian.net), got %q", host)
			}

			if err := validateOrgName(org); err != nil {
				return fmt.Errorf("--org: %w", err)
			}

			jiraEmail := os.Getenv("JIRA_EMAIL")
			jiraToken := os.Getenv("JIRA_API_TOKEN")
			if jiraEmail == "" || jiraToken == "" {
				return fmt.Errorf(
					"Jira credentials are required.\n" +
						"Set the following environment variables before running this command:\n" +
						"  export JIRA_EMAIL=you@example.com\n" +
						"  export JIRA_API_TOKEN=<your-jira-api-token>\n" +
						"You can generate an API token at: https://id.atlassian.com/manage-profile/security/api-tokens",
				)
			}

			token, err := resolveToken()
			if err != nil {
				return err
			}

			client := gh.New(token)
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			printer.Banner(Version())
			printer.Blank()
			printer.Header("Enrolling Jira project " + projectKey + " for " + org)
			printer.Blank()

			cfg, err := loadRepoConfig(ctx, client, printer, org)
			if err != nil {
				return err
			}

			for _, jp := range cfg.JiraProjects {
				if jp.ProjectKey == projectKey {
					printer.StepInfo(fmt.Sprintf("Jira project %s is already enrolled in fullsend", projectKey))
					return nil
				}
			}

			printer.StepStart("Validating Jira credentials")
			apiURL := fmt.Sprintf("https://%s/rest/api/3/project/%s", host, projectKey)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
			if err != nil {
				printer.StepFail("Failed to build Jira API request")
				return fmt.Errorf("building Jira API request: %w", err)
			}
			creds := base64.StdEncoding.EncodeToString([]byte(jiraEmail + ":" + jiraToken))
			req.Header.Set("Authorization", "Basic "+creds)
			req.Header.Set("Accept", "application/json")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				printer.StepFail("Failed to reach Jira API")
				return fmt.Errorf("reaching Jira at %s: %w", host, err)
			}
			resp.Body.Close()

			switch resp.StatusCode {
			case http.StatusOK:
				printer.StepDone("Jira credentials validated")
			case http.StatusUnauthorized, http.StatusForbidden:
				printer.StepFail("Invalid Jira credentials")
				return fmt.Errorf("invalid Jira credentials (HTTP %d): check JIRA_EMAIL and JIRA_API_TOKEN", resp.StatusCode)
			case http.StatusNotFound:
				printer.StepFail("Jira project not found")
				return fmt.Errorf("project %s not found in Jira at %s (HTTP 404)", projectKey, host)
			default:
				printer.StepFail(fmt.Sprintf("Unexpected Jira API response: %d", resp.StatusCode))
				return fmt.Errorf("unexpected Jira API response (HTTP %d) for project %s at %s", resp.StatusCode, projectKey, host)
			}

			cfg.JiraProjects = append(cfg.JiraProjects, config.JiraProjectConfig{
				ProjectKey:        projectKey,
				Host:              host,
				LinkedGitHubRepos: linkedRepos,
			})

			commitMsg := fmt.Sprintf("feat: enroll Jira project %s in fullsend", projectKey)
			printer.StepStart("Saving config.yaml")
			if _, err := saveRepoConfig(ctx, client, printer, org, cfg, commitMsg); err != nil {
				return err
			}

			printer.StepStart("Committing jira-dispatch workflow")
			workflowContent, err := scaffold.FullsendRepoFile(".github/workflows/jira-dispatch.yml")
			if err != nil {
				printer.StepFail("Failed to load jira-dispatch workflow from scaffold")
				return fmt.Errorf("loading jira-dispatch workflow: %w", err)
			}
			if err := client.CreateOrUpdateFile(ctx, org, forge.ConfigRepoName, ".github/workflows/jira-dispatch.yml", "feat: add jira-dispatch workflow", workflowContent); err != nil {
				printer.StepFail("Failed to commit jira-dispatch workflow")
				return fmt.Errorf("committing jira-dispatch workflow: %w", err)
			}
			printer.StepDone("jira-dispatch workflow committed")

			printer.StepStart("Storing Jira credentials as GitHub secrets")
			if err := client.CreateRepoSecret(ctx, org, forge.ConfigRepoName, "JIRA_EMAIL", jiraEmail); err != nil {
				printer.StepFail("Failed to store JIRA_EMAIL secret")
				return fmt.Errorf("storing JIRA_EMAIL secret: %w", err)
			}
			if err := client.CreateRepoSecret(ctx, org, forge.ConfigRepoName, "JIRA_API_TOKEN", jiraToken); err != nil {
				printer.StepFail("Failed to store JIRA_API_TOKEN secret")
				return fmt.Errorf("storing JIRA_API_TOKEN secret: %w", err)
			}
			printer.StepDone("Jira credentials stored")

			printer.Blank()
			printer.Summary("Jira project enrolled", []string{
				fmt.Sprintf("Organization: %s", org),
				fmt.Sprintf("Project key: %s", projectKey),
				fmt.Sprintf("Host: %s", host),
				"Next step: configure the Jira webhook to send events to fullsend",
				"Runbook: https://github.com/fullsend-ai/fullsend/blob/main/docs/runbooks/jira-automation-webhook.md",
			})

			return nil
		},
	}

	cmd.Flags().StringVar(&org, "org", "", "GitHub organization name (required)")
	cmd.Flags().StringVar(&host, "host", "", "Jira host (e.g. yourorg.atlassian.net, without https://)")
	cmd.Flags().StringSliceVar(&linkedRepos, "linked-repos", nil, "GitHub repositories linked to this Jira project (comma-separated)")
	_ = cmd.MarkFlagRequired("org")
	_ = cmd.MarkFlagRequired("host")

	return cmd
}

func newJiraUnenrollCmd() *cobra.Command {
	var org string

	cmd := &cobra.Command{
		Use:   "unenroll <PROJECT-KEY>",
		Short: "Unenroll a Jira project from fullsend",
		Long:  "Removes a Jira project from fullsend enrollment by updating config.yaml in the .fullsend repository.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectKey := args[0]

			if !jiraProjectKeyPattern.MatchString(projectKey) {
				return fmt.Errorf("invalid Jira project key %q: must match ^[A-Z][A-Z0-9_]{0,9}$", projectKey)
			}

			if err := validateOrgName(org); err != nil {
				return fmt.Errorf("--org: %w", err)
			}

			token, err := resolveToken()
			if err != nil {
				return err
			}

			client := gh.New(token)
			printer := ui.New(os.Stdout)
			ctx := cmd.Context()

			printer.Banner(Version())
			printer.Blank()
			printer.Header("Unenrolling Jira project " + projectKey + " from " + org)
			printer.Blank()

			cfg, err := loadRepoConfig(ctx, client, printer, org)
			if err != nil {
				return err
			}

			found := false
			var updated []config.JiraProjectConfig
			for _, jp := range cfg.JiraProjects {
				if jp.ProjectKey == projectKey {
					found = true
					continue
				}
				updated = append(updated, jp)
			}

			if !found {
				printer.StepInfo(fmt.Sprintf("Jira project %s is not enrolled in fullsend", projectKey))
				return nil
			}

			cfg.JiraProjects = updated

			if len(cfg.JiraProjects) == 0 {
				printer.StepWarn("No Jira projects remain enrolled. The JIRA_EMAIL and JIRA_API_TOKEN secrets in .fullsend can be deleted manually if no longer needed.")
			}

			commitMsg := fmt.Sprintf("chore: unenroll Jira project %s from fullsend", projectKey)
			if _, err := saveRepoConfig(ctx, client, printer, org, cfg, commitMsg); err != nil {
				return err
			}

			printer.Blank()
			printer.Summary("Jira project unenrolled", []string{
				fmt.Sprintf("Organization: %s", org),
				fmt.Sprintf("Project key: %s", projectKey),
				fmt.Sprintf("Remaining Jira projects: %d", len(cfg.JiraProjects)),
			})

			return nil
		},
	}

	cmd.Flags().StringVar(&org, "org", "", "GitHub organization name (required)")
	_ = cmd.MarkFlagRequired("org")

	return cmd
}
