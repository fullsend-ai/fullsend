package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestDummyPlaybackRuntimeMetadata(t *testing.T) {
	t.Parallel()

	rt := DummyPlaybackRuntime{}
	assert.Equal(t, "dummy-playback", rt.Name())
	assert.Equal(t, "fullsend.dummy-playback", rt.System())
	assert.Contains(t, rt.ConfigDir(), ".dummy-playback")
	assert.Equal(t, sandbox.SandboxWorkspace, rt.WorkspaceDir())
	assert.Nil(t, rt.EnvExports())
}

func TestDummyPlaybackRuntimeNoopMethods(t *testing.T) {
	t.Parallel()

	rt := DummyPlaybackRuntime{}
	assert.NoError(t, rt.ExtractTranscripts("", "", ""))
	assert.NoError(t, rt.ExtractDebugLog("", "", ""))
	assert.Nil(t, rt.ParseTranscriptErrors(""))

	_, ok := rt.ParseTranscriptFile("/nonexistent/path.jsonl")
	assert.False(t, ok)

	var buf bytes.Buffer
	rt.EmitTranscriptErrors(&buf, nil)
}

func TestDummyPlaybackRuntime_Bootstrap(t *testing.T) {
	t.Parallel()

	rt := DummyPlaybackRuntime{}
	err := rt.Bootstrap(stubBootstrapInput{sandboxName: "nonexistent-sandbox"})
	require.Error(t, err)
}

func TestDummyPlaybackRuntime_Bootstrap_NonZeroExit(t *testing.T) {
	t.Parallel()

	rt := DummyPlaybackRuntime{ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
		return "", "sandbox not found", 1, nil
	}}
	err := rt.Bootstrap(stubBootstrapInput{sandboxName: "nonexistent"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox not found")
}

func TestDummyPlaybackRuntime_ClearIterationArtifacts(t *testing.T) {
	t.Parallel()

	rt := DummyPlaybackRuntime{}
	err := rt.ClearIterationArtifacts("nonexistent-sandbox")
	require.Error(t, err)
}

func TestDummyPlaybackRuntime_ClearIterationArtifacts_NonZeroExit(t *testing.T) {
	t.Parallel()

	rt := DummyPlaybackRuntime{ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		if strings.Contains(cmd, "rm -rf") {
			return "", "sandbox not found", 1, nil
		}
		// clearStrayProcesses call succeeds
		return "stray processes killed: 0\n", "", 0, nil
	}}
	err := rt.ClearIterationArtifacts("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox not found")
}

// ClearIterationArtifacts sweeps stray processes before removing files —
// dummy-playback execs run in the real sandbox, so it gets the same
// between-iteration hygiene as the agent runtimes.
func TestDummyPlaybackRuntime_ClearIterationArtifacts_SweepsStraysBeforeFiles(t *testing.T) {
	t.Parallel()

	var cmds []string
	rt := DummyPlaybackRuntime{ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		cmds = append(cmds, cmd)
		return "stray processes killed: 0\n", "", 0, nil
	}}
	require.NoError(t, rt.ClearIterationArtifacts("sb"))
	require.Len(t, cmds, 2)
	assert.Equal(t, killStrayProcessesScript(), cmds[0])
	assert.Contains(t, cmds[1], "rm -rf")
}

// A failed sweep (exit 124 is the only exec failure sandbox.Exec reports)
// is warning-only: the file cleanup still runs and the result is nil.
func TestDummyPlaybackRuntime_ClearIterationArtifacts_SweepFailureIsNotAnError(t *testing.T) {
	t.Parallel()

	var cmds []string
	rt := DummyPlaybackRuntime{ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		cmds = append(cmds, cmd)
		if len(cmds) == 1 {
			return "", "boom", 124, errors.New("command timed out after 15s")
		}
		return "", "", 0, nil
	}}
	require.NoError(t, rt.ClearIterationArtifacts("sb"))
	require.Len(t, cmds, 2)
	assert.Contains(t, cmds[1], "rm -rf")
}

func TestLoadPlaylist(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "playlist.yaml")
	content := "current: 1\nresults:\n  - triage/sufficient\n  - review/approve\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	playlist, err := loadPlaylist(path)
	require.NoError(t, err)
	assert.Equal(t, 1, playlist.Current)
	require.Len(t, playlist.Results, 2)
	assert.Equal(t, "triage/sufficient", playlist.Results[0])
	assert.Equal(t, "review/approve", playlist.Results[1])
}

func TestLoadPlaylist_MissingFile(t *testing.T) {
	t.Parallel()

	_, err := loadPlaylist(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading playlist")
}

func TestLoadPlaylist_InvalidYAML(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(":\n- bad"), 0o644))

	_, err := loadPlaylist(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing playlist")
}

func TestLoadPlaylist_EmptyResults(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "empty.yaml")
	require.NoError(t, os.WriteFile(path, []byte("current: 1\nresults: []\n"), 0o644))

	_, err := loadPlaylist(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no results")
}

func TestLoadPlaylist_CurrentZero(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "zero.yaml")
	require.NoError(t, os.WriteFile(path, []byte("current: 0\nresults:\n  - a\n"), 0o644))

	_, err := loadPlaylist(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "current must be >= 1")
}

// setupResultDir creates a result directory with result.json and returns the fullsend dir.
func setupResultDir(t *testing.T, entryName, resultContent string, companionFiles map[string]string) string {
	t.Helper()
	fullsendDir := t.TempDir()
	entryDir := filepath.Join(fullsendDir, "results", entryName)
	require.NoError(t, os.MkdirAll(entryDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(entryDir, "result.json"), []byte(resultContent), 0o644))

	for relPath, content := range companionFiles {
		absPath := filepath.Join(entryDir, relPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(absPath), 0o755))
		require.NoError(t, os.WriteFile(absPath, []byte(content), 0o644))
	}

	playlistContent := fmt.Sprintf("current: 1\nresults:\n  - %s\n", entryName)
	require.NoError(t, os.MkdirAll(filepath.Join(fullsendDir, "results"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "results", "playlist.yaml"), []byte(playlistContent), 0o644))

	return fullsendDir
}

func TestDummyPlaybackRuntime_RunSuccess(t *testing.T) {
	t.Parallel()

	fullsendDir := setupResultDir(t, "triage/sufficient", `{"action":"sufficient"}`, nil)

	var uploadedContent []byte
	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
		UploadFn: func(_, localPath, _ string) error {
			var err error
			uploadedContent, err = os.ReadFile(localPath)
			return err
		},
		GitCommitFn: func(_ string, p *Playlist) error {
			assert.Equal(t, 2, p.Current)
			return nil
		},
	}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)

	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Equal(t, `{"action":"sufficient"}`, string(uploadedContent))
}

func TestDummyPlaybackRuntime_RunAdvancesIndex(t *testing.T) {
	t.Parallel()

	fullsendDir := t.TempDir()
	resultsDir := filepath.Join(fullsendDir, "results")
	require.NoError(t, os.MkdirAll(filepath.Join(resultsDir, "triage", "sufficient"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(resultsDir, "review", "approve"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(resultsDir, "triage", "sufficient", "result.json"),
		[]byte(`{"action":"sufficient"}`),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(resultsDir, "review", "approve", "result.json"),
		[]byte(`{"action":"approve"}`),
		0o644,
	))

	playlistContent := "current: 1\nresults:\n  - triage/sufficient\n  - review/approve\n"
	playlistPath := filepath.Join(resultsDir, "playlist.yaml")
	require.NoError(t, os.WriteFile(playlistPath, []byte(playlistContent), 0o644))

	var uploadedContents []string
	commitCount := 0
	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
		UploadFn: func(_, localPath, _ string) error {
			data, err := os.ReadFile(localPath)
			if err != nil {
				return err
			}
			uploadedContents = append(uploadedContents, string(data))
			return nil
		},
		GitCommitFn: func(path string, p *Playlist) error {
			commitCount++
			return localAdvancePlaylist(path, p)
		},
	}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)

	exit, err = rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)
	require.NoError(t, err)
	assert.Equal(t, 0, exit)

	require.Len(t, uploadedContents, 2)
	assert.Equal(t, `{"action":"sufficient"}`, uploadedContents[0])
	assert.Equal(t, `{"action":"approve"}`, uploadedContents[1])
	assert.Equal(t, 2, commitCount)
}

func TestDummyPlaybackRuntime_RunExhausted(t *testing.T) {
	t.Parallel()

	fullsendDir := setupResultDir(t, "triage/sufficient", `{"action":"sufficient"}`, nil)
	playlistPath := filepath.Join(fullsendDir, "results", "playlist.yaml")
	require.NoError(t, os.WriteFile(playlistPath, []byte("current: 2\nresults:\n  - triage/sufficient\n"), 0o644))

	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
	}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)
	assert.Equal(t, 1, exit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "playlist exhausted")
}

func TestDummyPlaybackRuntime_RunMissingResult(t *testing.T) {
	t.Parallel()

	fullsendDir := t.TempDir()
	resultsDir := filepath.Join(fullsendDir, "results")
	require.NoError(t, os.MkdirAll(resultsDir, 0o755))

	playlistContent := "current: 1\nresults:\n  - missing/nonexistent\n"
	require.NoError(t, os.WriteFile(filepath.Join(resultsDir, "playlist.yaml"), []byte(playlistContent), 0o644))

	rt := DummyPlaybackRuntime{}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)
	assert.Equal(t, 1, exit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading result")
}

func TestDummyPlaybackRuntime_RunPathTraversal(t *testing.T) {
	t.Parallel()

	fullsendDir := t.TempDir()
	resultsDir := filepath.Join(fullsendDir, "results")
	require.NoError(t, os.MkdirAll(resultsDir, 0o755))

	playlistContent := "current: 1\nresults:\n  - ../../etc/passwd\n"
	require.NoError(t, os.WriteFile(filepath.Join(resultsDir, "playlist.yaml"), []byte(playlistContent), 0o644))

	rt := DummyPlaybackRuntime{}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)
	assert.Equal(t, 1, exit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes results directory")
}

func TestDummyPlaybackRuntime_RunMissingPlaylist(t *testing.T) {
	t.Parallel()

	rt := DummyPlaybackRuntime{}
	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: t.TempDir(),
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)
	assert.Equal(t, 1, exit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading playlist")
}

func TestDummyPlaybackRuntime_RunCancelledContext(t *testing.T) {
	t.Parallel()

	fullsendDir := setupResultDir(t, "triage/sufficient", `{"action":"sufficient"}`, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rt := DummyPlaybackRuntime{}
	exit, err := rt.Run(ctx, RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)
	assert.Equal(t, 1, exit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
}

func TestDummyPlaybackRuntime_GitCommitFailureIsWarning(t *testing.T) {
	t.Parallel()

	fullsendDir := setupResultDir(t, "triage/sufficient", `{"action":"sufficient"}`, nil)

	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
		UploadFn: func(_, _, _ string) error { return nil },
		GitCommitFn: func(_ string, _ *Playlist) error {
			return fmt.Errorf("push failed: remote rejected")
		},
	}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)

	require.NoError(t, err)
	assert.Equal(t, 0, exit)
}

func TestDummyPlaybackRuntime_CompanionFiles(t *testing.T) {
	t.Parallel()

	companions := map[string]string{
		"main.go":     "package main\nfunc main() {}\n",
		"pkg/util.go": "package pkg\nfunc Util() {}\n",
	}
	fullsendDir := setupResultDir(t, "code/implemented", `{"target_branch":"main"}`, companions)

	uploaded := map[string]string{}
	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
		UploadFn: func(_, localPath, remoteDest string) error {
			data, err := os.ReadFile(localPath)
			if err != nil {
				return err
			}
			uploaded[remoteDest] = string(data)
			return nil
		},
		GitCommitFn: func(_ string, _ *Playlist) error { return nil },
	}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)

	require.NoError(t, err)
	assert.Equal(t, 0, exit)

	resultDest := filepath.Join(sandbox.SandboxWorkspace, "output", "agent-result.json")
	assert.Contains(t, uploaded, resultDest)
	assert.Equal(t, `{"target_branch":"main"}`, uploaded[resultDest])

	mainDest := filepath.Join(sandbox.SandboxWorkspace, "main.go")
	assert.Contains(t, uploaded, mainDest)
	assert.Equal(t, "package main\nfunc main() {}\n", uploaded[mainDest])

	utilDest := filepath.Join(sandbox.SandboxWorkspace, "pkg/util.go")
	assert.Contains(t, uploaded, utilDest)
	assert.Equal(t, "package pkg\nfunc Util() {}\n", uploaded[utilDest])
}

func TestDummyPlaybackRuntime_NoCompanionFiles(t *testing.T) {
	t.Parallel()

	fullsendDir := setupResultDir(t, "triage/sufficient", `{"action":"sufficient"}`, nil)

	uploadCount := 0
	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
		UploadFn: func(_, _, _ string) error {
			uploadCount++
			return nil
		},
		GitCommitFn: func(_ string, _ *Playlist) error { return nil },
	}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)

	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	assert.Equal(t, 1, uploadCount) // only agent-result.json
}

func TestInjectReviewMetadata(t *testing.T) {
	t.Run("injects all fields when env vars set", func(t *testing.T) {
		t.Setenv("PR_HEAD_SHA", "abc123def456abc123def456abc123def456abc1")
		t.Setenv("STATUS_NUMBER", "42")
		t.Setenv("GITHUB_REPOSITORY", "fullsend-ai-test/my-repo")
		content := []byte(`{"action":"approve","body":"looks good"}`)
		result := injectReviewMetadata(content, "github", ui.New(io.Discard))

		var parsed map[string]any
		require.NoError(t, json.Unmarshal(result, &parsed))
		assert.Equal(t, "abc123def456abc123def456abc123def456abc1", parsed["head_sha"])
		assert.Equal(t, float64(42), parsed["pr_number"])
		assert.Equal(t, "fullsend-ai-test/my-repo", parsed["repo"])
		assert.Equal(t, "approve", parsed["action"])
	})

	t.Run("injects only head_sha when others not set", func(t *testing.T) {
		t.Setenv("PR_HEAD_SHA", "abc123def456abc123def456abc123def456abc1")
		t.Setenv("STATUS_NUMBER", "")
		t.Setenv("GITHUB_REPOSITORY", "")
		content := []byte(`{"action":"approve","body":"ok"}`)
		result := injectReviewMetadata(content, "github", ui.New(io.Discard))

		var parsed map[string]any
		require.NoError(t, json.Unmarshal(result, &parsed))
		assert.Equal(t, "abc123def456abc123def456abc123def456abc1", parsed["head_sha"])
		assert.Nil(t, parsed["pr_number"])
		assert.Nil(t, parsed["repo"])
	})

	t.Run("skips when no env vars set", func(t *testing.T) {
		t.Setenv("PR_HEAD_SHA", "")
		t.Setenv("STATUS_NUMBER", "")
		t.Setenv("GITHUB_REPOSITORY", "")
		content := []byte(`{"action":"approve"}`)
		result := injectReviewMetadata(content, "github", ui.New(io.Discard))
		assert.Equal(t, content, result)
	})

	t.Run("skips when result has no action field", func(t *testing.T) {
		t.Setenv("PR_HEAD_SHA", "abc123def456abc123def456abc123def456abc1")
		content := []byte(`{"pr_number":1,"summary":"done"}`)
		result := injectReviewMetadata(content, "github", ui.New(io.Discard))
		assert.Equal(t, content, result)
	})

	t.Run("uses CI_PROJECT_PATH for gitlab forge", func(t *testing.T) {
		t.Setenv("PR_HEAD_SHA", "abc123def456abc123def456abc123def456abc1")
		t.Setenv("STATUS_NUMBER", "7")
		t.Setenv("GITHUB_REPOSITORY", "")
		t.Setenv("CI_PROJECT_PATH", "my-group/my-repo")
		content := []byte(`{"action":"approve","body":"lgtm"}`)
		result := injectReviewMetadata(content, "gitlab", ui.New(io.Discard))

		var parsed map[string]any
		require.NoError(t, json.Unmarshal(result, &parsed))
		assert.Equal(t, "my-group/my-repo", parsed["repo"])
		assert.Equal(t, "abc123def456abc123def456abc123def456abc1", parsed["head_sha"])
	})

	t.Run("skips non-review actions like triage", func(t *testing.T) {
		t.Setenv("PR_HEAD_SHA", "abc123def456abc123def456abc123def456abc1")
		t.Setenv("STATUS_NUMBER", "42")
		t.Setenv("GITHUB_REPOSITORY", "fullsend-ai-test/my-repo")
		content := []byte(`{"action":"sufficient","labels":["bug"]}`)
		result := injectReviewMetadata(content, "github", ui.New(io.Discard))
		assert.Equal(t, content, result)
	})
}

func TestDummyPlaybackRuntime_FixCommitsToCurrentBranch(t *testing.T) {
	t.Parallel()

	companions := map[string]string{
		"repo/src/fix.go": "package src\nfunc Fix() {}\n",
	}
	fullsendDir := setupResultDir(t, "fix/success", `{"pr_number":1}`, companions)

	var execCmds []string
	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
			execCmds = append(execCmds, cmd)
			return "", "", 0, nil
		},
		UploadFn:    func(_, _, _ string) error { return nil },
		GitCommitFn: func(_ string, _ *Playlist) error { return nil },
	}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     "/sandbox/workspace/repo",
	}, ui.New(io.Discard), time.Now(), nil)

	require.NoError(t, err)
	assert.Equal(t, 0, exit)

	// Should NOT contain "checkout -b" (no new branch for fix entries)
	for _, cmd := range execCmds {
		assert.NotContains(t, cmd, "checkout -b", "fix entries should not create new branches")
	}

	// Should contain a commit
	hasCommit := false
	for _, cmd := range execCmds {
		if strings.Contains(cmd, "git") && strings.Contains(cmd, "commit") {
			hasCommit = true
		}
	}
	assert.True(t, hasCommit, "fix entries should commit changes")
}

func TestDummyPlaybackRuntime_RepoPrefixCompanionFiles(t *testing.T) {
	t.Parallel()

	companions := map[string]string{
		"repo/src/fix.go": "package src\nfunc Fix() {}\n",
		"repo/README.md":  "# Updated README\n",
		"output/log.txt":  "some log output",
	}
	fullsendDir := setupResultDir(t, "code/implemented", `{"target_branch":"main"}`, companions)

	repoDir := "/sandbox/workspace/target-repo"
	uploaded := map[string]string{}
	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
		UploadFn: func(_, localPath, remoteDest string) error {
			data, err := os.ReadFile(localPath)
			if err != nil {
				return err
			}
			uploaded[remoteDest] = string(data)
			return nil
		},
		GitCommitFn: func(_ string, _ *Playlist) error { return nil },
	}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     repoDir,
	}, ui.New(io.Discard), time.Now(), nil)

	require.NoError(t, err)
	assert.Equal(t, 0, exit)

	// repo/ prefix files should go into repoDir
	assert.Equal(t, "package src\nfunc Fix() {}\n", uploaded[repoDir+"/src/fix.go"])
	assert.Equal(t, "# Updated README\n", uploaded[repoDir+"/README.md"])

	// non-repo files go to workspace root
	assert.Equal(t, "some log output", uploaded[filepath.Join(sandbox.SandboxWorkspace, "output/log.txt")])
}

func TestParsePlaybackCommentRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		wantOK     bool
		wantCLI    string
		wantPath   string
		wantMethod string
	}{
		{
			name:   "empty",
			input:  "",
			wantOK: false,
		},
		{
			name:       "legacy single-line defaults to gh",
			input:      "/repos/org/repo/issues/comments/42",
			wantOK:     true,
			wantCLI:    "gh",
			wantPath:   "/repos/org/repo/issues/comments/42",
			wantMethod: "PATCH",
		},
		{
			name:       "github two-line",
			input:      "gh\n/repos/org/repo/issues/comments/42",
			wantOK:     true,
			wantCLI:    "gh",
			wantPath:   "/repos/org/repo/issues/comments/42",
			wantMethod: "PATCH",
		},
		{
			name:       "gitlab two-line",
			input:      "glab\n/projects/org%2Frepo/issues/1/notes/99",
			wantOK:     true,
			wantCLI:    "glab",
			wantPath:   "/projects/org%2Frepo/issues/1/notes/99",
			wantMethod: "PUT",
		},
		{
			name:   "cli but no path",
			input:  "gh\n  \n",
			wantOK: false,
		},
		{
			name:   "invalid cli rejected",
			input:  "curl\n/some/path",
			wantOK: false,
		},
		{
			name:   "bash cli rejected",
			input:  "bash\n-c whoami",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, ok := parsePlaybackCommentRef(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.wantCLI, ref.cli)
				assert.Equal(t, tt.wantPath, ref.path)
				assert.Equal(t, tt.wantMethod, ref.method)
			}
		})
	}
}
