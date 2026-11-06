package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSandboxImageRelevanceGuard(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	data, err := os.ReadFile("../../.github/workflows/sandbox-images.yml")
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				ID  string `yaml:"id"`
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	var script string
	for _, step := range workflow.Jobs["changes"].Steps {
		if step.ID == "paths" {
			script = step.Run
		}
	}
	require.NotEmpty(t, script)
	repo, err := filepath.Abs("../..")
	require.NoError(t, err)
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	gitRun := func(dir string, input string, args ...string) string {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	gitRun(root, "", "clone", "--quiet", "--shared", "--no-checkout", repo, checkout)
	head := gitRun(checkout, "", "rev-parse", "HEAD")
	gitRun(checkout, "", "update-ref", "--no-deref", "HEAD", head)
	// No fixture commits, signing setup, or hooks are needed. An empty tree
	// provides a distinct base with relevant changes against this checkout.
	base := gitRun(checkout, "", "mktree")
	for _, tt := range []struct {
		name       string
		nonRepo    bool
		missingRef bool
		missingGit bool
		external   bool
		unchanged  bool
		want       string
		warning    bool
	}{
		{name: "detached relevant checkout", want: "true"},
		{name: "unchanged checkout", unchanged: true, want: "false"},
		{name: "missing CI refs", missingRef: true, want: "true", warning: true},
		{name: "non-repository", nonRepo: true, want: "true", warning: true},
		{name: "git unavailable", missingGit: true, want: "true", warning: true},
		{name: "ambient external diff cannot suppress validation", external: true, want: "true"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "output")
			old, next := base, head
			if tt.unchanged {
				old = head
			}
			if tt.missingRef {
				old, next = "", ""
			}
			cmd := exec.Command(bash, "-e", "-o", "pipefail", "-c", script)
			cmd.Dir = checkout
			if tt.nonRepo {
				cmd.Dir = root
			}
			cmd.Env = append(os.Environ(), "EVENT=merge_group", "BASE_SHA="+old, "HEAD_SHA="+next, "GITHUB_OUTPUT="+output)
			if tt.missingGit {
				cmd.Env = append(cmd.Env, "PATH="+filepath.Join(root, "unavailable"))
			}
			if tt.external {
				helper := filepath.Join(t.TempDir(), "diff-helper")
				require.NoError(t, os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0o700))
				cmd.Env = append(cmd.Env, "GIT_EXTERNAL_DIFF="+helper, "GIT_EXTERNAL_DIFF_TRUST_EXIT_CODE=1")
			}
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			result, err := os.ReadFile(output)
			require.NoError(t, err)
			assert.Equal(t, "relevant="+tt.want+"\n", string(result))
			assert.Equal(t, tt.warning, strings.Contains(string(out), "::warning::"), "%s", out)
		})
	}
}
