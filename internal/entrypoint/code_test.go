package entrypoint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func setupWorkspace(t *testing.T, includeReview bool) string {
	t.Helper()
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "code.md"), []byte("# Code Agent"), 0o644))
	if includeReview {
		require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "review.md"), []byte("# Review Agent"), 0o644))
	}
	return dir
}

func testEnv(workspace string) *Env {
	return &Env{
		Owner:         "testorg",
		Repo:          "testrepo",
		IssueNumber:   42,
		Workspace:     workspace,
		DefaultBranch: "main",
		BotToken:      "test-bot-token",
		AgentDir:      "agents",
	}
}

// codeRunFunc returns a RunFunc for the happy path. All git commands and
// agents succeed; git diff returns 1 (has changes).
func codeRunFunc(_ int, name string, args []string) (int, error) {
	if name == "git" && len(args) > 0 && args[0] == "diff" {
		return 1, nil // has changes
	}
	return 0, nil
}

func TestRunCode_HappyPath(t *testing.T) {
	workspace := setupWorkspace(t, true)
	env := testEnv(workspace)
	client := forge.NewFakeClient()
	runner := &FakeRunner{RunFunc: codeRunFunc}

	result, err := RunCode(context.Background(), env, runner, client, nil)
	require.NoError(t, err)
	assert.Equal(t, "agent/42", result.Branch)
	assert.Contains(t, result.PRURL, "/pull/")

	// Verify git identity was configured.
	assert.Equal(t, "git", runner.Calls[0].Name)
	assert.Equal(t, []string{"config", "user.name", "fullsend[bot]"}, runner.Calls[0].Args)

	// Verify branch was created.
	assert.Equal(t, []string{"checkout", "-b", "agent/42"}, runner.Calls[2].Args)

	// Verify code agent was invoked with sanitized env.
	assert.Equal(t, "claude", runner.Calls[3].Name)
	assert.Contains(t, runner.Calls[3].Args, "agents/code.md")

	// Verify secret scan ran.
	assert.Equal(t, "gitleaks", runner.Calls[5].Name)

	// Verify review agent ran.
	assert.Equal(t, "claude", runner.Calls[6].Name)
	assert.Contains(t, runner.Calls[6].Args, "agents/review.md")

	// Verify push and URL cleanup.
	assert.Equal(t, "git", runner.Calls[7].Name)
	assert.Equal(t, "remote", runner.Calls[7].Args[0])
	assert.Equal(t, "git", runner.Calls[8].Name)
	assert.Equal(t, "push", runner.Calls[8].Args[0])
	assert.Equal(t, "git", runner.Calls[9].Name)
	assert.Equal(t, "remote", runner.Calls[9].Args[0])
	assert.Contains(t, runner.Calls[9].Args[3], "https://github.com/")
	assert.NotContains(t, runner.Calls[9].Args[3], "x-access-token")

	// Verify PR was created.
	require.Len(t, client.CreatedProposals, 1)
	assert.True(t, client.CreatedProposals[0].Draft)
	assert.Contains(t, client.CreatedProposals[0].Title, "#42")

	// Verify labels were swapped.
	assert.Contains(t, client.AddedLabels, "testorg/testrepo/42/ready-for-review")
	assert.Contains(t, client.RemovedLabels, "testorg/testrepo/42/ready-to-code")
}

func TestRunCode_NoCommits(t *testing.T) {
	workspace := setupWorkspace(t, false)
	env := testEnv(workspace)
	client := forge.NewFakeClient()
	runner := &FakeRunner{
		RunFunc: func(_ int, _ string, _ []string) (int, error) {
			return 0, nil // git diff returns 0 = no changes
		},
	}

	_, err := RunCode(context.Background(), env, runner, client, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no commits")
	assert.Contains(t, client.AddedLabels, "testorg/testrepo/42/requires-manual-review")
}

func TestRunCode_GitDiffInfraError(t *testing.T) {
	workspace := setupWorkspace(t, false)
	env := testEnv(workspace)
	client := forge.NewFakeClient()
	runner := &FakeRunner{
		RunFunc: func(_ int, name string, args []string) (int, error) {
			if name == "git" && len(args) > 0 && args[0] == "diff" {
				return 128, nil // infrastructure error, not "has changes"
			}
			return 0, nil
		},
	}

	_, err := RunCode(context.Background(), env, runner, client, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "git diff exited 128")
}

func TestRunCode_AgentFails(t *testing.T) {
	workspace := setupWorkspace(t, false)
	env := testEnv(workspace)
	client := forge.NewFakeClient()
	runner := &FakeRunner{
		RunFunc: func(_ int, name string, _ []string) (int, error) {
			if name == "claude" {
				return 1, nil
			}
			return 0, nil
		},
	}

	_, err := RunCode(context.Background(), env, runner, client, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "code agent exited 1")
	assert.Contains(t, client.AddedLabels, "testorg/testrepo/42/requires-manual-review")
}

func TestRunCode_SecretScanFails(t *testing.T) {
	workspace := setupWorkspace(t, false)
	env := testEnv(workspace)
	client := forge.NewFakeClient()
	runner := &FakeRunner{
		RunFunc: func(_ int, name string, args []string) (int, error) {
			if name == "git" && len(args) > 0 && args[0] == "diff" {
				return 1, nil // has changes
			}
			if name == "gitleaks" {
				return 1, nil // secrets found
			}
			return 0, nil
		},
	}

	_, err := RunCode(context.Background(), env, runner, client, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret scan failed")
	assert.Contains(t, client.AddedLabels, "testorg/testrepo/42/requires-manual-review")
	require.Len(t, client.AddedComments, 1)
	assert.Contains(t, client.AddedComments[0].Body, "Secret scan")
}

func TestRunCode_ReviewRejects(t *testing.T) {
	workspace := setupWorkspace(t, true)
	env := testEnv(workspace)
	client := forge.NewFakeClient()
	runner := &FakeRunner{
		RunFunc: func(_ int, name string, args []string) (int, error) {
			if name == "git" && len(args) > 0 && args[0] == "diff" {
				return 1, nil // has changes
			}
			// Second claude call (review) rejects.
			if name == "claude" && len(args) > 1 && strings.Contains(args[1], "review.md") {
				return 1, nil
			}
			return 0, nil
		},
	}

	_, err := RunCode(context.Background(), env, runner, client, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "review rejected")
	assert.Contains(t, client.AddedLabels, "testorg/testrepo/42/requires-manual-review")
	require.Len(t, client.AddedComments, 1)
	assert.Contains(t, client.AddedComments[0].Body, "Pre-push review rejected")
}

func TestRunCode_ReviewAgentAbsent(t *testing.T) {
	workspace := setupWorkspace(t, false) // no review.md
	env := testEnv(workspace)
	client := forge.NewFakeClient()
	runner := &FakeRunner{RunFunc: codeRunFunc}

	result, err := RunCode(context.Background(), env, runner, client, nil)
	require.NoError(t, err)
	assert.Equal(t, "agent/42", result.Branch)

	// Verify no review agent call was made — only code agent.
	claudeCalls := 0
	for _, c := range runner.Calls {
		if c.Name == "claude" {
			claudeCalls++
		}
	}
	assert.Equal(t, 1, claudeCalls)
}

func TestRunCode_ExistingPR(t *testing.T) {
	workspace := setupWorkspace(t, false)
	env := testEnv(workspace)
	client := forge.NewFakeClient()
	client.PullRequests = map[string][]forge.ChangeProposal{
		"testorg/testrepo": {{
			URL:    "https://github.com/testorg/testrepo/pull/99",
			Title:  "existing PR",
			Number: 99,
			Head:   "agent/42",
		}},
	}
	runner := &FakeRunner{RunFunc: codeRunFunc}

	result, err := RunCode(context.Background(), env, runner, client, nil)
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/testorg/testrepo/pull/99", result.PRURL)
	assert.Empty(t, client.CreatedProposals)
}

func TestRunCode_AgentDefinitionMissing(t *testing.T) {
	dir := t.TempDir() // empty workspace, no agents/code.md
	env := testEnv(dir)
	client := forge.NewFakeClient()
	runner := &FakeRunner{}

	_, err := RunCode(context.Background(), env, runner, client, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent definition not found")
}

func TestRunCode_ResolvesDefaultBranch(t *testing.T) {
	workspace := setupWorkspace(t, false)
	env := testEnv(workspace)
	env.DefaultBranch = "" // force resolution from repo metadata
	client := forge.NewFakeClient()
	client.Repos = []forge.Repository{{
		Name:          "testrepo",
		FullName:      "testorg/testrepo",
		DefaultBranch: "develop",
	}}
	runner := &FakeRunner{RunFunc: codeRunFunc}

	result, err := RunCode(context.Background(), env, runner, client, nil)
	require.NoError(t, err)
	assert.Equal(t, "agent/42", result.Branch)
	assert.Equal(t, "develop", env.DefaultBranch)

	// Verify git diff used "develop..HEAD".
	for _, c := range runner.Calls {
		if c.Name == "git" && len(c.Args) > 0 && c.Args[0] == "diff" {
			assert.Contains(t, c.Args, "develop..HEAD")
		}
	}
}

func TestRunCode_TokenIsolation(t *testing.T) {
	workspace := setupWorkspace(t, false)
	env := testEnv(workspace)
	client := forge.NewFakeClient()
	runner := &FakeRunner{RunFunc: codeRunFunc}

	dirtyEnv := SanitizeEnv([]string{
		"PATH=/usr/bin",
		"HOME=/home/user",
		"LANG=en_US.UTF-8",
		"FULLSEND_CODE_BOT_TOKEN=" + env.BotToken,
		"GITHUB_TOKEN=ghp_should_be_stripped",
		"GH_TOKEN=ghp_also_stripped",
		"GITHUB_ENV=/tmp/github_env",
		"ACTIONS_RUNTIME_TOKEN=gha_runtime_secret",
	})

	_, err := RunCode(context.Background(), env, runner, client, dirtyEnv)
	require.NoError(t, err)

	// Find the claude (code agent) call and inspect its env.
	for _, c := range runner.Calls {
		if c.Name == "claude" {
			envStr := strings.Join(c.Env, "\n")
			assert.NotContains(t, envStr, env.BotToken, "bot token must not reach the agent")
			assert.NotContains(t, envStr, "GITHUB_TOKEN", "GITHUB_TOKEN must not reach the agent")
			assert.NotContains(t, envStr, "GH_TOKEN", "GH_TOKEN must not reach the agent")
			assert.NotContains(t, envStr, "GITHUB_ENV", "GITHUB_ENV must not reach the agent")
			assert.NotContains(t, envStr, "ACTIONS_RUNTIME_TOKEN", "ACTIONS_RUNTIME_TOKEN must not reach the agent")
			assert.Contains(t, c.Env, "PATH=/usr/bin", "safe vars should be preserved")
			return
		}
	}
	t.Fatal("expected a claude call but found none")
}

func TestExitCodeReader(t *testing.T) {
	reader := &ExitCodeReader{}

	approved := reader.Read(0, "/workspace")
	assert.True(t, approved.Approved)

	rejected := reader.Read(1, "/workspace")
	assert.False(t, rejected.Approved)
	assert.Contains(t, rejected.Summary, "exited 1")
}
