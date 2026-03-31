// Package install implements the fullsend installation workflow.
//
// The install process:
//  1. Lists existing repositories in the GitHub organization
//  2. Creates a GitHub App with minimum required permissions
//  3. Creates the .fullsend configuration repository with safe defaults
//  4. Generates config.yaml with all repos listed (disabled by default)
//  5. Creates a reusable GitHub Actions workflow in .fullsend
//  6. For each enabled repo, creates a PR connecting it to the agent dispatch
//
// The workflow is designed to be safe: nothing gets automatically merged,
// auto_merge defaults to false, and enabled repos receive PRs that must
// be reviewed before taking effect.
package install

import (
	"context"
	"fmt"
	"sort"

	"github.com/fullsend-ai/fullsend/internal/config"
	gh "github.com/fullsend-ai/fullsend/internal/github"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Options holds the parameters for an install operation.
type Options struct {
	// Org is the GitHub organization to install fullsend into.
	Org string

	// Repos is the list of repos to enable during installation.
	// If empty, all repos are listed but none are enabled.
	Repos []string

	// Agents is the list of agent roles to enable.
	// If empty, the default set (triage, implementation, review) is used.
	Agents []string
}

// Result holds the outcome of an install operation.
type Result struct {
	Config          *config.OrgConfig
	AppConfig       *gh.AppConfig
	PRs             map[string]*gh.PullRequest
	DefaultBranches map[string]string
	ConfigRepo      string
	OrgRepos        []string
}

// Installer performs the fullsend installation workflow.
type Installer struct {
	client  gh.Client
	printer *ui.Printer
}

// New creates an Installer with the given GitHub client and UI printer.
func New(client gh.Client, printer *ui.Printer) *Installer {
	return &Installer{
		client:  client,
		printer: printer,
	}
}

// Run executes the full installation workflow.
func (inst *Installer) Run(ctx context.Context, opts Options) (*Result, error) {
	if err := validateOrgName(opts.Org); err != nil {
		return nil, err
	}

	inst.printer.Banner()
	inst.printer.Header(fmt.Sprintf("Installing fullsend to %s", opts.Org))
	inst.printer.Blank()

	result := &Result{
		ConfigRepo: ".fullsend",
		PRs:        make(map[string]*gh.PullRequest),
	}

	// Step 1: Discover org repos
	repos, defaultBranches, err := inst.discoverRepos(ctx, opts.Org)
	if err != nil {
		inst.printer.StepFail("Failed to list organization repositories")
		return nil, fmt.Errorf("listing org repos: %w", err)
	}
	result.OrgRepos = repos
	result.DefaultBranches = defaultBranches

	// Validate --repo values against discovered repos
	if len(opts.Repos) > 0 {
		repoSet := make(map[string]bool, len(repos))
		for _, r := range repos {
			repoSet[r] = true
		}
		for _, r := range opts.Repos {
			if !repoSet[r] {
				inst.printer.StepWarn(fmt.Sprintf("Repo %q not found in organization — skipping", r))
			}
		}
	}

	// Step 2: Configure the GitHub App
	result.AppConfig = inst.configureApp(opts.Org)

	// Step 3: Generate config
	result.Config = inst.generateConfig(repos, opts)

	if err := result.Config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// Step 4: Create .fullsend repo
	if err := inst.createConfigRepo(ctx, opts.Org, result); err != nil {
		return nil, err
	}

	// Step 5: Create PRs for enabled repos
	if err := inst.createEnrollmentPRs(ctx, opts.Org, result); err != nil {
		return nil, err
	}

	// Print summary
	inst.printSummary(opts.Org, result)

	return result, nil
}

func (inst *Installer) discoverRepos(ctx context.Context, org string) ([]string, map[string]string, error) {
	inst.printer.StepStart("Discovering repositories...")

	allRepos, err := inst.client.ListOrgRepos(ctx, org)
	if err != nil {
		return nil, nil, err
	}

	var names []string
	defaultBranches := make(map[string]string)
	for _, r := range allRepos {
		// Skip the config repo itself if it already exists
		if r.Name == ".fullsend" {
			continue
		}
		names = append(names, r.Name)
		defaultBranches[r.Name] = r.DefaultBranch
	}
	sort.Strings(names)

	inst.printer.StepDone(fmt.Sprintf("Found %d repositories", len(names)))
	return names, defaultBranches, nil
}

func (inst *Installer) configureApp(org string) *gh.AppConfig {
	appConfig := gh.DefaultAppConfig(org)

	inst.printer.StepWarn("GitHub App creation requires manual setup")
	inst.printer.StepInfo(fmt.Sprintf("Create a GitHub App named %q at:", appConfig.Name))
	inst.printer.StepInfo(fmt.Sprintf("  https://github.com/organizations/%s/settings/apps/new", org))
	inst.printer.StepInfo(fmt.Sprintf("permissions: issues=%s, prs=%s, checks=%s, contents=%s",
		appConfig.Permissions.Issues,
		appConfig.Permissions.PullReqs,
		appConfig.Permissions.Checks,
		appConfig.Permissions.Contents))
	inst.printer.StepInfo("Store the App private key as a secret in the .fullsend repo after creation.")

	return appConfig
}

func (inst *Installer) generateConfig(repos []string, opts Options) *config.OrgConfig {
	cfg := config.NewOrgConfig(repos, opts.Repos, opts.Agents)

	enabledCount := len(cfg.EnabledRepos())
	inst.printer.StepDone(fmt.Sprintf("Configuration generated (%d/%d repos enabled)",
		enabledCount, len(repos)))

	return cfg
}

func (inst *Installer) createConfigRepo(ctx context.Context, org string, result *Result) error {
	inst.printer.StepStart("Creating .fullsend repository...")

	_, err := inst.client.CreateRepo(ctx, org, ".fullsend",
		"Fullsend agent configuration for "+org, true)
	if err != nil {
		inst.printer.StepFail("Failed to create .fullsend repository")
		return fmt.Errorf("creating .fullsend repo: %w", err)
	}

	// Write config.yaml
	configData, err := result.Config.Marshal()
	if err != nil {
		return fmt.Errorf("marshalling config: %w", err)
	}

	if err := inst.client.CreateFile(ctx, org, ".fullsend", "config.yaml",
		"Initialize fullsend configuration with safe defaults", configData); err != nil {
		inst.printer.StepFail("Failed to create config.yaml")
		return fmt.Errorf("creating config.yaml: %w", err)
	}

	// Write reusable workflow
	workflowContent := generateReusableWorkflow()
	if err := inst.client.CreateFile(ctx, org, ".fullsend", ".github/workflows/agent.yaml",
		"Add reusable agent dispatch workflow", []byte(workflowContent)); err != nil {
		inst.printer.StepFail("Failed to create reusable workflow")
		return fmt.Errorf("creating reusable workflow: %w", err)
	}

	// Write CODEOWNERS
	codeownersContent := generateCodeowners(org)
	if err := inst.client.CreateFile(ctx, org, ".fullsend", "CODEOWNERS",
		"Add CODEOWNERS to protect configuration", []byte(codeownersContent)); err != nil {
		inst.printer.StepFail("Failed to create CODEOWNERS")
		return fmt.Errorf("creating CODEOWNERS: %w", err)
	}

	inst.printer.StepDone("Created .fullsend repository with config and workflows")
	return nil
}

func (inst *Installer) createEnrollmentPRs(ctx context.Context, org string, result *Result) error {
	enabledRepos := result.Config.EnabledRepos()
	if len(enabledRepos) == 0 {
		inst.printer.StepInfo("No repos enabled — skip PR creation")
		inst.printer.StepInfo("Enable repos in .fullsend/config.yaml and re-run")
		return nil
	}

	sort.Strings(enabledRepos)
	inst.printer.Header("Creating enrollment PRs")
	inst.printer.Blank()

	for _, repo := range enabledRepos {
		defaultBranch := "main"
		if branch, ok := result.DefaultBranches[repo]; ok && branch != "" {
			defaultBranch = branch
		}
		pr, err := inst.enrollRepo(ctx, org, repo, defaultBranch)
		if err != nil {
			inst.printer.StepFail(fmt.Sprintf("Failed to create PR for %s: %v", repo, err))
			// Continue with other repos — don't fail the whole install
			continue
		}
		result.PRs[repo] = pr
		inst.printer.StepDone(fmt.Sprintf("PR created for %s", repo))
		inst.printer.StepInfo(pr.HTMLURL)
	}

	return nil
}

func (inst *Installer) enrollRepo(ctx context.Context, org, repo, defaultBranch string) (*gh.PullRequest, error) {
	branchName := "fullsend/enroll"

	if err := inst.client.CreateBranch(ctx, org, repo, branchName); err != nil {
		return nil, fmt.Errorf("creating branch: %w", err)
	}

	workflowContent := generateStubWorkflow(org)
	if err := inst.client.CreateFileOnBranch(ctx, org, repo, branchName,
		".github/workflows/fullsend.yaml",
		"Add fullsend agent dispatch workflow",
		[]byte(workflowContent)); err != nil {
		return nil, fmt.Errorf("creating workflow file: %w", err)
	}

	pr, err := inst.client.CreatePullRequest(ctx, org, repo,
		"Connect to fullsend agent pipeline",
		generatePRBody(org),
		branchName,
		defaultBranch)
	if err != nil {
		return nil, fmt.Errorf("creating pull request: %w", err)
	}

	return pr, nil
}

func (inst *Installer) printSummary(org string, result *Result) {
	items := []string{
		fmt.Sprintf("Config repo: %s/.fullsend", org),
		fmt.Sprintf("Repos discovered: %d", len(result.OrgRepos)),
	}

	if len(result.PRs) > 0 {
		items = append(items, fmt.Sprintf("Enrollment PRs: %d", len(result.PRs)))
	}

	items = append(items, "Auto-merge: disabled (safe default)")

	inst.printer.Summary("Installation complete", items)

	if len(result.PRs) > 0 {
		inst.printer.Header("Pull requests to review")
		inst.printer.Blank()
		for repo, pr := range result.PRs {
			inst.printer.PRLink(repo, pr.HTMLURL)
		}
		inst.printer.Blank()
		inst.printer.StepInfo("Review and merge these PRs to connect repos to the agent pipeline.")
		inst.printer.StepInfo("Nothing changes until a PR is merged.")
		inst.printer.Blank()
	}

	inst.printer.Header("Remaining manual steps")
	inst.printer.Blank()
	inst.printer.StepInfo(fmt.Sprintf("1. Create a GitHub App named %q", result.AppConfig.Name))
	inst.printer.StepInfo(fmt.Sprintf("   https://github.com/organizations/%s/settings/apps/new", org))
	inst.printer.StepInfo("2. Install the App to the organization")
	inst.printer.StepInfo(fmt.Sprintf("3. Store the App private key as a secret in %s/.fullsend", org))
	inst.printer.Blank()
}

// validateOrgName checks that the organization name is valid for GitHub.
func validateOrgName(org string) error {
	if org == "" {
		return fmt.Errorf("organization name cannot be empty")
	}
	for _, c := range org {
		isValid := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-'
		if !isValid {
			return fmt.Errorf("invalid organization name %q: only alphanumeric characters and hyphens allowed", org)
		}
	}
	if org[0] == '-' || org[len(org)-1] == '-' {
		return fmt.Errorf("invalid organization name %q: cannot start or end with a hyphen", org)
	}
	return nil
}

// generateReusableWorkflow produces the reusable workflow YAML for .fullsend.
func generateReusableWorkflow() string {
	return `# Reusable workflow for fullsend agent dispatch.
# Called by enrolled repos to run agents in response to GitHub events.
#
# This workflow is the execution entry point for all fullsend agents.
# It receives events from enrolled repos and dispatches to the appropriate
# agent based on the event type and organization configuration.
name: Agent Dispatch

on:
  workflow_call:
    inputs:
      event_type:
        description: "GitHub event type (issues, issue_comment, pull_request, etc.)"
        required: true
        type: string
      event_payload:
        description: "JSON-encoded event payload"
        required: true
        type: string
    secrets:
      APP_PRIVATE_KEY:
        description: "GitHub App private key for agent authentication"
        required: true

permissions:
  contents: read
  issues: write
  pull-requests: write
  checks: read

jobs:
  dispatch:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout .fullsend config
        uses: actions/checkout@v4
        with:
          repository: ${{ github.repository_owner }}/.fullsend

      - name: Run fullsend entrypoint
        env:
          EVENT_TYPE: ${{ inputs.event_type }}
          EVENT_PAYLOAD: ${{ inputs.event_payload }}
        run: |
          echo "Event: $EVENT_TYPE"
          echo "Dispatching to agent..."
          # fullsend entrypoint --scm github - <<< "$EVENT_PAYLOAD"
          echo "Agent dispatch not yet wired up (see https://github.com/fullsend-ai/fullsend/issues/125)"
`
}

// generateStubWorkflow produces the workflow YAML that enrolled repos add.
func generateStubWorkflow(org string) string {
	return fmt.Sprintf(`# fullsend agent dispatch — connects this repo to the fullsend pipeline.
#
# This workflow triggers on GitHub events and calls the reusable workflow
# in the .fullsend repo to dispatch the appropriate agent.
#
# Review the triggers below and adjust as needed for your repo.
# See https://github.com/fullsend-ai/fullsend for documentation.
name: fullsend

on:
  issues:
    types: [opened, edited, labeled]
  issue_comment:
    types: [created]
  pull_request:
    types: [opened, synchronize, labeled]
  pull_request_review:
    types: [submitted]

permissions:
  contents: read
  issues: write
  pull-requests: write
  checks: read

jobs:
  agent:
    uses: %s/.fullsend/.github/workflows/agent.yaml@main
    with:
      event_type: ${{ github.event_name }}
      event_payload: ${{ toJSON(github.event) }}
    secrets:
      APP_PRIVATE_KEY: ${{ secrets.FULLSEND_APP_PRIVATE_KEY }}
`, org)
}

// generatePRBody produces the PR description for enrollment PRs.
func generatePRBody(org string) string {
	return fmt.Sprintf(`## Connect to fullsend agent pipeline

This PR adds a GitHub Actions workflow that connects this repository to the
fullsend autonomous development pipeline managed in [%s/.fullsend](https://github.com/%s/.fullsend).

### What this does

- Triggers on issue, PR, and comment events
- Calls the reusable workflow in .fullsend to dispatch the appropriate agent
- Agents handle triage, implementation, and review based on org configuration

### What this does NOT do

- No code is changed in this repository
- No automatic merging — auto_merge is disabled by default
- No protected branches are affected — branch protection rules are respected
- Agents cannot force-push or modify protected branches

### Next steps

1. Review the workflow file added by this PR
2. Ensure branch protection is configured for your default branch
3. Merge when ready to enable the agent pipeline

---
*Created by [fullsend](https://github.com/fullsend-ai/fullsend) installation*
`, org, org)
}

// generateCodeowners produces CODEOWNERS content for the .fullsend repo.
func generateCodeowners(org string) string {
	return fmt.Sprintf(`# CODEOWNERS for .fullsend configuration repository
#
# All configuration changes require human review.
# Agents cannot modify their own guardrails.
#
# Adjust the team/users below to match your organization.
* @%s/admin
`, org)
}
