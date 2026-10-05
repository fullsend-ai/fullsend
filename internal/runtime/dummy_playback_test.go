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

	"github.com/fullsend-ai/fullsend/internal/forge"
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
	t.Setenv("GITHUB_REPOSITORY", "")
	t.Setenv("CI_PROJECT_PATH", "")
	t.Setenv("PR_HEAD_SHA", "")
	t.Setenv("STATUS_NUMBER", "")

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

func TestDummyPlaybackRuntime_RunSymlinkRejected(t *testing.T) {
	t.Parallel()

	fullsendDir := t.TempDir()
	entryDir := filepath.Join(fullsendDir, "results", "code", "round-1")
	require.NoError(t, os.MkdirAll(filepath.Join(entryDir, "repo"), 0o755))

	// Write the main result file.
	require.NoError(t, os.WriteFile(filepath.Join(entryDir, "result.json"), []byte(`{"target_branch":"main"}`), 0o644))

	// Plant a symlink as a companion file.
	target := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(target, []byte("sensitive data"), 0o644))
	require.NoError(t, os.Symlink(target, filepath.Join(entryDir, "repo", "evil.go")))

	playlistContent := "current: 1\nresults:\n  - code/round-1\n"
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "results", "playlist.yaml"), []byte(playlistContent), 0o644))

	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
		UploadFn:    func(_, _, _ string) error { return nil },
		GitCommitFn: func(_ string, _ *Playlist) error { return nil },
	}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)
	assert.Equal(t, 1, exit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlinks are not allowed")
}

func TestDummyPlaybackRuntime_RunResultSymlinkRejected(t *testing.T) {
	t.Parallel()

	fullsendDir := t.TempDir()
	entryDir := filepath.Join(fullsendDir, "results", "triage", "sufficient")
	require.NoError(t, os.MkdirAll(entryDir, 0o755))

	// Plant a symlink as result.json itself — the Lstat guard should reject it.
	target := filepath.Join(t.TempDir(), "secret.json")
	require.NoError(t, os.WriteFile(target, []byte(`{"action":"sufficient"}`), 0o644))
	require.NoError(t, os.Symlink(target, filepath.Join(entryDir, "result.json")))

	playlistContent := "current: 1\nresults:\n  - triage/sufficient\n"
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "results", "playlist.yaml"), []byte(playlistContent), 0o644))

	rt := DummyPlaybackRuntime{}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)
	assert.Equal(t, 1, exit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlinks are not allowed")
}

func TestDummyPlaybackRuntime_RunEntryDirSymlinkRejected(t *testing.T) {
	t.Parallel()

	fullsendDir := t.TempDir()
	realDir := filepath.Join(fullsendDir, "results", "real-entry")
	require.NoError(t, os.MkdirAll(realDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(realDir, "result.json"), []byte(`{"action":"ok"}`), 0o644))

	// Create a symlink entry that points to the real entry.
	symlinkEntry := filepath.Join(fullsendDir, "results", "symlink-entry")
	require.NoError(t, os.Symlink(realDir, symlinkEntry))

	playlistContent := "current: 1\nresults:\n  - symlink-entry\n"
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "results", "playlist.yaml"), []byte(playlistContent), 0o644))

	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
		UploadFn:    func(_, _, _ string) error { return nil },
		GitCommitFn: func(_ string, _ *Playlist) error { return nil },
	}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)
	assert.Equal(t, 1, exit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlinks are not allowed")
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

func TestDummyPlaybackRuntime_CompanionResultJsonNotSkipped(t *testing.T) {
	t.Parallel()

	// A companion file named result.json inside a subdirectory (e.g.
	// repo/result.json) must NOT be skipped — only the top-level
	// entry result.json should be excluded.
	companions := map[string]string{
		"repo/result.json": `{"nested": true}`,
	}
	fullsendDir := setupResultDir(t, "code/nested", `{"target_branch":"main"}`, companions)

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

	repoDir := "/sandbox/workspace/target-repo"
	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     repoDir,
	}, ui.New(io.Discard), time.Now(), nil)

	require.NoError(t, err)
	assert.Equal(t, 0, exit)

	// The nested repo/result.json should be uploaded as a companion file.
	nestedDest := repoDir + "/result.json"
	assert.Contains(t, uploaded, nestedDest)
	assert.Equal(t, `{"nested": true}`, uploaded[nestedDest])
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
		name             string
		input            string
		wantOK           bool
		wantOwner        string
		wantRepo         string
		wantCommentID    int
		wantNoteableType string
		wantNoteableIID  int
	}{
		{
			name:   "empty",
			input:  "",
			wantOK: false,
		},
		{
			name:          "legacy single-line defaults to gh",
			input:         "/repos/org/repo/issues/comments/42",
			wantOK:        true,
			wantOwner:     "org",
			wantRepo:      "repo",
			wantCommentID: 42,
		},
		{
			name:          "legacy single-line with trailing newline",
			input:         "/repos/org/repo/issues/comments/42\n",
			wantOK:        true,
			wantOwner:     "org",
			wantRepo:      "repo",
			wantCommentID: 42,
		},
		{
			name:          "github two-line",
			input:         "gh\n/repos/org/repo/issues/comments/42",
			wantOK:        true,
			wantOwner:     "org",
			wantRepo:      "repo",
			wantCommentID: 42,
		},
		{
			name:             "gitlab two-line",
			input:            "glab\n/projects/org%2Frepo/issues/1/notes/99",
			wantOK:           true,
			wantOwner:        "org",
			wantRepo:         "repo",
			wantCommentID:    99,
			wantNoteableType: "issues",
			wantNoteableIID:  1,
		},
		{
			name:             "gitlab nested group",
			input:            "glab\n/projects/group%2Fsubgroup%2Frepo/merge_requests/3/notes/7",
			wantOK:           true,
			wantOwner:        "group/subgroup",
			wantRepo:         "repo",
			wantCommentID:    7,
			wantNoteableType: "merge_requests",
			wantNoteableIID:  3,
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
		{
			name:   "argument injection in legacy single-line",
			input:  "--hostname=attacker.com",
			wantOK: false,
		},
		{
			name:   "argument injection in two-line path",
			input:  "gh\n--hostname=attacker.com",
			wantOK: false,
		},
		{
			name:   "relative path rejected in two-line",
			input:  "gh\nrepos/org/repo/issues/comments/42",
			wantOK: false,
		},
		{
			name:   "embedded newline in path rejected",
			input:  "gh\n/repos/org/repo/issues/comments/42\nextra-line",
			wantOK: false,
		},
		{
			name:   "github path with non-numeric comment id rejected",
			input:  "gh\n/repos/org/repo/issues/comments/abc",
			wantOK: false,
		},
		{
			name:   "gitlab path missing project segment rejected",
			input:  "glab\n/projects//issues/1/notes/99",
			wantOK: false,
		},
		{
			name:   "gitlab path with empty decoded owner rejected",
			input:  "glab\n/projects/%2Ffoo/issues/1/notes/99",
			wantOK: false,
		},
		{
			name:   "gitlab path with unencoded project id rejected",
			input:  "glab\n/projects/myproject/issues/1/notes/99",
			wantOK: false,
		},
		{
			// Regression: a query delimiter in the repo field used to slip
			// through the old `[^/]+/[^/]+` owner/repo capture, so the
			// request path built from it (/repos/org/repo?x=/issues/...)
			// addressed /repos/org/repo with the rest treated as query data.
			name:   "github path with query delimiter in repo rejected",
			input:  "gh\n/repos/org/repo?x=/issues/comments/42",
			wantOK: false,
		},
		{
			name:   "github path with query delimiter in owner rejected",
			input:  "gh\n/repos/org?x=y/repo/issues/comments/42",
			wantOK: false,
		},
		{
			name:   "github path with fragment delimiter in repo rejected",
			input:  "gh\n/repos/org/repo#frag/issues/comments/42",
			wantOK: false,
		},
		{
			name:   "github path with control character in repo rejected",
			input:  "gh\n/repos/org/re\tpo/issues/comments/42",
			wantOK: false,
		},
		{
			// Regression: the noteable type and IID used to be discarded
			// (the non-capturing group matched either "issues" or
			// "merge_requests" without recording which), so a note whose
			// actual parent type differs from the resolved client's fixed
			// noteTarget (e.g. an issue reference during MR CI) could not
			// be found. They are now preserved on the ref.
			name:             "gitlab merge request reference preserves type and iid",
			input:            "glab\n/projects/org%2Frepo/merge_requests/12/notes/55",
			wantOK:           true,
			wantOwner:        "org",
			wantRepo:         "repo",
			wantCommentID:    55,
			wantNoteableType: "merge_requests",
			wantNoteableIID:  12,
		},
		{
			// Regression: GitLab allows a namespace (group/user) to start
			// with an underscore, but the path used to be rejected before
			// decoding because the whole combined segment had to start
			// alphanumeric.
			name:             "gitlab namespace with leading underscore accepted",
			input:            "glab\n/projects/_group%2Frepo/issues/1/notes/99",
			wantOK:           true,
			wantOwner:        "_group",
			wantRepo:         "repo",
			wantCommentID:    99,
			wantNoteableType: "issues",
			wantNoteableIID:  1,
		},
		{
			// Regression: GitLab allows a project name to end with an
			// underscore or hyphen, but the path used to be rejected
			// because the whole combined segment had to end alphanumeric.
			name:             "gitlab project with trailing underscore accepted",
			input:            "glab\n/projects/org%2Frepo_/issues/1/notes/99",
			wantOK:           true,
			wantOwner:        "org",
			wantRepo:         "repo_",
			wantCommentID:    99,
			wantNoteableType: "issues",
			wantNoteableIID:  1,
		},
		{
			name:             "gitlab project with trailing hyphen accepted",
			input:            "glab\n/projects/org%2Frepo-/merge_requests/3/notes/7",
			wantOK:           true,
			wantOwner:        "org",
			wantRepo:         "repo-",
			wantCommentID:    7,
			wantNoteableType: "merge_requests",
			wantNoteableIID:  3,
		},
		{
			// A single-character namespace must still be alphanumeric or an
			// underscore; GitLab does not allow a bare hyphen or dot.
			name:   "gitlab single-char namespace hyphen rejected",
			input:  "glab\n/projects/-%2Frepo/issues/1/notes/99",
			wantOK: false,
		},
		{
			// GitLab reserves a trailing ".git" suffix on project names.
			name:   "gitlab project with reserved git suffix rejected",
			input:  "glab\n/projects/org%2Frepo.git/issues/1/notes/99",
			wantOK: false,
		},
		{
			// A multi-character namespace must still start with an
			// alphanumeric, underscore, or dot; GitLab does not allow it to
			// start with a hyphen.
			name:   "gitlab namespace with invalid leading hyphen rejected",
			input:  "glab\n/projects/-grp%2Frepo/issues/1/notes/99",
			wantOK: false,
		},
		{
			// A multi-character namespace must end with an alphanumeric,
			// underscore, or hyphen; GitLab does not allow a trailing dot.
			name:   "gitlab namespace with invalid trailing dot rejected",
			input:  "glab\n/projects/grp.%2Frepo/issues/1/notes/99",
			wantOK: false,
		},
		{
			// Regression: a percent-encoded delimiter decoded into the
			// middle of a namespace component (rather than at a component
			// boundary) must still be rejected; only the outer path's
			// pre-decode charset was validated before, so a disallowed
			// decoded character inside a single component slipped through.
			name:   "gitlab namespace with disallowed decoded character rejected",
			input:  "glab\n/projects/gr%3Fp%2Frepo/issues/1/notes/99",
			wantOK: false,
		},
		{
			// A project (repo leaf) name must start with an alphanumeric,
			// underscore, or dot; GitLab does not allow it to start with a
			// hyphen.
			name:   "gitlab project with invalid leading hyphen rejected",
			input:  "glab\n/projects/org%2F-repo/issues/1/notes/99",
			wantOK: false,
		},
		{
			// Same decoded-delimiter regression as above, but for the
			// project (repo leaf) component instead of a namespace.
			name:   "gitlab project with disallowed decoded character rejected",
			input:  "glab\n/projects/org%2Fre%3Fpo/issues/1/notes/99",
			wantOK: false,
		},
		{
			// A trailing encoded slash decodes to an empty project (repo
			// leaf) component, which must still be rejected even though
			// decodeCommentPath now splits on "/" instead of requiring the
			// whole combined segment to be non-empty at both ends.
			name:   "gitlab path with trailing encoded slash rejected",
			input:  "glab\n/projects/org%2F/issues/1/notes/99",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, ok := parsePlaybackCommentRef(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.wantOwner, ref.owner)
				assert.Equal(t, tt.wantRepo, ref.repo)
				assert.Equal(t, tt.wantCommentID, ref.commentID)
				assert.Equal(t, tt.wantNoteableType, ref.noteableType)
				assert.Equal(t, tt.wantNoteableIID, ref.noteableIID)
			}
		})
	}
}

func TestReadPlaybackComment(t *testing.T) {
	t.Parallel()

	t.Run("no comment file", func(t *testing.T) {
		t.Parallel()
		_, _, ok := readPlaybackComment(context.Background(), t.TempDir(), forge.NewFakeClient())
		assert.False(t, ok)
	})

	t.Run("invalid comment ref", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, playbackCommentFile), []byte("invalid"), 0o644))
		_, _, ok := readPlaybackComment(context.Background(), dir, forge.NewFakeClient())
		assert.False(t, ok)
	})

	t.Run("nil client", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, playbackCommentFile), []byte("gh\n/repos/org/repo/issues/comments/1"), 0o644))
		_, _, ok := readPlaybackComment(context.Background(), dir, nil)
		assert.False(t, ok)
	})

	t.Run("api call fails", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, playbackCommentFile), []byte("gh\n/repos/org/repo/issues/comments/1"), 0o644))
		fc := forge.NewFakeClient()
		fc.Errors = map[string]error{"GetIssueComment": fmt.Errorf("network error")}
		_, _, ok := readPlaybackComment(context.Background(), dir, fc)
		assert.False(t, ok)
	})

	t.Run("body missing prefix", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, playbackCommentFile), []byte("gh\n/repos/org/repo/issues/comments/1"), 0o644))
		fc := forge.NewFakeClient()
		fc.IssueComments = map[string][]forge.IssueComment{"org/repo/0": {{ID: 1, Body: "some unrelated body"}}}
		_, _, ok := readPlaybackComment(context.Background(), dir, fc)
		assert.False(t, ok)
	})

	t.Run("body non-numeric value", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, playbackCommentFile), []byte("gh\n/repos/org/repo/issues/comments/1"), 0o644))
		fc := forge.NewFakeClient()
		fc.IssueComments = map[string][]forge.IssueComment{"org/repo/0": {{ID: 1, Body: "playback-current: abc"}}}
		_, _, ok := readPlaybackComment(context.Background(), dir, fc)
		assert.False(t, ok)
	})

	t.Run("body value less than 1", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, playbackCommentFile), []byte("gh\n/repos/org/repo/issues/comments/1"), 0o644))
		fc := forge.NewFakeClient()
		fc.IssueComments = map[string][]forge.IssueComment{"org/repo/0": {{ID: 1, Body: "playback-current: 0"}}}
		_, _, ok := readPlaybackComment(context.Background(), dir, fc)
		assert.False(t, ok)
	})

	t.Run("success returns correct value and ref", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, playbackCommentFile), []byte("gh\n/repos/org/repo/issues/comments/42"), 0o644))
		fc := forge.NewFakeClient()
		fc.IssueComments = map[string][]forge.IssueComment{"org/repo/0": {{ID: 42, Body: "playback-current: 3"}}}
		val, ref, ok := readPlaybackComment(context.Background(), dir, fc)
		require.True(t, ok)
		assert.Equal(t, 3, val)
		assert.Equal(t, "org", ref.owner)
		assert.Equal(t, "repo", ref.repo)
		assert.Equal(t, 42, ref.commentID)
	})

	t.Run("gitlab ref", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, playbackCommentFile), []byte("glab\n/projects/org%2Frepo/issues/1/notes/99"), 0o644))
		fc := forge.NewFakeClient()
		fc.IssueComments = map[string][]forge.IssueComment{"org/repo/0": {{ID: 99, Body: "playback-current: 5"}}}
		val, ref, ok := readPlaybackComment(context.Background(), dir, fc)
		require.True(t, ok)
		assert.Equal(t, 5, val)
		assert.Equal(t, "org", ref.owner)
		assert.Equal(t, "repo", ref.repo)
		assert.Equal(t, 99, ref.commentID)
	})

	t.Run("gitlab merge request ref uses direct addressing, not the scan", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, playbackCommentFile), []byte("glab\n/projects/org%2Frepo/merge_requests/12/notes/55"), 0o644))
		fc := forge.NewFakeClient()
		// Fail the ID-only scan GetIssueComment uses, so the test proves
		// readPlaybackComment took the direct GetNoteOnParent path instead
		// -- the regression this guards is a GitLab client built for one
		// noteable type (e.g. issues) failing to find a note whose actual
		// parent is the other type (merge requests).
		fc.Errors = map[string]error{"GetIssueComment": fmt.Errorf("must not be called when the parent is known")}
		fc.IssueComments = map[string][]forge.IssueComment{"org/repo/0": {{ID: 55, Body: "playback-current: 6"}}}
		val, ref, ok := readPlaybackComment(context.Background(), dir, fc)
		require.True(t, ok)
		assert.Equal(t, 6, val)
		assert.Equal(t, 55, ref.commentID)
		assert.Equal(t, "merge_requests", ref.noteableType)
		assert.Equal(t, 12, ref.noteableIID)
		require.Len(t, fc.GetNoteOnParentCalls, 1)
		assert.Equal(t, "org", fc.GetNoteOnParentCalls[0].Owner)
		assert.Equal(t, "repo", fc.GetNoteOnParentCalls[0].Repo)
		assert.Equal(t, "merge_requests", fc.GetNoteOnParentCalls[0].ParentType)
		assert.Equal(t, 12, fc.GetNoteOnParentCalls[0].ParentIID)
		assert.Equal(t, 55, fc.GetNoteOnParentCalls[0].NoteID)
	})
}

func TestUpdatePlaybackComment(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		fc := forge.NewFakeClient()
		fc.IssueComments = map[string][]forge.IssueComment{"org/repo/0": {{ID: 42, Body: "playback-current: 1"}}}
		ref := playbackCommentRef{owner: "org", repo: "repo", commentID: 42}
		err := updatePlaybackComment(context.Background(), fc, ref, 4)
		require.NoError(t, err)
		require.Len(t, fc.UpdatedComments, 1)
		assert.Equal(t, "org", fc.UpdatedComments[0].Owner)
		assert.Equal(t, "repo", fc.UpdatedComments[0].Repo)
		assert.Equal(t, 42, fc.UpdatedComments[0].CommentID)
		assert.Equal(t, "playback-current: 4", fc.UpdatedComments[0].Body)
	})

	t.Run("api error", func(t *testing.T) {
		t.Parallel()
		fc := forge.NewFakeClient()
		fc.Errors = map[string]error{"UpdateIssueComment": fmt.Errorf("api call failed")}
		ref := playbackCommentRef{owner: "org", repo: "repo", commentID: 42}
		err := updatePlaybackComment(context.Background(), fc, ref, 4)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "updating playback comment")
	})

	t.Run("nil client", func(t *testing.T) {
		t.Parallel()
		ref := playbackCommentRef{owner: "org", repo: "repo", commentID: 42}
		err := updatePlaybackComment(context.Background(), nil, ref, 4)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "updating playback comment")
	})

	t.Run("gitlab ref", func(t *testing.T) {
		t.Parallel()
		fc := forge.NewFakeClient()
		fc.IssueComments = map[string][]forge.IssueComment{"org/repo/0": {{ID: 99, Body: "playback-current: 1"}}}
		ref := playbackCommentRef{owner: "org", repo: "repo", commentID: 99}
		err := updatePlaybackComment(context.Background(), fc, ref, 7)
		require.NoError(t, err)
		require.Len(t, fc.UpdatedComments, 1)
		assert.Equal(t, 99, fc.UpdatedComments[0].CommentID)
		assert.Equal(t, "playback-current: 7", fc.UpdatedComments[0].Body)
	})

	t.Run("gitlab merge request ref uses direct addressing, not the scan", func(t *testing.T) {
		t.Parallel()
		fc := forge.NewFakeClient()
		fc.Errors = map[string]error{"UpdateIssueComment": fmt.Errorf("must not be called when the parent is known")}
		fc.IssueComments = map[string][]forge.IssueComment{"org/repo/0": {{ID: 55, Body: "playback-current: 1"}}}
		ref := playbackCommentRef{owner: "org", repo: "repo", commentID: 55, noteableType: "merge_requests", noteableIID: 12}
		err := updatePlaybackComment(context.Background(), fc, ref, 8)
		require.NoError(t, err)
		require.Len(t, fc.UpdateNoteOnParentCalls, 1)
		assert.Equal(t, "org", fc.UpdateNoteOnParentCalls[0].Owner)
		assert.Equal(t, "repo", fc.UpdateNoteOnParentCalls[0].Repo)
		assert.Equal(t, "merge_requests", fc.UpdateNoteOnParentCalls[0].ParentType)
		assert.Equal(t, 12, fc.UpdateNoteOnParentCalls[0].ParentIID)
		assert.Equal(t, 55, fc.UpdateNoteOnParentCalls[0].NoteID)
		assert.Equal(t, "playback-current: 8", fc.UpdateNoteOnParentCalls[0].Body)
	})
}

func TestDummyPlaybackRuntime_RunWithPlaybackComment(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	t.Setenv("CI_PROJECT_PATH", "")
	t.Setenv("PR_HEAD_SHA", "")
	t.Setenv("STATUS_NUMBER", "")

	fullsendDir := setupResultDir(t, "triage/sufficient", `{"action":"sufficient"}`, nil)

	// Write a playback-comment-url file
	require.NoError(t, os.WriteFile(
		filepath.Join(fullsendDir, playbackCommentFile),
		[]byte("gh\n/repos/org/repo/issues/comments/1"),
		0o644,
	))

	fc := forge.NewFakeClient()
	fc.IssueComments = map[string][]forge.IssueComment{"org/repo/0": {{ID: 1, Body: "playback-current: 1"}}}
	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
		UploadFn:    func(_, _, _ string) error { return nil },
		GitCommitFn: func(_ string, _ *Playlist) error { return nil },
	}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
		ForgeClient: fc,
	}, ui.New(io.Discard), time.Now(), nil)

	require.NoError(t, err)
	assert.Equal(t, 0, exit)
	require.Len(t, fc.UpdatedComments, 1, "should update the tracking comment after serving the entry")
	assert.Equal(t, "playback-current: 2", fc.UpdatedComments[0].Body)
}

func TestDummyPlaybackRuntime_RunTrackingCommentInvalidPosition(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	t.Setenv("CI_PROJECT_PATH", "")
	t.Setenv("PR_HEAD_SHA", "")
	t.Setenv("STATUS_NUMBER", "")

	fullsendDir := setupResultDir(t, "triage/sufficient", `{"action":"sufficient"}`, nil)

	// Write a playback-comment-url file
	require.NoError(t, os.WriteFile(
		filepath.Join(fullsendDir, playbackCommentFile),
		[]byte("gh\n/repos/org/repo/issues/comments/1"),
		0o644,
	))

	fc := forge.NewFakeClient()
	// Return position beyond the playlist length.
	fc.IssueComments = map[string][]forge.IssueComment{"org/repo/0": {{ID: 1, Body: "playback-current: 99"}}}
	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
	}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
		ForgeClient: fc,
	}, ui.New(io.Discard), time.Now(), nil)

	assert.Equal(t, 1, exit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracking comment returned invalid position")
}

func TestDummyPlaybackRuntime_RunEmptyEntryName(t *testing.T) {
	t.Parallel()

	fullsendDir := t.TempDir()
	resultsDir := filepath.Join(fullsendDir, "results")
	require.NoError(t, os.MkdirAll(resultsDir, 0o755))

	playlistContent := "current: 1\nresults:\n  - '  '\n"
	require.NoError(t, os.WriteFile(filepath.Join(resultsDir, "playlist.yaml"), []byte(playlistContent), 0o644))

	rt := DummyPlaybackRuntime{}
	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)

	assert.Equal(t, 1, exit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "playlist entry 1 is empty")
}

func TestDummyPlaybackRuntime_WriteAgentResultExecError(t *testing.T) {
	t.Parallel()

	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 1, fmt.Errorf("sandbox exec failed")
		},
	}

	err := rt.writeAgentResult("sandbox", []byte(`{"test":true}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dummy-playback mkdir")
}

func TestDummyPlaybackRuntime_WriteAgentResultUploadError(t *testing.T) {
	t.Parallel()

	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
		UploadFn: func(_, _, _ string) error {
			return fmt.Errorf("upload failed")
		},
	}

	err := rt.writeAgentResult("sandbox", []byte(`{"test":true}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dummy-playback upload")
}

func TestLocalAdvancePlaylist(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "playlist.yaml")
		playlist := &Playlist{Current: 3, Results: []string{"a", "b", "c"}}
		err := localAdvancePlaylist(path, playlist)
		require.NoError(t, err)

		// Verify it was written correctly
		loaded, err := loadPlaylist(path)
		require.NoError(t, err)
		assert.Equal(t, 3, loaded.Current)
	})

	t.Run("write error", func(t *testing.T) {
		t.Parallel()
		// Use a path where the parent directory doesn't exist
		path := filepath.Join(t.TempDir(), "nonexistent", "nested", "playlist.yaml")
		playlist := &Playlist{Current: 2, Results: []string{"a"}}
		err := localAdvancePlaylist(path, playlist)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "writing playlist")
	})
}

func TestDummyPlaybackRuntime_CopyCompanionFilesExecError(t *testing.T) {
	t.Parallel()

	fullsendDir := setupResultDir(t, "code/test", `{"target_branch":"main"}`, map[string]string{
		"extra.txt": "some data",
	})
	entryDir := filepath.Join(fullsendDir, "results", "code", "test")

	callCount := 0
	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			callCount++
			return "", "", 1, fmt.Errorf("mkdir failed")
		},
	}

	_, err := rt.copyCompanionFiles("sandbox", t.TempDir(), entryDir, ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dummy-playback mkdir for companion")
}

func TestDummyPlaybackRuntime_CopyCompanionFilesUploadError(t *testing.T) {
	t.Parallel()

	fullsendDir := setupResultDir(t, "code/test2", `{"target_branch":"main"}`, map[string]string{
		"extra.txt": "some data",
	})
	entryDir := filepath.Join(fullsendDir, "results", "code", "test2")

	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
		UploadFn: func(_, _, _ string) error {
			return fmt.Errorf("upload failed")
		},
	}

	_, err := rt.copyCompanionFiles("sandbox", t.TempDir(), entryDir, ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dummy-playback upload companion")
}

func TestDummyPlaybackRuntime_CreateFeatureBranchError(t *testing.T) {
	t.Parallel()

	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "error", 1, fmt.Errorf("git checkout failed")
		},
	}

	err := rt.createFeatureBranch("sandbox", "/workspace/repo", "code/round-1", ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dummy-playback branch setup")
}

func TestDummyPlaybackRuntime_CommitToCurrentBranchError(t *testing.T) {
	t.Parallel()

	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "error", 1, fmt.Errorf("git add failed")
		},
	}

	err := rt.commitToCurrentBranch("sandbox", "/workspace/repo", "fix/round-1", ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dummy-playback fix commit")
}

func TestInjectReviewMetadata_InvalidJSON(t *testing.T) {
	t.Parallel()

	content := []byte(`not json at all`)
	result := injectReviewMetadata(content, "github", ui.New(io.Discard))
	assert.Equal(t, content, result, "should return original content on invalid JSON")
}

func TestInjectReviewMetadata_ShortSHA(t *testing.T) {
	t.Setenv("PR_HEAD_SHA", "abc")
	t.Setenv("STATUS_NUMBER", "")
	t.Setenv("GITHUB_REPOSITORY", "")
	t.Setenv("CI_PROJECT_PATH", "")
	content := []byte(`{"action":"approve"}`)
	result := injectReviewMetadata(content, "github", ui.New(io.Discard))

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(result, &parsed))
	assert.Equal(t, "abc", parsed["head_sha"])
}

func TestDummyPlaybackRuntime_AdvancePlaylistUsesLocalFallback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "playlist.yaml")
	playlist := &Playlist{Current: 2, Results: []string{"a", "b"}}

	// With no GitCommitFn, advancePlaylist should use localAdvancePlaylist
	rt := DummyPlaybackRuntime{}
	err := rt.advancePlaylist(path, playlist)
	require.NoError(t, err)

	loaded, err := loadPlaylist(path)
	require.NoError(t, err)
	assert.Equal(t, 2, loaded.Current)
}

func TestDummyPlaybackRuntime_RunWriteAgentResultError(t *testing.T) {
	t.Parallel()

	fullsendDir := setupResultDir(t, "triage/sufficient", `{"action":"sufficient"}`, nil)

	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
			// Fail the mkdir for output
			if strings.Contains(cmd, "mkdir") {
				return "", "", 1, fmt.Errorf("exec failed")
			}
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
}

func TestDummyPlaybackRuntime_RunFeatureBranchError(t *testing.T) {
	t.Parallel()

	companions := map[string]string{
		"repo/main.go": "package main",
	}
	fullsendDir := setupResultDir(t, "code/round-1", `{"target_branch":"main"}`, companions)

	callCount := 0
	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
			callCount++
			// Let mkdir pass, fail on git checkout
			if strings.Contains(cmd, "checkout -b") {
				return "", "", 1, fmt.Errorf("checkout failed")
			}
			return "", "", 0, nil
		},
		UploadFn:    func(_, _, _ string) error { return nil },
		GitCommitFn: func(_ string, _ *Playlist) error { return nil },
	}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)
	assert.Equal(t, 1, exit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "branch setup")
}

func TestDummyPlaybackRuntime_RunFixCommitError(t *testing.T) {
	t.Parallel()

	companions := map[string]string{
		"repo/fix.go": "package fix",
	}
	fullsendDir := setupResultDir(t, "fix/round-1", `{"pr_number":1}`, companions)

	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
			// Fail on git add for fix commits
			if strings.Contains(cmd, "git add") {
				return "", "", 1, fmt.Errorf("git add failed")
			}
			return "", "", 0, nil
		},
		UploadFn:    func(_, _, _ string) error { return nil },
		GitCommitFn: func(_ string, _ *Playlist) error { return nil },
	}

	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
	}, ui.New(io.Discard), time.Now(), nil)
	assert.Equal(t, 1, exit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fix commit")
}

func TestDummyPlaybackRuntime_RunUpdateCommentError(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	t.Setenv("CI_PROJECT_PATH", "")
	t.Setenv("PR_HEAD_SHA", "")
	t.Setenv("STATUS_NUMBER", "")

	fullsendDir := setupResultDir(t, "triage/sufficient", `{"action":"sufficient"}`, nil)
	require.NoError(t, os.WriteFile(
		filepath.Join(fullsendDir, playbackCommentFile),
		[]byte("gh\n/repos/org/repo/issues/comments/1"),
		0o644,
	))

	fc := forge.NewFakeClient()
	fc.IssueComments = map[string][]forge.IssueComment{"org/repo/0": {{ID: 1, Body: "playback-current: 1"}}}
	fc.Errors = map[string]error{"UpdateIssueComment": fmt.Errorf("update failed")}
	rt := DummyPlaybackRuntime{
		ExecFn: func(_ string, _ string, _ time.Duration) (string, string, int, error) {
			return "", "", 0, nil
		},
		UploadFn:    func(_, _, _ string) error { return nil },
		GitCommitFn: func(_ string, _ *Playlist) error { return nil },
	}

	// The update error should be a warning, not a failure
	exit, err := rt.Run(context.Background(), RunParams{
		SandboxName: "sandbox",
		FullsendDir: fullsendDir,
		RepoDir:     t.TempDir(),
		ForgeClient: fc,
	}, ui.New(io.Discard), time.Now(), nil)
	require.NoError(t, err, "update comment failure should be a warning, not a hard error")
	assert.Equal(t, 0, exit)
}
