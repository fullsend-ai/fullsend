package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/envfile"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func syncTargetRepoDir(srcDir, dstDir string, excludes []string) error {
	manifest, err := recordTargetRepoManifest(dstDir)
	if err != nil {
		return err
	}
	return syncTargetRepoDirWithManifest(srcDir, dstDir, excludes, manifest)
}

func TestRunCommand_HasSyncTargetRepoFlag(t *testing.T) {
	cmd := newRunCmd()
	flag := cmd.Flags().Lookup("sync-target-repo")
	require.NotNil(t, flag, "expected --sync-target-repo flag on run command")
	assert.Equal(t, "false", flag.DefValue)
}

func TestSyncTargetRepoDir_NewAndModifiedFiles(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Initial file in dst
	require.NoError(t, os.WriteFile(filepath.Join(dst, "existing.txt"), []byte("initial-content"), 0o644))

	// Modified file in src
	require.NoError(t, os.WriteFile(filepath.Join(src, "existing.txt"), []byte("updated-content"), 0o644))

	// New nested file in src
	require.NoError(t, os.MkdirAll(filepath.Join(src, "pkg", "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "pkg", "sub", "new.go"), []byte("package sub"), 0o644))

	err := syncTargetRepoDir(src, dst, []string{".git"})
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

func TestSyncTargetRepoDir_PreservesDotGit(t *testing.T) {
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

	err := syncTargetRepoDir(src, dst, []string{".git"})
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

func TestSyncTargetRepoDir_NestedDotGitAndSubmodulesPreserved(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// dst has a nested repository (e.g. vendor/x/.git) with config and hooks
	nestedGit := filepath.Join(dst, "vendor", "x", ".git")
	require.NoError(t, os.MkdirAll(filepath.Join(nestedGit, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nestedGit, "config"), []byte("authoritative-nested-config"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(nestedGit, "hooks", "pre-commit"), []byte("#!/bin/sh\nexit 0"), 0o755))

	// dst also has a submodule gitlink file (tools/y/.git as a regular file)
	toolsDir := filepath.Join(dst, "tools", "y")
	require.NoError(t, os.MkdirAll(toolsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(toolsDir, ".git"), []byte("gitdir: ../../.git/modules/tools/y"), 0o644))

	// src (sandbox download) has modified config, deleted hooks, and altered submodule gitlink
	srcNestedGit := filepath.Join(src, "vendor", "x", ".git")
	require.NoError(t, os.MkdirAll(srcNestedGit, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcNestedGit, "config"), []byte("malicious-nested-config"), 0o644))

	srcToolsDir := filepath.Join(src, "tools", "y")
	require.NoError(t, os.MkdirAll(srcToolsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcToolsDir, ".git"), []byte("gitdir: malicious"), 0o644))

	// src also has a valid regular file modification
	require.NoError(t, os.WriteFile(filepath.Join(src, "vendor", "x", "lib.go"), []byte("package x"), 0o644))

	err := syncTargetRepoDir(src, dst, []string{".git"})
	require.NoError(t, err)

	// Verify nested .git/config was NOT overwritten
	configData, err := os.ReadFile(filepath.Join(nestedGit, "config"))
	require.NoError(t, err)
	assert.Equal(t, "authoritative-nested-config", string(configData))

	// Verify nested .git/hooks was NOT pruned
	assert.FileExists(t, filepath.Join(nestedGit, "hooks", "pre-commit"))

	// Verify submodule gitlink file was NOT overwritten
	gitlinkData, err := os.ReadFile(filepath.Join(toolsDir, ".git"))
	require.NoError(t, err)
	assert.Equal(t, "gitdir: ../../.git/modules/tools/y", string(gitlinkData))

	// Verify legitimate file was copied
	assert.FileExists(t, filepath.Join(dst, "vendor", "x", "lib.go"))
}

func TestSyncTargetRepoDir_AncestorOfExcludedPathNotReplaced(t *testing.T) {
	excludes := []string{".git", filepath.Join("build", "output")}

	t.Run("source replaces ancestor dir with regular file", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()

		// dst has build/output/results.json
		dstOut := filepath.Join(dst, "build", "output")
		require.NoError(t, os.MkdirAll(dstOut, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dstOut, "results.json"), []byte("{\"status\":\"ok\"}"), 0o644))

		// src replaces "build" with a regular file
		require.NoError(t, os.WriteFile(filepath.Join(src, "build"), []byte("not-a-directory"), 0o644))

		err := syncTargetRepoDir(src, dst, excludes)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ancestor of excluded path")

		// Verify dst/build/output/results.json was preserved
		assert.FileExists(t, filepath.Join(dstOut, "results.json"))
	})

	t.Run("source replaces ancestor dir with symlink", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()

		// dst has build/output/results.json
		dstOut := filepath.Join(dst, "build", "output")
		require.NoError(t, os.MkdirAll(dstOut, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dstOut, "results.json"), []byte("{\"status\":\"ok\"}"), 0o644))

		// src replaces "build" with a symlink
		require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(src, "build")))

		err := syncTargetRepoDir(src, dst, excludes)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ancestor of excluded path")

		// Verify dst/build/output/results.json was preserved
		assert.FileExists(t, filepath.Join(dstOut, "results.json"))
	})

	t.Run("source replaces ancestor of nested git with regular file", func(t *testing.T) {
		src := t.TempDir()
		dst := t.TempDir()

		// dst has vendor/x/.git/config
		dstGit := filepath.Join(dst, "vendor", "x", ".git")
		require.NoError(t, os.MkdirAll(dstGit, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dstGit, "config"), []byte("submod"), 0o644))

		// src replaces vendor/x with a regular file
		require.NoError(t, os.MkdirAll(filepath.Join(src, "vendor"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(src, "vendor", "x"), []byte("not-a-dir"), 0o644))

		err := syncTargetRepoDir(src, dst, []string{".git"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ancestor of excluded path")

		// Verify dst/vendor/x/.git/config was preserved
		assert.FileExists(t, filepath.Join(dstGit, "config"))
	})
}

func TestSyncTargetRepoDir_NestedDotGitSurvivesParentPrune(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// dst has vendor/x/.git/config and vendor/x/lib.go
	dstSubmodGit := filepath.Join(dst, "vendor", "x", ".git")
	require.NoError(t, os.MkdirAll(dstSubmodGit, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dstSubmodGit, "config"), []byte("submod-config"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dst, "vendor", "x", "lib.go"), []byte("package x"), 0o644))

	// src has kept.txt and does NOT have vendor/x at all (agent deleted vendor/x)
	require.NoError(t, os.WriteFile(filepath.Join(src, "kept.txt"), []byte("kept"), 0o644))

	err := syncTargetRepoDir(src, dst, []string{".git"})
	require.NoError(t, err)

	// lib.go was pruned
	assert.NoFileExists(t, filepath.Join(dst, "vendor", "x", "lib.go"))

	// nested .git was strictly preserved
	assert.FileExists(t, filepath.Join(dstSubmodGit, "config"))
}

func TestSyncTargetRepoDir_RunnerInjectedAndWorkingDirExcludes(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// src has runner-injected and uncommitted agent working files
	require.NoError(t, os.WriteFile(filepath.Join(src, "AGENTS.md"), []byte("# Injected AGENTS.md"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "CLAUDE.md"), []byte("# Injected CLAUDE.md"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(src, ".agentready"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, ".agentready", "state.json"), []byte("{}"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(src, ".fullsend-workspace"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, ".fullsend-workspace", "cache.bin"), []byte("123"), 0o644))

	// src also has legitimate agent code
	require.NoError(t, os.MkdirAll(filepath.Join(src, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "pkg", "app.go"), []byte("package pkg"), 0o644))

	syncExcludes := []string{".git", "AGENTS.md", "CLAUDE.md", ".agentready", ".fullsend-workspace"}
	err := syncTargetRepoDir(src, dst, syncExcludes)
	require.NoError(t, err)

	// Legitimate file copied
	assert.FileExists(t, filepath.Join(dst, "pkg", "app.go"))

	// Injected files and working dirs NOT copied to dst
	assert.NoFileExists(t, filepath.Join(dst, "AGENTS.md"))
	assert.NoFileExists(t, filepath.Join(dst, "CLAUDE.md"))
	assert.NoDirExists(t, filepath.Join(dst, ".agentready"))
	assert.NoDirExists(t, filepath.Join(dst, ".fullsend-workspace"))
}

func TestSyncTargetRepoDir_HardlinksBrokenOnAtomicWrite(t *testing.T) {
	externalDir := t.TempDir()
	extSharedFile := filepath.Join(externalDir, "shared_store.txt")
	require.NoError(t, os.WriteFile(extSharedFile, []byte("original-shared-inode"), 0o644))

	dst := t.TempDir()
	dstLinkedFile := filepath.Join(dst, "linked.txt")
	// Create hardlink: dst/linked.txt shares inode with externalDir/shared_store.txt
	require.NoError(t, os.Link(extSharedFile, dstLinkedFile))

	src := t.TempDir()
	// src has modified content for linked.txt
	require.NoError(t, os.WriteFile(filepath.Join(src, "linked.txt"), []byte("agent-modified-content"), 0o644))

	err := syncTargetRepoDir(src, dst, []string{".git"})
	require.NoError(t, err)

	// dst file should have new content
	dstData, err := os.ReadFile(dstLinkedFile)
	require.NoError(t, err)
	assert.Equal(t, "agent-modified-content", string(dstData))

	// External file MUST remain unchanged because hardlink was broken atomically
	extData, err := os.ReadFile(extSharedFile)
	require.NoError(t, err)
	assert.Equal(t, "original-shared-inode", string(extData))
}

func TestSyncTargetRepoDir_PrunesDeletedFiles(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// dst has a file that src does NOT have (e.g. agent ran 'rm deleted.txt')
	require.NoError(t, os.WriteFile(filepath.Join(dst, "deleted.txt"), []byte("to be deleted"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dst, "obsolete_dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dst, "obsolete_dir", "old.txt"), []byte("old"), 0o644))

	// src has kept.txt
	require.NoError(t, os.WriteFile(filepath.Join(src, "kept.txt"), []byte("keep me"), 0o644))

	err := syncTargetRepoDir(src, dst, []string{".git"})
	require.NoError(t, err)

	// Kept file exists
	assert.FileExists(t, filepath.Join(dst, "kept.txt"))

	// Pruned file and directory are gone
	assert.NoFileExists(t, filepath.Join(dst, "deleted.txt"))
	assert.NoDirExists(t, filepath.Join(dst, "obsolete_dir"))
}

func TestSyncTargetRepoDir_RespectsExcludes(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// dst has an output directory (e.g. CI output or cache)
	outputDir := filepath.Join(dst, "output")
	require.NoError(t, os.MkdirAll(outputDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "run.log"), []byte("runner output log"), 0o644))

	// src has a file
	require.NoError(t, os.WriteFile(filepath.Join(src, "app.py"), []byte("print('hello')"), 0o644))

	err := syncTargetRepoDir(src, dst, []string{".git", "output"})
	require.NoError(t, err)

	// Excluded output directory should NOT have been pruned
	assert.FileExists(t, filepath.Join(outputDir, "run.log"))
	assert.FileExists(t, filepath.Join(dst, "app.py"))
}

func TestSyncTargetRepoDir_SymlinksAndPermissions(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Executable file in src
	execFile := filepath.Join(src, "run.sh")
	require.NoError(t, os.WriteFile(execFile, []byte("#!/bin/sh\necho ok"), 0o755))

	// Symlink in src pointing within repo
	require.NoError(t, os.Symlink("run.sh", filepath.Join(src, "symlink.sh")))

	err := syncTargetRepoDir(src, dst, []string{".git"})
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

func TestSyncTargetRepoDir_SameDirNoop(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0o644))

	err := syncTargetRepoDir(dir, dir, []string{".git"})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, "file.txt"))
}

func TestIsExcludedPath(t *testing.T) {
	excludes := []string{".git", "output", "build/tmp", "AGENTS.md", ".agentready"}

	assert.True(t, isExcludedPath(".git", excludes))
	assert.True(t, isExcludedPath(".git/config", excludes))
	assert.True(t, isExcludedPath(".git/objects/abc", excludes))
	assert.True(t, isExcludedPath("vendor/x/.git", excludes))
	assert.True(t, isExcludedPath("vendor/x/.git/config", excludes))
	assert.True(t, isExcludedPath("tools/y/.git", excludes))
	assert.True(t, isExcludedPath("output", excludes))
	assert.True(t, isExcludedPath("output/run.log", excludes))
	assert.True(t, isExcludedPath("build/tmp", excludes))
	assert.True(t, isExcludedPath("build/tmp/test.txt", excludes))
	assert.True(t, isExcludedPath("AGENTS.md", excludes))
	assert.True(t, isExcludedPath(".agentready", excludes))
	assert.True(t, isExcludedPath(".agentready/state.json", excludes))

	assert.False(t, isExcludedPath(".github", excludes))
	assert.False(t, isExcludedPath("output.txt", excludes))
	assert.False(t, isExcludedPath("build", excludes))
	assert.False(t, isExcludedPath("src/main.go", excludes))
	assert.False(t, isExcludedPath("src/git.go", excludes))
}

func TestRunAgent_SyncTargetRepo_SkippedOnPreflightFailure(t *testing.T) {
	dir := preflightTestSetup(t, "agent: agents/code.md\nrole: test\nvalidation_loop:\n  script: scripts/validate.sh\n  preflight_check: \"exit 1\"\n  max_iterations: 2\n")

	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	var buf bytes.Buffer
	repoDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "initial.txt"), []byte("initial"), 0o644))

	err := runAgent(context.Background(), "code", dir, "", repoDir, "", nil, false, "", "", "", rFlags,
		statusOpts{}, ui.New(&buf), false, runOverrideFlags{syncTargetRepo: true, syncTargetRepoSet: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation_loop.preflight_check failed")

	// Verify repoDir was not modified
	data, readErr := os.ReadFile(filepath.Join(repoDir, "initial.txt"))
	require.NoError(t, readErr)
	assert.Equal(t, "initial", string(data))
}

func TestRunCommand_FlagOverridesEnvVar(t *testing.T) {
	t.Setenv("FULLSEND_SYNC_TARGET_REPO", "true")

	// Case 1: Flag omitted -> environment fallback enables syncTargetRepo
	cmd := newRunCmd()
	cmd.SetArgs([]string{"dummy", "--fullsend-dir", t.TempDir(), "--target-repo", t.TempDir()})
	// Parse flags without executing RunE
	err := cmd.ParseFlags([]string{"--fullsend-dir", t.TempDir(), "--target-repo", t.TempDir()})
	require.NoError(t, err)
	assert.False(t, cmd.Flags().Changed("sync-target-repo"))

	// Case 2: Flag explicitly set to false -> flagChanged is true, overrides env var
	cmd2 := newRunCmd()
	err = cmd2.ParseFlags([]string{"--sync-target-repo=false", "--fullsend-dir", t.TempDir(), "--target-repo", t.TempDir()})
	require.NoError(t, err)
	assert.True(t, cmd2.Flags().Changed("sync-target-repo"))
	val, err := cmd2.Flags().GetBool("sync-target-repo")
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

func TestSyncTargetRepoDir_NestedExcludesSurviveCopyAndPrune(t *testing.T) {
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
	err := syncTargetRepoDir(src, dst, []string{".git", excludeRel})
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

func TestSyncTargetRepoDir_ExistingSymlinkNotFollowed(t *testing.T) {
	externalDir := t.TempDir()
	extSecretFile := filepath.Join(externalDir, "secret.txt")
	require.NoError(t, os.WriteFile(extSecretFile, []byte("sensitive-external-data"), 0o600))

	dst := t.TempDir()
	// dst has a symlink pointing to externalDir/secret.txt
	dstLink := filepath.Join(dst, "target.txt")
	require.NoError(t, os.Symlink(extSecretFile, dstLink))

	src := t.TempDir()
	// src has a regular file with safe content
	require.NoError(t, os.WriteFile(filepath.Join(src, "target.txt"), []byte("safe-sandbox-content"), 0o644))

	err := syncTargetRepoDir(src, dst, []string{".git"})
	require.NoError(t, err)

	// Verify dst/target.txt is now a regular file, NOT a symlink
	fi, err := os.Lstat(dstLink)
	require.NoError(t, err)
	assert.True(t, fi.Mode().IsRegular(), "destination file should be a regular file, not a symlink")
	dstData, err := os.ReadFile(dstLink)
	require.NoError(t, err)
	assert.Equal(t, "safe-sandbox-content", string(dstData))

	// Verify the external secret file was NOT overwritten through the symlink
	extData, err := os.ReadFile(extSecretFile)
	require.NoError(t, err)
	assert.Equal(t, "sensitive-external-data", string(extData))
}

func TestSyncTargetRepoDir_PermissionUpdatesOnExistingFiles(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Initial files in dst: make_exec.sh is 0644, make_nonexec.sh is 0755
	require.NoError(t, os.WriteFile(filepath.Join(dst, "make_exec.sh"), []byte("#!/bin/sh\necho exec"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dst, "make_nonexec.sh"), []byte("#!/bin/sh\necho nonexec"), 0o755))

	// Files in src with updated permissions: make_exec.sh is 0755, make_nonexec.sh is 0644
	require.NoError(t, os.WriteFile(filepath.Join(src, "make_exec.sh"), []byte("#!/bin/sh\necho exec"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "make_nonexec.sh"), []byte("#!/bin/sh\necho nonexec"), 0o644))

	err := syncTargetRepoDir(src, dst, []string{".git"})
	require.NoError(t, err)

	// Verify permissions on existing files were updated
	info1, err := os.Stat(filepath.Join(dst, "make_exec.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info1.Mode().Perm())

	info2, err := os.Stat(filepath.Join(dst, "make_nonexec.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info2.Mode().Perm())
}

func TestShouldSyncTargetRepo(t *testing.T) {
	// Gating criteria:
	// syncEnabled, repoExtractedOK, hasValidationLoop, validationPassed, timedOut, transcriptErr, exitCode
	assert.True(t, shouldSyncTargetRepo(true, true, false, false, false, false, 0), "all success with no validation loop")
	assert.True(t, shouldSyncTargetRepo(true, true, true, true, false, false, 0), "all success with passing validation loop")

	assert.False(t, shouldSyncTargetRepo(false, true, false, false, false, false, 0), "disabled sync")
	assert.False(t, shouldSyncTargetRepo(true, false, false, false, false, false, 0), "repo extraction failed")
	assert.False(t, shouldSyncTargetRepo(true, true, true, false, false, false, 0), "validation failed")
	assert.False(t, shouldSyncTargetRepo(true, true, false, false, true, false, 0), "iteration timed out")
	assert.False(t, shouldSyncTargetRepo(true, true, false, false, false, true, 0), "transcript error override")
	assert.False(t, shouldSyncTargetRepo(true, true, false, false, false, false, 1), "non-zero agent exit code")
	assert.False(t, shouldSyncTargetRepo(true, true, false, false, false, true, 1), "transcript error and non-zero exit")
}

func TestRunOverrides_EnvFileEnablesSyncTargetRepo(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "test.env")
	require.NoError(t, os.WriteFile(envFile, []byte("FULLSEND_SYNC_TARGET_REPO=true\n"), 0o644))

	require.NoError(t, envfile.Load(envFile))
	t.Cleanup(func() {
		os.Unsetenv("FULLSEND_SYNC_TARGET_REPO")
	})

	overrides, err := resolveRunOverrides(runOverrideFlags{}, os.Getenv, "")
	require.NoError(t, err)
	assert.True(t, overrides.syncTargetRepo)
	assert.Equal(t, "FULLSEND_SYNC_TARGET_REPO", overrides.syncTargetRepoSource)

	overridesFlag, err := resolveRunOverrides(runOverrideFlags{syncTargetRepo: false, syncTargetRepoSet: true}, os.Getenv, "")
	require.NoError(t, err)
	assert.False(t, overridesFlag.syncTargetRepo)
	assert.Equal(t, "--sync-target-repo flag", overridesFlag.syncTargetRepoSource)
}

func TestSyncTargetRepoDirWithManifest_PreservesStrippedSymlinksAndHostOnlyFiles(t *testing.T) {
	repoDir := t.TempDir()
	srcDir := t.TempDir()

	// Initial host state before upload.
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "tracked.txt"), []byte("v1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "deleted.txt"), []byte("gone"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "deleteddir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "deleteddir", "old.txt"), []byte("old"), 0o644))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(repoDir, "abs-link")))
	require.NoError(t, os.Symlink("nonexistent-target", filepath.Join(repoDir, "dangling-link")))
	require.NoError(t, os.Symlink("../../outside", filepath.Join(repoDir, "escaping-link")))

	manifest, err := recordTargetRepoManifest(repoDir)
	require.NoError(t, err)
	assert.True(t, manifest["tracked.txt"])
	assert.True(t, manifest["deleted.txt"])
	assert.True(t, manifest["deleteddir/old.txt"])

	// Simulate files created on the host after sandbox upload started (absent from manifest).
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "host-only.txt"), []byte("keep-host"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "deleteddir", "host-only-nested.txt"), []byte("keep-nested"), 0o644))

	// Sanitized sandbox download updates tracked.txt, adds added.txt, omits deleted* and stripped symlinks.
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "tracked.txt"), []byte("v2"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "added.txt"), []byte("new"), 0o644))

	require.NoError(t, syncTargetRepoDirWithManifest(srcDir, repoDir, []string{".git"}, manifest))

	data, err := os.ReadFile(filepath.Join(repoDir, "tracked.txt"))
	require.NoError(t, err)
	assert.Equal(t, "v2", string(data))

	data, err = os.ReadFile(filepath.Join(repoDir, "added.txt"))
	require.NoError(t, err)
	assert.Equal(t, "new", string(data))

	_, err = os.Lstat(filepath.Join(repoDir, "deleted.txt"))
	assert.True(t, os.IsNotExist(err), "pre-existing deleted file should be pruned")
	_, err = os.Lstat(filepath.Join(repoDir, "deleteddir", "old.txt"))
	assert.True(t, os.IsNotExist(err), "pre-existing file inside deleted directory should be pruned")

	// Dangerous/dangling symlinks and post-upload host-only files must survive.
	target, err := os.Readlink(filepath.Join(repoDir, "abs-link"))
	require.NoError(t, err)
	assert.Equal(t, "/etc/passwd", target)

	target, err = os.Readlink(filepath.Join(repoDir, "dangling-link"))
	require.NoError(t, err)
	assert.Equal(t, "nonexistent-target", target)

	target, err = os.Readlink(filepath.Join(repoDir, "escaping-link"))
	require.NoError(t, err)
	assert.Equal(t, "../../outside", target)

	data, err = os.ReadFile(filepath.Join(repoDir, "host-only.txt"))
	require.NoError(t, err)
	assert.Equal(t, "keep-host", string(data))

	data, err = os.ReadFile(filepath.Join(repoDir, "deleteddir", "host-only-nested.txt"))
	require.NoError(t, err)
	assert.Equal(t, "keep-nested", string(data))
}

func TestSyncTargetRepoDirWithManifest_ReadonlyRepoRestoresDirModes(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	t.Cleanup(func() {
		_ = os.Chmod(srcDir, 0o755)
		_ = os.Chmod(filepath.Join(srcDir, "rodir"), 0o755)
		_ = os.Chmod(dstDir, 0o755)
		_ = os.Chmod(filepath.Join(dstDir, "rodir"), 0o755)
		_ = os.Chmod(filepath.Join(dstDir, "prunedir"), 0o755)
	})

	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "rodir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "rodir", "new.txt"), []byte("readonly-content"), 0o444))
	require.NoError(t, os.Chmod(filepath.Join(srcDir, "rodir"), 0o555))

	require.NoError(t, os.MkdirAll(filepath.Join(dstDir, "rodir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dstDir, "rodir", "stale.txt"), []byte("stale"), 0o444))
	require.NoError(t, os.Chmod(filepath.Join(dstDir, "rodir"), 0o555))

	require.NoError(t, os.MkdirAll(filepath.Join(dstDir, "prunedir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dstDir, "prunedir", "old.txt"), []byte("old"), 0o444))
	require.NoError(t, os.Chmod(filepath.Join(dstDir, "prunedir"), 0o555))

	require.NoError(t, os.Chmod(dstDir, 0o555))

	manifest, err := recordTargetRepoManifest(dstDir)
	require.NoError(t, err)
	require.NoError(t, syncTargetRepoDirWithManifest(srcDir, dstDir, []string{".git"}, manifest))

	rootInfo, err := os.Stat(dstDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o555), rootInfo.Mode().Perm())

	roDirInfo, err := os.Stat(filepath.Join(dstDir, "rodir"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o555), roDirInfo.Mode().Perm())

	data, err := os.ReadFile(filepath.Join(dstDir, "rodir", "new.txt"))
	require.NoError(t, err)
	assert.Equal(t, "readonly-content", string(data))

	_, err = os.Lstat(filepath.Join(dstDir, "rodir", "stale.txt"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Lstat(filepath.Join(dstDir, "prunedir"))
	assert.True(t, os.IsNotExist(err), "empty read-only directory should be pruned")
}

func TestSyncTargetRepoDirWithManifest_UnchangedPreservesInodeModTimeAndSymlink(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	past := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

	srcFile := filepath.Join(srcDir, "same.txt")
	dstFile := filepath.Join(dstDir, "same.txt")
	require.NoError(t, os.WriteFile(srcFile, []byte("identical"), 0o644))
	require.NoError(t, os.WriteFile(dstFile, []byte("identical"), 0o644))
	require.NoError(t, os.Chtimes(dstFile, past, past))

	srcPermFile := filepath.Join(srcDir, "perm.txt")
	dstPermFile := filepath.Join(dstDir, "perm.txt")
	require.NoError(t, os.WriteFile(srcPermFile, []byte("same-bytes"), 0o755))
	require.NoError(t, os.WriteFile(dstPermFile, []byte("same-bytes"), 0o644))

	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "target.txt"), []byte("t"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dstDir, "target.txt"), []byte("t"), 0o644))
	require.NoError(t, os.Symlink("target.txt", filepath.Join(srcDir, "same-link")))
	dstLink := filepath.Join(dstDir, "same-link")
	require.NoError(t, os.Symlink("target.txt", dstLink))

	origFileInfo, err := os.Stat(dstFile)
	require.NoError(t, err)
	origPermInfo, err := os.Stat(dstPermFile)
	require.NoError(t, err)
	origLinkInfo, err := os.Lstat(dstLink)
	require.NoError(t, err)

	require.NoError(t, syncTargetRepoDirWithManifest(srcDir, dstDir, []string{".git"}, nil))

	newFileInfo, err := os.Stat(dstFile)
	require.NoError(t, err)
	assert.Equal(t, origFileInfo.Sys().(*syscall.Stat_t).Ino, newFileInfo.Sys().(*syscall.Stat_t).Ino, "unchanged regular file inode must be preserved")
	assert.True(t, newFileInfo.ModTime().Equal(past), "unchanged regular file mtime must be preserved")

	newPermInfo, err := os.Stat(dstPermFile)
	require.NoError(t, err)
	assert.Equal(t, origPermInfo.Sys().(*syscall.Stat_t).Ino, newPermInfo.Sys().(*syscall.Stat_t).Ino, "permission-only change must update mode without replacing inode")
	assert.Equal(t, os.FileMode(0o755), newPermInfo.Mode().Perm())

	newLinkInfo, err := os.Lstat(dstLink)
	require.NoError(t, err)
	assert.Equal(t, origLinkInfo.Sys().(*syscall.Stat_t).Ino, newLinkInfo.Sys().(*syscall.Stat_t).Ino, "unchanged symlink must not be replaced")
}

func TestDoInjectClaudeMDPointer_ReturnsInjectionSuccess(t *testing.T) {
	var buf bytes.Buffer
	var cmds []string
	ok := doInjectClaudeMDPointer("sb", "/workspace/repo", ui.New(&buf), func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		cmds = append(cmds, cmd)
		return "", "", 0, nil
	})
	assert.True(t, ok)
	assert.Len(t, cmds, 2)

	buf.Reset()
	cmds = nil
	ok = doInjectClaudeMDPointer("sb", "/workspace/repo", ui.New(&buf), func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		cmds = append(cmds, cmd)
		return "", "permission denied", 1, errors.New("exit 1")
	})
	assert.False(t, ok)
	assert.Len(t, cmds, 1)
	assert.Contains(t, buf.String(), "Could not inject CLAUDE.md")
}

func TestSyncTargetRepoSkipReason_And_RunTargetRepoSync(t *testing.T) {
	cases := []struct {
		name            string
		syncEnabled     bool
		repoExtractedOK bool
		hasLoop         bool
		validationPass  bool
		timedOut        bool
		transcriptErr   bool
		exitCode        int
		exitReason      string
		validatedIter   int
		runCount        int
		wantSync        bool
		wantReason      string
	}{
		{name: "disabled", syncEnabled: false, wantSync: false, wantReason: ""},
		{name: "transcript error", syncEnabled: true, repoExtractedOK: true, transcriptErr: true, wantSync: false, wantReason: "transcript error"},
		{name: "loop timeout", syncEnabled: true, repoExtractedOK: true, hasLoop: true, timedOut: true, wantSync: false, wantReason: "timed out"},
		{name: "loop validation failed", syncEnabled: true, repoExtractedOK: true, hasLoop: true, validationPass: false, wantSync: false, wantReason: "validation did not pass"},
		{name: "loop validated earlier iteration", syncEnabled: true, repoExtractedOK: true, hasLoop: true, validationPass: true, validatedIter: 1, runCount: 2, wantSync: false, wantReason: "validated iteration 1 is not the last downloaded iteration 2"},
		{name: "no loop timeout", syncEnabled: true, repoExtractedOK: true, timedOut: true, wantSync: false, wantReason: "timed out"},
		{name: "no loop non-zero exit", syncEnabled: true, repoExtractedOK: true, exitCode: 2, wantSync: false, wantReason: "agent exited with code 2"},
		{name: "no loop behavioral subtype still syncs", syncEnabled: true, repoExtractedOK: true, exitCode: 1, exitReason: "error_max_turns", wantSync: true},
		{name: "loop passed non-zero exit still syncs", syncEnabled: true, repoExtractedOK: true, hasLoop: true, validationPass: true, exitCode: 1, validatedIter: 1, runCount: 1, wantSync: true},
		{name: "extraction failed", syncEnabled: true, repoExtractedOK: false, wantSync: false, wantReason: "repository extraction failed"},
		{name: "loop success on last iter", syncEnabled: true, repoExtractedOK: true, hasLoop: true, validationPass: true, validatedIter: 2, runCount: 2, wantSync: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotSync, gotReason := syncTargetRepoSkipReason(tc.syncEnabled, tc.repoExtractedOK, tc.hasLoop, tc.validationPass, tc.timedOut, tc.transcriptErr, tc.exitCode, tc.exitReason, tc.validatedIter, tc.runCount)
			assert.Equal(t, tc.wantSync, gotSync)
			assert.Equal(t, tc.wantReason, gotReason)
		})
	}

	var buf bytes.Buffer
	called := false
	status := runTargetRepoSync(true, true, true, false, false, false, 0, "", 0, 1, "/src", "/dst", "/dst/out", nil, nil, ui.New(&buf), func(_, _ string, _ []string, _ map[string]bool) error {
		called = true
		return nil
	})
	assert.False(t, called)
	assert.Equal(t, "skipped (validation did not pass)", status)
	assert.Contains(t, buf.String(), "Skipping target repository synchronization: validation did not pass")

	buf.Reset()
	status = runTargetRepoSync(true, true, false, false, false, false, 0, "", 0, 1, "/src", "/dst", "/dst/out", []string{"AGENTS.md"}, map[string]bool{}, ui.New(&buf), func(_, _ string, _ []string, _ map[string]bool) error {
		return errors.New("boom")
	})
	assert.Equal(t, "failed (boom)", status)
	assert.Contains(t, buf.String(), "Target repository synchronization failed: boom")

	buf.Reset()
	called = false
	status = runTargetRepoSync(true, true, false, false, false, false, 0, "", 0, 1, "/src", "/dst", "/dst/out", nil, nil, ui.New(&buf), func(_, _ string, _ []string, _ map[string]bool) error {
		called = true
		return nil
	})
	assert.False(t, called)
	assert.Equal(t, "skipped (pre-run manifest snapshot unavailable)", status)
	assert.Contains(t, buf.String(), "Skipping target repository synchronization: pre-run manifest snapshot unavailable")

	buf.Reset()
	var gotExcludes []string
	var gotManifest map[string]bool
	manifest := map[string]bool{"a.txt": true}
	targetDir := t.TempDir()
	status = runTargetRepoSync(true, true, false, false, false, false, 0, "", 0, 1, "/src", targetDir, filepath.Join(targetDir, "output"), []string{"AGENTS.md", "CLAUDE.md"}, manifest, ui.New(&buf), func(_, _ string, excludes []string, preRun map[string]bool) error {
		gotExcludes = excludes
		gotManifest = preRun
		return nil
	})
	assert.Equal(t, "synchronized to "+targetDir, status)
	assert.Equal(t, []string{".git", "output", "AGENTS.md", "CLAUDE.md"}, gotExcludes)
	assert.Equal(t, manifest, gotManifest)
}

func setupDummySyncRunHarness(t *testing.T, postScriptMarker string) string {
	t.Helper()
	dir := t.TempDir()
	postScript := fmt.Sprintf("#!/bin/sh\n: > %q\n", postScriptMarker)
	files := map[string]string{
		"agents/code.md":                  "You are a code agent.",
		"harness/code.yaml":               "agent: agents/code.md\nrole: code\npolicy: policies/base.yaml\npost_script: scripts/post.sh\n",
		"policies/base.yaml":              "version: 1\n",
		"config.yaml":                     "agents:\n  - harness/code.yaml\n",
		"behaviour/current-scenario.yaml": "ops:\n  - op: read_file\n    args: agent-run-marker\n",
		"scripts/post.sh":                 postScript,
	}
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		require.NoError(t, os.WriteFile(p, []byte(content), mode))
	}
	return dir
}

func installOpenshellDownloadWrapper(t *testing.T, repoBase, sandboxRepoDir string) {
	t.Helper()
	useFakeOpenshellProviders(t)
	realStub, err := filepath.Abs(filepath.Join("testdata", "providers-stub", "openshell"))
	require.NoError(t, err)

	wrapperDir := t.TempDir()
	wrapperScript := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "sandbox" ] && [ "$2" = "download" ]; then
  case "$4" in
    */%s|*/%s/)
      mkdir -p "$5"
      cp -a %q/. "$5"/
      exit 0
      ;;
  esac
  mkdir -p "$5"
  exit 0
fi
exec %q "$@"
`, repoBase, repoBase, sandboxRepoDir, realStub)
	require.NoError(t, os.WriteFile(filepath.Join(wrapperDir, "openshell"), []byte(wrapperScript), 0o755))
	t.Setenv("PATH", wrapperDir+string(filepath.ListSeparator)+os.Getenv("PATH"))
}

func TestRunAgent_SyncTargetRepo_EndToEndSafeDownloadPreservesStrippedSymlinks(t *testing.T) {
	t.Setenv("FULLSEND_RUNTIME", "dummy")
	t.Setenv("FULLSEND_SYNC_TARGET_REPO", "")

	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "app.go"), []byte("package main // v1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "stale.txt"), []byte("remove me"), 0o644))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(repoDir, "abs-link")))

	// Sandbox download staging source includes modified app.go, added.txt,
	// omits stale.txt, and includes abs-link (which sanitizeDownload strips).
	sandboxRepo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sandboxRepo, "app.go"), []byte("package main // v2\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sandboxRepo, "added.txt"), []byte("from sandbox\n"), 0o644))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(sandboxRepo, "abs-link")))

	installOpenshellDownloadWrapper(t, filepath.Base(repoDir), sandboxRepo)

	postMarker := filepath.Join(t.TempDir(), "post-ran")
	dir := setupDummySyncRunHarness(t, postMarker)
	outputDir := filepath.Join(t.TempDir(), "output")
	var buf bytes.Buffer

	err := runAgent(context.Background(), "code", dir, outputDir, repoDir, fakeSandboxBinary(t), nil, false, "", "", "",
		resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(&buf), false, runOverrideFlags{syncTargetRepo: true, syncTargetRepoSet: true})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(repoDir, "app.go"))
	require.NoError(t, err)
	assert.Equal(t, "package main // v2\n", string(data))

	data, err = os.ReadFile(filepath.Join(repoDir, "added.txt"))
	require.NoError(t, err)
	assert.Equal(t, "from sandbox\n", string(data))

	_, err = os.Lstat(filepath.Join(repoDir, "stale.txt"))
	assert.True(t, os.IsNotExist(err), "stale file deleted in sandbox should be pruned")

	linkTarget, err := os.Readlink(filepath.Join(repoDir, "abs-link"))
	require.NoError(t, err, "absolute symlink stripped by SafeDownload must be preserved on host")
	assert.Equal(t, "/etc/passwd", linkTarget)

	gitHead, err := os.ReadFile(filepath.Join(repoDir, ".git", "HEAD"))
	require.NoError(t, err)
	assert.Equal(t, "ref: refs/heads/main\n", string(gitHead))

	assert.Contains(t, buf.String(), "Target repo sync:")
	assert.Contains(t, buf.String(), "synchronized to "+repoDir)
	_, err = os.Stat(postMarker)
	assert.NoError(t, err, "post_script must execute after successful sync")
}

func TestRunAgent_SyncTargetRepo_SyncFailureStillRunsPostScript(t *testing.T) {
	t.Setenv("FULLSEND_RUNTIME", "dummy")
	t.Setenv("FULLSEND_SYNC_TARGET_REPO", "")

	repoDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "app.go"), []byte("v1"), 0o644))
	sandboxRepo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sandboxRepo, "app.go"), []byte("v2"), 0o644))

	installOpenshellDownloadWrapper(t, filepath.Base(repoDir), sandboxRepo)

	origSyncFn := syncTargetRepoDirFn
	t.Cleanup(func() {
		syncTargetRepoDirFn = origSyncFn
	})
	syncTargetRepoDirFn = func(_, _ string, _ []string, _ map[string]bool) error {
		return errors.New("disk full")
	}

	postMarker := filepath.Join(t.TempDir(), "post-ran")
	dir := setupDummySyncRunHarness(t, postMarker)
	outputDir := filepath.Join(t.TempDir(), "output")
	var buf bytes.Buffer

	err := runAgent(context.Background(), "code", dir, outputDir, repoDir, fakeSandboxBinary(t), nil, false, "", "", "",
		resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(&buf), false, runOverrideFlags{syncTargetRepo: true, syncTargetRepoSet: true})
	require.NoError(t, err, "sync failure after completed run must warn rather than aborting post_script")

	assert.Contains(t, buf.String(), "Target repository synchronization failed: disk full")
	assert.Contains(t, buf.String(), "failed (disk full)")
	_, err = os.Stat(postMarker)
	assert.NoError(t, err, "post_script must still run when target repo sync fails")
}

func TestSyncTargetRepoDirWithManifest_ReadonlySourcePreservesWritableHostModes(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// Writable host checkout (0755 dirs, 0644/0755 files) before a readonly_repo run.
	require.NoError(t, os.MkdirAll(filepath.Join(dstDir, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dstDir, "pkg", "unchanged.txt"), []byte("same"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dstDir, "pkg", "modified.txt"), []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dstDir, "pkg", "became_exec.sh"), []byte("#!/bin/sh\necho hi"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dstDir, "pkg", "was_exec.sh"), []byte("#!/bin/sh\necho keep"), 0o755))

	manifest, err := recordTargetRepoManifest(dstDir)
	require.NoError(t, err)

	// Sandbox checkout after step 8c `chmod a-w` (0555 dirs, 0444/0555 files).
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(srcDir, "pkg"), 0o755)
		_ = os.Chmod(filepath.Join(srcDir, "newdir"), 0o755)
	})
	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "pkg", "unchanged.txt"), []byte("same"), 0o444))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "pkg", "modified.txt"), []byte("new"), 0o444))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "pkg", "became_exec.sh"), []byte("#!/bin/sh\necho hi"), 0o555))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "pkg", "was_exec.sh"), []byte("#!/bin/sh\necho keep"), 0o555))
	require.NoError(t, os.Chmod(filepath.Join(srcDir, "pkg"), 0o555))

	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "newdir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "newdir", "added.txt"), []byte("added"), 0o444))
	require.NoError(t, os.Chmod(filepath.Join(srcDir, "newdir"), 0o555))

	require.NoError(t, syncTargetRepoDirWithManifest(srcDir, dstDir, []string{".git"}, manifest))

	pkgInfo, err := os.Stat(filepath.Join(dstDir, "pkg"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), pkgInfo.Mode().Perm(), "existing writable host directory must stay writable")

	newDirInfo, err := os.Stat(filepath.Join(dstDir, "newdir"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), newDirInfo.Mode().Perm(), "new directory on writable host checkout must be writable")

	unchangedInfo, err := os.Stat(filepath.Join(dstDir, "pkg", "unchanged.txt"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), unchangedInfo.Mode().Perm(), "existing 0644 file must not become 0444")

	modifiedInfo, err := os.Stat(filepath.Join(dstDir, "pkg", "modified.txt"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), modifiedInfo.Mode().Perm(), "modified file must preserve host write bits")

	becameExecInfo, err := os.Stat(filepath.Join(dstDir, "pkg", "became_exec.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), becameExecInfo.Mode().Perm(), "executable bit change from readonly source must keep host write bits")

	wasExecInfo, err := os.Stat(filepath.Join(dstDir, "pkg", "was_exec.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), wasExecInfo.Mode().Perm(), "existing 0755 file must stay 0755")

	addedInfo, err := os.Stat(filepath.Join(dstDir, "newdir", "added.txt"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), addedInfo.Mode().Perm(), "new file on writable host checkout must not be 0444")
}

func TestSyncTargetRepoDirWithManifest_ModeOnlyChangeBreaksHardlink(t *testing.T) {
	externalDir := t.TempDir()
	extSharedFile := filepath.Join(externalDir, "store.txt")
	require.NoError(t, os.WriteFile(extSharedFile, []byte("shared-content"), 0o644))

	dstDir := t.TempDir()
	dstLinkedFile := filepath.Join(dstDir, "linked.sh")
	require.NoError(t, os.Link(extSharedFile, dstLinkedFile))

	manifest, err := recordTargetRepoManifest(dstDir)
	require.NoError(t, err)

	srcDir := t.TempDir()
	// Same bytes, different mode (0755 vs 0644).
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "linked.sh"), []byte("shared-content"), 0o755))

	require.NoError(t, syncTargetRepoDirWithManifest(srcDir, dstDir, []string{".git"}, manifest))

	dstInfo, err := os.Stat(dstLinkedFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), dstInfo.Mode().Perm())

	extInfo, err := os.Stat(extSharedFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), extInfo.Mode().Perm(), "mode-only sync update must break hardlink instead of mutating shared inode")
	assert.NotEqual(t, extInfo.Sys().(*syscall.Stat_t).Ino, dstInfo.Sys().(*syscall.Stat_t).Ino)
}

func TestSyncTargetRepoDirWithManifest_NilManifestSkipsPruning(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dstDir, "host-only.txt"), []byte("keep"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "new.txt"), []byte("new"), 0o644))

	require.NoError(t, syncTargetRepoDirWithManifest(srcDir, dstDir, []string{".git"}, nil))

	assert.FileExists(t, filepath.Join(dstDir, "new.txt"))
	assert.FileExists(t, filepath.Join(dstDir, "host-only.txt"), "nil manifest must fail closed and skip pruning")
}

func TestRunOverrides_InvalidSyncTargetRepo(t *testing.T) {
	t.Setenv("FULLSEND_SYNC_TARGET_REPO", "yes")
	_, err := resolveRunOverrides(runOverrideFlags{}, os.Getenv, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FULLSEND_SYNC_TARGET_REPO")
	assert.Contains(t, err.Error(), `"yes"`)
}
