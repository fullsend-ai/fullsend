package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

const enabledPRPollConfig = "fork_pr_review_poll:\n  enabled: true\n"

// prPollTestEnv clears the environment the driver reads and fails any
// attempt to build a GitHub client unless a test replaces it.
func prPollTestEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GITHUB_REPOSITORY", forge.VarAppSet, forge.VarReviewClientID} {
		t.Setenv(k, "")
	}
	origAuth := ghAuthTokenFn
	ghAuthTokenFn = func() ([]byte, error) { return nil, errors.New("no gh auth") }
	orig := newPRPollClient
	t.Cleanup(func() { newPRPollClient = orig; ghAuthTokenFn = origAuth })
	newPRPollClient = func(string) prPollClient {
		t.Fatal("the poll must not contact GitHub")
		return nil
	}
}

func runPRPoll(t *testing.T, configBody string, flags prPollFlags) (string, string, error) {
	t.Helper()
	if flags.fullsendDir == "" {
		flags.fullsendDir = writeJiraPollConfig(t, configBody)
	}
	if flags.outputPath == "" {
		flags.outputPath = filepath.Join(t.TempDir(), "dispatches.json")
	}
	if flags.targetRepo == "" {
		flags.targetRepo = "o/r"
	}
	var stderr strings.Builder
	cmd := &cobra.Command{}
	cmd.SetErr(&stderr)
	err := runGitHubPRPoll(cmd, flags)
	data, _ := os.ReadFile(flags.outputPath)
	return string(data), stderr.String(), err
}

func TestRunGitHubPRPoll_SkipsWithoutContactingGitHub(t *testing.T) {
	tests := map[string]struct {
		config string
		want   string
	}{
		"disabled by default":   {config: "", want: "fork PR review poll is disabled"},
		"disabled explicitly":   {config: "fork_pr_review_poll:\n  enabled: false\n", want: "fork PR review poll is disabled"},
		"kill switch":           {config: enabledPRPollConfig + "kill_switch: true\n", want: "kill switch is active"},
		"review agent disabled": {config: enabledPRPollConfig + "agents:\n  - name: review\n    enabled: false\n", want: "the review agent is disabled"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			prPollTestEnv(t)
			out, stderr, err := runPRPoll(t, tt.config, prPollFlags{})
			require.NoError(t, err)
			assert.Equal(t, "[]\n", out)
			assert.Contains(t, stderr, tt.want)
		})
	}
}

func TestRunGitHubPRPoll_ArgumentErrors(t *testing.T) {
	tests := map[string]struct {
		flags  prPollFlags
		config string
		env    map[string]string
		want   string
	}{
		"bad target repo":    {flags: prPollFlags{targetRepo: "nope"}, want: "must be an owner/repo slug"},
		"nested target repo": {flags: prPollFlags{targetRepo: "a/b/c"}, want: "must be an owner/repo slug"},
		"dotdot target repo": {flags: prPollFlags{targetRepo: "a/..b"}, want: "must be an owner/repo slug"},
		"missing since":      {config: enabledPRPollConfig, want: "--since is required"},
		"invalid since":      {config: enabledPRPollConfig, flags: prPollFlags{since: "yesterday"}, want: "must be an RFC 3339 timestamp"},
		"missing token":      {config: enabledPRPollConfig, flags: prPollFlags{since: "2026-11-17T18:00:00Z"}, want: "no GitHub token found"},
		"invalid config":     {config: "fork_pr_review_poll:\n  enabled: [\n", want: "load config"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			prPollTestEnv(t)
			_, _, err := runPRPoll(t, tt.config, tt.flags)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}

	t.Run("missing target repo", func(t *testing.T) {
		prPollTestEnv(t)
		var stderr strings.Builder
		cmd := &cobra.Command{}
		cmd.SetErr(&stderr)
		err := runGitHubPRPoll(cmd, prPollFlags{outputPath: "x", fullsendDir: t.TempDir()})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--target-repo or GITHUB_REPOSITORY is required")
	})
	t.Run("missing output", func(t *testing.T) {
		prPollTestEnv(t)
		cmd := &cobra.Command{}
		err := runGitHubPRPoll(cmd, prPollFlags{targetRepo: "o/r", fullsendDir: t.TempDir()})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--output is required")
	})
	t.Run("output not writable", func(t *testing.T) {
		prPollTestEnv(t)
		_, _, err := runPRPoll(t, "", prPollFlags{outputPath: filepath.Join(t.TempDir(), "missing", "d.json")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write dispatches")
	})
}

func TestRunGitHubPRPoll_DispatchesThroughCommand(t *testing.T) {
	prPollTestEnv(t)
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	t.Setenv(forge.VarReviewClientID, "Iv1.review")
	t.Setenv(forge.VarAppSet, "custom")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/o/r/pulls":
			_, _ = w.Write([]byte(`[{"number":3,"html_url":"https://github.com/o/r/pull/3","updated_at":"2026-11-17T19:00:00Z",
				"user":{"login":"alice","type":"User"},
				"head":{"ref":"f","sha":"0123456789abcdef0123456789abcdef01234567","repo":{"full_name":"alice/r","owner":{"login":"alice"}}},
				"base":{"ref":"main","sha":"b","repo":{"full_name":"o/r"}}}]`))
		case r.URL.Path == "/repos/o/r/collaborators/alice/permission":
			_, _ = w.Write([]byte(`{"permission":"read","role_name":"read"}`))
		case strings.HasSuffix(r.URL.Path, "/runs"):
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()
	newPRPollClient = func(token string) prPollClient {
		assert.Equal(t, "tok", token)
		return gh.New(token).WithBaseURL(srv.URL)
	}

	dir := writeJiraPollConfig(t, enabledPRPollConfig+"authorization:\n  - provider: owners_file\n")
	// The OWNERS file at the repository root makes the read-only author a
	// reviewer.
	repoRoot := filepath.Dir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "OWNERS"), []byte("reviewers:\n  - alice\n"), 0o600))
	fullsendDir := filepath.Join(repoRoot, ".fullsend")
	require.NoError(t, os.Rename(dir, fullsendDir))

	out := filepath.Join(t.TempDir(), "dispatches.json")
	cmd := newPollCmd()
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--input-driver", prPollInputDriver, "--fullsend-dir", fullsendDir, "--output", out, "--since", "2026-11-17T18:00:00Z"})
	require.NoError(t, cmd.Execute(), stderr.String())

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	var refs []harnessdispatch.ExecutionRef
	require.NoError(t, json.Unmarshal(data, &refs))
	require.Len(t, refs, 1, stderr.String())
	assert.Equal(t, "review", refs[0].Agent)
	assert.Equal(t, "3", refs[0].StatusNumber)
	assert.Contains(t, stderr.String(), "PR #3 head 0123456: dispatch")
	assert.Contains(t, stderr.String(), "GitHub API calls")
}

func TestReviewBotLogins(t *testing.T) {
	assert.Equal(t, []string{"o-review[bot]", "fullsend-ai-review[bot]"}, reviewBotLogins("o", ""))
	assert.Equal(t, []string{"o-review[bot]", "fullsend-ai-review[bot]", "x-review[bot]"}, reviewBotLogins("o", "x"))
}

func TestOwnersFileResolver(t *testing.T) {
	root := t.TempDir()
	resolve := ownersFileResolver(root)

	// A missing OWNERS file keeps the collaborator role.
	role, err := resolve("alice", normevent.RoleRead)
	require.NoError(t, err)
	assert.Equal(t, normevent.RoleRead, role)

	require.NoError(t, os.WriteFile(filepath.Join(root, "OWNERS"), []byte("approvers:\n  - bob\nreviewers:\n  - alice\n"), 0o600))
	role, err = resolve("alice", normevent.RoleNone)
	require.NoError(t, err)
	assert.Equal(t, normevent.RoleTriage, role)
	role, err = resolve("bob", normevent.RoleRead)
	require.NoError(t, err)
	assert.Equal(t, normevent.RoleWrite, role)
}

func TestWritePRPollDispatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.json")
	require.NoError(t, writePRPollDispatches(path, nil))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "[]\n", string(data))

	require.NoError(t, writePRPollDispatches(path, []harnessdispatch.ExecutionRef{{Agent: "review", EventPayload: json.RawMessage(`{}`)}}))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"agent": "review"`)
}
