package cli

import (
	"context"
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

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/prpoll"
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
	cmd.SetContext(context.Background())
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
		"missing token": {
			config: enabledPRPollConfig, flags: prPollFlags{since: "2026-11-17T18:00:00Z"},
			env:  map[string]string{forge.VarReviewClientID: "Iv1.review"},
			want: "no GitHub token found",
		},
		// Without the review App client ID the poll fails before it reads
		// GitHub, and writes no dispatch file.
		"missing review client ID": {
			config: enabledPRPollConfig, flags: prPollFlags{since: "2026-11-17T18:00:00Z"},
			env:  map[string]string{"GITHUB_TOKEN": "tok"},
			want: "FULLSEND_REVIEW_CLIENT_ID",
		},
		"invalid config": {config: "fork_pr_review_poll:\n  enabled: [\n", want: "load config"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			prPollTestEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			out, _, err := runPRPoll(t, tt.config, tt.flags)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Empty(t, out, "no dispatch file is written")
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
		case r.URL.Path == "/repos/o/r":
			_, _ = w.Write([]byte(`{"full_name":"o/r","default_branch":"main"}`))
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
	assert.Contains(t, stderr.String(), "warning: no --state-file")
}

// prPollServer serves one dispatchable fork PR by a write collaborator and
// counts repository lookups.
func prPollServer(t *testing.T, repoLookups *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/o/r/pulls":
			_, _ = w.Write([]byte(`[{"number":3,"html_url":"https://github.com/o/r/pull/3","updated_at":"2026-11-17T19:00:00Z",
				"user":{"login":"alice","type":"User"},
				"head":{"ref":"f","sha":"0123456789abcdef0123456789abcdef01234567","repo":{"full_name":"alice/r","owner":{"login":"alice"}}},
				"base":{"ref":"main","sha":"b","repo":{"full_name":"o/r"}}}]`))
		case r.URL.Path == "/repos/o/r/collaborators/alice/permission":
			_, _ = w.Write([]byte(`{"permission":"write","role_name":"write"}`))
		case r.URL.Path == "/repos/o/r":
			*repoLookups++
			_, _ = w.Write([]byte(`{"full_name":"o/r","default_branch":"main"}`))
		case strings.HasSuffix(r.URL.Path, "/runs"):
			_, _ = w.Write([]byte(`{"workflow_runs":[]}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunGitHubPRPoll_StateFile(t *testing.T) {
	prPollTestEnv(t)
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv(forge.VarReviewClientID, "Iv1.review")
	repoLookups := 0
	srv := prPollServer(t, &repoLookups)
	newPRPollClient = func(token string) prPollClient { return gh.New(token).WithBaseURL(srv.URL) }

	fullsendDir := writeJiraPollConfig(t, "fork_pr_review_poll:\n  enabled: true\n  max_attempts_per_head: 1\n")
	statePath := filepath.Join(t.TempDir(), "state.json")
	marker := statePath + prPollStateChangedSuffix
	pollOnce := func() string {
		t.Helper()
		out := filepath.Join(t.TempDir(), "dispatches.json")
		cmd := newPollCmd()
		var stderr strings.Builder
		cmd.SetErr(&stderr)
		cmd.SetArgs([]string{"--input-driver", prPollInputDriver, "--target-repo", "o/r", "--fullsend-dir", fullsendDir,
			"--output", out, "--since", "2026-11-17T18:00:00Z", "--state-file", statePath, "--default-branch", "main"})
		require.NoError(t, cmd.Execute(), stderr.String())
		return stderr.String()
	}

	// Cycle 1 dispatches and records the dispatch.
	log := pollOnce()
	assert.Contains(t, log, "PR #3 head 0123456: dispatch")
	assert.Contains(t, log, "state: changed (1 heads)")
	assert.FileExists(t, marker)
	data, err := os.ReadFile(statePath)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"3:0123456789abcdef0123456789abcdef01234567":{"dispatches":1,"status":""`)

	// Cycle 2 finds the attempt limit reached and records the head as
	// exhausted.
	log = pollOnce()
	assert.Contains(t, log, "skip: attempts exhausted (1 dispatches)")
	assert.Contains(t, log, "state: changed")
	data, err = os.ReadFile(statePath)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"status":"exhausted"`)

	// Cycle 3 skips the head from the state: nothing changes, and the
	// marker of the last cycle is removed.
	log = pollOnce()
	assert.Contains(t, log, "skip: attempts exhausted (cached, 1 dispatches)")
	assert.Contains(t, log, "1 heads skipped from cache")
	assert.Contains(t, log, "state: unchanged")
	assert.NoFileExists(t, marker)
	assert.Zero(t, repoLookups, "--default-branch is used")
}

func TestRunGitHubPRPoll_DefaultBranchFromRepository(t *testing.T) {
	prPollTestEnv(t)
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv(forge.VarReviewClientID, "Iv1.review")
	repoLookups := 0
	srv := prPollServer(t, &repoLookups)
	newPRPollClient = func(token string) prPollClient { return gh.New(token).WithBaseURL(srv.URL) }
	_, stderr, err := runPRPoll(t, enabledPRPollConfig, prPollFlags{since: "2026-11-17T18:00:00Z"})
	require.NoError(t, err, stderr)
	assert.Equal(t, 1, repoLookups)
}

func TestRunGitHubPRPoll_StateErrors(t *testing.T) {
	t.Run("corrupt state file is a warning", func(t *testing.T) {
		prPollTestEnv(t)
		t.Setenv("GITHUB_TOKEN", "tok")
		t.Setenv(forge.VarReviewClientID, "Iv1.review")
		repoLookups := 0
		srv := prPollServer(t, &repoLookups)
		newPRPollClient = func(token string) prPollClient { return gh.New(token).WithBaseURL(srv.URL) }
		statePath := filepath.Join(t.TempDir(), "state.json")
		require.NoError(t, os.WriteFile(statePath, []byte("{garbage"), 0o600))
		out, stderr, err := runPRPoll(t, enabledPRPollConfig, prPollFlags{since: "2026-11-17T18:00:00Z", stateFile: statePath, defaultBranch: "main"})
		require.NoError(t, err)
		assert.Contains(t, out, `"agent": "review"`)
		assert.Contains(t, stderr, "is unusable (invalid JSON")
		assert.Contains(t, stderr, "state: changed")
	})
	t.Run("stale marker that cannot be removed", func(t *testing.T) {
		prPollTestEnv(t)
		statePath := filepath.Join(t.TempDir(), "state.json")
		// A non-empty directory at the marker path cannot be removed.
		require.NoError(t, os.MkdirAll(filepath.Join(statePath+prPollStateChangedSuffix, "x"), 0o700))
		_, _, err := runPRPoll(t, "", prPollFlags{stateFile: statePath})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "remove poll state marker")
	})
	t.Run("unwritable state", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing", "state.json")
		state := prpoll.NewState()
		var log strings.Builder
		require.NoError(t, savePRPollState(path, state, &log))
		assert.Equal(t, "state: unchanged\n", log.String())

		corrupt := filepath.Join(t.TempDir(), "corrupt.json")
		require.NoError(t, os.WriteFile(corrupt, []byte("x"), 0o600))
		state = prpoll.LoadState(corrupt, nil)
		require.True(t, state.Changed())
		err := savePRPollState(path, state, &log)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write poll state")
	})
}

func TestReviewBotLogins(t *testing.T) {
	assert.Equal(t, []string{"o-review[bot]", "fullsend-ai-review[bot]"}, reviewBotLogins("o", ""))
	assert.Equal(t, []string{"o-review[bot]", "fullsend-ai-review[bot]", "x-review[bot]"}, reviewBotLogins("o", "x"))
}

func TestPRPollAuthorizer(t *testing.T) {
	repoRoot := t.TempDir()
	fullsendDir := filepath.Join(repoRoot, ".fullsend")
	require.NoError(t, os.Mkdir(fullsendDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "config.yaml"),
		[]byte("version: \"1\"\n"+enabledPRPollConfig+"authorization:\n  - provider: owners_file\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "OWNERS"), []byte("reviewers:\n  - alice\n"), 0o600))
	cfg, err := config.LoadConfig(fullsendDir, config.LoadOpts{})
	require.NoError(t, err)
	authorize := prPollAuthorizer(fullsendDir+"/", cfg)

	event := func(login string, kind normevent.ActorKind, role normevent.ActorRole, verified bool) *normevent.Event {
		return &normevent.Event{
			Repo:   "o/r",
			Source: normevent.Source{System: normevent.SystemGitHub, RawType: "pull_request"},
			Actor:  normevent.Actor{ID: login, Kind: kind, Role: role, RoleVerified: &verified, IsEntityAuthor: true},
		}
	}
	tests := map[string]struct {
		ev   *normevent.Event
		want bool
	}{
		"OWNERS reviewer who is not a collaborator": {ev: event("alice", normevent.ActorHuman, normevent.RoleNone, true), want: true},
		"triage collaborator":                       {ev: event("carol", normevent.ActorHuman, normevent.RoleTriage, true), want: true},
		"read-only collaborator":                    {ev: event("bob", normevent.ActorHuman, normevent.RoleRead, true), want: false},
		"not a collaborator":                        {ev: event("bob", normevent.ActorHuman, normevent.RoleNone, true), want: false},
		"lookup failure for an OWNERS reviewer":     {ev: event("alice", normevent.ActorHuman, normevent.RoleNone, false), want: false},
		"lookup failure for a writer":               {ev: event("carol", normevent.ActorHuman, normevent.RoleWrite, false), want: false},
		"bot with write access":                     {ev: event("dep[bot]", normevent.ActorBot, normevent.RoleWrite, true), want: false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			before := tt.ev.Actor.Role
			assert.Equal(t, tt.want, authorize(tt.ev))
			assert.Equal(t, before, tt.ev.Actor.Role, "the OWNERS upgrade does not mutate the event")
		})
	}

	t.Run("OWNERS ignored when the provider is off", func(t *testing.T) {
		dir := writeJiraPollConfig(t, enabledPRPollConfig)
		require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(dir), "OWNERS"), []byte("reviewers:\n  - alice\n"), 0o600))
		cfg, err := config.LoadConfig(dir, config.LoadOpts{})
		require.NoError(t, err)
		assert.False(t, prPollAuthorizer(dir, cfg)(event("alice", normevent.ActorHuman, normevent.RoleNone, true)))
	})
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
