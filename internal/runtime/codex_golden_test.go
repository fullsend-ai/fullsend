package runtime

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/codex/golden")

// codexGoldenSHA matches a hex SHA-256. The goldens pin the launch command's
// shape, not its digests, so each one is stored as a placeholder.
var codexGoldenSHA = regexp.MustCompile(`[0-9a-f]{64}`)

// hooksOn is the RunParams of a run with hooks enabled.
var hooksOn = RunParams{RepoDir: sandbox.SandboxWorkspace + "/repo", HooksSettingsPath: CodexRuntime{}.codexHooksPath()}

// codexGoldenLaunch stores a launch command one ` && ` fragment per line, so
// a diff shows the fragment that changed.
func codexGoldenLaunch(cmd string) string {
	return strings.ReplaceAll(codexGoldenSHA.ReplaceAllString(cmd, "<sha256>"), " && ", " &&\n")
}

// codexAllHooksOff is harness security enabled with every sandbox hook
// disabled: hooks.json is still written, with no hook-script group in it.
func codexAllHooksOff() security.SandboxHookConfig {
	off := false
	return security.SandboxHookConfigFromHarness(&harness.Harness{
		Security: &harness.SecurityConfig{
			SandboxHooks: &harness.SandboxHooks{
				Tirith:                  &harness.TirithConfig{Enabled: &off},
				SSRFPreTool:             &off,
				CanaryPreTool:           &off,
				CanaryPostTool:          &off,
				SecretRedactPostTool:    &off,
				UnicodePostTool:         &off,
				ContextSuppressPostTool: &off,
			},
		},
	})
}

// TestCodexGolden pins the bytes of config.toml, hooks.json and the launch
// command CodexRuntime renders for one agent, so a change shows as a diff.
//
// Regenerate with: go test ./internal/runtime/ -run TestCodexGolden -update
func TestCodexGolden(t *testing.T) {
	t.Parallel()

	def, err := parsePiAgent([]byte(codexTestAgentDef))
	require.NoError(t, err)
	repo := sandbox.SandboxWorkspace + "/repo"
	// What Bootstrap records with security disabled.
	noHooks := testRunnerHeldDigests
	noHooks.HooksJSON = ""
	noHooks.HookScripts = nil
	noHooks.SecurityEnv = nil
	// What Bootstrap records when an inference.gateway block applies.
	gatewayHooks := testRunnerHeldDigests
	gatewayHooks.GatewayBaseURL = testCodexGatewayBaseURL

	cases := []struct {
		name string
		got  func(t *testing.T) string
	}{
		{"config.toml", func(t *testing.T) string {
			data, err := renderCodexConfig(sandbox.SandboxCodexConfig, repo,
				codexDeveloperInstructions("triage", def, nil), "", nil, "", "")
			require.NoError(t, err)
			return string(data)
		}},
		{"config-gateway.toml", func(t *testing.T) string {
			data, err := renderCodexConfig(sandbox.SandboxCodexConfig, repo,
				codexDeveloperInstructions("triage", def, nil), testCodexGatewayBaseURL, nil, "", "")
			require.NoError(t, err)
			return string(data)
		}},
		{"config-delegating.toml", func(t *testing.T) string {
			review, err := parsePiAgent([]byte(codexTestDelegatingAgentDef))
			require.NoError(t, err)
			roles, defaultModel, _, err := resolveCodexChildModels(codexGoldenRoles(t), nil, nil, "", "", codexTestCatalog())
			require.NoError(t, err)
			data, err := renderCodexConfig(sandbox.SandboxCodexConfig, repo,
				codexDeveloperInstructions("review", review, roles), "", roles, defaultModel, "")
			require.NoError(t, err)
			return string(data)
		}},
		{"launch-gateway.txt", func(t *testing.T) string {
			return codexGoldenLaunch(buildCodexRunCommand(hooksOn, codexModel{ID: "vendor/org/model", Gateway: true}, "high", true, gatewayHooks))
		}},
		{"hooks-default.json", func(t *testing.T) string {
			data, _, err := codexHooksJSON(sandbox.SandboxCodexConfig, testCodexPython,
				security.SandboxHookConfigFromHarness(&harness.Harness{}))
			require.NoError(t, err)
			return string(data)
		}},
		{"hooks-all-off.json", func(t *testing.T) string {
			data, _, err := codexHooksJSON(sandbox.SandboxCodexConfig, testCodexPython, codexAllHooksOff())
			require.NoError(t, err)
			return string(data)
		}},
		{"launch-hooks.txt", func(t *testing.T) string {
			return codexGoldenLaunch(buildCodexRunCommand(hooksOn, codexModel{ID: "gpt-5.6-luna"}, "high", true, testRunnerHeldDigests))
		}},
		{"launch-nohooks.txt", func(t *testing.T) string {
			return codexGoldenLaunch(buildCodexRunCommand(RunParams{RepoDir: repo}, codexModel{ID: "gpt-5.6-luna"}, "", false, noHooks))
		}},
		// The role files of a delegating agent and its launch command.
		// codexRoles sorts by name (TestCodexRoles_SkillPersonasSorted), so the
		// indexes are correctness, default, explore.
		{"roles/correctness.toml", func(t *testing.T) string { return string(codexGoldenRoles(t)[0].toml()) }},
		{"roles/default.toml", func(t *testing.T) string { return string(codexGoldenRoles(t)[1].toml()) }},
		{"roles/explore.toml", func(t *testing.T) string { return string(codexGoldenRoles(t)[2].toml()) }},
		{"roles/correctness-model.toml", func(t *testing.T) string {
			resolved, _, _, err := resolveCodexChildModels(codexGoldenRoles(t), nil,
				map[string]*string{"correctness": strp("openai/gpt-5.6-sol")}, "", "", codexTestCatalog())
			require.NoError(t, err)
			require.Equal(t, "correctness", resolved[0].Name, "codexRoles sorts by name")
			return string(resolved[0].toml())
		}},
		{"launch-delegating.txt", func(t *testing.T) string {
			return codexGoldenLaunch(buildCodexRunCommand(hooksOn, codexModel{ID: "gpt-5.6-luna"}, "high", true, codexGoldenDelegatingDigests(t)))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.got(t)
			if !strings.HasSuffix(got, "\n") {
				// end-of-file-fixer keeps a final newline on every tracked file.
				got += "\n"
			}
			path := filepath.Join("testdata", "codex", "golden", tc.name)
			if *update {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
				return
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err, "run with -update to create the golden file")
			assert.Equal(t, string(want), got,
				"%s differs from the golden file; if the change is intended, re-run with -update and review the diff",
				tc.name)
		})
	}
}

// codexGoldenRoles renders the roles of a review agent with the test
// correctness persona (codexTestPersona): three files, no model line.
func codexGoldenRoles(t *testing.T) []codexRole {
	t.Helper()
	skill := t.TempDir()
	writePersonaFile(t, skill, "correctness", codexTestPersona)
	roles, _ := codexRoles([]string{skill}, "review")
	return roles
}

// codexGoldenDelegatingDigests is testRunnerHeldDigests plus the role
// entries a delegating agent's Bootstrap records.
func codexGoldenDelegatingDigests(t *testing.T) codexRunnerHeldDigestSet {
	t.Helper()
	d := testRunnerHeldDigests
	d.RoleFiles = map[string]string{}
	for _, role := range codexGoldenRoles(t) {
		d.RoleFiles[role.Name] = codexAssetSHA256(role.toml())
	}
	return d
}

// The invariant of the role files: a delegating agent's launch command
// differs from a non-delegating one in the FULLSEND_CODEX_SPAWN_DIGESTS
// value and in nothing else. Rendered here from both digest sets with the
// golden inputs, so it holds independently of the files.
func TestCodexGolden_DelegatingLaunchDiffersOnlyInSpawnDigests(t *testing.T) {
	t.Parallel()

	plain := strings.Split(codexGoldenLaunch(buildCodexRunCommand(hooksOn, codexModel{ID: "gpt-5.6-luna"}, "high", true, testRunnerHeldDigests)), "\n")
	delegating := strings.Split(codexGoldenLaunch(buildCodexRunCommand(hooksOn, codexModel{ID: "gpt-5.6-luna"}, "high", true, codexGoldenDelegatingDigests(t))), "\n")
	require.Len(t, delegating, len(plain), "no fragment added or removed")

	var changed []int
	for i := range plain {
		if plain[i] != delegating[i] {
			changed = append(changed, i)
		}
	}
	require.Len(t, changed, 1, "exactly one fragment differs")
	assert.Equal(t, "export "+codexSpawnDigestsEnv+"='hooks.json:<sha256>'", strings.TrimSuffix(plain[changed[0]], " &&"))
	assert.Equal(t, "export "+codexSpawnDigestsEnv+
		"='agents/correctness.toml:<sha256> agents/default.toml:<sha256> agents/explore.toml:<sha256> hooks.json:<sha256>'",
		strings.TrimSuffix(delegating[changed[0]], " &&"), "one entry per role, ascending, file-name bound")
}
