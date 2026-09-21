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

func TestRunCommand_FlagOverridesEnvVar(t *testing.T) {
	t.Setenv("FULLSEND_SYNC_WORKSPACE", "true")

	// Case 1: Flag omitted -> environment fallback enables syncWorkspace
	cmd := newRunCmd()
	cmd.SetArgs([]string{"dummy", "--fullsend-dir", t.TempDir(), "--target-repo", t.TempDir()})
	// Parse flags without executing RunE
	err := cmd.ParseFlags([]string{"--fullsend-dir", t.TempDir(), "--target-repo", t.TempDir()})
	require.NoError(t, err)
	assert.False(t, cmd.Flags().Changed("sync-workspace"))

	// Case 2: Flag explicitly set to false -> flagChanged is true, overrides env var
	cmd2 := newRunCmd()
	err = cmd2.ParseFlags([]string{"--sync-workspace=false", "--fullsend-dir", t.TempDir(), "--target-repo", t.TempDir()})
	require.NoError(t, err)
	assert.True(t, cmd2.Flags().Changed("sync-workspace"))
	val, err := cmd2.Flags().GetBool("sync-workspace")
	require.NoError(t, err)
	assert.False(t, val)
}

func TestSyncOutputExcludeRel(t *testing.T) {
	repo := t.TempDir()

	// Top-level relative output
	rel, ok := syncOutputExcludeRel(repo, filepath.Join(repo, "output"))
	assert.True(t, ok)
	assert.Equal(t, "output", rel)

	// Multi-segment relative output (e.g. build/output, .fullsend/runs)
	rel, ok = syncOutputExcludeRel(repo, filepath.Join(repo, "build", "output"))
	assert.True(t, ok)
	assert.Equal(t, filepath.Join("build", "output"), rel)

	rel, ok = syncOutputExcludeRel(repo, filepath.Join(repo, ".fullsend", "runs"))
	assert.True(t, ok)
	assert.Equal(t, filepath.Join(".fullsend", "runs"), rel)

	// Sibling or external output directory
	otherDir := t.TempDir()
	_, ok = syncOutputExcludeRel(repo, otherDir)
	assert.False(t, ok)

	// Target repo itself
	_, ok = syncOutputExcludeRel(repo, repo)
	assert.False(t, ok)

	// Empty paths
	_, ok = syncOutputExcludeRel("", repo)
	assert.False(t, ok)
	_, ok = syncOutputExcludeRel(repo, "")
	assert.False(t, ok)
}

func TestSyncWorkspaceDir_NestedExcludesSurviveCopyAndPrune(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Destination has a nested output directory containing run artifacts
	nestedOut := filepath.Join(dst, "build", "output")
	require.NoError(t, os.MkdirAll(filepath.Join(nestedOut, "artifacts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nestedOut, "results.json"), []byte("{\"status\":\"ok\"}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(nestedOut, "artifacts", "trace.log"), []byte("log trace"), 0o644))

	// Destination also has an obsolete sibling file under build/ that src does not have
	require.NoError(t, os.WriteFile(filepath.Join(dst, "build", "old_scratch.txt"), []byte("prune me"), 0o644))

	// Destination has an obsolete root file that should be pruned
	require.NoError(t, os.WriteFile(filepath.Join(dst, "deprecated.txt"), []byte("remove"), 0o644))

	// Source has new files, but does NOT contain the build directory at all
	require.NoError(t, os.MkdirAll(filepath.Join(src, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "pkg", "lib.go"), []byte("package pkg"), 0o644))

	// Source sandbox also created files under build/output (which should be skipped on copy)
	require.NoError(t, os.MkdirAll(filepath.Join(src, "build", "output"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "build", "output", "sandbox_extra.tmp"), []byte("ignore"), 0o644))

	excludeRel := filepath.Join("build", "output")
	err := syncWorkspaceDir(src, dst, []string{".git", excludeRel})
	require.NoError(t, err)

	// 1. New file copied
	assert.FileExists(t, filepath.Join(dst, "pkg", "lib.go"))

	// 2. Deprecated file pruned
	assert.NoFileExists(t, filepath.Join(dst, "deprecated.txt"))

	// 3. Obsolete sibling under build pruned
	assert.NoFileExists(t, filepath.Join(dst, "build", "old_scratch.txt"))

	// 4. Nested output run artifacts strictly preserved
	assert.FileExists(t, filepath.Join(nestedOut, "results.json"))
	assert.FileExists(t, filepath.Join(nestedOut, "artifacts", "trace.log"))

	// 5. Excluded copy path was NOT copied over from src
	assert.NoFileExists(t, filepath.Join(nestedOut, "sandbox_extra.tmp"))
}

func TestIsAncestorOfExcludedPath(t *testing.T) {
	excludes := []string{".git", filepath.Join("build", "output"), filepath.Join(".fullsend", "runs")}

	assert.True(t, isAncestorOfExcludedPath("build", excludes))
	assert.True(t, isAncestorOfExcludedPath(".fullsend", excludes))

	assert.False(t, isAncestorOfExcludedPath(filepath.Join("build", "output"), excludes))
	assert.False(t, isAncestorOfExcludedPath("src", excludes))
	assert.False(t, isAncestorOfExcludedPath("builder", excludes))
}
