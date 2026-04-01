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
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// AgentCredentials holds an agent's config entry plus its private key.
type AgentCredentials struct {
	config.AgentEntry

	// PEM is the private key for this agent's GitHub App.
	PEM string
}

// Options holds the parameters for an install operation.
type Options struct {
	// Org is the GitHub organization to install fullsend into.
	Org string

	// Agents holds the credentials for each agent app created during setup.
	Agents []AgentCredentials

	// Roles is the list of agent roles to enable.
	// If empty, the default set (triage, coder, review) is used.
	Roles []string

	// Repos is the list of repos to enable during installation.
	// If empty, all repos are listed but none are enabled.
	Repos []string
}

// Result holds the outcome of an install operation.
type Result struct {
	Config          *config.OrgConfig
	Proposals       map[string]*forge.ChangeProposal
	DefaultBranches map[string]string
	ConfigRepo      string
	OrgRepos        []string
	SecretsStored   int
}

// Installer performs the fullsend installation workflow.
type Installer struct {
	client  forge.Client
	printer *ui.Printer
}

// New creates an Installer with the given forge client and UI printer.
func New(client forge.Client, printer *ui.Printer) *Installer {
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
		Proposals:  make(map[string]*forge.ChangeProposal),
	}

	// Step 1: Discover org repos
	discovered, err := inst.discoverRepos(ctx, opts.Org)
	if err != nil {
		inst.printer.StepFail("Failed to list organization repositories")
		return nil, fmt.Errorf("listing org repos: %w", err)
	}
	result.OrgRepos = discovered.Names
	result.DefaultBranches = discovered.DefaultBranches

	// Validate --repo values against discovered repos
	if len(opts.Repos) > 0 {
		repoSet := make(map[string]bool, len(discovered.Names))
		for _, r := range discovered.Names {
			repoSet[r] = true
		}
		for _, r := range opts.Repos {
			if !repoSet[r] {
				inst.printer.StepWarn(fmt.Sprintf("Repo %q not found in organization — skipping", r))
			}
		}
	}

	// Step 2: Log agent apps
	inst.logAgentApps(opts)

	// Step 3: Generate config
	result.Config = inst.generateConfig(discovered.Names, opts)

	if err := result.Config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// Step 4: Get authenticated user for CODEOWNERS
	username, userErr := inst.client.GetAuthenticatedUser(ctx)
	if userErr != nil {
		inst.printer.StepWarn(fmt.Sprintf("Could not get authenticated user: %v", userErr))
		username = opts.Org + "/admin" // fallback to org team
	}

	// Step 5: Create .fullsend repo if needed, then write config files
	if discovered.ConfigRepoExists {
		inst.printer.StepDone(".fullsend repository already exists")
	} else {
		makePrivate := discovered.HasPrivateRepos
		if makePrivate {
			inst.printer.StepStart("Creating .fullsend repository (private — your org has private repos)...")
		} else {
			inst.printer.StepStart("Creating .fullsend repository (public — your org has no private repos)...")
		}
		if _, repoErr := inst.client.CreateRepo(ctx, opts.Org, ".fullsend",
			"Fullsend agent configuration for "+opts.Org, makePrivate); repoErr != nil {
			inst.printer.StepFail("Failed to create .fullsend repository")
			return nil, fmt.Errorf("creating .fullsend repo: %w", repoErr)
		}
	}

	if err := inst.writeConfigFiles(ctx, opts.Org, result, username, !discovered.ConfigRepoExists); err != nil {
		return nil, err
	}

	// Step 6: Store agent PEM keys as repo secrets
	secretsStored, storeErr := inst.storeAgentSecrets(ctx, opts.Org, opts.Agents)
	if storeErr != nil {
		return nil, storeErr
	}
	result.SecretsStored = secretsStored

	// Step 7: Watch the repo onboarding workflow and report PRs
	if err := inst.watchOnboarding(ctx, opts.Org, result); err != nil {
		// Non-fatal — the workflow may not have triggered yet
		inst.printer.StepWarn(fmt.Sprintf("Could not watch onboarding: %v", err))
	}

	// Print summary
	inst.printSummary(opts.Org, result)

	return result, nil
}

// discoverResult holds the output of repo discovery.
type discoverResult struct {
	DefaultBranches  map[string]string
	Names            []string
	ConfigRepoExists bool
	HasPrivateRepos  bool
}

func (inst *Installer) discoverRepos(ctx context.Context, org string) (*discoverResult, error) {
	inst.printer.StepStart("Discovering repositories...")

	allRepos, err := inst.client.ListOrgRepos(ctx, org)
	if err != nil {
		return nil, err
	}

	result := &discoverResult{
		DefaultBranches: make(map[string]string),
	}

	for _, r := range allRepos {
		if r.Name == ".fullsend" {
			result.ConfigRepoExists = true
			continue
		}
		result.Names = append(result.Names, r.Name)
		result.DefaultBranches[r.Name] = r.DefaultBranch
		if r.Private {
			result.HasPrivateRepos = true
		}
	}
	sort.Strings(result.Names)

	inst.printer.StepDone(fmt.Sprintf("Found %d repositories", len(result.Names)))
	return result, nil
}

func (inst *Installer) logAgentApps(opts Options) {
	if len(opts.Agents) == 0 {
		inst.printer.StepInfo("No agent apps configured")
		return
	}

	inst.printer.StepDone(fmt.Sprintf("%d agent apps configured", len(opts.Agents)))
	for _, a := range opts.Agents {
		inst.printer.StepInfo(fmt.Sprintf("  %s: %s", a.Role, a.Name))
	}
}

func (inst *Installer) generateConfig(repos []string, opts Options) *config.OrgConfig {
	entries := make([]config.AgentEntry, len(opts.Agents))
	for i, a := range opts.Agents {
		entries[i] = a.AgentEntry
	}
	cfg := config.NewOrgConfig(repos, opts.Repos, opts.Roles, entries)

	enabledCount := len(cfg.EnabledRepos())
	inst.printer.StepDone(fmt.Sprintf("Configuration generated (%d/%d repos enabled)",
		enabledCount, len(repos)))

	return cfg
}

// storeAgentSecrets stores each agent's PEM key as a repo secret.
// Returns the number of secrets actually stored (0 if PEM keys are empty,
// which happens when reusing existing apps).
func (inst *Installer) storeAgentSecrets(ctx context.Context, org string, agents []AgentCredentials) (int, error) {
	// Check if any agents have PEM keys to store
	hasPEMs := false
	for _, a := range agents {
		if a.PEM != "" {
			hasPEMs = true
			break
		}
	}

	if !hasPEMs {
		if len(agents) > 0 {
			inst.printer.StepInfo("Agent apps were reused from a previous install — PEM keys are only")
			inst.printer.StepInfo("available at creation time. Secrets were not updated.")
			inst.printer.StepInfo("To rotate secrets, delete and recreate the apps.")
		}
		return 0, nil
	}

	inst.printer.Header("Storing agent secrets")
	inst.printer.Blank()

	stored := 0
	for _, agent := range agents {
		if agent.PEM == "" {
			continue
		}

		secretName := fmt.Sprintf("FULLSEND_%s_APP_PRIVATE_KEY", strings.ToUpper(agent.Role))
		inst.printer.StepStart(fmt.Sprintf("Storing %s...", secretName))

		if err := inst.client.CreateRepoSecret(ctx, org, ".fullsend", secretName, agent.PEM); err != nil {
			inst.printer.StepFail(fmt.Sprintf("Failed to store %s: %v", secretName, err))
			return stored, fmt.Errorf("storing secret %s: %w", secretName, err)
		}

		inst.printer.StepDone(fmt.Sprintf("Stored %s", secretName))
		stored++
	}

	return stored, nil
}

// writeConfigFiles writes config.yaml, the reusable workflow, and CODEOWNERS
// into the .fullsend repo. If newRepo is true, the first file creation is
// retried with backoff to wait for GitHub to finish initializing the branch.
func (inst *Installer) writeConfigFiles(ctx context.Context, org string, result *Result, codeowner string, newRepo bool) error {
	inst.printer.StepStart("Writing configuration files...")

	configData, err := result.Config.Marshal()
	if err != nil {
		return fmt.Errorf("marshalling config: %w", err)
	}

	// Required files — failure is fatal
	required := []struct {
		path, message string
		content       []byte
	}{
		{"config.yaml", "Initialize fullsend configuration", configData},
		{".github/workflows/agent.yaml", "Add reusable agent dispatch workflow", []byte(generateReusableWorkflow())},
		{".github/workflows/repo-onboard.yaml", "Add repo onboarding workflow", []byte(generateOnboardingWorkflow(org))},
	}

	for i, f := range required {
		isFirst := i == 0 && newRepo
		if writeErr := inst.writeFileWithRetry(ctx, org, ".fullsend", f.path,
			f.message, f.content, isFirst); writeErr != nil {
			// If a workflow file fails with 404, it's likely the workflow scope is missing
			if strings.HasPrefix(f.path, ".github/workflows/") && strings.Contains(writeErr.Error(), "404") {
				inst.printer.Blank()
				inst.printer.ErrorBox("Missing 'workflow' token scope",
					"Writing to .github/workflows/ requires the 'workflow' scope on your\n"+
						"  GitHub token. The gh CLI does not request this scope by default.\n\n"+
						"  Fix: run 'gh auth refresh -s workflow' to add the scope, then re-run install.")
			}
			return writeErr
		}
	}

	// CODEOWNERS is optional — failure is logged but not fatal
	if writeErr := inst.writeFileWithRetry(ctx, org, ".fullsend", "CODEOWNERS",
		"Add CODEOWNERS to protect configuration", []byte(generateCodeowners(codeowner)), false); writeErr != nil {
		inst.printer.StepWarn(fmt.Sprintf("Could not write CODEOWNERS: %v", writeErr))
		inst.printer.StepInfo("You can add this file manually later.")
	}

	inst.printer.StepDone("Configuration files written to .fullsend")
	return nil
}

// writeFileWithRetry writes a single file to a repo with retry/backoff.
// If firstFile is true, uses more retries to handle new repo initialization.
func (inst *Installer) writeFileWithRetry(ctx context.Context, org, repo, path, message string, content []byte, firstFile bool) error {
	maxAttempts := 3
	if firstFile {
		maxAttempts = 5
	}

	var lastErr error
	for attempt := range maxAttempts {
		if attempt > 0 {
			wait := time.Duration(attempt) * 2 * time.Second
			inst.printer.StepInfo(fmt.Sprintf("Retrying %s in %v...", path, wait))
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		lastErr = inst.client.CreateOrUpdateFile(ctx, org, repo, path, message, content)
		if lastErr == nil {
			return nil
		}
	}

	inst.printer.StepFail(fmt.Sprintf("Failed to write %s: %v", path, lastErr))
	return fmt.Errorf("writing %s: %w", path, lastErr)
}

// watchOnboarding monitors the repo onboarding workflow after config is pushed.
// It waits for the workflow to start and complete, then collects any PRs created
// across the org's repos and reports them to the user.
func (inst *Installer) watchOnboarding(ctx context.Context, org string, result *Result) error {
	enabledRepos := result.Config.EnabledRepos()
	if len(enabledRepos) == 0 {
		inst.printer.StepInfo("No repos enabled — repo onboarding will run when you enable repos")
		inst.printer.StepInfo("Edit config.yaml in .fullsend to enable repos, then push to main")
		return nil
	}

	inst.printer.Header("Watching repo onboarding")
	inst.printer.Blank()
	inst.printer.StepInfo("The repo onboarding workflow should start shortly...")
	inst.printer.StepInfo(fmt.Sprintf("It will create PRs to add the fullsend workflow to %d enabled repos.", len(enabledRepos)))
	inst.printer.Blank()

	// Poll for the workflow run to appear and complete
	var run *forge.WorkflowRun
	maxWait := 120 * time.Second
	pollInterval := 5 * time.Second
	deadline := time.Now().Add(maxWait)

	for time.Now().Before(deadline) {
		latest, err := inst.client.GetLatestWorkflowRun(ctx, org, ".fullsend", "repo-onboard.yaml")
		if err != nil {
			// Workflow may not exist yet — keep waiting
			inst.printer.StepInfo("Waiting for onboarding workflow to start...")
			select {
			case <-time.After(pollInterval):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		if latest == nil {
			select {
			case <-time.After(pollInterval):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		run = latest

		if run.Status == "completed" {
			break
		}

		inst.printer.StepInfo(fmt.Sprintf("Onboarding workflow is %s... (%s)",
			run.Status, run.HTMLURL))

		select {
		case <-time.After(pollInterval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if run == nil {
		inst.printer.StepWarn("Onboarding workflow did not start within timeout")
		inst.printer.StepInfo("Check the Actions tab at:")
		inst.printer.StepInfo(fmt.Sprintf("  https://github.com/%s/.fullsend/actions", org))
		return nil
	}

	if run.Status != "completed" {
		inst.printer.StepInfo(fmt.Sprintf("Onboarding workflow is still running: %s", run.HTMLURL))
		inst.printer.StepInfo("Check back later for the enrollment PRs.")
		return nil
	}

	if run.Conclusion == "success" {
		inst.printer.StepDone("Repo onboarding workflow completed successfully")
	} else {
		inst.printer.StepWarn(fmt.Sprintf("Onboarding workflow finished with conclusion: %s", run.Conclusion))
		inst.printer.StepInfo(fmt.Sprintf("Details: %s", run.HTMLURL))
	}

	// Collect enrollment PRs from enabled repos
	inst.printer.Blank()
	inst.printer.Header("Enrollment pull requests")
	inst.printer.Blank()

	foundPRs := false
	sort.Strings(enabledRepos)
	for _, repo := range enabledRepos {
		prs, err := inst.client.ListRepoPullRequests(ctx, org, repo)
		if err != nil {
			continue
		}
		for _, pr := range prs {
			if strings.Contains(pr.Title, "fullsend") {
				inst.printer.PRLink(repo, pr.URL)
				result.Proposals[repo] = &forge.ChangeProposal{
					Number: pr.Number,
					URL:    pr.URL,
					Title:  pr.Title,
				}
				foundPRs = true
			}
		}
	}

	if foundPRs {
		inst.printer.Blank()
		inst.printer.StepInfo("Review and merge these PRs to complete onboarding.")
		inst.printer.StepInfo("Nothing changes until a PR is merged.")
	} else {
		inst.printer.StepInfo("No enrollment PRs found — repos may already be onboarded.")
	}
	inst.printer.Blank()

	return nil
}

func (inst *Installer) printSummary(org string, result *Result) {
	items := []string{
		fmt.Sprintf("Config repo: %s/.fullsend", org),
		fmt.Sprintf("Repos discovered: %d", len(result.OrgRepos)),
	}

	if len(result.Proposals) > 0 {
		items = append(items, fmt.Sprintf("Enrollment PRs: %d", len(result.Proposals)))
	}

	if result.SecretsStored > 0 {
		items = append(items, fmt.Sprintf("Secrets stored: %d", result.SecretsStored))
	}

	items = append(items, "Auto-merge: disabled (safe default)")

	inst.printer.Summary("Installation complete", items)

	if len(result.Proposals) > 0 {
		inst.printer.Header("Pull requests to review")
		inst.printer.Blank()
		for repo, proposal := range result.Proposals {
			inst.printer.PRLink(repo, proposal.URL)
		}
		inst.printer.Blank()
		inst.printer.StepInfo("Review and merge these PRs to connect repos to the agent pipeline.")
		inst.printer.StepInfo("Nothing changes until a PR is merged.")
		inst.printer.Blank()
	}
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

// generateOnboardingWorkflow produces the repo onboarding workflow for .fullsend.
// This workflow runs on push to main and ensures each enabled repo has the
// fullsend shim workflow installed. It creates PRs to add or remove the shim.
func generateOnboardingWorkflow(org string) string {
	return fmt.Sprintf(`# Repo onboarding workflow for fullsend.
# Runs when config.yaml changes on main and ensures each enabled repo
# has the fullsend shim workflow installed. Creates PRs to add it.
name: Repo Onboarding

on:
  push:
    branches: [main]
    paths: [config.yaml]
  workflow_dispatch: {}

permissions:
  contents: read

jobs:
  onboard:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout .fullsend config
        uses: actions/checkout@v4

      - name: Generate app token
        id: app-token
        uses: actions/create-github-app-token@v1
        with:
          app-id: ${{ vars.FULLSEND_APP_ID }}
          private-key: ${{ secrets.FULLSEND_FULLSEND_APP_PRIVATE_KEY }}
          owner: %[1]s

      - name: Onboard repos
        env:
          GH_TOKEN: ${{ steps.app-token.outputs.token }}
          ORG: %[1]s
        run: |
          set -euo pipefail

          echo "Reading config.yaml..."
          ENABLED_REPOS=$(yq -r '.repos | to_entries[] | select(.value.enabled == true) | .key' config.yaml)

          if [ -z "$ENABLED_REPOS" ]; then
            echo "No repos enabled in config.yaml"
            exit 0
          fi

          SHIM_WORKFLOW=$(cat <<'WORKFLOW_EOF'
          %[2]s
          WORKFLOW_EOF
          )

          echo "$ENABLED_REPOS" | while read -r REPO; do
            echo "--- Checking $REPO ---"

            # Check if the shim workflow already exists
            if gh api "repos/${ORG}/${REPO}/contents/.github/workflows/fullsend.yaml" --silent 2>/dev/null; then
              echo "  ✓ Shim workflow already exists in ${REPO}"
              continue
            fi

            echo "  → Creating PR to add shim workflow to ${REPO}"

            # Check if a PR already exists
            EXISTING_PR=$(gh pr list --repo "${ORG}/${REPO}" --head "fullsend/onboard" --json number --jq '.[0].number' 2>/dev/null || true)
            if [ -n "$EXISTING_PR" ]; then
              echo "  ✓ PR #${EXISTING_PR} already exists"
              continue
            fi

            # Create branch and PR
            DEFAULT_BRANCH=$(gh api "repos/${ORG}/${REPO}" --jq '.default_branch')
            SHA=$(gh api "repos/${ORG}/${REPO}/git/ref/heads/${DEFAULT_BRANCH}" --jq '.object.sha')

            gh api "repos/${ORG}/${REPO}/git/refs" \
              -f ref="refs/heads/fullsend/onboard" \
              -f sha="${SHA}" 2>/dev/null || true

            echo "$SHIM_WORKFLOW" | gh api "repos/${ORG}/${REPO}/contents/.github/workflows/fullsend.yaml" \
              --method PUT \
              -f message="Add fullsend agent dispatch workflow" \
              -f content="$(echo "$SHIM_WORKFLOW" | base64 -w0)" \
              -f branch="fullsend/onboard"

            gh pr create \
              --repo "${ORG}/${REPO}" \
              --base "${DEFAULT_BRANCH}" \
              --head "fullsend/onboard" \
              --title "Connect to fullsend agent pipeline" \
              --body "$(cat <<PR_EOF
          ## Connect to fullsend agent pipeline

          This PR adds a GitHub Actions workflow that connects this repository to the
          fullsend autonomous development pipeline managed in [${ORG}/.fullsend](https://github.com/${ORG}/.fullsend).

          ### What this does

          - Triggers on issue, PR, and comment events
          - Calls the reusable workflow in .fullsend to dispatch the appropriate agent

          ### What this does NOT do

          - No code is changed in this repository
          - No automatic merging — auto_merge is disabled by default

          ---
          *Created by fullsend repo onboarding*
          PR_EOF
          )"

            echo "  ✓ PR created for ${REPO}"
          done

          echo ""
          echo "Onboarding complete."
`, org, strings.ReplaceAll(GenerateStubWorkflow(org), "\n", "\n          "))
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

// GenerateStubWorkflow produces the workflow YAML that enrolled repos add.
// Exported for use by the `repo onboard` CLI command.
func GenerateStubWorkflow(org string) string {
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

// GeneratePRBody produces the PR description for enrollment PRs.
// Exported for use by the `repo onboard` CLI command.
func GeneratePRBody(org string) string {
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
// The owner is the @username or @org/team that owns all files.
func generateCodeowners(owner string) string {
	return fmt.Sprintf(`# CODEOWNERS for .fullsend configuration repository
#
# All configuration changes require human review.
# Agents cannot modify their own guardrails.
#
# Adjust the owners below to match your organization.
* @%s
`, owner)
}
