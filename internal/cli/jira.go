package cli

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/jira"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

var jiraProjectKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,9}$`)

func newJiraCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jira",
		Short: "Manage Jira project integration",
		Long:  "Commands for connecting Jira projects to fullsend agent pipelines.",
	}
	cmd.AddCommand(newJiraEnrollCmd())
	return cmd
}

type jiraEnrollConfig struct {
	jiraHost            string
	projectKey          string
	jiraEmail           string
	jiraAPIToken        string
	githubPAT           string
	linkedRepos         string
	skipAutomationRules bool
	skipWorkflows       bool
	dryRun              bool
}

func newJiraEnrollCmd() *cobra.Command {
	var cfg jiraEnrollConfig

	cmd := &cobra.Command{
		Use:   "enroll <owner/repo>",
		Short: "Enroll a Jira project for fullsend triage",
		Long: `Connects a Jira Cloud project to a GitHub repository's fullsend pipeline.

This command:
  1. Creates or updates .jira.yml in the GitHub repo
  2. Creates Jira Automation rules for triage dispatch
  3. Commits dispatch and triage workflow files
  4. Sets Jira credential secrets on the GitHub repo

Prerequisites:
  - The GitHub repo must have fullsend installed (per-repo mode)
  - Jira agent customization files must exist in .fullsend/customized/
  - A Jira API token (https://id.atlassian.com/manage-profile/security/api-tokens)
  - A GitHub fine-grained PAT with Contents: Read and write on the target repo`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, repo, isRepo := parseTarget(args[0])
			if !isRepo {
				return fmt.Errorf("argument must be owner/repo (e.g., myorg/myrepo)")
			}
			return runJiraEnroll(cmd.Context(), owner, repo, cfg)
		},
	}

	cmd.Flags().StringVar(&cfg.jiraHost, "jira-host", "", "Jira Cloud hostname (e.g., myorg.atlassian.net)")
	cmd.Flags().StringVar(&cfg.projectKey, "project-key", "", "Jira project key (e.g., MYPROJ)")
	cmd.Flags().StringVar(&cfg.jiraEmail, "jira-email", "", "Jira user email (env: JIRA_EMAIL)")
	cmd.Flags().StringVar(&cfg.jiraAPIToken, "jira-api-token", "", "Jira API token (env: JIRA_API_TOKEN)")
	cmd.Flags().StringVar(&cfg.githubPAT, "github-pat", "", "GitHub PAT for Jira webhook auth (env: FULLSEND_JIRA_GITHUB_PAT)")
	cmd.Flags().StringVar(&cfg.linkedRepos, "linked-repos", "", "comma-separated org/repo values for cross-repo search")
	cmd.Flags().BoolVar(&cfg.skipAutomationRules, "skip-automation-rules", false, "skip creating Jira Automation rules")
	cmd.Flags().BoolVar(&cfg.skipWorkflows, "skip-workflows", false, "skip committing GitHub workflow files (use when workflows already exist in the repo)")
	cmd.Flags().BoolVar(&cfg.dryRun, "dry-run", false, "preview changes without executing")

	_ = cmd.MarkFlagRequired("jira-host")
	_ = cmd.MarkFlagRequired("project-key")

	return cmd
}

func runJiraEnroll(ctx context.Context, owner, repo string, cfg jiraEnrollConfig) error {
	printer := ui.New(os.Stdout)
	printer.Banner(version)

	// --- Validate inputs ---

	if !jiraProjectKeyPattern.MatchString(cfg.projectKey) {
		return fmt.Errorf("invalid project key %q: must match %s", cfg.projectKey, jiraProjectKeyPattern.String())
	}
	if cfg.jiraHost == "" {
		return fmt.Errorf("--jira-host is required")
	}
	if strings.HasPrefix(cfg.jiraHost, "https://") || strings.HasPrefix(cfg.jiraHost, "http://") {
		return fmt.Errorf("--jira-host should be a hostname (e.g., myorg.atlassian.net), not a URL")
	}

	// --- Resolve credentials ---

	jiraEmail := resolveEnvOrFlag(cfg.jiraEmail, "JIRA_EMAIL")
	if jiraEmail == "" {
		return fmt.Errorf("Jira email is required: use --jira-email or set JIRA_EMAIL")
	}

	jiraAPIToken := resolveEnvOrFlag(cfg.jiraAPIToken, "JIRA_API_TOKEN")
	if jiraAPIToken == "" {
		return fmt.Errorf("Jira API token is required: use --jira-api-token or set JIRA_API_TOKEN")
	}

	githubPAT := resolveEnvOrFlag(cfg.githubPAT, "FULLSEND_JIRA_GITHUB_PAT")
	if githubPAT == "" && !cfg.skipAutomationRules {
		return fmt.Errorf("GitHub PAT is required for automation rules: use --github-pat or set FULLSEND_JIRA_GITHUB_PAT")
	}

	ghToken, err := resolveToken()
	if err != nil {
		return fmt.Errorf("resolving GitHub token: %w", err)
	}
	ghClient := gh.New(ghToken)

	printer.Header(fmt.Sprintf("Enrolling Jira project %s on %s → %s/%s", cfg.projectKey, cfg.jiraHost, owner, repo))

	if cfg.dryRun {
		printer.StepInfo("DRY RUN — no changes will be made")
	}

	// --- Validate Jira project ---

	printer.StepStart("Validating Jira project")
	jiraClient := jira.NewClient(jiraEmail, jiraAPIToken)

	var projectID string
	if !cfg.dryRun {
		projectInfo, err := jiraClient.ValidateProject(ctx, cfg.jiraHost, cfg.projectKey)
		if err != nil {
			printer.StepFail("Jira project validation failed")
			return err
		}
		projectID = projectInfo.ID
	} else {
		projectID = "<dry-run>"
	}
	printer.StepDone(fmt.Sprintf("Jira project %s is accessible (ID: %s)", cfg.projectKey, projectID))

	// --- Get Cloud ID and Account ID ---

	var cloudID, accountID string
	if !cfg.skipAutomationRules {
		printer.StepStart("Fetching Jira Cloud ID and account info")
		if !cfg.dryRun {
			cloudID, err = jiraClient.GetCloudID(ctx, cfg.jiraHost)
			if err != nil {
				printer.StepFail("Failed to get Cloud ID")
				return err
			}
			accountID, err = jiraClient.GetCurrentUser(ctx, cfg.jiraHost)
			if err != nil {
				printer.StepFail("Failed to get account ID")
				return err
			}
		} else {
			cloudID = "<dry-run>"
			accountID = "<dry-run>"
		}
		printer.StepDone(fmt.Sprintf("Cloud ID: %s", cloudID))
	}

	// --- Create/update .jira.yml ---

	printer.StepStart("Updating .jira.yml")

	var linkedRepos []string
	if cfg.linkedRepos != "" {
		linkedRepos = strings.Split(cfg.linkedRepos, ",")
		for i := range linkedRepos {
			linkedRepos[i] = strings.TrimSpace(linkedRepos[i])
		}
	}

	jiraCfg, err := loadOrCreateJiraConfig(ctx, ghClient, owner, repo)
	if err != nil {
		printer.StepFail("Failed to load .jira.yml")
		return err
	}

	project := jira.JiraProjectConfig{
		ProjectKey:        cfg.projectKey,
		Host:              cfg.jiraHost,
		LinkedGitHubRepos: linkedRepos,
	}

	added := jiraCfg.AddProject(project)
	if !added {
		printer.StepDone(fmt.Sprintf("Project %s already enrolled in .jira.yml", cfg.projectKey))
	} else if cfg.dryRun {
		printer.StepDone(fmt.Sprintf("Would add %s to .jira.yml", cfg.projectKey))
	} else {
		data, err := jiraCfg.Marshal()
		if err != nil {
			return fmt.Errorf("marshaling .jira.yml: %w", err)
		}
		if err := ghClient.CreateOrUpdateFile(ctx, owner, repo, ".jira.yml",
			"chore: enroll Jira project "+cfg.projectKey, data); err != nil {
			printer.StepFail("Failed to write .jira.yml")
			return fmt.Errorf("writing .jira.yml: %w", err)
		}
		printer.StepDone(fmt.Sprintf("Added %s to .jira.yml", cfg.projectKey))
	}

	// --- Create automation rules ---

	rulesCreated := true
	if !cfg.skipAutomationRules {
		rc := jira.RuleContext{
			Owner:     owner,
			Repo:      repo,
			GitHubPAT: githubPAT,
			AccountID: accountID,
			CloudID:   cloudID,
			ProjectID: projectID,
		}

		rules := []jira.CreateRuleRequest{
			jira.AutoTriageRule(rc),
			jira.SlashCommandRule(rc, "/fs-triage"),
		}

		for _, rule := range rules {
			name := rule.Rule.Name
			printer.StepStart(fmt.Sprintf("Creating automation rule: %s", name))

			if cfg.dryRun {
				printer.StepDone(fmt.Sprintf("Would create rule: %s", name))
				continue
			}

			exists, err := jiraClient.RuleExistsByName(ctx, cloudID, name)
			if err != nil {
				printer.StepInfo("Could not check existing rules (will attempt creation)")
			} else if exists {
				printer.StepDone(fmt.Sprintf("Rule already exists: %s", name))
				continue
			}

			uuid, err := jiraClient.CreateAutomationRule(ctx, cloudID, rule)
			if err != nil {
				if strings.Contains(err.Error(), "HTTP 403") {
					rulesCreated = false
					break
				}
				printer.StepFail(fmt.Sprintf("Failed to create rule: %s", name))
				return fmt.Errorf("creating automation rule %q: %w", name, err)
			}
			printer.StepDone(fmt.Sprintf("Created rule: %s (uuid: %s)", name, uuid))
		}

		if !rulesCreated && !cfg.dryRun {
			printer.StepWarn("Automation API requires Jira site admin (https://jira.atlassian.com/browse/AUTO-2120)")
			printer.StepInfo("Create these rules manually in the Jira UI:")
			printer.StepInfo("Navigate to: Space settings > Automation > Create flow")
			printer.Blank()
			printer.Raw(jira.ManualRuleInstructions(owner, repo))
			printer.Blank()
		}
	}

	// --- Commit workflow files ---

	if !cfg.skipWorkflows {
		printer.StepStart("Committing workflow files")

		dispatchYML, err := scaffold.JiraDispatchTemplate()
		if err != nil {
			return fmt.Errorf("reading jira-dispatch.yml template: %w", err)
		}
		triageYML, err := scaffold.JiraTriageTemplate()
		if err != nil {
			return fmt.Errorf("reading jira-triage.yml template: %w", err)
		}

		files := []forge.TreeFile{
			{Path: ".github/workflows/jira-dispatch.yml", Content: dispatchYML, Mode: "100644"},
			{Path: ".github/workflows/jira-triage.yml", Content: triageYML, Mode: "100644"},
		}

		if cfg.dryRun {
			for _, f := range files {
				printer.StepInfo(fmt.Sprintf("  Would write %s", f.Path))
			}
			printer.StepDone("Would commit workflow files")
		} else {
			committed, err := ghClient.CommitFiles(ctx, owner, repo,
				"chore: add Jira triage workflows for "+cfg.projectKey, files)
			if err != nil {
				printer.StepFail("Failed to commit workflow files")
				return fmt.Errorf("committing workflow files: %w", err)
			}
			if committed {
				printer.StepDone("Committed workflow files")
			} else {
				printer.StepDone("Workflow files already up to date")
			}
		}
	}

	// --- Set GitHub secrets ---

	printer.StepStart("Setting GitHub secrets")
	secrets := map[string]string{
		"JIRA_HOST":      cfg.jiraHost,
		"JIRA_EMAIL":     jiraEmail,
		"JIRA_API_TOKEN": jiraAPIToken,
	}

	for name, value := range secrets {
		if cfg.dryRun {
			printer.StepInfo(fmt.Sprintf("  Would set secret %s", name))
		} else {
			if err := ghClient.CreateRepoSecret(ctx, owner, repo, name, value); err != nil {
				printer.StepFail(fmt.Sprintf("Failed to set secret %s", name))
				return fmt.Errorf("setting secret %s: %w", name, err)
			}
		}
	}
	printer.StepDone("GitHub secrets configured")

	// --- Summary ---

	summary := []string{
		fmt.Sprintf("Jira project: %s (%s)", cfg.projectKey, cfg.jiraHost),
		fmt.Sprintf("GitHub repo: %s/%s", owner, repo),
	}
	if !cfg.skipAutomationRules {
		if rulesCreated {
			summary = append(summary, "Automation rules: created via API")
		} else {
			summary = append(summary, "Automation rules: manual setup required (see instructions above)")
		}
	}
	if !cfg.skipWorkflows {
		summary = append(summary, "Workflows: jira-dispatch.yml, jira-triage.yml")
	}
	summary = append(summary, "Secrets: JIRA_HOST, JIRA_EMAIL, JIRA_API_TOKEN")

	if cfg.dryRun {
		printer.Summary("Dry run complete", summary)
	} else {
		printer.Summary("Jira enrollment complete", summary)
	}

	return nil
}

// resolveEnvOrFlag returns the flag value if set, otherwise falls back
// to the environment variable.
func resolveEnvOrFlag(flagValue, envVar string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv(envVar)
}

// loadOrCreateJiraConfig reads .jira.yml from the repo, or creates a
// new empty config if the file does not exist.
func loadOrCreateJiraConfig(ctx context.Context, client forge.Client, owner, repo string) (*jira.JiraConfig, error) {
	data, err := client.GetFileContent(ctx, owner, repo, ".jira.yml")
	if err != nil {
		// File does not exist — start with empty config.
		return jira.NewJiraConfig(), nil
	}
	return jira.ParseJiraConfig(data)
}
