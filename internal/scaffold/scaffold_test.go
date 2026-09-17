package scaffold

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
		".github/scripts/setup-agent-env.sh",
		"scripts/fullsend-check-output",
		"scripts/prepare-sandbox-credentials.sh",
		"templates/shim-per-repo.yaml",
		".github/workflows/prioritize.yml",
	}

	for _, path := range expected {
		content, err := FullsendRepoFile(path)
		require.NoError(t, err, "reading %s", path)
		assert.NotEmpty(t, content, "%s should not be empty", path)
	}
}

// TestPerOrgScaffoldFilesRemoved guards against reintroducing per-org-only
// scaffold assets (ADR 0044): the org dispatch and maintenance workflows,
// the org-routed thin stage callers, the workflow-call shim template, and
// the enrollment reconciliation and source-repo validation scripts.
func TestPerOrgScaffoldFilesRemoved(t *testing.T) {
	for _, path := range []string{
		".github/workflows/dispatch.yml",
		".github/workflows/repo-maintenance.yml",
		".github/workflows/triage.yml",
		".github/workflows/code.yml",
		".github/workflows/review.yml",
		".github/workflows/fix.yml",
		".github/workflows/retro.yml",
		"templates/shim-workflow-call.yaml",
		"scripts/reconcile-repos.sh",
		"scripts/reconcile-repos-test.sh",
		"scripts/validate-source-repo.sh",
	} {
		_, err := FullsendRepoFile(path)
		assert.Error(t, err, "per-org scaffold file %s must not be embedded", path)
	}
}

func TestShimPerRepoTemplateContent(t *testing.T) {
	content, err := FullsendRepoFile("templates/shim-per-repo.yaml")
	require.NoError(t, err)
	s := string(content)
	assert.True(t, strings.HasPrefix(s, "---\n"), "per-repo shim must start with YAML document start marker")
	assert.Contains(t, s, "dispatch:")
	assert.Contains(t, s, "stop-fix:")
	assert.Contains(t, s, "__REUSABLE_DISPATCH__")
	// reusable-dispatch.yml defaults install_mode to per-repo; the shim
	// must not pass it explicitly (#2887).
	assert.NotContains(t, s, "install_mode")
	assert.Contains(t, s, "FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}")
	assert.Contains(t, s, "FULLSEND_OPENAI_API_KEY: ${{ secrets.FULLSEND_OPENAI_API_KEY }}")
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
			StopFix struct {
				Permissions map[string]string `yaml:"permissions"`
			} `yaml:"stop-fix"`
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

	// Stop-fix job permissions
	assert.Equal(t, map[string]string{
		"contents":      "read",
		"issues":        "write",
		"pull-requests": "write",
	}, pr.Jobs.StopFix.Permissions, "stop-fix job permissions")
}

// TestShimStopFixAuthorization verifies the stop-fix job authorizes the
// /fs-fix-stop command via the collaborator permission API (ADR 0054) rather
// than author_association. See issue #5421: author_association grants
// CONTRIBUTOR to anyone with a single merged PR, which let an unauthorized
// external contributor disable the fix agent on another user's PR.
func TestShimStopFixAuthorization(t *testing.T) {
	for _, tmpl := range []string{
		"templates/shim-per-repo.yaml",
	} {
		t.Run(tmpl, func(t *testing.T) {
			content, err := FullsendRepoFile(tmpl)
			require.NoError(t, err)
			s := string(content)

			// CONTRIBUTOR must not gate any command — it is granted to anyone
			// with a single merged PR (the DoS vector from #5421).
			assert.NotContains(t, s, "CONTRIBUTOR",
				"stop-fix must not authorize based on the CONTRIBUTOR association")

			// The label step must call the collaborator permission API and
			// require an admin|maintain|write role (ADR 0054).
			assert.Contains(t, s, "collaborators/$COMMENT_USER_LOGIN/permission",
				"stop-fix must check permission via the collaborator API")
			assert.Contains(t, s, "admin|maintain|write",
				"stop-fix must require admin/maintain/write access")

			// The PR author retains an escape hatch on their own PR regardless
			// of their permission level.
			assert.Contains(t, s, `"$COMMENT_USER_LOGIN" == "$ISSUE_USER_LOGIN"`,
				"stop-fix must allow the PR author")

			// Unauthorized callers exit before the label is applied.
			assert.Contains(t, s, `if [[ "$authorized" != "true" ]]; then`,
				"stop-fix must gate labeling on the authorization result")

			// The authorization gate must precede the label mutation, not just
			// coexist with it — moving the gate below the label would fail open.
			gateIdx := strings.Index(s, `if [[ "$authorized" != "true" ]]; then`)
			labelIdx := strings.Index(s, "gh label create")
			require.GreaterOrEqual(t, gateIdx, 0)
			require.GreaterOrEqual(t, labelIdx, 0)
			assert.Less(t, gateIdx, labelIdx,
				"the authorization exit gate must come before the label mutation")

			// The coarse job-level if: must not resurrect author_association
			// gating (the false-negative ADR 0054 exists to avoid).
			assert.NotContains(t, s, "author_association ==",
				"stop-fix job if: must not gate on author_association")
		})
	}
}

// TestShimStopFixAuthorizationRuntime executes the stop-fix job's embedded
// bash against a stubbed `gh` binary to verify the authorization logic at
// runtime (not just by static string matching): the PR-author escape hatch,
// approval for write+ collaborators, denial for read-only collaborators, and
// fail-closed behavior when the permission API errors.
func TestShimStopFixAuthorizationRuntime(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	extractRun := func(t *testing.T, tmpl string) string {
		t.Helper()
		content, err := FullsendRepoFile(tmpl)
		require.NoError(t, err)
		var doc struct {
			Jobs struct {
				StopFix struct {
					Steps []struct {
						Run string `yaml:"run"`
					} `yaml:"steps"`
				} `yaml:"stop-fix"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(content, &doc))
		require.NotEmpty(t, doc.Jobs.StopFix.Steps, "stop-fix must have a step")
		run := doc.Jobs.StopFix.Steps[0].Run
		require.NotEmpty(t, run, "stop-fix step must have a run script")
		return run
	}

	// runScenario runs the script with a stub gh that logs its invocations and
	// applies the script's --jq expression to the given permission response
	// (or fails when response == "FAIL"). It returns the combined output and
	// whether the label mutation (`gh pr edit`) ran.
	runScenario := func(t *testing.T, script, commentUser, issueUser, response string) (string, bool) {
		t.Helper()
		dir := t.TempDir()
		logPath := filepath.Join(dir, "gh.log")
		stub := "#!/usr/bin/env bash\n" +
			"echo \"$@\" >> \"$GH_STUB_LOG\"\n" +
			"if [[ \"$1\" == \"api\" ]]; then\n" +
			"  if [[ \"$GH_STUB_RESPONSE\" == \"FAIL\" ]]; then echo 'simulated api failure' >&2; exit 1; fi\n" +
			"  [[ \"$3\" == \"--jq\" ]] || exit 1\n" +
			"  jq -r \"$4\" <<<\"$GH_STUB_RESPONSE\"; exit\n" +
			"fi\n" +
			"exit 0\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(stub), 0o755))
		scriptPath := filepath.Join(dir, "stop-fix.sh")
		require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o644))

		cmd := exec.Command("bash", scriptPath)
		cmd.Env = append(os.Environ(),
			"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GH_STUB_LOG="+logPath,
			"GH_STUB_RESPONSE="+response,
			"COMMENT_USER_LOGIN="+commentUser,
			"ISSUE_USER_LOGIN="+issueUser,
			"REPO=octo/repo",
			"PR_NUMBER=1",
			"GH_TOKEN=stub",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "script must exit 0 (fail-closed, not error): %s", out)

		logBytes, _ := os.ReadFile(logPath)
		labeled := strings.Contains(string(logBytes), "pr edit")
		return string(out) + string(logBytes), labeled
	}

	for _, tmpl := range []string{
		"templates/shim-per-repo.yaml",
	} {
		t.Run(tmpl, func(t *testing.T) {
			script := extractRun(t, tmpl)

			t.Run("pr author escape hatch", func(t *testing.T) {
				// Author with only read access can still stop on their own PR,
				// and the permission API is never consulted.
				out, labeled := runScenario(t, script, "alice", "alice", `{"role_name":"read"}`)
				assert.True(t, labeled, "PR author must be able to stop the fix agent")
				assert.NotContains(t, out, "api repos/", "author hatch must skip the permission API")
			})

			t.Run("write collaborator authorized", func(t *testing.T) {
				_, labeled := runScenario(t, script, "bob", "alice", `{"role_name":"write"}`)
				assert.True(t, labeled, "write-access collaborator must be authorized")
			})

			t.Run("custom role with maintain flags authorized", func(t *testing.T) {
				_, labeled := runScenario(t, script, "bob", "alice",
					`{"permission":"write","user":{"login":"custom-role-maintainer","type":"User","permissions":{"admin":false,"maintain":true,"push":true,"triage":true,"pull":true},"role_name":"Repo Maintainer"},"role_name":"Repo Maintainer"}`)
				assert.True(t, labeled, "custom role with effective maintain must be authorized")
			})

			t.Run("custom role with triage flags denied", func(t *testing.T) {
				_, labeled := runScenario(t, script, "bob", "alice",
					`{"permission":"read","role_name":"Helper","user":{"permissions":{"triage":true,"pull":true}}}`)
				assert.False(t, labeled, "stop-fix requires write; effective triage must be denied")
			})

			t.Run("custom role without effective permission denied", func(t *testing.T) {
				_, labeled := runScenario(t, script, "bob", "alice", `{"role_name":"Mystery"}`)
				assert.False(t, labeled, "custom role with no effective permission must be denied")
			})

			t.Run("read collaborator denied", func(t *testing.T) {
				out, labeled := runScenario(t, script, "bob", "alice", `{"role_name":"read"}`)
				assert.False(t, labeled, "read-only collaborator must be denied")
				assert.Contains(t, out, "not authorized")
			})

			t.Run("api failure fails closed", func(t *testing.T) {
				out, labeled := runScenario(t, script, "bob", "alice", "FAIL")
				assert.False(t, labeled, "API failure must fail closed (deny)")
				assert.Contains(t, out, "Permission API call failed",
					"API failure must emit a diagnostic warning")
			})
		})
	}
}

// TestCollaboratorPermissionJQ checks that reusable-dispatch.yml and the
// stop-fix shims share one --jq role resolution and that it maps custom roles
// to their effective base role (#7834).
func TestCollaboratorPermissionJQ(t *testing.T) {
	jqExpr := regexp.MustCompile(`(?s)/permission" \\\s*--jq '(.*?)' 2>`)
	extract := func(t *testing.T, content []byte) string {
		t.Helper()
		m := jqExpr.FindSubmatch(content)
		require.NotNil(t, m, "collaborator permission --jq expression not found")
		return strings.Join(strings.Fields(string(m[1])), " ")
	}

	dispatch, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml"))
	require.NoError(t, err)
	shim, err := FullsendRepoFile("templates/shim-per-repo.yaml")
	require.NoError(t, err)
	expr := extract(t, dispatch)
	require.Equal(t, expr, extract(t, shim), "dispatch and stop-fix shim must resolve roles identically")
	managed, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "fullsend.yaml"))
	require.NoError(t, err)
	require.Equal(t, expr, extract(t, managed), "this repo's managed shim must match the template")

	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}

	cases := []struct{ name, response, want string }{
		{"built-in role", `{"permission":"read","role_name":"triage"}`, "triage"},
		{"custom maintain", `{"permission":"write","user":{"login":"custom-role-maintainer","type":"User","permissions":{"admin":false,"maintain":true,"push":true,"triage":true,"pull":true},"role_name":"Repo Maintainer"},"role_name":"Repo Maintainer"}`, "maintain"},
		{"custom triage", `{"permission":"read","role_name":"Helper","user":{"permissions":{"triage":true,"pull":true}}}`, "triage"},
		{"custom legacy write", `{"permission":"write","role_name":"Dev"}`, "write"},
		{"custom legacy read stays read", `{"permission":"read","role_name":"Helper"}`, "read"},
		{"custom non-boolean flag ignores legacy", `{"permission":"write","role_name":"Dev","user":{"permissions":{"push":"true"}}}`, "none"},
		{"custom all flags false ignores legacy", `{"permission":"write","role_name":"Dev","user":{"permissions":{"admin":false,"maintain":false,"push":false,"triage":false,"pull":false}}}`, "none"},
		{"custom null flags use legacy", `{"permission":"write","role_name":"Dev","user":{"permissions":null}}`, "write"},
		{"custom empty flags ignore legacy", `{"permission":"write","role_name":"Dev","user":{"permissions":{}}}`, "none"},
		{"custom non-object flags ignore legacy", `{"permission":"write","role_name":"Dev","user":{"permissions":[]}}`, "none"},
		{"missing role_name", `{"permission":"write","user":{"permissions":{"push":true,"pull":true}}}`, "none"},
		{"non-string role_name", `{"permission":"write","role_name":7}`, "none"},
		{"custom no signals", `{"role_name":"Mystery"}`, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("jq", "-r", expr)
			cmd.Stdin = strings.NewReader(tc.response)
			out, err := cmd.Output()
			require.NoError(t, err)
			assert.Equal(t, tc.want, strings.TrimSpace(string(out)))
		})
	}
}

// TestManagedShimStopFixNotStale guards against drift between the shim
// template and this repo's own rendered managed workflow
// (.github/workflows/fullsend.yaml). That file is generated from
// shim-per-repo.yaml at deploy time; if a template security fix lands
// without regenerating it, the repo keeps running the vulnerable logic. See
// issue #5421.
func TestManagedShimStopFixNotStale(t *testing.T) {
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

	assert.NotContains(t, s, "CONTRIBUTOR",
		"managed shim must not authorize based on the CONTRIBUTOR association")
	assert.NotContains(t, s, "author_association ==",
		"managed shim job if: must not gate on author_association")
	assert.Contains(t, s, "collaborators/$COMMENT_USER_LOGIN/permission",
		"managed shim must check permission via the collaborator API")
	assert.Contains(t, s, "admin|maintain|write",
		"managed shim must require admin/maintain/write access")
	assert.Contains(t, s, `"$COMMENT_USER_LOGIN" == "$ISSUE_USER_LOGIN"`,
		"managed shim must preserve the PR-author escape hatch")
}

func TestWalkFullsendRepo(t *testing.T) {
	var paths []string
	err := WalkFullsendRepo(func(path string, content []byte) error {
		paths = append(paths, path)
		return nil
	})
	require.NoError(t, err)
	assert.Contains(t, paths, ".github/workflows/prioritize.yml")
	assert.Contains(t, paths, "templates/shim-per-repo.yaml")
}

// vestigialLayeredDirs are layeredDirs entries the embed has shipped nothing
// under since #5552 moved agent content to fullsend-ai/agents. Nothing may
// rely on CI layering them (#6689, #6834). Drop an entry once it ships content.
var vestigialLayeredDirs = map[string]bool{
	"agents/":  true,
	"skills/":  true,
	"schemas/": true,
	"harness/": true,
	"plugins/": true,
	"env/":     true,
}

// TestLayeredDirsShipContent: every non-vestigial layered directory has
// embedded files, so workspace preparation's [[ -d ]] guard never skips one a
// consumer relies on, and no vestigial entry hides a directory that ships.
func TestLayeredDirsShipContent(t *testing.T) {
	counts := make(map[string]int, len(layeredDirs))
	require.NoError(t, WalkLayeredContent(func(path string, _ []byte) error {
		for _, dir := range layeredDirs {
			if strings.HasPrefix(path, dir) {
				counts[dir]++
			}
		}
		return nil
	}))

	for _, dir := range layeredDirs {
		if vestigialLayeredDirs[dir] {
			assert.Zero(t, counts[dir],
				"%s ships %d embedded file(s) but is listed as vestigial; remove it from vestigialLayeredDirs", dir, counts[dir])
			continue
		}
		assert.NotZero(t, counts[dir],
			"%s is layered but the embed has no files under it, so CI never layers it (#6834); ship content or drop the entry", dir)
	}
	for dir := range vestigialLayeredDirs {
		assert.Contains(t, layeredDirs, dir, "vestigialLayeredDirs entry %s is not in layeredDirs", dir)
	}

	// An empty policies/ entry is #6834; a file behind it would be a second
	// fleet policy with no drift guard (#7268).
	assert.NotContains(t, layeredDirs, "policies/")
	_, err := FullsendRepoFile("policies/base.yaml")
	assert.Error(t, err, "scaffold must not ship policies/base.yaml; see #7268")
}

func TestLayeredDirsNotInstalled(t *testing.T) {
	skippedPrefixes := []string{
		"agents/",
		"skills/",
		"schemas/",
		"harness/",
		"plugins/",
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

func TestSetupAgentEnvContent(t *testing.T) {
	content, err := FullsendRepoFile(".github/scripts/setup-agent-env.sh")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "AGENT_PREFIX")
	assert.Contains(t, s, "GITHUB_ENV")
	// Per-run override passthrough from repository variables (#6526).
	assert.Contains(t, s, "FULLSEND_REPO_VARS")
	for _, key := range []string{"FULLSEND_RUNTIME", "FULLSEND_MODEL", "FULLSEND_EFFORT", "FULLSEND_FALLBACK_MODELS", "FULLSEND_PI_PROVIDER", "FULLSEND_PI_MODEL", "FULLSEND_CODEX_MODEL"} {
		assert.Contains(t, s, key)
	}
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
	assert.Contains(t, s, "FULLSEND_OPENAI_API_KEY: ${{ secrets.FULLSEND_OPENAI_API_KEY }}")
	assert.Contains(t, s, "concurrency:")
	assert.Contains(t, s, "fullsend-prioritize-")
	assert.Contains(t, s, "cancel-in-progress: true")
	assert.Contains(t, s, "permissions:")
	assert.Contains(t, s, "actions: write")
	assert.Contains(t, s, "id-token: write")
	assert.Contains(t, s, "issues: write")
	assert.Contains(t, s, "contents: read")
}

func TestScaffoldShimsForwardOpenAIAPIKey(t *testing.T) {
	const gcpForward = "FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}"
	const openAIForward = "FULLSEND_OPENAI_API_KEY: ${{ secrets.FULLSEND_OPENAI_API_KEY }}"
	var checked int
	err := WalkFullsendRepoAll(func(path string, content []byte) error {
		if !strings.HasSuffix(path, ".yml") && !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		s := string(content)
		if !strings.Contains(s, gcpForward) {
			return nil
		}
		checked++
		assert.Contains(t, s, openAIForward,
			"%s forwards FULLSEND_GCP_PROJECT_ID but not FULLSEND_OPENAI_API_KEY", path)
		return nil
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, checked, 2,
		"expected at least the prioritize thin caller and the per-repo shim to forward GCP_PROJECT_ID")
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
			path:   ".github/workflows/prioritize.yml",
			expect: "# This file is managed by fullsend. Do not edit it directly.\n# Upstream: https://github.com/fullsend-ai/fullsend/blob/main/internal/scaffold/fullsend-repo/.github/workflows/prioritize.yml\n",
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
		{path: "scripts/setup-prioritize.sh", expect: "# This file is managed by fullsend. Do not edit it directly.\n# Upstream: https://github.com/fullsend-ai/fullsend/blob/main/internal/scaffold/fullsend-repo/scripts/setup-prioritize.sh\n"},
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
	header := ManagedHeader("scripts/setup-prioritize.sh")
	result := PrependManagedHeader("scripts/setup-prioritize.sh", content)

	assert.True(t, strings.HasPrefix(string(result), "#!/bin/bash\n"))
	assert.Contains(t, string(result), header)
	assert.Contains(t, string(result), "set -euo pipefail")
}

func TestPrependManagedHeaderNoHeader(t *testing.T) {
	content := []byte("# AGENTS.md\nSome content\n")
	result := PrependManagedHeader("AGENTS.md", content)
	assert.Equal(t, content, result, "files without headers should be returned unchanged")
}

func TestScaffoldGitHubROProfile_GraphQLEndpoint(t *testing.T) {
	data, err := FullsendRepoFile("profiles/fullsend-github-ro.yaml")
	require.NoError(t, err)

	var profile struct {
		Endpoints []struct {
			Host        string `yaml:"host"`
			Port        int    `yaml:"port"`
			Protocol    string `yaml:"protocol"`
			Access      string `yaml:"access"`
			Enforcement string `yaml:"enforcement"`
			Path        string `yaml:"path"`
		} `yaml:"endpoints"`
	}
	require.NoError(t, yaml.Unmarshal(data, &profile))

	// The scaffold copy must include exactly one GraphQL endpoint for
	// api.github.com so that generated agents can use `gh --json`,
	// `gh issue view`, etc. without an egress policy denial. Its shape
	// matches the fullsend-ai/agents copy of this profile. See #7014.
	var found int
	for _, ep := range profile.Endpoints {
		if ep.Host != "api.github.com" || ep.Protocol != "graphql" {
			continue
		}
		found++
		assert.Equal(t, 443, ep.Port,
			"GraphQL endpoint port must be 443")
		assert.Equal(t, "/graphql", ep.Path,
			"GraphQL endpoint path must be /graphql")
		assert.Equal(t, "read-only", ep.Access,
			"GraphQL endpoint access must be read-only")
		assert.Equal(t, "enforce", ep.Enforcement,
			"GraphQL endpoint enforcement must be enforce")
	}
	assert.Equal(t, 1, found,
		"scaffold fullsend-github-ro profile must include exactly one GraphQL endpoint for api.github.com")
}

func TestScaffoldPackageRegistriesProfile_Permissions(t *testing.T) {
	data, err := FullsendRepoFile("profiles/fullsend-package-registries.yaml")
	require.NoError(t, err)

	var profile struct {
		Endpoints []struct {
			Host              string `yaml:"host"`
			AllowEncodedSlash bool   `yaml:"allow_encoded_slash"`
		} `yaml:"endpoints"`
		Binaries []string `yaml:"binaries"`
	}
	require.NoError(t, yaml.Unmarshal(data, &profile))

	// The embedded copy is the authoritative definition of this built-in
	// provider, so it must keep the permissions the fullsend-ai/agents copy
	// has. Scoped npm package metadata requests use a %2F-encoded slash,
	// which the proxy rejects unless allow_encoded_slash is set. See #8007.
	encodedSlash := map[string]bool{}
	for _, ep := range profile.Endpoints {
		encodedSlash[ep.Host] = ep.AllowEncodedSlash
	}
	npmHosts := map[string]bool{"registry.npmjs.org": true, "registry.yarnpkg.com": true}
	for host := range npmHosts {
		require.Contains(t, encodedSlash, host,
			"scaffold package-registries profile must include endpoint %s", host)
	}
	require.Contains(t, encodedSlash, "pypi.org",
		"scaffold package-registries profile must include endpoint pypi.org")
	// allow_encoded_slash is scoped to the npm registries only.
	for host, allowed := range encodedSlash {
		assert.Equal(t, npmHosts[host], allowed,
			"endpoint %s: allow_encoded_slash must be set exactly on the npm registries", host)
	}

	for _, bin := range []string{"**/uv", "**/uvx"} {
		assert.Contains(t, profile.Binaries, bin,
			"scaffold package-registries profile must allow binary %s", bin)
	}
}

func TestScaffoldGitleaksProfile_Permissions(t *testing.T) {
	data, err := FullsendRepoFile("profiles/fullsend-gitleaks.yaml")
	require.NoError(t, err)

	var profile struct {
		Binaries []string `yaml:"binaries"`
	}
	require.NoError(t, yaml.Unmarshal(data, &profile))

	// pre-commit uses git to clone the gitleaks hook repository from
	// github.com; the fullsend-ai/agents copy allowed it. See #8007.
	assert.Contains(t, profile.Binaries, "**/git",
		"scaffold gitleaks profile must allow binary **/git")
}

func TestScaffoldVertexProfile_BinaryAllowlist(t *testing.T) {
	data, err := FullsendRepoFile("profiles/fullsend-vertex-ai.yaml")
	require.NoError(t, err)

	var profile struct {
		Binaries []string `yaml:"binaries"`
	}
	require.NoError(t, yaml.Unmarshal(data, &profile))

	// Pin the whole list, not just the two entries #6971 added: this copy
	// must stay in sync with profiles/fullsend-vertex-ai.yaml in
	// fullsend-ai/agents (the fleet copy), which is what the sandbox
	// actually enforces. Claude Code 2.1.2xx installs its native binary at
	// bin/claude.exe even on Linux, so **/claude alone denies it STS access.
	assert.ElementsMatch(t, []string{"**/claude", "**/claude.exe", "**/node", "**/pi"}, profile.Binaries,
		"scaffold Vertex profile binaries drifted from the pinned allowlist; keep it in sync with the fullsend-ai/agents copy")
}
