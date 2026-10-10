package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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

			cmd := exec.Command("bash", scriptPath)
			cmd.Dir = root
			out, err := cmd.CombinedOutput()

			if tc.wantHalt {
				require.Error(t, err, "stdout/stderr: %s", out)
			} else {
				require.NoError(t, err, "stdout/stderr: %s", out)
			}
		})
	}
}
