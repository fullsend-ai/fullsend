package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestRunCommand_HasSyncWorkspaceFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("sync-workspace")
	require.NotNil(t, flag, "expected --sync-workspace flag on run command")
	assert.Equal(t, "false", flag.DefValue)
}

func TestSyncWorkspaceDir_NewAndModifiedFiles(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Initial file in dst
	require.NoError(t, os.WriteFile(filepath.Join(dst, "existing.txt"), []byte("initial-content"), 0o644))

	// Modified file in src
	require.NoError(t, os.WriteFile(filepath.Join(src, "existing.txt"), []byte("updated-content"), 0o644))

	// New nested file in src
	require.NoError(t, os.MkdirAll(filepath.Join(src, "pkg", "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "pkg", "sub", "new.go"), []byte("package sub"), 0o644))

	err := syncWorkspaceDir(src, dst, []string{".git"})
	require.NoError(t, err)

	// Verify updated content
	updatedData, err := os.ReadFile(filepath.Join(dst, "existing.txt"))
	require.NoError(t, err)
	assert.Equal(t, "updated-content", string(updatedData))

	// Verify new file
	newData, err := os.ReadFile(filepath.Join(dst, "pkg", "sub", "new.go"))
	require.NoError(t, err)
	assert.Equal(t, "package sub", string(newData))
}

func TestSyncWorkspaceDir_PreservesDotGit(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// dst has a .git directory with config and commits
	dstGit := filepath.Join(dst, ".git")
	require.NoError(t, os.MkdirAll(dstGit, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dstGit, "config"), []byte("authoritative-git-config"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dstGit, "HEAD"), []byte("ref: refs/heads/main"), 0o644))

	// src might have a different or dummy .git from sandbox
	srcGit := filepath.Join(src, ".git")
	require.NoError(t, os.MkdirAll(srcGit, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcGit, "config"), []byte("sandbox-git-config"), 0o644))

	// src has an updated file
	require.NoError(t, os.WriteFile(filepath.Join(src, "main.go"), []byte("package main"), 0o644))

	err := syncWorkspaceDir(src, dst, []string{".git"})
	require.NoError(t, err)

	// Verify .git was preserved
	configData, err := os.ReadFile(filepath.Join(dstGit, "config"))
	require.NoError(t, err)
	assert.Equal(t, "authoritative-git-config", string(configData))

	headData, err := os.ReadFile(filepath.Join(dstGit, "HEAD"))
	require.NoError(t, err)
	assert.Equal(t, "ref: refs/heads/main", string(headData))

	// Verify main.go was copied
	mainData, err := os.ReadFile(filepath.Join(dst, "main.go"))
	require.NoError(t, err)
	assert.Equal(t, "package main", string(mainData))
}

func TestSyncWorkspaceDir_PrunesDeletedFiles(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// dst has a file that src does NOT have (e.g. agent ran 'rm deleted.txt')
	require.NoError(t, os.WriteFile(filepath.Join(dst, "deleted.txt"), []byte("to be deleted"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dst, "obsolete_dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dst, "obsolete_dir", "old.txt"), []byte("old"), 0o644))

	// src has kept.txt
	require.NoError(t, os.WriteFile(filepath.Join(src, "kept.txt"), []byte("keep me"), 0o644))

	err := syncWorkspaceDir(src, dst, []string{".git"})
	require.NoError(t, err)

	// Kept file exists
	assert.FileExists(t, filepath.Join(dst, "kept.txt"))

	// Pruned file and directory are gone
	assert.NoFileExists(t, filepath.Join(dst, "deleted.txt"))
	assert.NoDirExists(t, filepath.Join(dst, "obsolete_dir"))
}

func TestSyncWorkspaceDir_RespectsExcludes(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// dst has an output directory (e.g. CI output or cache)
	outputDir := filepath.Join(dst, "output")
	require.NoError(t, os.MkdirAll(outputDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "run.log"), []byte("runner output log"), 0o644))

	// src has a file
	require.NoError(t, os.WriteFile(filepath.Join(src, "app.py"), []byte("print('hello')"), 0o644))

	err := syncWorkspaceDir(src, dst, []string{".git", "output"})
	require.NoError(t, err)

	// Excluded output directory should NOT have been pruned
	assert.FileExists(t, filepath.Join(outputDir, "run.log"))
	assert.FileExists(t, filepath.Join(dst, "app.py"))
}

func TestSyncWorkspaceDir_SymlinksAndPermissions(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Executable file in src
	execFile := filepath.Join(src, "run.sh")
	require.NoError(t, os.WriteFile(execFile, []byte("#!/bin/sh\necho ok"), 0o755))

	// Symlink in src pointing within repo
	require.NoError(t, os.Symlink("run.sh", filepath.Join(src, "symlink.sh")))

	err := syncWorkspaceDir(src, dst, []string{".git"})
	require.NoError(t, err)

	// Verify permissions
	dstExec := filepath.Join(dst, "run.sh")
	info, err := os.Stat(dstExec)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	// Verify symlink
	dstLink := filepath.Join(dst, "symlink.sh")
	target, err := os.Readlink(dstLink)
	require.NoError(t, err)
	assert.Equal(t, "run.sh", target)
}

func TestSyncWorkspaceDir_SameDirNoop(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0o644))

	err := syncWorkspaceDir(dir, dir, []string{".git"})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, "file.txt"))
}

func TestIsExcludedPath(t *testing.T) {
	excludes := []string{".git", "output", "build/tmp"}

	assert.True(t, isExcludedPath(".git", excludes))
	assert.True(t, isExcludedPath(".git/config", excludes))
	assert.True(t, isExcludedPath(".git/objects/abc", excludes))
	assert.True(t, isExcludedPath("output", excludes))
	assert.True(t, isExcludedPath("output/run.log", excludes))
	assert.True(t, isExcludedPath("build/tmp", excludes))
	assert.True(t, isExcludedPath("build/tmp/test.txt", excludes))

	assert.False(t, isExcludedPath(".github", excludes))
	assert.False(t, isExcludedPath("output.txt", excludes))
	assert.False(t, isExcludedPath("build", excludes))
	assert.False(t, isExcludedPath("src/main.go", excludes))
}

func TestRunAgent_SyncWorkspace_SkippedOnFailure(t *testing.T) {
	dir := preflightTestSetup(t, "agent: agents/code.md\nrole: test\nvalidation_loop:\n  script: scripts/validate.sh\n  preflight_check: \"exit 1\"\n  max_iterations: 2\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	var buf bytes.Buffer
	repoDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "initial.txt"), []byte("initial"), 0o644))

	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(&buf), false, runOverrideFlags{syncWorkspace: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation_loop.preflight_check failed")

	// Verify repoDir was not modified
	data, readErr := os.ReadFile(filepath.Join(repoDir, "initial.txt"))
	require.NoError(t, readErr)
	assert.Equal(t, "initial", string(data))
}
