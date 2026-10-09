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

	cases := []struct {
		name string
		got  func(t *testing.T) string
	}{
		{"config.toml", func(t *testing.T) string {
			data, err := renderCodexConfig(sandbox.SandboxCodexConfig, repo,
				codexDeveloperInstructions("triage", def))
			require.NoError(t, err)
			return string(data)
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
			return codexGoldenLaunch(buildCodexRunCommand(hooksOn, "gpt-5.6-luna", "high", true, testRunnerHeldDigests))
		}},
		{"launch-nohooks.txt", func(t *testing.T) string {
			return codexGoldenLaunch(buildCodexRunCommand(RunParams{RepoDir: repo}, "gpt-5.6-luna", "", false, noHooks))
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
