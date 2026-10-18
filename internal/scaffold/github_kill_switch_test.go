package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// ghWorkflowStep and friends mirror just enough of
// .github/workflows/reusable-dispatch.yml's shape to extract a single
// step's `run:` script for isolated execution. Unknown fields (if,
// env, uses, with, permissions, outputs, ...) are ignored by
// yaml.Unmarshal.
type ghWorkflowStep struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
}

type ghWorkflowJob struct {
	Steps []ghWorkflowStep `yaml:"steps"`
}

type ghWorkflowFile struct {
	Jobs map[string]ghWorkflowJob `yaml:"jobs"`
}

// extractReusableDispatchStepScript reads reusable-dispatch.yml from
// the repo root and returns the `run:` script text of the named step
// in the named job, so it can be executed in isolation against
// fixture .fullsend/config.yaml files without spinning up a real
// Actions run.
func extractReusableDispatchStepScript(t *testing.T, jobName, stepName string) string {
	t.Helper()
	path := filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml")
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	var wf ghWorkflowFile
	require.NoError(t, yaml.Unmarshal(content, &wf))

	job, ok := wf.Jobs[jobName]
	require.True(t, ok, "job %q not found in reusable-dispatch.yml", jobName)
	for _, step := range job.Steps {
		if step.Name == stepName {
			require.NotEmpty(t, step.Run, "step %q has no run script", stepName)
			return step.Run
		}
	}
	t.Fatalf("step %q not found in job %q", stepName, jobName)
	return ""
}

func TestCheckKillSwitchStep_ConsultsConfigBaseYAML(t *testing.T) {
	if _, err := exec.LookPath("yq"); err != nil {
		t.Skip("yq not found in PATH")
	}

	script := extractReusableDispatchStepScript(t, "route", "Check kill switch")

	cases := []struct {
		name           string
		configYAML     string
		configBaseYAML string
		wantHalt       bool
	}{
		{
			name:       "overlay true halts dispatch",
			configYAML: "kill_switch: true\n",
			wantHalt:   true,
		},
		{
			name:           "overlay silent, base active halts dispatch",
			configBaseYAML: "kill_switch: true\n",
			wantHalt:       true,
		},
		{
			name:           "overlay explicit false overrides base active",
			configYAML:     "kill_switch: false\n",
			configBaseYAML: "kill_switch: true\n",
			wantHalt:       false,
		},
		{
			name:     "neither file sets kill_switch",
			wantHalt: false,
		},
		{
			name:       "overlay True (capital) halts dispatch",
			configYAML: "kill_switch: True\n",
			wantHalt:   true,
		},
		{
			name:           "base True (capital) halts dispatch when overlay is silent",
			configBaseYAML: "kill_switch: True\n",
			wantHalt:       true,
		},
		{
			name:           "overlay kill_switch: null falls through to base",
			configYAML:     "kill_switch: null\n",
			configBaseYAML: "kill_switch: true\n",
			wantHalt:       true,
		},
		{
			name:           "overlay true survives a malformed base file",
			configYAML:     "kill_switch: true\n",
			configBaseYAML: "kill_switch: [unterminated\n",
			wantHalt:       true,
		},
		{
			name:           "overlay bare kill_switch: falls through to base",
			configYAML:     "kill_switch:\n",
			configBaseYAML: "kill_switch: true\n",
			wantHalt:       true,
		},
		{
			name:           "overlay kill_switch: ~ falls through to base",
			configYAML:     "kill_switch: ~\n",
			configBaseYAML: "kill_switch: true\n",
			wantHalt:       true,
		},
		{
			name:           "overlay kill_switch: Null falls through to base",
			configYAML:     "kill_switch: Null\n",
			configBaseYAML: "kill_switch: true\n",
			wantHalt:       true,
		},
		{
			name:           "overlay present but silent on kill_switch falls through to base",
			configYAML:     "# no kill_switch key here\n",
			configBaseYAML: "kill_switch: true\n",
			wantHalt:       true,
		},
		{
			name:       "overlay yes (bare) halts dispatch",
			configYAML: "kill_switch: yes\n",
			wantHalt:   true,
		},
		{
			name:       "overlay Yes (capital) halts dispatch",
			configYAML: "kill_switch: Yes\n",
			wantHalt:   true,
		},
		{
			name:       `overlay "yes" (quoted) halts dispatch`,
			configYAML: "kill_switch: \"yes\"\n",
			wantHalt:   true,
		},
		{
			name:           "base yes halts dispatch when overlay is silent",
			configBaseYAML: "kill_switch: yes\n",
			wantHalt:       true,
		},
		{
			name:       "overlay no does not halt dispatch",
			configYAML: "kill_switch: no\n",
			wantHalt:   false,
		},
		{
			name:       `overlay "no" (quoted) does not halt dispatch`,
			configYAML: "kill_switch: \"no\"\n",
			wantHalt:   false,
		},
		{
			name:       "overlay on does not halt dispatch (on/off intentionally unsupported)",
			configYAML: "kill_switch: on\n",
			wantHalt:   false,
		},
		{
			name:       "overlay off does not halt dispatch (on/off intentionally unsupported)",
			configYAML: "kill_switch: off\n",
			wantHalt:   false,
		},
		{
			name:       "overlay y does not halt dispatch (y/n intentionally unsupported)",
			configYAML: "kill_switch: y\n",
			wantHalt:   false,
		},
		{
			name:       "overlay n does not halt dispatch (y/n intentionally unsupported)",
			configYAML: "kill_switch: n\n",
			wantHalt:   false,
		},
		{
			name:       `overlay kill_switch: "true" (double-quoted) halts dispatch`,
			configYAML: "kill_switch: \"true\"\n",
			wantHalt:   true,
		},
		{
			name:       "overlay kill_switch: 'true' (single-quoted) halts dispatch",
			configYAML: "kill_switch: 'true'\n",
			wantHalt:   true,
		},
		{
			name:           `base kill_switch: "true" (double-quoted) halts dispatch when overlay is silent`,
			configBaseYAML: "kill_switch: \"true\"\n",
			wantHalt:       true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".fullsend"), 0o755))
			if tc.configYAML != "" {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".fullsend", "config.yaml"), []byte(tc.configYAML), 0o644))
			}
			if tc.configBaseYAML != "" {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".fullsend", "config.base.yaml"), []byte(tc.configBaseYAML), 0o644))
			}

			scriptPath := filepath.Join(root, "check-kill-switch.sh")
			require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))
			outputPath := filepath.Join(root, "github-output")
			require.NoError(t, os.WriteFile(outputPath, nil, 0o600))

			cmd := exec.Command("bash", scriptPath)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "REQUESTED_STAGE=code", "GITHUB_OUTPUT="+outputPath)
			out, err := cmd.CombinedOutput()
			output, readErr := os.ReadFile(outputPath)
			require.NoError(t, readErr)

			require.NoError(t, err, "stdout/stderr: %s", out)
			if tc.wantHalt {
				assert.Contains(t, string(output), "skipped=true")
			} else {
				assert.NotContains(t, string(output), "skipped=true")
			}
		})
	}
}

func TestAuthorizationMetadataStep_EmitsPauseForHumanLaunch(t *testing.T) {
	script := extractReusableDispatchStepScript(t, "route", "Emit authorization denial metadata")
	cases := []struct {
		name         string
		killSwitch   string
		roleSkipped  string
		agentSkipped string
		prSkipped    string
		wantPause    bool
		wantDenied   bool
	}{
		{
			name:       "kill switch emits pause notice metadata",
			killSwitch: "true",
			wantPause:  true,
		},
		{
			name:        "disabled role suppresses denial",
			roleSkipped: "true",
		},
		{
			name:         "disabled agent suppresses denial",
			agentSkipped: "true",
		},
		{
			name:      "existing code PR suppresses denial",
			prSkipped: "true",
		},
		{
			name:       "runnable unauthorized launch emits denial",
			wantDenied: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			scriptPath := filepath.Join(root, "emit-authorization-metadata.sh")
			require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))
			outputPath := filepath.Join(root, "github-output")
			require.NoError(t, os.WriteFile(outputPath, nil, 0o600))

			cmd := exec.Command("bash", scriptPath)
			cmd.Dir = root
			cmd.Env = append(os.Environ(),
				"EVENT_NAME=issue_comment",
				"COMMENT_USER_TYPE=User",
				"COMMENT_USER_LOGIN=alice",
				"COMMENT_USER_ID=1234",
				"ISSUE_NUMBER=42",
				"COMMAND_APPLICABLE=true",
				"REQUESTED_STAGE=code",
				"AUTH_CHECK_FAILED=false",
				"ROUTE_AUTHORIZATION_DENIED=true",
				"KILL_SWITCH_SKIPPED="+tc.killSwitch,
				"ROLE_SKIPPED="+tc.roleSkipped,
				"AGENT_SKIPPED="+tc.agentSkipped,
				"PR_SKIPPED="+tc.prSkipped,
				"GITHUB_OUTPUT="+outputPath,
			)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "stdout/stderr: %s", out)

			output, err := os.ReadFile(outputPath)
			require.NoError(t, err)
			if tc.wantPause {
				assert.Contains(t, string(output), "dispatch_blocked_reason=kill_switch\n")
				assert.Contains(t, string(output), "authorization_actor=alice\n")
				assert.Contains(t, string(output), "authorization_actor_id=1234\n")
				assert.Contains(t, string(output), "authorization_number=42\n")
			}
			if tc.wantDenied {
				assert.Contains(t, string(output), "authorization_denied=true\n")
			} else {
				assert.NotContains(t, string(output), "authorization_denied=true\n")
			}
		})
	}
}
