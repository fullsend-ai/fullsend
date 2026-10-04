package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestReusableDispatchSkipsRetroForScaffoldBranches executes the actual route
// script, so the branch guard cannot drift from the workflow's behaviour.
func TestReusableDispatchSkipsRetroForScaffoldBranches(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "reusable-dispatch.yml"))
	require.NoError(t, err)

	var workflow struct {
		Jobs struct {
			Route struct {
				Steps []struct {
					Name string `yaml:"name"`
					Run  string `yaml:"run"`
				} `yaml:"steps"`
			} `yaml:"route"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(content, &workflow))

	var script string
	for _, step := range workflow.Jobs.Route.Steps {
		if step.Name == "Determine stage" {
			script = step.Run
			break
		}
	}
	require.NotEmpty(t, script, "route job must retain its Determine stage script")

	runRoute := func(t *testing.T, branch string) string {
		t.Helper()
		dir := t.TempDir()
		scriptPath := filepath.Join(dir, "route.sh")
		outputPath := filepath.Join(dir, "output")
		require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o644))

		cmd := exec.Command("bash", scriptPath)
		cmd.Env = append(os.Environ(),
			"EVENT_NAME=pull_request_target",
			"EVENT_ACTION=closed",
			"PR_HEAD_REF="+branch,
			"GITHUB_OUTPUT="+outputPath,
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "route script must exit cleanly: %s", out)

		result, err := os.ReadFile(outputPath)
		require.NoError(t, err)
		return string(result)
	}

	for _, branch := range []string{
		"fullsend/onboard",
		"fullsend/offboard",
		"fullsend/scaffold-install",
	} {
		t.Run(branch, func(t *testing.T) {
			require.Equal(t, "stage=\n", runRoute(t, branch))
		})
	}

	t.Run("ordinary branch still routes to retro", func(t *testing.T) {
		require.Equal(t, "stage=retro\ntrigger_source=\n", runRoute(t, "fix/8043"))
	})
}
