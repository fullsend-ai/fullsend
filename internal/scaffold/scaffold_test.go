package scaffold

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/harness"
)

func TestFileModeMatchesFilesystem(t *testing.T) {
	scaffoldRoot := "fullsend-repo"

	var onDiskExecutable []string
	err := filepath.WalkDir(scaffoldRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		relPath := path[len(scaffoldRoot)+1:]
		if info.Mode()&0o111 != 0 {
			onDiskExecutable = append(onDiskExecutable, relPath)
		}
		return nil
	})
	require.NoError(t, err)

	for _, path := range onDiskExecutable {
		assert.Equal(t, "100755", FileMode(path),
			"file %s is executable on disk but not in executableFiles", path)
	}

	for path := range executableFiles {
		info, statErr := os.Stat(filepath.Join(scaffoldRoot, path))
		require.NoError(t, statErr, "file %s is in executableFiles but not on disk", path)
		assert.NotEqual(t, os.FileMode(0), info.Mode()&0o111,
			"file %s is in executableFiles but is not executable on disk", path)
	}
}

func TestFullsendRepoFilesExist(t *testing.T) {
	expected := []string{
		".github/workflows/dispatch.yml",
		".github/workflows/triage.yml",
		".github/workflows/code.yml",
		".github/workflows/review.yml",
		".github/workflows/fix.yml",
		".github/workflows/repo-maintenance.yml",
		".github/scripts/setup-agent-env.sh",
		"agents/triage.md",
		"agents/code.md",
		"env/gcp-vertex.env",
		"env/triage.env",
		"env/code-agent.env",
		"harness/triage.yaml",
		"harness/code.yaml",
		"policies/triage.yaml",
		"plugins/gopls-lsp/plugin.json",
		"policies/code.yaml",
		"schemas/triage-result.schema.json",
		"scripts/post-triage.sh",
		"scripts/pre-triage.sh",
		"scripts/scan-secrets",
		"scripts/pre-code.sh",
		"scripts/pre-review.sh",
		"scripts/post-code.sh",
		"scripts/reconcile-repos.sh",
		"scripts/validate-output-schema.sh",
		"scripts/fullsend-check-output",
		"scripts/validate-source-repo.sh",
		"skills/code-implementation/SKILL.md",
		"skills/issue-labels/SKILL.md",
		"templates/shim-workflow-call.yaml",
		"agents/prioritize.md",
		"env/prioritize.env",
		"harness/prioritize.yaml",
		"policies/prioritize.yaml",
		"schemas/prioritize-result.schema.json",
		"scripts/setup-prioritize.sh",
		"scripts/pre-prioritize.sh",
		"scripts/post-prioritize.sh",
		"scripts/lib/github-api-csma.sh",
		"scripts/post-prioritize-test.sh",
		".github/workflows/prioritize.yml",
		".github/workflows/prioritize-scheduler.yml",
	}

	for _, path := range expected {
		content, err := FullsendRepoFile(path)
		require.NoError(t, err, "reading %s", path)
		assert.NotEmpty(t, content, "%s should not be empty", path)
	}
}

func TestShimWorkflowCallTemplateContent(t *testing.T) {
	content, err := FullsendRepoFile("templates/shim-workflow-call.yaml")
	require.NoError(t, err)
	s := string(content)
	// yamllint document-start rule requires --- at the top
	assert.True(t, strings.HasPrefix(s, "---\n"), "shim workflow must start with YAML document start marker")
	// ADR 34: shim has 2 jobs (dispatch + stop-fix), not per-stage jobs
	assert.Contains(t, s, "dispatch:")
	assert.Contains(t, s, "stop-fix:")
	assert.Contains(t, s, "event_action:")
	assert.Contains(t, s, "id-token: write")
	assert.Contains(t, s, "__ORG__/.fullsend/.github/workflows/dispatch.yml@main")
	// Dispatch concurrency group (no cancel — thin callers handle per-stage cancellation)
	assert.Contains(t, s, "fullsend-dispatch-${{")
	assert.Contains(t, s, "cancel-in-progress: false")
	// Event triggers
	assert.Contains(t, s, "pull_request_target")
	assert.Contains(t, s, "pull_request_review")
	assert.Contains(t, s, "issue_comment")
	assert.Contains(t, s, "issues:")
	// Bot filter
	assert.Contains(t, s, "comment.user.type != 'Bot'")
	// stop-fix authorization
	assert.Contains(t, s, "/fs-fix-stop")
	assert.Contains(t, s, "fullsend-no-fix")
	// Per-stage jobs removed
	assert.NotContains(t, s, "dispatch-triage")
	assert.NotContains(t, s, "dispatch-code")
	assert.NotContains(t, s, "dispatch-review")
	assert.NotContains(t, s, "dispatch-fix-bot")
	assert.NotContains(t, s, "dispatch-fix-human")
	assert.NotContains(t, s, "dispatch-retro")
	assert.NotContains(t, s, "stage: triage")
	assert.NotContains(t, s, "stage: code")
	assert.NotContains(t, s, "stage: review")
	assert.NotContains(t, s, "stage: fix")
	assert.NotContains(t, s, "stage: retro")
	assert.NotContains(t, s, "FULLSEND_DISPATCH_TOKEN")
	assert.NotContains(t, s, "FULLSEND_DISPATCH_URL")
	assert.NotContains(t, s, "curl")
}

func TestShimTriggerParity(t *testing.T) {
	// Both shim templates must declare the same event trigger types so that
	// per-repo and workflow-call installation modes have identical behavior.
	perRepo, err := FullsendRepoFile("templates/shim-per-repo.yaml")
	require.NoError(t, err)
	workflowCall, err := FullsendRepoFile("templates/shim-workflow-call.yaml")
	require.NoError(t, err)

	type onSection struct {
		On map[string]struct {
			Types []string `yaml:"types"`
		} `yaml:"on"`
	}

	var prOn, wcOn onSection
	require.NoError(t, yaml.Unmarshal(perRepo, &prOn))
	require.NoError(t, yaml.Unmarshal(workflowCall, &wcOn))

	// Check that each shared event has matching sub-types.
	for event, wcTrigger := range wcOn.On {
		prTrigger, ok := prOn.On[event]
		require.True(t, ok, "per-repo shim is missing event trigger %q", event)
		assert.ElementsMatch(t, wcTrigger.Types, prTrigger.Types,
			"event %q types differ between shim templates", event)
	}
	for event := range prOn.On {
		_, ok := wcOn.On[event]
		assert.True(t, ok, "per-repo shim has extra event trigger %q not in workflow-call shim", event)
	}
}

func TestDispatchWorkflowContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/workflows/dispatch.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "workflow_call:")
	assert.NotContains(t, s, "workflow_dispatch:")
	// ADR 34: event_action input replaces stage input
	assert.Contains(t, s, "event_action:")
	assert.Contains(t, s, "required: true")
	// Routing logic
	assert.Contains(t, s, "Determine stage")
	assert.Contains(t, s, "/fs-triage")
	assert.Contains(t, s, "/fs-code")
	assert.Contains(t, s, "/fs-review")
	assert.Contains(t, s, "/fs-fix")
	assert.Contains(t, s, "/fs-retro")
	assert.Contains(t, s, "/fs-prioritize")
	assert.Contains(t, s, "ready-to-code")
	assert.Contains(t, s, "ready-for-review")
	assert.Contains(t, s, "TRIGGERING_LABEL")
	assert.Contains(t, s, "pull_request_target")
	assert.Contains(t, s, "pull_request_review")
	assert.Contains(t, s, "changes_requested")
	assert.Contains(t, s, "needs-info")
	assert.Contains(t, s, `! has_label "feature"`)
	assert.Contains(t, s, "opened|synchronize|ready_for_review")
	// /code must only run on issues, not PRs; /review must only run on PRs
	assert.Contains(t, s, "IS_PR")
	assert.Regexp(t, `(?s)/fs-review\).*?IS_PR`, s)
	assert.Regexp(t, `(?s)ready-for-review.*?IS_PR`, s)
	// Author association checks
	assert.Contains(t, s, "is_authorized")
	assert.Contains(t, s, "OWNER|MEMBER|COLLABORATOR")
	assert.Contains(t, s, `COMMENT_AUTHOR_ASSOC`)
	// Auto-triage requires assoc != NONE or issue author
	assert.Contains(t, s, "is_issue_author")
	// Bot filtering
	assert.Contains(t, s, `COMMENT_USER_TYPE`)
	assert.Contains(t, s, `!= "Bot"`)
	// No-fix label check (uses PR_LABELS for pull_request_review events)
	assert.Contains(t, s, "fullsend-no-fix")
	assert.Contains(t, s, "PR_LABELS")
	// Fork PR detection
	assert.Contains(t, s, "PR_HEAD_REPO")
	assert.Contains(t, s, "PR_BASE_REPO")
	// Kill switch and role check
	assert.Contains(t, s, "kill_switch")
	assert.Contains(t, s, "defaults.roles")
	// Stage output
	assert.Contains(t, s, "steps.route.outputs.stage")
	assert.Contains(t, s, "trigger_source")
	// Fan-out (unchanged)
	assert.Contains(t, s, "# fullsend-stage:")
	assert.Contains(t, s, "gh workflow run")
	assert.Contains(t, s, "permissions: {}")
	assert.Contains(t, s, "permissions:")
	assert.Contains(t, s, "actions: write")
	assert.Contains(t, s, "contents: read")
	assert.Contains(t, s, "id-token: write")
	assert.Contains(t, s, "set -euo pipefail")
	assert.Contains(t, s, "dispatched=0")
	assert.Contains(t, s, "No workflows found for stage")
	assert.Contains(t, s, "|| true")
	assert.Contains(t, s, "Invalid stage name")
	assert.Contains(t, s, `^[a-z][a-z0-9_-]*$`)
	assert.Contains(t, s, "dispatch.yml")
	assert.Contains(t, s, "self-dispatch guard")
	assert.Contains(t, s, "Scanned")
	assert.Contains(t, s, "skipped")
	// Verify OIDC mint is the sole token path
	assert.Contains(t, s, "FULLSEND_MINT_URL")
	assert.Contains(t, s, "oidc-mint")
	assert.Contains(t, s, "/v1/token")
	assert.Contains(t, s, "fullsend-mint")
	assert.Contains(t, s, "job.workflow_repository")
	// Verify both OIDC token and minted token are masked
	assert.Contains(t, s, "::add-mask::$OIDC_TOKEN")
	assert.Contains(t, s, "::add-mask::$TOKEN")
	assert.NotContains(t, s, "create-github-app-token")
	assert.NotContains(t, s, "FULLSEND_FULLSEND_APP_PRIVATE_KEY")
	assert.NotContains(t, s, "FULLSEND_FULLSEND_CLIENT_ID")
}

func TestWalkFullsendRepo(t *testing.T) {
	var paths []string
	err := WalkFullsendRepo(func(path string, content []byte) error {
		paths = append(paths, path)
		return nil
	})
	require.NoError(t, err)
	assert.True(t, len(paths) >= 15, "expected at least 15 installed files, got %d", len(paths))
}

func TestLayeredDirsNotInstalled(t *testing.T) {
	skippedPrefixes := []string{
		"agents/",
		"skills/",
		"schemas/",
		"harness/",
		"plugins/",
		"policies/",
		"scripts/",
		"env/",
		".github/actions/",
		".github/scripts/",
	}
	err := WalkFullsendRepo(func(path string, _ []byte) error {
		for _, prefix := range skippedPrefixes {
			if strings.HasPrefix(path, prefix) {
				t.Errorf("WalkFullsendRepo should not include %s (layered/upstream-only dir %s)", path, prefix)
			}
		}
		return nil
	})
	require.NoError(t, err)
}

func TestCustomizedDirsInstalled(t *testing.T) {
	expected := map[string]bool{
		"customized/agents/.gitkeep":   false,
		"customized/skills/.gitkeep":   false,
		"customized/schemas/.gitkeep":  false,
		"customized/harness/.gitkeep":  false,
		"customized/plugins/.gitkeep":  false,
		"customized/policies/.gitkeep": false,
		"customized/scripts/.gitkeep":  false,
		"customized/env/.gitkeep":      false,
	}
	err := WalkFullsendRepo(func(path string, _ []byte) error {
		if _, ok := expected[path]; ok {
			expected[path] = true
		}
		return nil
	})
	require.NoError(t, err)
	for path, found := range expected {
		assert.True(t, found, "WalkFullsendRepo should include %s", path)
	}
}

func TestWalkFullsendRepoAllIncludesEverything(t *testing.T) {
	var filtered, all []string
	err := WalkFullsendRepo(func(path string, _ []byte) error {
		filtered = append(filtered, path)
		return nil
	})
	require.NoError(t, err)
	err = WalkFullsendRepoAll(func(path string, _ []byte) error {
		all = append(all, path)
		return nil
	})
	require.NoError(t, err)
	assert.Greater(t, len(all), len(filtered),
		"WalkFullsendRepoAll (%d files) should return more files than WalkFullsendRepo (%d files)",
		len(all), len(filtered))
	// All filtered paths must appear in the all set.
	allSet := make(map[string]struct{}, len(all))
	for _, p := range all {
		allSet[p] = struct{}{}
	}
	for _, p := range filtered {
		_, ok := allSet[p]
		assert.True(t, ok, "WalkFullsendRepo path %s missing from WalkFullsendRepoAll", p)
	}
}

func TestTriageWorkflowContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/workflows/triage.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "# fullsend-stage: triage")
	assert.Contains(t, s, "workflow_dispatch")
	assert.Contains(t, s, "event_type")
	assert.Contains(t, s, "source_repo")
	assert.Contains(t, s, "event_payload")
	assert.Contains(t, s, "__REUSABLE_WORKFLOW__")
	assert.NotContains(t, s, "distribution_mode")
	assert.Contains(t, s, "FULLSEND_MINT_URL")
	assert.NotContains(t, s, "secrets: inherit")
	assert.Contains(t, s, "FULLSEND_GCP_WIF_PROVIDER: ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}")
	assert.Contains(t, s, "FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}")
	assert.Contains(t, s, "concurrency:")
	assert.Contains(t, s, "fullsend-triage-")
	assert.Contains(t, s, "cancel-in-progress: true")
	// Permissions required by the reusable workflow
	assert.Contains(t, s, "permissions:")
	assert.Contains(t, s, "actions: write")
	assert.Contains(t, s, "id-token: write")
	assert.Contains(t, s, "issues: write")
	assert.Contains(t, s, "contents: read")
}

func TestCodeAgentContent(t *testing.T) {
	content, err := FullsendRepoFile("agents/code.md")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "code")
	assert.Contains(t, s, "disallowedTools")
	assert.Contains(t, s, "code-implementation")
}

func TestCodeImplementationSkillAPIContractGuidance(t *testing.T) {
	content, err := FullsendRepoFile("skills/code-implementation/SKILL.md")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "Verify API contracts per code path")
	assert.Contains(t, s, "or changes a parameter sent to an external API")
}

func TestCodeWorkflowContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/workflows/code.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "# fullsend-stage: code")
	assert.Contains(t, s, "workflow_dispatch")
	assert.Contains(t, s, "__REUSABLE_WORKFLOW__")
	assert.NotContains(t, s, "distribution_mode")
	assert.Contains(t, s, "FULLSEND_MINT_URL")
	assert.NotContains(t, s, "secrets: inherit")
	assert.Contains(t, s, "FULLSEND_GCP_WIF_PROVIDER: ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}")
	assert.Contains(t, s, "FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}")
	assert.NotContains(t, s, "GCP_WIF_SA_EMAIL")
	assert.Contains(t, s, "concurrency:")
	assert.Contains(t, s, "fullsend-code-")
	assert.Contains(t, s, "cancel-in-progress: true")
	// Permissions required by the reusable workflow
	assert.Contains(t, s, "permissions:")
	assert.Contains(t, s, "actions: write")
	assert.Contains(t, s, "contents: write")
	assert.Contains(t, s, "id-token: write")
	assert.Contains(t, s, "issues: write")
	assert.Contains(t, s, "packages: read")
	assert.Contains(t, s, "pull-requests: write")
}

func TestReviewWorkflowContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/workflows/review.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "# fullsend-stage: review")
	assert.Contains(t, s, "workflow_dispatch")
	assert.Contains(t, s, "__REUSABLE_WORKFLOW__")
	assert.NotContains(t, s, "distribution_mode")
	assert.Contains(t, s, "FULLSEND_MINT_URL")
	assert.NotContains(t, s, "secrets: inherit")
	assert.Contains(t, s, "FULLSEND_GCP_WIF_PROVIDER: ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}")
	assert.Contains(t, s, "FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}")
	assert.Contains(t, s, "concurrency:")
	assert.Contains(t, s, "fullsend-review-")
	assert.Contains(t, s, "cancel-in-progress: true")
	// Permissions required by the reusable workflow
	assert.Contains(t, s, "permissions:")
	assert.Contains(t, s, "actions: write")
	assert.Contains(t, s, "contents: read")
	assert.Contains(t, s, "id-token: write")
	assert.Contains(t, s, "issues: write")
	assert.Contains(t, s, "pull-requests: write")
}

func TestFixWorkflowContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/workflows/fix.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "# fullsend-stage: fix")
	assert.Contains(t, s, "workflow_dispatch")
	assert.Contains(t, s, "trigger_source")
	assert.Contains(t, s, "__REUSABLE_WORKFLOW__")
	assert.NotContains(t, s, "distribution_mode")
	assert.Contains(t, s, "FULLSEND_MINT_URL")
	assert.NotContains(t, s, "secrets: inherit")
	assert.Contains(t, s, "FULLSEND_GCP_WIF_PROVIDER: ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}")
	assert.Contains(t, s, "FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}")
	assert.Contains(t, s, "concurrency:")
	assert.Contains(t, s, "fullsend-fix-")
	assert.Contains(t, s, "cancel-in-progress: true")
	// Permissions required by the reusable workflow
	assert.Contains(t, s, "permissions:")
	assert.Contains(t, s, "actions: write")
	assert.Contains(t, s, "contents: write")
	assert.Contains(t, s, "id-token: write")
	assert.Contains(t, s, "issues: write")
	assert.Contains(t, s, "packages: read")
	assert.Contains(t, s, "pull-requests: write")
}

func TestRetroWorkflowContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/workflows/retro.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "# fullsend-stage: retro")
	assert.Contains(t, s, "workflow_dispatch")
	assert.Contains(t, s, "__REUSABLE_WORKFLOW__")
	assert.NotContains(t, s, "distribution_mode")
	assert.Contains(t, s, "FULLSEND_MINT_URL")
	assert.NotContains(t, s, "secrets: inherit")
	assert.Contains(t, s, "FULLSEND_GCP_WIF_PROVIDER: ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}")
	assert.Contains(t, s, "FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}")
	assert.Contains(t, s, "concurrency:")
	assert.Contains(t, s, "fullsend-retro-")
	assert.Contains(t, s, "cancel-in-progress: true")
	// Permissions required by the reusable workflow
	assert.Contains(t, s, "permissions:")
	assert.Contains(t, s, "actions: write")
	assert.Contains(t, s, "contents: read")
	assert.Contains(t, s, "id-token: write")
	assert.Contains(t, s, "issues: write")
}

func TestValidateSourceRepoContent(t *testing.T) {
	content, err := FullsendRepoFile("scripts/validate-source-repo.sh")
	require.NoError(t, err)
	s := string(content)
	// Verify security-critical format regex
	assert.Contains(t, s, "^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$")
	assert.Contains(t, s, "Invalid source_repo format")
	// Verify owner check
	assert.Contains(t, s, "REPO_OWNER=\"${SOURCE_REPO%%/*}\"")
	assert.Contains(t, s, "source_repo owner does not match org")
	// Verify allowlist check
	assert.Contains(t, s, "REPO_NAME=\"${SOURCE_REPO#*/}\"")
	assert.Contains(t, s, "repo is not enabled in config.yaml")
	// Verify required environment variables
	assert.Contains(t, s, "${SOURCE_REPO:?SOURCE_REPO is required}")
	assert.Contains(t, s, "${GITHUB_REPOSITORY_OWNER:?GITHUB_REPOSITORY_OWNER is required}")
	// Verify error messages use ::error:: format
	assert.Contains(t, s, "::error::")
	// Verify config.yaml existence check (not masked by 2>/dev/null)
	assert.Contains(t, s, "config.yaml not found")
	// Verify yq availability check
	assert.Contains(t, s, "yq command not found")
}

func TestCodeHarnessContent(t *testing.T) {
	content, err := FullsendRepoFile("harness/code.yaml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "agents/code.md")
	assert.Contains(t, s, "pre_script")
	assert.Contains(t, s, "post_script")
	assert.Contains(t, s, "runner_env")
	assert.Contains(t, s, "PUSH_TOKEN")
}

func TestScanSecretsContent(t *testing.T) {
	content, err := FullsendRepoFile("scripts/scan-secrets")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "gitleaks")
	assert.Contains(t, s, "scan-secrets")
}

func TestScanSecretsImageMatchesScaffold(t *testing.T) {
	imageContent, err := os.ReadFile("../../images/code/scan-secrets")
	require.NoError(t, err)
	scaffoldContent, err := FullsendRepoFile("scripts/scan-secrets")
	require.NoError(t, err)
	assert.Equal(t, string(imageContent), string(scaffoldContent),
		"images/code/scan-secrets must stay in sync with scaffold scripts/scan-secrets")
}

func TestSetupAgentEnvContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/scripts/setup-agent-env.sh")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "AGENT_PREFIX")
	assert.Contains(t, s, "GITHUB_ENV")
}

func TestTriageAgentPromptContent(t *testing.T) {
	content, err := FullsendRepoFile("agents/triage.md")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "agent-result.json")
	assert.Contains(t, s, "clarity_scores")
	assert.Contains(t, s, "Anti-premature-resolution")
}

func TestTriageSchemaContent(t *testing.T) {
	content, err := FullsendRepoFile("schemas/triage-result.schema.json")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "$schema")
	assert.Contains(t, s, "insufficient")
	assert.Contains(t, s, "duplicate")
	assert.Contains(t, s, "sufficient")
}

func TestHarnessesLoadAndValidate(t *testing.T) {
	// Extract the full scaffold to a temp dir so harness.Load can resolve
	// relative paths and validate that referenced files exist. This catches
	// harness validation errors (e.g., missing fields, invalid combinations)
	// the same way the runner would at startup.
	dir := t.TempDir()
	err := WalkFullsendRepoAll(func(path string, content []byte) error {
		dest := filepath.Join(dir, path)
		if mkErr := os.MkdirAll(filepath.Dir(dest), 0o755); mkErr != nil {
			return mkErr
		}
		return os.WriteFile(dest, content, 0o644)
	})
	require.NoError(t, err, "extracting scaffold")

	// Find all harness YAML files.
	entries, err := os.ReadDir(filepath.Join(dir, "harness"))
	require.NoError(t, err)

	var loaded int
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".yaml") && !strings.HasSuffix(e.Name(), ".yml")) {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			harnessPath := filepath.Join(dir, "harness", e.Name())

			t.Run("Load", func(t *testing.T) {
				h, loadErr := harness.Load(harnessPath)
				require.NoError(t, loadErr, "Load should succeed")

				// Top-level pre/post scripts serve as defaults even
				// without forge resolution (local dev without --forge).
				assert.NotEmpty(t, h.PreScript, "PreScript should be set at top level as default")
				assert.NotEmpty(t, h.PostScript, "PostScript should be set at top level as default")
				assert.NotNil(t, h.Forge, "Forge map should be present")
				assert.Contains(t, h.Forge, "github", "Forge should have a github key")

				resolveErr := h.ResolveRelativeTo(dir)
				require.NoError(t, resolveErr, "ResolveRelativeTo should succeed")

				existErr := h.ValidateFilesExist()
				require.NoError(t, existErr, "ValidateFilesExist should succeed")
			})

			t.Run("LoadWithOpts_github", func(t *testing.T) {
				h, loadErr := harness.LoadWithOpts(harnessPath, harness.LoadOpts{ForgePlatform: "github"})
				require.NoError(t, loadErr, "LoadWithOpts should succeed")

				assert.Nil(t, h.Forge, "Forge should be nil after resolution")
				assert.NotEmpty(t, h.PreScript, "PreScript should be set after forge resolution")
				assert.NotEmpty(t, h.PostScript, "PostScript should be set after forge resolution")
				assert.NotEmpty(t, h.RunnerEnv, "RunnerEnv should be non-empty after merge")

				resolveErr := h.ResolveRelativeTo(dir)
				require.NoError(t, resolveErr, "ResolveRelativeTo should succeed")

				existErr := h.ValidateFilesExist()
				require.NoError(t, existErr, "ValidateFilesExist should succeed")
			})
		})
		loaded++
	}
	assert.True(t, loaded >= 2, "expected at least 2 harnesses, got %d", loaded)
}

func TestHarnessForgeRunnerEnvMerge(t *testing.T) {
	dir := t.TempDir()
	err := WalkFullsendRepoAll(func(path string, content []byte) error {
		dest := filepath.Join(dir, path)
		if mkErr := os.MkdirAll(filepath.Dir(dest), 0o755); mkErr != nil {
			return mkErr
		}
		return os.WriteFile(dest, content, 0o644)
	})
	require.NoError(t, err, "extracting scaffold")

	tests := []struct {
		file            string
		topLevelKeys    []string
		forgeGithubKeys []string
	}{
		{
			file:            "triage.yaml",
			topLevelKeys:    []string{"FULLSEND_OUTPUT_SCHEMA"},
			forgeGithubKeys: []string{"GITHUB_ISSUE_URL", "GH_TOKEN"},
		},
		{
			file:            "code.yaml",
			topLevelKeys:    []string{"TARGET_BRANCH"},
			forgeGithubKeys: []string{"PUSH_TOKEN", "PUSH_TOKEN_SOURCE", "REPO_FULL_NAME", "ISSUE_NUMBER", "REPO_DIR"},
		},
		{
			file:            "review.yaml",
			topLevelKeys:    []string{"FULLSEND_OUTPUT_SCHEMA"},
			forgeGithubKeys: []string{"REVIEW_TOKEN", "REPO_FULL_NAME", "PR_NUMBER", "GITHUB_PR_URL"},
		},
		{
			file:            "fix.yaml",
			topLevelKeys:    []string{"TARGET_BRANCH", "TRIGGER_SOURCE", "HUMAN_INSTRUCTION", "FIX_ITERATION", "REVIEW_BODY_FILE", "PRE_AGENT_HEAD", "FULLSEND_OUTPUT_SCHEMA", "FULLSEND_OUTPUT_FILE"},
			forgeGithubKeys: []string{"PUSH_TOKEN", "PUSH_TOKEN_SOURCE", "REPO_FULL_NAME", "PR_NUMBER", "REPO_DIR"},
		},
		{
			file:            "retro.yaml",
			topLevelKeys:    []string{"FULLSEND_OUTPUT_SCHEMA"},
			forgeGithubKeys: []string{"ORIGINATING_URL", "REPO_FULL_NAME", "GH_TOKEN"},
		},
		{
			file:            "prioritize.yaml",
			topLevelKeys:    []string{"FULLSEND_OUTPUT_SCHEMA"},
			forgeGithubKeys: []string{"GITHUB_ISSUE_URL", "GH_TOKEN", "ORG", "PROJECT_NUMBER"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			harnessPath := filepath.Join(dir, "harness", tt.file)
			h, loadErr := harness.LoadWithOpts(harnessPath, harness.LoadOpts{ForgePlatform: "github"})
			require.NoError(t, loadErr)

			for _, key := range tt.topLevelKeys {
				assert.Contains(t, h.RunnerEnv, key, "merged RunnerEnv should contain top-level key %s", key)
			}
			for _, key := range tt.forgeGithubKeys {
				assert.Contains(t, h.RunnerEnv, key, "merged RunnerEnv should contain forge.github key %s", key)
			}
		})
	}
}

func TestRepoMaintenanceWorkflowContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/workflows/repo-maintenance.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "config.yaml")
	assert.Contains(t, s, "templates/shim-workflow-call.yaml",
		"push trigger must include workflow_call shim template so changes propagate to enrolled repos")
	assert.NotContains(t, s, "templates/shim-workflow.yaml",
		"PAT shim template reference should be removed")
	assert.Contains(t, s, "fullsend-ai/fullsend/.github/actions/mint-token@v0")
	assert.Contains(t, s, "Checkout upstream scripts")
	assert.Contains(t, s, "Prepare scripts")
	assert.Contains(t, s, "customized/scripts")
	assert.Contains(t, s, "role: fullsend")
	assert.Contains(t, s, "id-token: write")
	assert.NotContains(t, s, "create-github-app-token")
	assert.NotContains(t, s, "FULLSEND_FULLSEND_CLIENT_ID")
	assert.NotContains(t, s, "./.github/actions/")
}

func TestRepoMaintenanceTokenCoversAllRepos(t *testing.T) {
	content, err := FullsendRepoFile(".github/workflows/repo-maintenance.yml")
	require.NoError(t, err)
	s := string(content)

	// The mint-token step must request access to ALL repos (enabled + disabled),
	// not just enabled ones. Without access to disabled repos, the reconcile
	// script can't check for or remove the shim workflow, and silently skips
	// unenrollment (gh api fails, 2>/dev/null hides it, script thinks "already
	// unenrolled").
	assert.Contains(t, s, "select(.value.enabled == true or .value.enabled == false)",
		"repo-list step must extract both enabled and disabled repos so the minted token covers them for unenrollment")
}

func TestReconcileReposContent(t *testing.T) {
	content, err := FullsendRepoFile("scripts/reconcile-repos.sh")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "shim-workflow-call.yaml")
	assert.NotContains(t, s, "shim-workflow.yaml\"",
		"reconcile-repos.sh should not reference deleted PAT shim template")
	assert.NotContains(t, s, "dispatch.mode",
		"reconcile-repos.sh should not parse dispatch mode")
	assert.Contains(t, s, "private repos cannot be enrolled",
		"reconcile-repos.sh should skip private repos to prevent log exposure")
}

func TestPrioritizeWorkflowContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/workflows/prioritize.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "# fullsend-stage: prioritize")
	assert.Contains(t, s, "workflow_dispatch")
	assert.Contains(t, s, "event_type")
	assert.Contains(t, s, "source_repo")
	assert.Contains(t, s, "event_payload")
	assert.Contains(t, s, "__REUSABLE_WORKFLOW__")
	assert.NotContains(t, s, "distribution_mode")
	assert.Contains(t, s, "FULLSEND_MINT_URL")
	assert.Contains(t, s, "FULLSEND_PROJECT_NUMBER")
	assert.NotContains(t, s, "secrets: inherit")
	assert.Contains(t, s, "FULLSEND_GCP_WIF_PROVIDER: ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}")
	assert.Contains(t, s, "FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}")
	assert.Contains(t, s, "concurrency:")
	assert.Contains(t, s, "fullsend-prioritize-")
	assert.Contains(t, s, "cancel-in-progress: true")
	assert.Contains(t, s, "permissions:")
	assert.Contains(t, s, "actions: write")
	assert.Contains(t, s, "id-token: write")
	assert.Contains(t, s, "issues: write")
	assert.Contains(t, s, "contents: read")
}

func TestPrioritizeSchedulerWorkflowContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/workflows/prioritize-scheduler.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "# schedule:", "cron trigger should be commented out by default (#778)")
	assert.Contains(t, s, "#   - cron:", "cron trigger should be commented out by default (#778)")
	assert.Contains(t, s, "workflow_dispatch")
	assert.Contains(t, s, "fullsend-prioritize-scheduler")
	assert.Contains(t, s, "RICE Score")
	assert.Contains(t, s, "prioritize.yml")
	assert.Contains(t, s, "FULLSEND_PROJECT_NUMBER")
	assert.Contains(t, s, "FULLSEND_PROJECT_NUMBER is not set; skipping prioritize scheduler")
	guardIndex := strings.Index(s, `if [[ -z "${PROJECT_NUMBER}" ]]; then`)
	projectViewIndex := strings.Index(s, `gh project view "${PROJECT_NUMBER}"`)
	require.NotEqual(t, -1, guardIndex)
	require.NotEqual(t, -1, projectViewIndex)
	assert.Less(t, guardIndex, projectViewIndex, "PROJECT_NUMBER must be checked before gh project view")
	assert.Contains(t, s, "fullsend-ai/fullsend/.github/actions/mint-token@v0")
	assert.Contains(t, s, "role: fullsend")
	assert.Contains(t, s, "id-token: write")
	assert.NotContains(t, s, "create-github-app-token")
	assert.NotContains(t, s, "FULLSEND_FULLSEND_CLIENT_ID")
}

func TestPrioritizeSchedulerSkipsWhenProjectNumberUnset(t *testing.T) {
	content, err := FullsendRepoFile(".github/workflows/prioritize-scheduler.yml")
	require.NoError(t, err)

	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(content, &workflow))

	dispatchJob, ok := workflow.Jobs["dispatch"]
	require.True(t, ok, "dispatch job should exist")

	var runScript string
	for _, step := range dispatchJob.Steps {
		if step.Name == "Find issues and dispatch prioritize runs" {
			runScript = step.Run
			break
		}
	}
	require.NotEmpty(t, runScript, "prioritize scheduler dispatch script should exist")

	tmpDir := t.TempDir()
	binDir := filepath.Join(tmpDir, "bin")
	require.NoError(t, os.Mkdir(binDir, 0o755))

	ghLog := filepath.Join(tmpDir, "gh-calls.log")
	fakeGH := "#!/usr/bin/env bash\n" +
		"printf 'gh called: %s\\n' \"$*\" >> " + strconv.Quote(ghLog) + "\n" +
		"exit 99\n"
	ghPath := filepath.Join(binDir, "gh")
	require.NoError(t, os.WriteFile(ghPath, []byte(fakeGH), 0o755))

	scriptPath := filepath.Join(tmpDir, "prioritize-scheduler-run.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(runScript), 0o755))

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = []string{
		"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PROJECT_NUMBER=",
		"ORG=test-org",
		"GH_TOKEN=test-token",
		"WIP_LIMIT=5",
		"STALE_THRESHOLD=7d",
		"GITHUB_REPOSITORY=test-org/.fullsend",
	}

	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	assert.Contains(t, string(output), "FULLSEND_PROJECT_NUMBER is not set; skipping prioritize scheduler")
	_, statErr := os.Stat(ghLog)
	assert.True(t, os.IsNotExist(statErr), "gh should not be called when PROJECT_NUMBER is unset")
}

func TestPrioritizeAgentPromptContent(t *testing.T) {
	content, err := FullsendRepoFile("agents/prioritize.md")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "agent-result.json")
	assert.Contains(t, s, "RICE")
	assert.Contains(t, s, "Reach")
	assert.Contains(t, s, "Impact")
	assert.Contains(t, s, "Confidence")
	assert.Contains(t, s, "Effort")
	assert.Contains(t, s, "customer-research skill")
}

func TestPrioritizeSchemaContent(t *testing.T) {
	content, err := FullsendRepoFile("schemas/prioritize-result.schema.json")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "$schema")
	assert.Contains(t, s, "reach")
	assert.Contains(t, s, "impact")
	assert.Contains(t, s, "confidence")
	assert.Contains(t, s, "effort")
	assert.Contains(t, s, "reasoning")
}

func TestPrioritizeHarnessContent(t *testing.T) {
	content, err := FullsendRepoFile("harness/prioritize.yaml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "agents/prioritize.md")
	assert.Contains(t, s, "pre_script")
	assert.Contains(t, s, "post_script")
	assert.Contains(t, s, "runner_env")
	assert.Contains(t, s, "PROJECT_NUMBER")
}

func TestAllScaffoldYAMLDocumentStartMarker(t *testing.T) {
	// yamllint document-start rule requires --- at the top of every YAML file.
	// Walk embedded scaffold YAML/YML files and verify each starts with "---\n".
	var checked int
	err := WalkFullsendRepoAll(func(path string, content []byte) error {
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil
		}
		assert.True(t, strings.HasPrefix(string(content), "---\n"),
			"%s must start with YAML document start marker (---)", path)
		checked++
		return nil
	})
	require.NoError(t, err)
	assert.True(t, checked >= 20, "expected at least 20 YAML files, got %d", checked)
}

func TestManagedHeader(t *testing.T) {
	tests := []struct {
		path   string
		expect string
	}{
		// YAML workflow files get a header
		{
			path:   ".github/workflows/triage.yml",
			expect: "# This file is managed by fullsend. Do not edit it directly.\n# Upstream: https://github.com/fullsend-ai/fullsend/blob/main/internal/scaffold/fullsend-repo/.github/workflows/triage.yml\n",
		},
		// YAML template files get a header
		{
			path:   "templates/shim-per-repo.yaml",
			expect: "# This file is managed by fullsend. Do not edit it directly.\n# Upstream: https://github.com/fullsend-ai/fullsend/blob/main/internal/scaffold/fullsend-repo/templates/shim-per-repo.yaml\n",
		},
		// Markdown files are skipped (user-readable docs)
		{path: "AGENTS.md", expect: ""},
		// .gitkeep files are skipped
		{path: "customized/agents/.gitkeep", expect: ""},
		// JSON files are skipped (no comment syntax)
		{path: "schemas/triage-result.schema.json", expect: ""},
		// Shell scripts get a header
		{path: "scripts/pre-triage.sh", expect: "# This file is managed by fullsend. Do not edit it directly.\n# Upstream: https://github.com/fullsend-ai/fullsend/blob/main/internal/scaffold/fullsend-repo/scripts/pre-triage.sh\n"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got := ManagedHeader(tc.path)
			assert.Equal(t, tc.expect, got)
		})
	}
}

func TestManagedHeaderPreservesShebang(t *testing.T) {
	// When content starts with #!, the header should go after the shebang line
	content := []byte("#!/bin/bash\nset -euo pipefail\n")
	header := ManagedHeader("scripts/pre-triage.sh")
	result := PrependManagedHeader("scripts/pre-triage.sh", content)

	assert.True(t, strings.HasPrefix(string(result), "#!/bin/bash\n"))
	assert.Contains(t, string(result), header)
	assert.Contains(t, string(result), "set -euo pipefail")
}

func TestPrependManagedHeaderNoHeader(t *testing.T) {
	content := []byte("# AGENTS.md\nSome content\n")
	result := PrependManagedHeader("AGENTS.md", content)
	assert.Equal(t, content, result, "files without headers should be returned unchanged")
}

func TestValidateTriageDeleted(t *testing.T) {
	_, err := FullsendRepoFile("scripts/validate-triage.sh")
	assert.Error(t, err, "validate-triage.sh should have been deleted")
}
