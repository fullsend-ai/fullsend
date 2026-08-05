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
		"scripts/reconcile-repos.sh",
		"scripts/fullsend-check-output",
		"scripts/validate-source-repo.sh",
		"scripts/prepare-sandbox-credentials.sh",
		"templates/shim-workflow-call.yaml",
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
	// ADR 34: shim has 2 jobs (dispatch + stop-agent), not per-stage jobs
	assert.Contains(t, s, "dispatch:")
	assert.Contains(t, s, "stop-agent:")
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
	// stop-agent authorization + commands
	assert.Contains(t, s, "/fs-stop")
	assert.Contains(t, s, "/fs-fix-stop")
	assert.Contains(t, s, "fullsend-no-")
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

	// Permissions assertions (YAML-parsed, not string-contains) — #5785
	var wc struct {
		Permissions map[string]string `yaml:"permissions"`
		Jobs        struct {
			Dispatch struct {
				Permissions map[string]string `yaml:"permissions"`
			} `yaml:"dispatch"`
			StopAgent struct {
				Permissions map[string]string `yaml:"permissions"`
			} `yaml:"stop-agent"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(content, &wc))

	// Workflow-level: least-privilege default must be empty permissions
	require.NotNil(t, wc.Permissions,
		"workflow-level permissions must be present (permissions: {})")
	assert.Empty(t, wc.Permissions,
		"workflow-level permissions must be empty (least-privilege default)")

	// Dispatch job: intentionally narrower than per-repo mode
	assert.Equal(t, map[string]string{
		"actions":       "write",
		"id-token":      "write",
		"contents":      "read",
		"pull-requests": "read",
	}, wc.Jobs.Dispatch.Permissions, "dispatch job permissions")

	// Negative assertions: workflow-call dispatch must NOT have write
	// access to contents or pull-requests (intentionally narrower than
	// per-repo mode).
	assert.NotEqual(t, "write", wc.Jobs.Dispatch.Permissions["contents"],
		"workflow-call dispatch must not have contents: write")
	assert.NotEqual(t, "write", wc.Jobs.Dispatch.Permissions["pull-requests"],
		"workflow-call dispatch must not have pull-requests: write")

	// Stop-agent job permissions (label + comment only; no cancel)
	assert.Equal(t, map[string]string{
		"contents":      "read",
		"issues":        "write",
		"pull-requests": "write",
	}, wc.Jobs.StopAgent.Permissions, "stop-agent job permissions")
}

func TestShimPerRepoTemplateContent(t *testing.T) {
	content, err := FullsendRepoFile("templates/shim-per-repo.yaml")
	require.NoError(t, err)
	s := string(content)
	assert.True(t, strings.HasPrefix(s, "---\n"), "per-repo shim must start with YAML document start marker")
	assert.Contains(t, s, "dispatch:")
	assert.Contains(t, s, "stop-agent:")
	assert.Contains(t, s, "__REUSABLE_DISPATCH__")
	assert.Contains(t, s, "install_mode: per-repo")
	// Per-role concurrency lives in reusable-dispatch.yml, not a monolithic shim group (#2452).
	assert.NotContains(t, s, "fullsend-dispatch-${{")
	assert.NotRegexp(t, `(?m)^\s+concurrency:`, s)
	assert.Contains(t, s, "per-role cancel-in-progress groups live in reusable-dispatch.yml")

	// Permissions assertions (YAML-parsed, not string-contains) — #5785
	var pr struct {
		Permissions map[string]string `yaml:"permissions"`
		Jobs        struct {
			Dispatch struct {
				Permissions map[string]string `yaml:"permissions"`
			} `yaml:"dispatch"`
			StopAgent struct {
				Permissions map[string]string `yaml:"permissions"`
			} `yaml:"stop-agent"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(content, &pr))

	// Workflow-level: least-privilege default must be empty permissions
	require.NotNil(t, pr.Permissions,
		"workflow-level permissions must be present (permissions: {})")
	assert.Empty(t, pr.Permissions,
		"workflow-level permissions must be empty (least-privilege default)")

	// Dispatch job: per-repo mode needs broader permissions than
	// workflow-call because the agent runs in this repo's context.
	assert.Equal(t, map[string]string{
		"actions":       "write",
		"id-token":      "write",
		"contents":      "write",
		"issues":        "write",
		"packages":      "read",
		"pull-requests": "write",
	}, pr.Jobs.Dispatch.Permissions, "dispatch job permissions")

	// Stop-agent job permissions
	assert.Equal(t, map[string]string{
		"contents":      "read",
		"issues":        "write",
		"pull-requests": "write",
	}, pr.Jobs.StopAgent.Permissions, "stop-agent job permissions")
}

// TestShimStopAgentAuthorization verifies the stop-agent job authorizes
// /fs-stop (and /fs-fix-stop) via the collaborator permission API (ADR 0054)
// rather than author_association. See issue #5421: author_association grants
// CONTRIBUTOR to anyone with a single merged PR, which let an unauthorized
// external contributor disable agents on another user's PR.
func TestShimStopAgentAuthorization(t *testing.T) {
	script, err := FullsendRepoFile(".github/scripts/stop-agent.sh")
	require.NoError(t, err)
	s := string(script)

	assert.NotContains(t, s, "CONTRIBUTOR",
		"stop-agent must not authorize based on the CONTRIBUTOR association")
	assert.Contains(t, s, "collaborators/${COMMENT_USER_LOGIN}/permission",
		"stop-agent must check permission via the collaborator API")
	assert.Contains(t, s, "admin|maintain|write",
		"stop-agent must require admin/maintain/write access")
	assert.Contains(t, s, `"${COMMENT_USER_LOGIN}" == "${ISSUE_USER_LOGIN}"`,
		"stop-agent must allow the issue/PR author")
	assert.Contains(t, s, `if [[ "${authorized}" != "true" ]]; then`,
		"stop-agent must gate labeling on the authorization result")
	gateIdx := strings.Index(s, `if [[ "${authorized}" != "true" ]]; then`)
	labelIdx := strings.Index(s, "gh label create")
	require.GreaterOrEqual(t, gateIdx, 0)
	require.GreaterOrEqual(t, labelIdx, 0)
	assert.Less(t, gateIdx, labelIdx,
		"the authorization exit gate must come before the label mutation")
	assert.Contains(t, s, `sed 's/^[[:space:]]*//'`,
		"stop-agent must trim leading whitespace before parsing")

	for _, tmpl := range []string{
		"templates/shim-workflow-call.yaml",
		"templates/shim-per-repo.yaml",
	} {
		t.Run(tmpl, func(t *testing.T) {
			content, err := FullsendRepoFile(tmpl)
			require.NoError(t, err)
			ys := string(content)
			assert.NotContains(t, ys, "author_association ==",
				"stop-agent job if: must not gate on author_association")
			assert.Contains(t, ys, "bash .github/scripts/stop-agent.sh",
				"shim must invoke the shared stop-agent script")
			assert.Contains(t, ys, "actions/checkout@",
				"shim must check out the stop-agent script")
			assert.Contains(t, ys, "!contains(github.event.comment.body, '/fs-stop')",
				"dispatch job must skip /fs-stop comments")
		})
	}
}

// TestShimStopAgentAuthorizationRuntime executes stop-agent.sh against a
// stubbed `gh` binary to verify the authorization logic at runtime (not just
// by static string matching): the PR-author escape hatch, approval for write+
// collaborators, denial for read-only collaborators, and fail-closed behavior
// when the permission API errors.
func TestShimStopAgentAuthorizationRuntime(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	scriptBytes, err := FullsendRepoFile(".github/scripts/stop-agent.sh")
	require.NoError(t, err)
	script := string(scriptBytes)

	runScenario := func(t *testing.T, commentUser, issueUser, role, commentBody, issueIsPR string) (string, bool) {
		t.Helper()
		dir := t.TempDir()
		logPath := filepath.Join(dir, "gh.log")
		stub := "#!/usr/bin/env bash\n" +
			"echo \"$@\" >> \"$GH_STUB_LOG\"\n" +
			"for ((i=1; i<=$#; i++)); do\n" +
			"  if [[ \"${!i}\" == \"--body-file\" ]]; then\n" +
			"    j=$((i+1)); cat \"${!j}\" >> \"$GH_STUB_LOG\"\n" +
			"  fi\n" +
			"done\n" +
			"if [[ \"$1\" == \"api\" ]]; then\n" +
			"  if [[ \"$GH_STUB_ROLE\" == \"FAIL\" ]]; then echo 'simulated api failure' >&2; exit 1; fi\n" +
			"  echo \"$GH_STUB_ROLE\"; exit 0\n" +
			"fi\n" +
			"exit 0\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(stub), 0o755))
		scriptPath := filepath.Join(dir, "stop-agent.sh")
		require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))

		cmd := exec.Command("bash", scriptPath)
		cmd.Env = append(os.Environ(),
			"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GH_STUB_LOG="+logPath,
			"GH_STUB_ROLE="+role,
			"COMMENT_USER_LOGIN="+commentUser,
			"ISSUE_USER_LOGIN="+issueUser,
			"COMMENT_BODY="+commentBody,
			"ISSUE_IS_PR="+issueIsPR,
			"REPO=octo/repo",
			"ISSUE_NUMBER=1",
			"GH_TOKEN=stub",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "script must exit 0 (fail-closed, not error): %s", out)

		logBytes, _ := os.ReadFile(logPath)
		log := string(logBytes)
		labeled := strings.Contains(log, "pr edit") || strings.Contains(log, "issue edit")
		return string(out) + log, labeled
	}

	t.Run("pr author escape hatch", func(t *testing.T) {
		out, labeled := runScenario(t, "alice", "alice", "read", "/fs-fix-stop", "true")
		assert.True(t, labeled)
		assert.NotContains(t, out, "api repos/")
	})
	t.Run("write collaborator authorized", func(t *testing.T) {
		_, labeled := runScenario(t, "bob", "alice", "write", "/fs-fix-stop", "true")
		assert.True(t, labeled)
	})
	t.Run("read collaborator denied", func(t *testing.T) {
		out, labeled := runScenario(t, "bob", "alice", "read", "/fs-fix-stop", "true")
		assert.False(t, labeled)
		assert.Contains(t, out, "not authorized")
	})
	t.Run("api failure fails closed", func(t *testing.T) {
		out, labeled := runScenario(t, "bob", "alice", "FAIL", "/fs-fix-stop", "true")
		assert.False(t, labeled)
		assert.Contains(t, out, "Permission API call failed")
	})
	t.Run("fs-stop review applies single label", func(t *testing.T) {
		out, labeled := runScenario(t, "alice", "alice", "write", "/fs-stop review", "true")
		assert.True(t, labeled)
		assert.Contains(t, out, "fullsend-no-review")
		assert.NotContains(t, out, "fullsend-no-fix")
	})
	t.Run("leading whitespace still stops", func(t *testing.T) {
		out, labeled := runScenario(t, "alice", "alice", "write", "  /fs-stop review", "true")
		assert.True(t, labeled)
		assert.Contains(t, out, "fullsend-no-review")
	})
	t.Run("bare fs-stop on PR applies PR-meaningful labels only", func(t *testing.T) {
		out, labeled := runScenario(t, "alice", "alice", "write", "/fs-stop", "true")
		assert.True(t, labeled)
		for _, label := range []string{"fullsend-no-review", "fullsend-no-fix", "fullsend-no-retro"} {
			assert.Contains(t, out, label)
		}
		assert.NotContains(t, out, "fullsend-no-triage")
		assert.NotContains(t, out, "fullsend-no-code")
	})
	t.Run("bare fs-stop on issue applies issue-meaningful labels only", func(t *testing.T) {
		out, labeled := runScenario(t, "alice", "alice", "write", "/fs-stop", "false")
		assert.True(t, labeled)
		assert.Contains(t, out, "issue edit")
		assert.Contains(t, out, "fullsend-no-triage")
		assert.Contains(t, out, "fullsend-no-code")
		assert.NotContains(t, out, "fullsend-no-review")
	})
	t.Run("fs-stop triage on issue uses issue edit", func(t *testing.T) {
		out, labeled := runScenario(t, "alice", "alice", "write", "/fs-stop triage", "false")
		assert.True(t, labeled)
		assert.Contains(t, out, "issue edit")
		assert.Contains(t, out, "fullsend-no-triage")
	})
	t.Run("fs-stop fix on issue notes cross-context", func(t *testing.T) {
		out, labeled := runScenario(t, "alice", "alice", "write", "/fs-stop fix", "false")
		assert.True(t, labeled)
		assert.Contains(t, out, "fullsend-no-fix")
		assert.Contains(t, out, "do not carry over")
	})
	t.Run("unknown agent posts error without labeling", func(t *testing.T) {
		out, labeled := runScenario(t, "alice", "alice", "write", "/fs-stop bogus", "true")
		assert.False(t, labeled)
		assert.Contains(t, out, "Unknown or unsupported agent")
	})
	t.Run("injection-like arg rejected without labeling", func(t *testing.T) {
		out, labeled := runScenario(t, "alice", "alice", "write", `/fs-stop x";echo`, "true")
		assert.False(t, labeled)
		assert.Contains(t, out, "Unknown or unsupported agent")
	})
	t.Run("shims invoke shared script and skip stop in dispatch", func(t *testing.T) {
		for _, tmpl := range []string{"templates/shim-workflow-call.yaml", "templates/shim-per-repo.yaml"} {
			raw, err := FullsendRepoFile(tmpl)
			require.NoError(t, err)
			ys := string(raw)
			assert.Contains(t, ys, "bash .github/scripts/stop-agent.sh")
			assert.Contains(t, ys, "contains(github.event.comment.body, '/fs-stop')")
			assert.NotContains(t, ys, "VALID_BARE")
		}
	})
}

// TestManagedShimStopAgentNotStale guards against drift between the shim
// template and this repo's own rendered managed workflow
// (.github/workflows/fullsend.yaml). That file is generated from
// shim-workflow-call.yaml at deploy time; if a template security fix lands
// without regenerating it, the repo keeps running the vulnerable logic. See
// issue #5421.
func TestManagedShimStopAgentNotStale(t *testing.T) {
	// Walk up from the package dir to the repo root that holds the managed file.
	dir, err := os.Getwd()
	require.NoError(t, err)
	var managedPath string
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, ".github", "workflows", "fullsend.yaml")
		if _, statErr := os.Stat(candidate); statErr == nil {
			managedPath = candidate
			break
		}
		dir = filepath.Dir(dir)
	}
	if managedPath == "" {
		t.Skip("managed .github/workflows/fullsend.yaml not found from test dir")
	}

	content, err := os.ReadFile(managedPath)
	require.NoError(t, err)
	s := string(content)

	assert.Contains(t, s, "stop-agent:",
		"managed shim must use the generalized stop-agent job")
	assert.NotContains(t, s, "CONTRIBUTOR",
		"managed shim must not authorize based on the CONTRIBUTOR association")
	assert.NotContains(t, s, "author_association ==",
		"managed shim job if: must not gate on author_association")
	assert.Contains(t, s, "bash .github/scripts/stop-agent.sh",
		"managed shim must invoke the shared stop-agent script")
	assert.Contains(t, s, "actions/checkout@",
		"managed shim must check out the stop-agent script")
	assert.Contains(t, s, "!contains(github.event.comment.body, '/fs-stop')",
		"managed shim dispatch must skip /fs-stop comments")
	assert.NotContains(t, s, "VALID_BARE",
		"parsing logic must live in the shared script, not the YAML")

	scriptPath := filepath.Clean(filepath.Join(filepath.Dir(managedPath), "..", "scripts", "stop-agent.sh"))
	_, err = os.Stat(scriptPath)
	require.NoError(t, err, "repo must ship .github/scripts/stop-agent.sh for the managed shim")
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
	assert.Contains(t, s, "ready-for-triage")
	assert.Contains(t, s, "ready-to-code")
	assert.Contains(t, s, "ready-for-review")
	assert.Contains(t, s, "TRIGGERING_LABEL")
	assert.Contains(t, s, "pull_request_target")
	assert.Contains(t, s, "pull_request_review")
	assert.Contains(t, s, "changes_requested")
	assert.Contains(t, s, "needs-info")
	assert.Contains(t, s, `! has_label "feature"`)
	// Coupled needs-info re-entry must respect fullsend-no-triage (regression guard).
	assert.Contains(t, s, `has_label "needs-info" && ! has_label "feature" && ! has_label "fullsend-no-triage"`)
	assert.Contains(t, s, "opened|synchronize|ready_for_review")
	// /code must only run on issues, not PRs
	assert.Contains(t, s, "ISSUE_HAS_PR")
	// Authorization checks (collaborator permission API using .role_name)
	assert.Contains(t, s, "is_authorized")
	assert.Contains(t, s, "has_repo_permission")
	assert.Contains(t, s, "has_write_permission")
	assert.Contains(t, s, ".role_name")
	assert.Contains(t, s, "admin|maintain|write")
	// Observation stages accept triage; mutation stages stay write+ (#5223)
	assert.Contains(t, s, `is_authorized triage`)
	assert.Regexp(t, `is_authorized; then\s*\n\s+STAGE="code"`, s)
	assert.Regexp(t, `is_authorized; then\s*\n\s+STAGE="fix"`, s)
	assert.Contains(t, s, `COMMENT_AUTHOR_ASSOC`)
	// Auto-triage requires assoc != NONE or issue author
	assert.Contains(t, s, "is_issue_author")
	// Bot filtering
	assert.Contains(t, s, `COMMENT_USER_TYPE`)
	assert.Contains(t, s, `!= "Bot"`)
	// No-* label checks (auto paths; slash commands bypass)
	assert.Contains(t, s, "fullsend-no-fix")
	assert.Contains(t, s, "fullsend-no-review")
	assert.Contains(t, s, "fullsend-no-triage")
	assert.Contains(t, s, "fullsend-no-code")
	assert.Contains(t, s, "fullsend-no-retro")
	assert.Contains(t, s, "/fs-stop|/fs-fix-stop")
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
	assert.True(t, len(paths) >= 10, "expected at least 10 installed files, got %d", len(paths))
}

func TestLayeredDirsNotInstalled(t *testing.T) {
	skippedPrefixes := []string{
		"agents/",
		"skills/",
		"schemas/",
		"harness/",
		"plugins/",
		"policies/",
		"profiles/",
		"providers/",
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

func TestNoCustomizedDirsInstalled(t *testing.T) {
	err := WalkFullsendRepo(func(path string, _ []byte) error {
		assert.False(t, strings.HasPrefix(path, "customized/"),
			"WalkFullsendRepo should not include deprecated customized/ paths, got: %s", path)
		return nil
	})
	require.NoError(t, err)
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

func TestSetupAgentEnvContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/scripts/setup-agent-env.sh")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "AGENT_PREFIX")
	assert.Contains(t, s, "GITHUB_ENV")
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
	assert.Contains(t, s, "fullsend-ai/fullsend/.github/actions/mint-token@__FULLSEND_AI_REF__")
	assert.Contains(t, s, "Checkout upstream scripts")
	assert.Contains(t, s, "Prepare scripts")
	assert.NotContains(t, s, "customized/scripts",
		"customized/ overlay removed per ADR-0064")
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
	assert.Contains(t, s, "fullsend-ai/fullsend/.github/actions/mint-token@__FULLSEND_AI_REF__")
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
	assert.True(t, checked >= 10, "expected at least 10 YAML files, got %d", checked)
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
		// JSON files are skipped (no comment syntax)
		{path: "data/example.json", expect: ""},
		// Shell scripts get a header
		{path: "scripts/reconcile-repos.sh", expect: "# This file is managed by fullsend. Do not edit it directly.\n# Upstream: https://github.com/fullsend-ai/fullsend/blob/main/internal/scaffold/fullsend-repo/scripts/reconcile-repos.sh\n"},
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
	header := ManagedHeader("scripts/reconcile-repos.sh")
	result := PrependManagedHeader("scripts/reconcile-repos.sh", content)

	assert.True(t, strings.HasPrefix(string(result), "#!/bin/bash\n"))
	assert.Contains(t, string(result), header)
	assert.Contains(t, string(result), "set -euo pipefail")
}

func TestPrependManagedHeaderNoHeader(t *testing.T) {
	content := []byte("# AGENTS.md\nSome content\n")
	result := PrependManagedHeader("AGENTS.md", content)
	assert.Equal(t, content, result, "files without headers should be returned unchanged")
}
