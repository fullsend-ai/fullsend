package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeDownload_RemovesAbsoluteSymlinks(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "real.txt"), []byte("ok"), 0o644))
	require.NoError(t, os.Symlink("/nonexistent/target", filepath.Join(dir, "danger")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(dir, "real.txt"))
	assert.NoError(t, err)

	_, err = os.Lstat(filepath.Join(dir, "danger"))
	assert.True(t, os.IsNotExist(err), "absolute symlink should have been removed")
}

func TestSanitizeDownload_KeepsRelativeSymlinksInsideRepo(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "target.txt"), []byte("ok"), 0o644))
	// Relative symlink: sub/link -> ../target.txt (stays inside dir)
	require.NoError(t, os.Symlink("../target.txt", filepath.Join(dir, "sub", "link")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	_, err = os.Lstat(filepath.Join(dir, "sub", "link"))
	assert.NoError(t, err, "relative in-repo symlink should be preserved")
}

func TestSanitizeDownload_RemovesSymlinkChainEscape(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "real"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	// dirlink -> ../real: relative, inside repo — sanitizeDownload keeps it.
	require.NoError(t, os.Symlink("../real", filepath.Join(dir, "sub", "dirlink")))
	// escape -> "sub/dirlink/../../etc/passwd":
	//   filepath.Clean sees: dir/sub/dirlink/../../etc/passwd → dir/etc/passwd (inside, passes textual check)
	//   EvalSymlinks follows: sub/dirlink → dir/real → ../../etc/passwd → outside dir (escapes)
	require.NoError(t, os.Symlink("sub/dirlink/../../etc/passwd", filepath.Join(dir, "escape")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	_, err = os.Lstat(filepath.Join(dir, "sub", "dirlink"))
	assert.NoError(t, err, "in-repo dirlink should be preserved")

	_, err = os.Lstat(filepath.Join(dir, "escape"))
	assert.True(t, os.IsNotExist(err), "symlink-chain escape should be removed")
}

func TestSanitizeDownload_RemovesRelativeSymlinksEscapingRepo(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	// Relative symlink that traverses above dir root.
	require.NoError(t, os.Symlink("../../etc/passwd", filepath.Join(dir, "sub", "escape")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	_, err = os.Lstat(filepath.Join(dir, "sub", "escape"))
	assert.True(t, os.IsNotExist(err), "escaping relative symlink should have been removed")
}

func TestSanitizeDownload_RemovesDirSymlinkIndirection(t *testing.T) {
	repo := t.TempDir()

	// Place a secret file outside the repo root.
	secret := filepath.Join(filepath.Dir(repo), "secret")
	require.NoError(t, os.WriteFile(secret, []byte("leaked"), 0o644))
	t.Cleanup(func() { os.Remove(secret) })

	// d/x is a directory symlink to "." — relative, inside repo, so kept.
	// e targets "d/x/../../secret" which textually cleans to repo/secret (inside),
	// but on the filesystem d/x resolves to d/, so ../../secret escapes.
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "d"), 0o755))
	require.NoError(t, os.Symlink(".", filepath.Join(repo, "d", "x")))
	require.NoError(t, os.Symlink("d/x/../../secret", filepath.Join(repo, "e")))

	require.NoError(t, sanitizeDownload(repo))

	_, err := os.Lstat(filepath.Join(repo, "e"))
	assert.True(t, os.IsNotExist(err), "dir-symlink indirection escape should have been removed")
}

func TestSanitizeDownload_RemovesGitHooks(t *testing.T) {
	dir := t.TempDir()

	// Create .git/hooks/ with a script.
	hooksDir := filepath.Join(dir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\nmalicious"), 0o755))

	// Create a safe file under .git/.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core]"), 0o644))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// .git/hooks/ should be removed entirely.
	_, err = os.Stat(hooksDir)
	assert.True(t, os.IsNotExist(err), ".git/hooks/ should have been removed")

	// .git/config should survive.
	_, err = os.Stat(filepath.Join(dir, ".git", "config"))
	assert.NoError(t, err)
}

func TestSanitizeDownload_NestedSymlinks(t *testing.T) {
	dir := t.TempDir()

	// Create nested structure with symlinks at various depths.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a", "b", "real.txt"), []byte("ok"), 0o644))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(dir, "a", "b", "link")))
	require.NoError(t, os.Symlink("/etc/shadow", filepath.Join(dir, "a", "top-link")))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// Real file survives.
	_, err = os.Stat(filepath.Join(dir, "a", "b", "real.txt"))
	assert.NoError(t, err)

	// Both symlinks removed.
	_, err = os.Lstat(filepath.Join(dir, "a", "b", "link"))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Lstat(filepath.Join(dir, "a", "top-link"))
	assert.True(t, os.IsNotExist(err))
}

func TestSanitizeDownload_RemovesSubmoduleGitHooks(t *testing.T) {
	dir := t.TempDir()

	// Create submodule .git/hooks/ with a script.
	subHooks := filepath.Join(dir, "vendor", "dep", ".git", "hooks")
	require.NoError(t, os.MkdirAll(subHooks, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(subHooks, "post-checkout"), []byte("#!/bin/sh\nmalicious"), 0o755))

	// Create a safe file in the submodule .git/.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vendor", "dep", ".git", "config"), []byte("[core]"), 0o644))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// Submodule .git/hooks/ should be removed.
	_, err = os.Stat(subHooks)
	assert.True(t, os.IsNotExist(err), "submodule .git/hooks/ should have been removed")

	// Submodule .git/config should survive.
	_, err = os.Stat(filepath.Join(dir, "vendor", "dep", ".git", "config"))
	assert.NoError(t, err)
}

func TestSanitizeDownload_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	err := sanitizeDownload(dir)
	assert.NoError(t, err)
}

// TestSanitizeDownload_RemovesAncestryMetadata removes replacement refs,
// grafts, and commit-graph caches while keeping the rest of .git intact.
func TestSanitizeDownload_RemovesAncestryMetadata(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	writeFile(t, root, ".git/config", "[core]\n")
	writeFile(t, root, ".git/index", "index bytes\n")
	writeFile(t, root, ".git/objects/ab/object", "object\n")
	writeFile(t, root, ".git/objects/pack/pack-abc.idx", "pack\n")
	writeFile(t, root, ".git/refs/heads/main", testOID+"\n")
	writeFile(t, root, ".git/refs/remotes/origin/main", testOID+"\n")
	writeFile(t, root, ".git/refs/tags/v1", testOID+"\n")
	writeFile(t, root, ".git/refs/stash", testOID+"\n")
	writeFile(t, root, ".git/refs/notes/commits", testOID+"\n")
	writeFile(t, root, ".git/refs/bisect/good", testOID+"\n")
	writeFile(t, root, ".git/refs/custom/x", testOID+"\n")
	writeFile(t, root, ".git/refs/replace/"+testOID, testOID+"\n")
	writeFile(t, root, ".git/info/exclude", "# exclude\n")
	writeFile(t, root, ".git/info/refs", "cache\n")
	writeFile(t, root, ".git/info/grafts", testOID+" "+testOID+"\n")
	writeFile(t, root, ".git/objects/info/packs", "cache\n")
	writeFile(t, root, ".git/objects/info/commit-graph", "graph\n")
	writeFile(t, root, ".git/objects/info/commit-graphs/chain", "graph chain\n")
	writeFile(t, root, ".git/modules/dep/config", "[core]\n")
	writeFile(t, root, ".git/worktrees/wt/HEAD", "ref: refs/heads/wt\n")

	require.NoError(t, sanitizeDownload(root))

	for _, rel := range []string{
		".git/HEAD", ".git/config", ".git/index",
		".git/objects/ab/object", ".git/objects/pack/pack-abc.idx",
		".git/refs/heads/main", ".git/refs/remotes/origin/main",
		".git/refs/tags/v1", ".git/refs/stash", ".git/refs/notes/commits",
		".git/refs/bisect/good", ".git/refs/custom/x",
		".git/info/exclude", ".git/info/refs", ".git/objects/info/packs",
		".git/modules/dep/config", ".git/worktrees/wt/HEAD",
	} {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		assert.NoError(t, err, "%s must be retained", rel)
	}
	for _, rel := range []string{
		".git/refs/replace", ".git/info/grafts",
		".git/objects/info/commit-graph", ".git/objects/info/commit-graphs",
	} {
		_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		assert.True(t, os.IsNotExist(err), "%s must be removed", rel)
	}
}

// TestSanitizeDownload_PreservesLiteralBackslashNames checks that a Unix file
// whose name contains backslashes is not treated as a slash-separated metadata
// path.
func TestSanitizeDownload_PreservesLiteralBackslashNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("backslash is a path separator on Windows")
	}
	root := t.TempDir()
	name := `.git/objects\info\commit-graph`
	writeFile(t, root, name, "graph\n")

	require.NoError(t, sanitizeDownload(root))

	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(name)))
	require.NoError(t, err, "literal backslash name must survive sanitization")
}

// TestSanitizeDownload_RejectsSymlinkedGitMetadata fails closed when the root
// .git or a container holding filtered metadata is a symlink.
func TestSanitizeDownload_RejectsSymlinkedGitMetadata(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{
			name: "root git",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "real-git"), 0o755))
				require.NoError(t, os.Symlink("real-git", filepath.Join(root, ".git")))
			},
		},
		{
			name: "refs",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
				require.NoError(t, os.MkdirAll(filepath.Join(root, "real-refs"), 0o755))
				require.NoError(t, os.Symlink("../real-refs", filepath.Join(root, ".git", "refs")))
			},
		},
		{
			name: "info",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
				require.NoError(t, os.MkdirAll(filepath.Join(root, "real-info"), 0o755))
				require.NoError(t, os.Symlink("../real-info", filepath.Join(root, ".git", "info")))
			},
		},
		{
			name: "objects",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
				require.NoError(t, os.MkdirAll(filepath.Join(root, "real-objects"), 0o755))
				require.NoError(t, os.Symlink("../real-objects", filepath.Join(root, ".git", "objects")))
			},
		},
		{
			name: "objects info",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "objects"), 0o755))
				require.NoError(t, os.MkdirAll(filepath.Join(root, "real-oinfo"), 0o755))
				require.NoError(t, os.Symlink("../../real-oinfo", filepath.Join(root, ".git", "objects", "info")))
			},
		},
		{
			name: "nested git",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "vendor", "dep", "real-git"), 0o755))
				require.NoError(t, os.Symlink("real-git", filepath.Join(root, "vendor", "dep", ".git")))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)

			err := sanitizeDownload(root)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "git metadata")
		})
	}
}

func TestSanitizeDownload_RejectsRedirectedRootGitMetadata(t *testing.T) {
	requireGit(t)
	for _, layout := range []string{"gitfile", "commondir"} {
		t.Run(layout, func(t *testing.T) {
			r := newGitRepo(t)
			head := r.commitFile("file.txt", "original")
			gitDir := filepath.Join(r.dir, ".git")
			headFile, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
			require.NoError(t, err)
			require.NoError(t, os.Rename(gitDir, filepath.Join(r.dir, "git-metadata")))
			if layout == "gitfile" {
				writeFile(t, r.dir, ".git", "gitdir: git-metadata\n")
			} else {
				writeFile(t, r.dir, ".git/HEAD", string(headFile))
				writeFile(t, r.dir, ".git/commondir", "../git-metadata\n")
			}

			assert.Equal(t, head, r.git("rev-parse", "HEAD"), "Git must recognize the redirected layout")
			require.Error(t, sanitizeDownload(r.dir), "redirected Git metadata must be rejected")
		})
	}
}

// TestSanitizeDownload_AcceptsReftableGitStorage accepts a real reftable
// repository and preserves its refs, HEAD, and config bytes.
func TestSanitizeDownload_AcceptsReftableGitStorage(t *testing.T) {
	requireReftableSanitization(t)
	r := newReftableRepo(t)
	head := r.commitFile("file.txt", "original")
	configPath := filepath.Join(r.dir, ".git", "config")
	configBefore, err := os.ReadFile(configPath)
	require.NoError(t, err)

	require.NoError(t, sanitizeDownload(r.dir))

	assert.Equal(t, head, r.git("rev-parse", "HEAD"))
	assert.Equal(t, "main", r.git("symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, head, r.git("rev-parse", "main"))
	_, err = os.Stat(filepath.Join(r.dir, ".git", "reftable", "tables.list"))
	require.NoError(t, err, "reftable tables must survive sanitization")
	configAfter, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, configBefore, configAfter, "repository config bytes must be preserved")
}

// TestSanitizeDownload_RemovesReftableReplacementRefs removes a forged
// ancestry bridge stored as a reftable replacement ref.
func TestSanitizeDownload_RemovesReftableReplacementRefs(t *testing.T) {
	requireReftableSanitization(t)
	r := newReftableRepo(t)
	human := r.commitFile("human.txt", "human work")
	tree := r.git("rev-parse", human+"^{tree}")
	orphan := r.git("commit-tree", tree, "-m", "agent orphan")
	bridge := r.git("commit-tree", tree, "-p", human, "-m", "bridge")
	r.git("replace", orphan, bridge)

	require.True(t, r.isAncestor(human, orphan))
	require.NoError(t, sanitizeDownload(r.dir))

	assert.False(t, r.isAncestor(human, orphan), "forged ancestry must not survive")
	assert.Empty(t, r.git("replace", "--list"))
	assert.Equal(t, human, r.git("rev-parse", "main"))
}

// TestSanitizeDownload_ReftableSymbolicReplacementRemovesLinkOnly deletes a
// symbolic replacement ref without dereferencing its target branch.
func TestSanitizeDownload_ReftableSymbolicReplacementRemovesLinkOnly(t *testing.T) {
	requireReftableSanitization(t)
	r := newReftableRepo(t)
	head := r.commitFile("human.txt", "human work")
	link := "refs/replace/" + head
	r.git("symbolic-ref", link, "refs/heads/main")
	require.Equal(t, "refs/heads/main", r.git("symbolic-ref", link))

	require.NoError(t, sanitizeDownload(r.dir))

	_, err := r.tryGit("show-ref", "--verify", link)
	require.Error(t, err, "symbolic replacement ref must be removed")
	assert.Equal(t, head, r.git("rev-parse", "refs/heads/main"), "target branch must be unchanged")
}

// TestSanitizeDownload_ReftableUnbornChecksOut accepts an unborn reftable
// repository with no refs yet.
func TestSanitizeDownload_ReftableUnbornChecksOut(t *testing.T) {
	requireReftableSanitization(t)
	r := newReftableRepo(t)

	require.NoError(t, sanitizeDownload(r.dir))

	assert.Equal(t, "refs/heads/main", r.git("symbolic-ref", "HEAD"))
	assert.Empty(t, r.git("for-each-ref", "refs/heads/"))
}

// TestSanitizeDownload_ReftableShallowBoundary accepts a shallow reftable
// repository and preserves its shallow boundary.
func TestSanitizeDownload_ReftableShallowBoundary(t *testing.T) {
	requireReftableSanitization(t)
	src := newGitRepo(t)
	src.commitFile("file.txt", "one")
	src.commitFile("file.txt", "two")

	r := newReftableRepo(t)
	r.git("remote", "add", "origin", src.dir)
	r.git("fetch", "--quiet", "--depth=1", "origin", "main")
	r.git("reset", "--quiet", "--hard", "FETCH_HEAD")
	shallowBefore, err := os.ReadFile(filepath.Join(r.dir, ".git", "shallow"))
	require.NoError(t, err, "fixture must be shallow")

	require.NoError(t, sanitizeDownload(r.dir))

	shallowAfter, err := os.ReadFile(filepath.Join(r.dir, ".git", "shallow"))
	require.NoError(t, err)
	assert.Equal(t, shallowBefore, shallowAfter, "shallow boundary must survive sanitization")
	assert.Equal(t, "1", r.git("rev-list", "--count", "HEAD"))
}

// TestSanitizeDownload_ReftableIgnoresEvilConfig proves repository fsck and
// hook settings cannot weaken replacement cleanup or execute code.
func TestSanitizeDownload_ReftableIgnoresEvilConfig(t *testing.T) {
	requireReftableSanitization(t)
	r := newReftableRepo(t)
	human := r.commitFile("human.txt", "human work")
	tree := r.git("rev-parse", human+"^{tree}")
	orphan := r.git("commit-tree", tree, "-m", "agent orphan")
	bridge := r.git("commit-tree", tree, "-p", human, "-m", "bridge")
	r.git("replace", orphan, bridge)

	hookDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(hookDir, "reference-transaction")
	script := "#!/bin/sh\nprintf ran > " + marker + "\n"
	require.NoError(t, os.WriteFile(hook, []byte(script), 0o755))
	skipList := writeFile(t, t.TempDir(), "skiplist", "")

	for key, value := range map[string]string{
		"core.hooksPath":             hookDir,
		"fsck.badReftableTableName":  "ignore",
		"fsck.badRefOid":             "ignore",
		"fsck.badHeadTarget":         "ignore",
		"fsck.badReferentName":       "ignore",
		"fsck.symrefTargetIsNotARef": "ignore",
		"fsck.skipList":              skipList,
	} {
		r.git("config", key, value)
	}
	configPath := filepath.Join(r.dir, ".git", "config")
	configBefore, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.NoFileExists(t, marker, "hook must not run during setup")

	require.NoError(t, sanitizeDownload(r.dir))

	assert.NoFileExists(t, marker, "repository hook must never execute")
	assert.Empty(t, r.git("replace", "--list"), "repository fsck config must not weaken cleanup")
	configAfter, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, configBefore, configAfter, "repository config bytes must be preserved")
}

// TestSafeDownload_ReftableIgnoresConfigHooks proves repository config cannot
// define a reference-transaction hook that runs while replacement refs are
// deleted, for both object formats, and that ordinary refs survive.
func TestSafeDownload_ReftableIgnoresConfigHooks(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			requireReftableSanitization(t)
			r := newReftableRepoFormat(t, format)
			human := r.commitFile("human.txt", "human work")
			tree := r.git("rev-parse", human+"^{tree}")
			orphan := r.git("commit-tree", tree, "-m", "agent orphan")
			bridge := r.git("commit-tree", tree, "-p", human, "-m", "bridge")
			r.git("replace", orphan, bridge)
			r.git("symbolic-ref", "refs/heads/alias", "refs/heads/main")
			r.git("tag", "-a", "v1", "-m", "v1")
			head := r.git("rev-parse", "HEAD")
			tag := r.git("rev-parse", "v1")

			marker := filepath.Join(t.TempDir(), "hook-ran")
			r.git("config", "hook.probe.command", "printf ran > '"+marker+"'")
			r.git("config", "hook.probe.event", "reference-transaction")
			r.git("config", "hook.probe.enabled", "true")
			require.NoFileExists(t, marker, "hook must not run while refs are created")

			serveDownloadFrom(t, r.dir)
			localDir := filepath.Join(t.TempDir(), "repo")
			require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
				RepositorySnapshot{}))

			assert.NoFileExists(t, marker, "repository config hook must never execute")
			out, err := runGit(t, localDir, r.env, "replace", "--list")
			require.NoError(t, err)
			assert.Empty(t, out, "replacement refs must not be published")
			assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "HEAD"))
			assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "main"))
			assert.Equal(t, "refs/heads/main", r.gitAt(localDir, "symbolic-ref", "refs/heads/alias"))
			assert.Equal(t, tag, r.gitAt(localDir, "rev-parse", "v1"))
		})
	}
}

// TestSanitizeDownload_ReftableCorruptTableFailsClosed rejects a corrupt
// reftable record and never publishes it through SafeDownload.
func TestSanitizeDownload_ReftableCorruptTableFailsClosed(t *testing.T) {
	r := newReftableRepo(t)
	r.commitFile("file.txt", "original")
	tables, err := filepath.Glob(filepath.Join(r.dir, ".git", "reftable", "*.ref"))
	require.NoError(t, err)
	require.NotEmpty(t, tables)
	data, err := os.ReadFile(tables[0])
	require.NoError(t, err)
	data[4] ^= 0xff // corrupt the table format version
	require.NoError(t, os.WriteFile(tables[0], data, 0o644))

	require.Error(t, sanitizeDownload(r.dir), "corrupt reftable bytes must fail closed")

	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	err = SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
	assert.NoDirExists(t, localDir, "corrupt reftable storage must not be published")
}

// TestSafeDownload_RemovesReplacementRefsByExactName removes every replacement
// ref by its exact byte suffix, including suffixes that are not object ids or
// contain non-breaking spaces, and leaves HEAD and branches unchanged.
func TestSafeDownload_RemovesReplacementRefsByExactName(t *testing.T) {
	cases := []struct {
		name     string
		suffix   string
		ordinary bool
	}{
		{name: "object id suffix", ordinary: true},
		{name: "non-hex suffix", suffix: "not-an-object-id"},
		{name: "non-breaking space suffix", suffix: "\u00a0"},
		{name: "surrounded non-breaking space suffix", suffix: "\u00a0name\u00a0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireReftableSanitization(t)
			r := newReftableRepo(t)
			human := r.commitFile("file.txt", "original")
			suffix, target := tc.suffix, human
			if tc.ordinary {
				target = r.commitFile("agent.txt", "agent work")
				suffix = human
			}
			ref := "refs/replace/" + suffix
			out, err := runGit(t, r.dir, r.env, "update-ref", "--no-deref", ref, target)
			require.NoError(t, err, out)
			_, err = runGit(t, r.dir, r.env, "show-ref", "--verify", ref)
			require.NoError(t, err, "fixture must contain the replacement ref")
			head := r.git("rev-parse", "HEAD")

			serveDownloadFrom(t, r.dir)
			localDir := filepath.Join(t.TempDir(), "repo")
			require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
				RepositorySnapshot{}))

			_, err = runGit(t, localDir, r.env, "show-ref", "--verify", ref)
			require.Error(t, err, "replacement ref %q must be removed", ref)
			out, err = runGit(t, localDir, r.env, "for-each-ref", "refs/replace/")
			require.NoError(t, err)
			assert.Empty(t, out, "no replacement refs may be published")
			assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "HEAD"))
			assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "main"))
		})
	}
}

// TestSanitizeDownload_ReftableVersionAndQueryFailures fails closed when the
// native version is too old, unparseable, the storage or object-format query
// fails, the object format is unsupported, or the trusted store cannot be
// initialized.
func TestSanitizeDownload_ReftableVersionAndQueryFailures(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "git 2.52",
			body: "if [ \"$1\" = \"--version\" ]; then\n  echo 'git version 2.52.0'\n  exit 0\nfi",
		},
		{
			name: "unparseable version",
			body: "if [ \"$1\" = \"--version\" ]; then\n  echo 'git version bogus'\n  exit 0\nfi",
		},
		{
			name: "version query failure",
			body: "if [ \"$1\" = \"--version\" ]; then exit 3; fi",
		},
		{
			name: "storage query failure",
			body: "for a in \"$@\"; do\n  if [ \"$a\" = config ]; then exit 2; fi\ndone",
		},
		{
			name: "object format query failure",
			body: "for a in \"$@\"; do\n  if [ \"$a\" = extensions.objectFormat ]; then exit 2; fi\ndone",
		},
		{
			name: "unsupported object format",
			body: "for a in \"$@\"; do\n  if [ \"$a\" = extensions.objectFormat ]; then echo sha3; exit 0; fi\ndone",
		},
		{
			name: "trusted store init failure",
			body: "for a in \"$@\"; do\n  if [ \"$a\" = init ]; then exit 2; fi\ndone",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newReftableRepo(t)
			r.commitFile("file.txt", "original")
			installGitShim(t, tc.body)

			require.Error(t, sanitizeDownload(r.dir))
		})
	}
}

// TestSanitizeDownload_ReftableListingFailureFailsClosed rejects a repository
// whose replacement refs cannot be listed, before any ref is modified.
func TestSanitizeDownload_ReftableListingFailureFailsClosed(t *testing.T) {
	r := newReftableRepo(t)
	r.commitFile("file.txt", "original")
	installGitShim(t, "for a in \"$@\"; do\n  if [ \"$a\" = replace ]; then exit 2; fi\ndone")

	require.Error(t, sanitizeDownload(r.dir))
}

// TestSanitizeDownload_ReftableDeleteFailureFailsClosed rejects the download
// when replacement refs cannot be deleted.
func TestSanitizeDownload_ReftableDeleteFailureFailsClosed(t *testing.T) {
	r := newReftableRepo(t)
	human := r.commitFile("file.txt", "original")
	tree := r.git("rev-parse", human+"^{tree}")
	orphan := r.git("commit-tree", tree, "-m", "agent orphan")
	bridge := r.git("commit-tree", tree, "-p", human, "-m", "bridge")
	r.git("replace", orphan, bridge)
	installGitShim(t, "for a in \"$@\"; do\n  if [ \"$a\" = update-ref ]; then exit 2; fi\ndone")

	require.Error(t, sanitizeDownload(r.dir))
	assert.NotEmpty(t, r.git("replace", "--list"), "failed deletion must not drop the replacement refs")
}

// TestSanitizeDownload_ReftableLayoutMatrix rejects malformed reftable
// layouts before native Git runs.
func TestSanitizeDownload_ReftableLayoutMatrix(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{
			name: "reftable symlink",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
				require.NoError(t, os.MkdirAll(filepath.Join(root, "outside"), 0o755))
				require.NoError(t, os.Symlink("../outside", filepath.Join(root, ".git", "reftable")))
			},
		},
		{
			name: "reftable file",
			setup: func(t *testing.T, root string) {
				writeFile(t, root, ".git/reftable", "not a directory\n")
			},
		},
		{
			name: "tables list symlink",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "reftable"), 0o755))
				writeFile(t, root, "outside", "x\n")
				require.NoError(t, os.Symlink("../../outside", filepath.Join(root, ".git", "reftable", "tables.list")))
			},
		},
		{
			name: "tables list fifo",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "reftable"), 0o755))
				requireFIFO(t, filepath.Join(root, ".git", "reftable"), "tables.list")
			},
		},
		{
			name: "table symlink",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "reftable"), 0o755))
				writeFile(t, root, "outside", "x\n")
				require.NoError(t, os.Symlink("../../outside", filepath.Join(root, ".git", "reftable", "table.ref")))
			},
		},
		{
			name: "nested directory",
			setup: func(t *testing.T, root string) {
				writeFile(t, root, ".git/reftable/sub/table.ref", "x\n")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)

			err := sanitizeDownload(root)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "reftable")
		})
	}
}

// TestSanitizeDownload_ReftableRejectsNonRegularHEAD fails closed when the
// reftable checkout's HEAD is missing or not a regular file.
func TestSanitizeDownload_ReftableRejectsNonRegularHEAD(t *testing.T) {
	requireReftableSanitization(t)
	cases := []struct {
		name         string
		setup        func(t *testing.T, path string)
		wantRejected bool
		wantNotExist bool
	}{
		{
			name:         "missing",
			setup:        func(t *testing.T, path string) { require.NoError(t, os.Remove(path)) },
			wantRejected: true,
			wantNotExist: true,
		},
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0o755))
			},
			wantRejected: true,
		},
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink("config", path))
			},
			wantRejected: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newReftableRepo(t)
			r.commitFile("file.txt", "original")
			tc.setup(t, filepath.Join(r.dir, ".git", "HEAD"))

			err := sanitizeDownload(r.dir)
			require.Error(t, err)
			assert.Equal(t, tc.wantRejected, errors.Is(err, ErrRepositoryRejected), "got %v", err)
			if tc.wantNotExist {
				assert.True(t, errors.Is(err, os.ErrNotExist), "a missing HEAD keeps its cause: %v", err)
			}
		})
	}
}

// TestRemoveReplacementRefs_MissingStoreFailsClosed surfaces native verification
// failures instead of deleting anything.
func TestRemoveReplacementRefs_MissingStoreFailsClosed(t *testing.T) {
	requireGit(t)
	require.Error(t, removeReplacementRefs(t.TempDir(), filepath.Join(t.TempDir(), "missing")))
}

// TestSanitizeDownload_RejectsReftableWorktreeMetadata rejects registered
// worktree metadata for reftable, where extra ref stores could live.
func TestSanitizeDownload_RejectsReftableWorktreeMetadata(t *testing.T) {
	r := newReftableRepo(t)
	r.commitFile("file.txt", "original")
	writeFile(t, r.dir, ".git/worktrees/wt/gitdir", "../outside\n")
	sentinel := filepath.Join(t.TempDir(), "outside")
	require.NoFileExists(t, sentinel)

	err := sanitizeDownload(r.dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worktree")
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	assert.NoFileExists(t, sentinel, "worktree manifest must not be followed")
}

// TestSanitizeDownload_ReftableNestedStoresCleaned cleans replacement refs in
// both the root and an ordinary nested checkout's own reftable store.
func TestSanitizeDownload_ReftableNestedStoresCleaned(t *testing.T) {
	requireReftableSanitization(t)
	root := newReftableRepo(t)
	rootHuman := root.commitFile("root.txt", "root work")
	rootTree := root.git("rev-parse", rootHuman+"^{tree}")
	rootOrphan := root.git("commit-tree", rootTree, "-m", "root orphan")
	rootBridge := root.git("commit-tree", rootTree, "-p", rootHuman, "-m", "root bridge")
	root.git("replace", rootOrphan, rootBridge)

	nested := initReftableRepo(t, filepath.Join(root.dir, "vendor", "dep"))
	nestedHuman := nested.commitFile("dep.txt", "dep work")
	nestedTree := nested.git("rev-parse", nestedHuman+"^{tree}")
	nestedOrphan := nested.git("commit-tree", nestedTree, "-m", "dep orphan")
	nestedBridge := nested.git("commit-tree", nestedTree, "-p", nestedHuman, "-m", "dep bridge")
	nested.git("replace", nestedOrphan, nestedBridge)

	require.NoError(t, sanitizeDownload(root.dir))

	assert.Empty(t, root.git("replace", "--list"), "root replacement refs must be removed")
	assert.Empty(t, nested.git("replace", "--list"), "nested replacement refs must be removed")
	assert.Equal(t, rootHuman, root.git("rev-parse", "main"))
	assert.Equal(t, nestedHuman, nested.git("rev-parse", "main"))
}

// TestSanitizeDownload_PackedRefsDropsReplacements drops refs/replace records
// and their peeled lines while preserving every other record byte-for-byte.
func TestSanitizeDownload_PackedRefsDropsReplacements(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "drops replacement record and peeled line",
			in:   testOID + " refs/replace/" + testOID + "\n^" + testOID + "\n" + testOID + " refs/heads/main\n^" + testOID256 + "\n",
			want: testOID + " refs/heads/main\n^" + testOID256 + "\n",
		},
		{
			name: "keeps other namespaces with header, order, and peeled lines",
			in:   "# pack-refs with: peeled fully-peeled sorted \n" + testOID + " refs/heads/main\n" + testOID + " refs/tags/v1\n^" + testOID256 + "\n" + testOID + " refs/remotes/origin/main\n" + testOID + " refs/notes/x\n" + testOID + " refs/stash\n" + testOID + " refs/bisect/good\n" + testOID + " refs/custom/x\n",
			want: "# pack-refs with: peeled fully-peeled sorted \n" + testOID + " refs/heads/main\n" + testOID + " refs/tags/v1\n^" + testOID256 + "\n" + testOID + " refs/remotes/origin/main\n" + testOID + " refs/notes/x\n" + testOID + " refs/stash\n" + testOID + " refs/bisect/good\n" + testOID + " refs/custom/x\n",
		},
		{
			name: "namespace match is case-sensitive and exact",
			in:   testOID + " REFS/replace/x\n" + testOID + " refs/Replace/x\n" + testOID + " refs/replaces/x\n" + testOID + " refs/replace\n" + testOID + " refs/heads/replace\n",
			want: testOID + " REFS/replace/x\n" + testOID + " refs/Replace/x\n" + testOID + " refs/replaces/x\n" + testOID + " refs/replace\n" + testOID + " refs/heads/replace\n",
		},
		{
			name: "preserves unicode ref names and tab separators",
			in:   testOID + " refs/heads/feat\u00a0ure\n" + testOID + "\trefs/tags/v\u20031\n",
			want: testOID + " refs/heads/feat\u00a0ure\n" + testOID + "\trefs/tags/v\u20031\n",
		},
		{
			name: "sha256 record",
			in:   testOID256 + " refs/heads/main\n",
			want: testOID256 + " refs/heads/main\n",
		},
		{
			name: "keeps malformed, unicode, near-miss, and interrupted peeled lines",
			in: "# pack-refs with: peeled fully-peeled sorted \n" +
				"scratch\n" +
				testOID + " refs/replaces/x\r\n" +
				testOID + " refs/heads/feat\u00a0ure\n" +
				testOID + " refs/replace/" + testOID + "\n" +
				"# refs/replace/comment between record and peeled line\n" +
				"^" + testOID + "\n" +
				testOID + " refs/replace/x\n" +
				"^" + testOID + "\n" +
				"^" + testOID256 + "\n",
			want: "# pack-refs with: peeled fully-peeled sorted \n" +
				"scratch\n" +
				testOID + " refs/replaces/x\r\n" +
				testOID + " refs/heads/feat\u00a0ure\n" +
				"# refs/replace/comment between record and peeled line\n" +
				"^" + testOID + "\n" +
				"^" + testOID256 + "\n",
		},
		{
			name: "blank line interrupts peeled association",
			in:   testOID + " refs/replace/x\n\n^" + testOID + "\n",
			want: "\n^" + testOID + "\n",
		},
		{
			name: "final line without newline is preserved",
			in:   testOID + " refs/heads/main\n# tail",
			want: testOID + " refs/heads/main\n# tail",
		},
		{
			name: "final replacement line without newline is dropped",
			in:   testOID + " refs/heads/main\n" + testOID + " refs/replace/x",
			want: testOID + " refs/heads/main\n",
		},
		{
			name: "crlf replacement record and peeled line are dropped",
			in:   testOID + " refs/replace/x\r\n^" + testOID + "\r\n" + testOID + " refs/heads/main\r\n",
			want: testOID + " refs/heads/main\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, ".git/packed-refs", tc.in)
			require.NoError(t, sanitizeDownload(root))
			got, err := os.ReadFile(filepath.Join(root, ".git", "packed-refs"))
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
		})
	}

	t.Run("orphan peeled line is preserved", func(t *testing.T) {
		root := t.TempDir()
		content := "^" + testOID + "\n"
		writeFile(t, root, ".git/packed-refs", content)
		require.NoError(t, sanitizeDownload(root))
		got, err := os.ReadFile(filepath.Join(root, ".git", "packed-refs"))
		require.NoError(t, err)
		assert.Equal(t, content, string(got))
	})

	t.Run("non-regular packed refs is dropped", func(t *testing.T) {
		root := t.TempDir()
		external := filepath.Join(t.TempDir(), "packed-refs")
		require.NoError(t, os.WriteFile(external, []byte(testOID+" refs/heads/main\n"), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
		require.NoError(t, os.Symlink(external, filepath.Join(root, ".git", "packed-refs")))

		require.NoError(t, sanitizeDownload(root))
		_, err := os.Lstat(filepath.Join(root, ".git", "packed-refs"))
		assert.True(t, os.IsNotExist(err), "symlinked packed-refs must be dropped")
		_, err = os.Stat(external)
		assert.NoError(t, err, "symlink target must not be touched")
	})

	t.Run("hardlink target is not truncated", func(t *testing.T) {
		root := t.TempDir()
		external := filepath.Join(t.TempDir(), "shared-packed-refs")
		content := testOID + " refs/replace/x\n" + testOID + " refs/heads/main\n"
		require.NoError(t, os.WriteFile(external, []byte(content), 0o644))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
		packed := filepath.Join(root, ".git", "packed-refs")
		if err := os.Link(external, packed); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}

		require.NoError(t, sanitizeDownload(root))
		shared, err := os.ReadFile(external)
		require.NoError(t, err)
		assert.Equal(t, content, string(shared), "hardlink target must not be truncated")
		got, err := os.ReadFile(packed)
		require.NoError(t, err)
		assert.Equal(t, testOID+" refs/heads/main\n", string(got))
	})

	t.Run("read error propagates", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root bypasses permission checks")
		}
		root := t.TempDir()
		packed := writeFile(t, root, ".git/packed-refs", testOID+" refs/heads/main\n")
		require.NoError(t, os.Chmod(packed, 0o000))
		t.Cleanup(func() { _ = os.Chmod(packed, 0o644) })

		err := sanitizeDownload(root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "packed-refs")
	})

	t.Run("directory is rejected", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "packed-refs"), 0o755))
		err := sanitizeDownload(root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "packed-refs")
	})
}

// TestReadBoundedFile checks the exact bound: limit bytes are accepted and one
// more is reported as oversized.
func TestReadBoundedFile(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		limit     int64
		oversized bool
	}{
		{name: "empty file", limit: 8},
		{name: "exactly the limit", content: "12345678", limit: 8},
		{name: "one over the limit", content: "123456789", limit: 8, oversized: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file")
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o644))

			data, oversized, err := readBoundedFile(path, tc.limit)
			require.NoError(t, err)
			assert.Equal(t, tc.oversized, oversized)
			assert.Equal(t, tc.content, string(data))
		})
	}
}

// TestFilterPackedRefs_Limits rejects an oversized packed-refs file without
// touching it and skips the rewrite when nothing is removed.
func TestFilterPackedRefs_Limits(t *testing.T) {
	t.Run("oversized file fails closed", func(t *testing.T) {
		root := t.TempDir()
		packed := writeFile(t, root, "packed-refs", "")
		require.NoError(t, os.Truncate(packed, maxPackedRefsBytes+1))

		err := filterPackedRefs(packed)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
		assert.Contains(t, err.Error(), fmt.Sprintf("larger than %d bytes", maxPackedRefsBytes))
		info, statErr := os.Stat(packed)
		require.NoError(t, statErr)
		assert.Equal(t, int64(maxPackedRefsBytes+1), info.Size(), "a rejected file must be left untouched")
		entries, readErr := os.ReadDir(root)
		require.NoError(t, readErr)
		require.Len(t, entries, 1, "no temporary file is created")
	})

	t.Run("unchanged file is not replaced", func(t *testing.T) {
		root := t.TempDir()
		packed := writeFile(t, root, "packed-refs", testOID+" refs/heads/main\n")
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Link(packed, alias); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}

		require.NoError(t, filterPackedRefs(packed))
		packedInfo, err := os.Stat(packed)
		require.NoError(t, err)
		aliasInfo, err := os.Stat(alias)
		require.NoError(t, err)
		assert.True(t, os.SameFile(packedInfo, aliasInfo), "an unchanged file must keep its inode")
	})
}

// TestSanitizeDownload_RejectsAlternateObjectStores rejects repositories that
// delegate object lookup to an alternate store, including a real shared clone.
func TestSanitizeDownload_RejectsAlternateObjectStores(t *testing.T) {
	for _, name := range []string{"alternates", "http-alternates"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, ".git/objects/info/"+name, "/elsewhere/objects\n")
			err := sanitizeDownload(root)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "git metadata")
		})
	}

	t.Run("shared clone", func(t *testing.T) {
		requireGit(t)
		r := newGitRepo(t)
		head := r.commitFile("file.txt", "original")
		parent := t.TempDir()
		cloneDir := filepath.Join(parent, "clone")
		r.gitAt(parent, "clone", "--quiet", "--shared", r.dir, "clone")
		_, err := runGit(t, cloneDir, r.env, "cat-file", "-e", head)
		require.NoError(t, err, "Git must read objects through the alternate store")
		require.Error(t, sanitizeDownload(cloneDir), "alternate object stores must be rejected")
	})
}

func TestSanitizeDownload_NestedGitCompatibility(t *testing.T) {
	root := t.TempDir()

	// Ordinary directory-form nested repo is filtered.
	writeFile(t, root, "vendor/dep/.git/refs/replace/"+testOID, testOID+"\n")
	writeFile(t, root, "vendor/dep/.git/config", "[core]\n")
	writeFile(t, root, "vendor/dep/.git/refs/heads/main", testOID+"\n")

	// Unlike a root gitfile, a nested gitfile does not redirect Git operations
	// run at the download root. Preserve it for compatibility; this does not
	// establish safety for Git operations inside submodules or linked worktrees.
	gitfile := writeFile(t, root, "vendor/other/.git", "gitdir: ../../.git/modules/other\n")
	writeFile(t, root, ".git/modules/other/config", "[core]\n")
	writeFile(t, root, ".git/worktrees/wt/HEAD", "ref: refs/heads/wt\n")

	require.NoError(t, sanitizeDownload(root))

	_, err := os.Lstat(filepath.Join(root, "vendor", "dep", ".git", "refs", "replace"))
	assert.True(t, os.IsNotExist(err), "nested replace refs must be filtered")
	data, err := os.ReadFile(filepath.Join(root, "vendor", "dep", ".git", "config"))
	require.NoError(t, err)
	assert.Equal(t, "[core]\n", string(data))

	data, err = os.ReadFile(gitfile)
	require.NoError(t, err)
	assert.Equal(t, "gitdir: ../../.git/modules/other\n", string(data), "nested gitfile must survive")
}

// TestSanitizeDownload_PreservesNormalRepo checks that an ordinary repository's
// status, index, and history survive sanitization.
func TestSanitizeDownload_PreservesNormalRepo(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("a.txt", "one")
	r.commitFile("b.txt", "two")
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, "a.txt"), []byte("one\nstaged\n"), 0o644))
	r.git("add", "a.txt")
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, "b.txt"), []byte("two\nunstaged\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, "untracked.txt"), []byte("new\n"), 0o644))

	before := r.snapshot()
	require.NoError(t, sanitizeDownload(r.dir))
	assert.Equal(t, before, r.snapshot())

	r.git("commit", "--quiet", "-m", "after sanitize")
	assert.Equal(t, "after sanitize", r.git("log", "-1", "--format=%s"))
}

func TestSanitizeDownload_BlocksInventedGitAncestry(t *testing.T) {
	requireGit(t)

	cases := []struct {
		name   string
		inject func(t *testing.T, r *gitRepo, orphan, bridge string)
	}{
		{
			name: "loose replacement ref",
			inject: func(t *testing.T, r *gitRepo, orphan, bridge string) {
				r.git("replace", orphan, bridge)
			},
		},
		{
			name: "packed replacement ref",
			inject: func(t *testing.T, r *gitRepo, orphan, bridge string) {
				r.git("replace", orphan, bridge)
				r.git("pack-refs", "--all", "--prune")
				packed, err := os.ReadFile(filepath.Join(r.dir, ".git", "packed-refs"))
				require.NoError(t, err)
				require.Contains(t, string(packed), "refs/replace/"+orphan)
			},
		},
		{
			name: "graft",
			inject: func(t *testing.T, r *gitRepo, orphan, bridge string) {
				writeFile(t, r.dir, ".git/info/grafts", orphan+" "+bridge+"\n")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newGitRepo(t)
			human := r.commitFile("human.txt", "human work")
			tree := r.git("rev-parse", human+"^{tree}")
			orphan := r.git("commit-tree", tree, "-m", "agent orphan")
			bridge := r.git("commit-tree", tree, "-p", human, "-m", "bridge")
			tc.inject(t, r, orphan, bridge)

			assert.True(t, r.isAncestor(human, orphan))
			require.NoError(t, sanitizeDownload(r.dir))
			assert.False(t, r.isAncestor(human, orphan))
			assert.Equal(t, human, r.git("rev-parse", "main"))
		})
	}
}

func TestSanitizeDownload_LocalPlainPushRejectsInventedAncestry(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	human := r.commitFile("human.txt", "human work")
	remote := filepath.Join(t.TempDir(), "remote.git")
	r.git("init", "--bare", "--quiet", remote)
	r.git("push", "--quiet", remote, "main")

	tree := r.git("rev-parse", human+"^{tree}")
	orphan := r.git("commit-tree", tree, "-m", "agent orphan")
	bridge := r.git("commit-tree", tree, "-p", human, "-m", "bridge")
	r.git("replace", orphan, bridge)
	require.True(t, r.isAncestor(human, orphan))

	require.NoError(t, sanitizeDownload(r.dir))
	require.False(t, r.isAncestor(human, orphan))

	_, err := r.tryGit("push", "--quiet", remote, orphan+":main")
	require.Error(t, err, "plain push of divergent history must be rejected")
	assert.Equal(t, human, r.gitAt(remote, "rev-parse", "main"))
}

func TestErrSymlink_ErrorMethod(t *testing.T) {
	// Verify that errSymlink implements error and that fmt.Sprintf does not panic.
	underlying := fmt.Errorf("permission denied")

	t.Run("with target", func(t *testing.T) {
		e := &errSymlink{Path: "/repo/bad-link", Target: "/etc/passwd", Err: underlying}
		msg := e.Error()
		assert.Contains(t, msg, "/repo/bad-link")
		assert.Contains(t, msg, "/etc/passwd")
		assert.Contains(t, msg, "permission denied")

		// Verify fmt.Sprintf does not panic on errSymlink.
		formatted := fmt.Sprintf("%v", e)
		assert.NotEmpty(t, formatted)
	})

	t.Run("without target", func(t *testing.T) {
		e := &errSymlink{Path: "/repo/unreadable", Err: underlying}
		msg := e.Error()
		assert.Contains(t, msg, "/repo/unreadable")
		assert.Contains(t, msg, "permission denied")
		assert.NotContains(t, msg, "->")
	})

	t.Run("unwrap", func(t *testing.T) {
		e := &errSymlink{Path: "/repo/link", Target: "/tmp", Err: underlying}
		assert.ErrorIs(t, e, underlying)
	})
}

func TestSanitizeDownload_RemoveFailureReturnsErrSymlink(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("read-only directory trick is Linux-specific")
	}
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}

	// Create a directory with a dangerous absolute symlink, then make the
	// parent directory read-only so os.Remove fails inside sanitizeDownload.
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(sub, "bad-link")))

	// A second dangerous symlink later in the walk — it should remain
	// unremoved because WalkDir aborts after the first error.
	require.NoError(t, os.Symlink("/etc/shadow", filepath.Join(sub, "second-link")))

	// Make sub read-only so os.Remove fails.
	require.NoError(t, os.Chmod(sub, 0o555))
	t.Cleanup(func() {
		os.Chmod(sub, 0o755) // restore for TempDir cleanup
	})

	err := sanitizeDownload(dir)
	require.Error(t, err)

	var symErr *errSymlink
	require.True(t, errors.As(err, &symErr), "expected *errSymlink, got %T: %v", err, err)
	assert.Contains(t, symErr.Path, "bad-link")
	assert.NotNil(t, symErr.Err)

	// The second symlink should still exist because the walk aborted.
	_, statErr := os.Lstat(filepath.Join(sub, "second-link"))
	assert.NoError(t, statErr, "second symlink should remain after walk aborted on first error")
}

func TestSafeDownload_RetriesIntoFreshDirectoryAfterPartialExtraction(t *testing.T) {
	requireGit(t)
	src := newGitRepo(t)

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "download-paths.log")
	counterPath := filepath.Join(binDir, "attempt-count")

	script := fmt.Sprintf(`#!/bin/sh
set -e
if [ "$2" != "download" ]; then
  exit 0
fi
dest=$5
printf '%%s\n' "$dest" >> "%s"
attempt=0
if [ -f "%s" ]; then
  attempt=$(/bin/cat "%s")
fi
attempt=$((attempt + 1))
printf '%%s\n' "$attempt" > "%s"
if [ "$attempt" -eq 1 ]; then
  /bin/mkdir -p "$dest/node_modules/@esbuild/linux-x64/bin" "$dest/node_modules/esbuild/bin"
  printf 'esbuild' > "$dest/node_modules/@esbuild/linux-x64/bin/esbuild"
  /bin/ln "$dest/node_modules/@esbuild/linux-x64/bin/esbuild" "$dest/node_modules/esbuild/bin/esbuild"
  exit 1
fi
/bin/cp -R "%s/.git" "$dest/.git"
printf 'safe\n' > "$dest/safe.txt"
/bin/ln -s /etc/passwd "$dest/dangerous-link"
exit 0
`, logPath, counterPath, counterPath, counterPath, src.dir)
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(filepath.ListSeparator)+os.Getenv("PATH"))

	parent := t.TempDir()
	localDir := filepath.Join(parent, "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{}))

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	paths := strings.Split(strings.TrimSpace(string(log)), "\n")
	require.Len(t, paths, 2)
	assert.NotEqual(t, paths[0], paths[1], "retry must use a fresh staging directory")

	content, err := os.ReadFile(filepath.Join(localDir, "safe.txt"))
	require.NoError(t, err)
	assert.Equal(t, "safe\n", string(content))

	_, lstatErr := os.Lstat(filepath.Join(localDir, "dangerous-link"))
	assert.True(t, os.IsNotExist(lstatErr), "dangerous symlink must be removed before publishing")
	_, statErr := os.Lstat(filepath.Join(localDir, "node_modules"))
	assert.True(t, os.IsNotExist(statErr), "partial first attempt must not leak into the published directory")

	entries, err := os.ReadDir(parent)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".fullsend-safe-download-"),
			"staging directory must be removed after success: %s", entry.Name())
	}
}

func TestSafeDownload_CleansTrailingSeparator(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	writeFile(t, r.dir, "safe.txt", "safe\n")
	serveDownloadFrom(t, r.dir)

	parent := t.TempDir()
	localDir := filepath.Join(parent, "repo") + string(os.PathSeparator)
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{}))

	content, err := os.ReadFile(filepath.Join(parent, "repo", "safe.txt"))
	require.NoError(t, err)
	assert.Equal(t, "safe\n", string(content))

	entries, readErr := os.ReadDir(parent)
	require.NoError(t, readErr)
	require.Len(t, entries, 1)
	assert.Equal(t, "repo", entries[0].Name())
}

func TestSafeDownload_ExhaustsRetries(t *testing.T) {
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "download" ]; then
  exit 1
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir)

	parent := t.TempDir()
	localDir := filepath.Join(parent, "repo")
	err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "safe download from sandbox failed after 2 attempts")
	assert.Contains(t, err.Error(), "attempt 1/2")
	assert.Contains(t, err.Error(), "attempt 2/2")

	_, statErr := os.Lstat(localDir)
	assert.True(t, os.IsNotExist(statErr), "failed download must not publish a partial directory")

	entries, readErr := os.ReadDir(parent)
	require.NoError(t, readErr)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".fullsend-safe-download-"),
			"staging directory must be removed after failure: %s", entry.Name())
	}
}

func TestSafeDownload_ReturnsErrorWhenReplacingExistingDownloadFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	requireGit(t)

	r := newGitRepo(t)
	writeFile(t, r.dir, "safe.txt", "safe\n")
	serveDownloadFrom(t, r.dir)

	parent := t.TempDir()
	localDir := filepath.Join(parent, "repo")
	readonlyDir := filepath.Join(localDir, "readonly")
	require.NoError(t, os.MkdirAll(readonlyDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(readonlyDir, "file.txt"), []byte("keep"), 0o644))
	require.NoError(t, os.Chmod(readonlyDir, 0o555))
	t.Cleanup(func() {
		require.NoError(t, os.Chmod(readonlyDir, 0o755))
	})

	err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replacing existing download directory")
	assert.False(t, errors.Is(err, ErrRepositoryValidation), "a publication failure is not a validation failure: %v", err)
}

func TestSafeDownload_ReturnsErrorWhenSanitizationFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}

	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "download" ]; then
  printf 'safe\n' > "$5/safe.txt"
  /bin/ln -s /etc/passwd "$5/dangerous-link"
  /bin/mkdir "$5/readonly"
  printf 'nested\n' > "$5/readonly/nested.txt"
  /bin/chmod 0555 "$5/readonly"
  /bin/chmod 0555 "$5"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir)

	parent := t.TempDir()
	localDir := filepath.Join(parent, "repo")
	t.Cleanup(func() {
		entries, readErr := os.ReadDir(parent)
		if readErr != nil {
			return
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), ".fullsend-safe-download-") {
				continue
			}
			stagingDir := filepath.Join(parent, entry.Name())
			_ = os.Chmod(stagingDir, 0o755)
			_ = os.RemoveAll(stagingDir)
		}
	})

	err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sanitizing downloaded directory")
	assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
	var symErr *errSymlink
	assert.True(t, errors.As(err, &symErr), "the sanitization cause must stay reachable: %v", err)

	_, statErr := os.Lstat(localDir)
	assert.True(t, os.IsNotExist(statErr), "unsafe download must not be published")

	entries, readErr := os.ReadDir(parent)
	require.NoError(t, readErr)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".fullsend-safe-download-"),
			"staging directory must be removed after sanitization failure: %s", entry.Name())
	}
}

func TestSafeDownload_DoesNotPublishUnsupportedGitMetadata(t *testing.T) {
	cases := map[string]string{
		"root git":                       `/bin/mkdir -p "$5/real-git" && /bin/ln -s real-git "$5/.git"`,
		"objects":                        `/bin/mkdir -p "$5/.git" && /bin/ln -s ../real-objects "$5/.git/objects"`,
		"gitfile":                        `/bin/mkdir -p "$5/real-git" && printf 'gitdir: real-git\n' > "$5/.git"`,
		"commondir":                      `/bin/mkdir -p "$5/.git" "$5/real-git" && printf '../real-git\n' > "$5/.git/commondir"`,
		"reftable symlink":               `/bin/mkdir -p "$5/.git" && /bin/ln -s ../outside "$5/.git/reftable"`,
		"nested git symlink":             `/bin/mkdir -p "$5/vendor/dep/real-git" && /bin/ln -s real-git "$5/vendor/dep/.git"`,
		"alternates":                     `/bin/mkdir -p "$5/.git/objects/info" && printf '/elsewhere/objects\n' > "$5/.git/objects/info/alternates"`,
		"http-alternates":                `/bin/mkdir -p "$5/.git/objects/info" && printf 'https://example.invalid/objects\n' > "$5/.git/objects/info/http-alternates"`,
		"shallow symlink":                `/bin/mkdir -p "$5/.git" && /bin/ln -s ../../outside "$5/.git/shallow"`,
		"shallow directory":              `/bin/mkdir -p "$5/.git/shallow"`,
		"shallow symlink case variant":   `/bin/mkdir -p "$5/.git" && /bin/ln -s ../../outside "$5/.git/SHALLOW"`,
		"shallow directory case variant": `/bin/mkdir -p "$5/.git/Shallow"`,
		"packed-refs directory":          `/bin/mkdir -p "$5/.git/packed-refs"`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			binDir := t.TempDir()
			script := "#!/bin/sh\nif [ \"$2\" = \"download\" ]; then\n" + body + "\nexit 0\nfi\nexit 0\n"
			require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
			t.Setenv("PATH", binDir)

			parent := t.TempDir()
			localDir := filepath.Join(parent, "repo")
			err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "sanitizing downloaded directory")
			assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
			assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
			_, statErr := os.Lstat(localDir)
			assert.True(t, os.IsNotExist(statErr), "unsafe download must not be published")
		})
	}
}

// TestValidateGitCheckout checks the shared read-only preflight used for
// trusted host inputs: real directories only, native resolution, no mutation.
func TestValidateGitCheckout(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")

	t.Run("normal checkout", func(t *testing.T) {
		require.NoError(t, validateGitCheckout(r.dir))
	})

	t.Run("missing .git", func(t *testing.T) {
		tree := t.TempDir()
		writeFile(t, tree, "file.txt", "content\n")
		err := validateGitCheckout(tree)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "checking git checkout")
	})

	t.Run("empty .git", func(t *testing.T) {
		tree := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(tree, ".git"), 0o755))
		require.Error(t, validateGitCheckout(tree))
	})

	t.Run("gitfile", func(t *testing.T) {
		tree := t.TempDir()
		writeFile(t, tree, ".git", "gitdir: "+filepath.Join(r.dir, ".git")+"\n")
		err := validateGitCheckout(tree)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a real directory")
	})

	t.Run("symlink .git rejected before query", func(t *testing.T) {
		tree := t.TempDir()
		require.NoError(t, os.Symlink(filepath.Join(r.dir, ".git"), filepath.Join(tree, ".git")))
		t.Setenv("PATH", t.TempDir()) // no Git: the layout guard must run first
		err := validateGitCheckout(tree)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a real directory")
	})

	t.Run("trusted source metadata is not modified", func(t *testing.T) {
		src := newGitRepo(t)
		src.commitFile("file.txt", "content")
		hook := writeFile(t, src.dir, ".git/hooks/pre-commit", "#!/bin/sh\nmalicious\n")
		grafts := writeFile(t, src.dir, ".git/info/grafts", "graft data\n")
		replace := writeFile(t, src.dir, ".git/refs/replace/"+strings.Repeat("a", 40), "replace data\n")

		require.NoError(t, validateGitCheckout(src.dir))

		for path, want := range map[string]string{
			hook:    "#!/bin/sh\nmalicious\n",
			grafts:  "graft data\n",
			replace: "replace data\n",
		} {
			got, err := os.ReadFile(path)
			require.NoError(t, err, "%s must survive validation", path)
			assert.Equal(t, want, string(got))
		}
	})

	t.Run("reftable checkout is read-only", func(t *testing.T) {
		requireReftableSanitization(t)
		src := newReftableRepo(t)
		src.commitFile("file.txt", "content")
		configPath := filepath.Join(src.dir, ".git", "config")
		configBefore, err := os.ReadFile(configPath)
		require.NoError(t, err)
		hook := writeFile(t, src.dir, ".git/hooks/pre-commit", "#!/bin/sh\nmalicious\n")
		grafts := writeFile(t, src.dir, ".git/info/grafts", "graft data\n")

		require.NoError(t, validateGitCheckout(src.dir))

		configAfter, err := os.ReadFile(configPath)
		require.NoError(t, err)
		assert.Equal(t, configBefore, configAfter, "reftable config bytes must be preserved")
		for path, want := range map[string]string{
			hook:   "#!/bin/sh\nmalicious\n",
			grafts: "graft data\n",
		} {
			got, err := os.ReadFile(path)
			require.NoError(t, err, "%s must survive validation", path)
			assert.Equal(t, want, string(got))
		}
	})

	t.Run("reftable rejected on git 2.52", func(t *testing.T) {
		src := newReftableRepo(t)
		src.commitFile("file.txt", "content")

		installGitShim(t, "if [ \"$1\" = \"--version\" ]; then\n  echo 'git version 2.52.0'\n  exit 0\nfi")

		err := validateGitCheckout(src.dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "2.53")
	})

	t.Run("files backend is not version gated", func(t *testing.T) {
		src := newGitRepo(t)
		src.commitFile("file.txt", "content")

		installGitShim(t, "if [ \"$1\" = \"--version\" ]; then\n  echo 'git version 2.52.0'\n  exit 0\nfi")

		require.NoError(t, validateGitCheckout(src.dir))
	})
}

func TestValidateRepositoryRoot(t *testing.T) {
	t.Run("checkout is not plain", func(t *testing.T) {
		requireGit(t)
		r := newGitRepo(t)
		r.commitFile("file.txt", "content")
		plain, err := ValidateRepositoryRoot(r.dir)
		require.NoError(t, err)
		assert.False(t, plain)
	})

	t.Run("unborn checkout is not plain", func(t *testing.T) {
		requireGit(t)
		plain, err := ValidateRepositoryRoot(newGitRepo(t).dir)
		require.NoError(t, err)
		assert.False(t, plain)
	})

	t.Run("gitfile is never plain", func(t *testing.T) {
		tree := t.TempDir()
		writeFile(t, tree, ".git", "gitdir: elsewhere\n")
		plain, err := ValidateRepositoryRoot(tree)
		require.Error(t, err)
		assert.False(t, plain)
		assert.Contains(t, err.Error(), "not a real directory")
	})

	cases := []struct {
		name    string
		entries []string
		wantErr bool
	}{
		{name: "empty directory", entries: nil},
		{name: "head only", entries: []string{"HEAD"}},
		{name: "objects only", entries: []string{"objects"}},
		{name: "refs only", entries: []string{"refs"}},
		{name: "commondir only", entries: []string{"commondir"}},
		{name: "head and objects", entries: []string{"HEAD", "objects"}},
		{name: "head and refs", entries: []string{"HEAD", "refs"}},
		{name: "objects and refs", entries: []string{"objects", "refs"}},
		{name: "head and commondir", entries: []string{"HEAD", "commondir"}, wantErr: true},
		{name: "bare root", entries: []string{"HEAD", "objects", "refs"}, wantErr: true},
		{name: "bare root with commondir", entries: []string{"HEAD", "commondir", "objects", "refs"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree := t.TempDir()
			for _, name := range tc.entries {
				writeFile(t, tree, name, "x\n")
			}
			plain, err := ValidateRepositoryRoot(tree)
			if tc.wantErr {
				require.Error(t, err)
				assert.False(t, plain)
				assert.Contains(t, err.Error(), "checking git checkout")
				return
			}
			require.NoError(t, err)
			assert.True(t, plain, "a root without .git or Git directory metadata is plain")
		})
	}

	t.Run("dangling symlink marker still counts", func(t *testing.T) {
		tree := t.TempDir()
		writeFile(t, tree, "HEAD", "ref: refs/heads/main\n")
		require.NoError(t, os.Symlink("missing", filepath.Join(tree, "commondir")))
		plain, err := ValidateRepositoryRoot(tree)
		require.Error(t, err)
		assert.False(t, plain)
	})

	t.Run("dangling symlink bare root still counts", func(t *testing.T) {
		tree := t.TempDir()
		require.NoError(t, os.Symlink("missing", filepath.Join(tree, "HEAD")))
		writeFile(t, tree, "objects", "x\n")
		writeFile(t, tree, "refs", "x\n")
		plain, err := ValidateRepositoryRoot(tree)
		require.Error(t, err)
		assert.False(t, plain)
	})

	t.Run("non-directory root fails closed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "root")
		require.NoError(t, os.WriteFile(path, []byte("x\n"), 0o644))
		plain, err := ValidateRepositoryRoot(path)
		require.Error(t, err)
		assert.False(t, plain)
	})
}

// TestSafeDownload_AcceptsReftableCheckout publishes a sanitized reftable
// checkout with its HEAD and refs intact and replacement refs removed.
func TestSafeDownload_AcceptsReftableCheckout(t *testing.T) {
	requireReftableSanitization(t)
	r := newReftableRepo(t)
	human := r.commitFile("file.txt", "original")
	tree := r.git("rev-parse", human+"^{tree}")
	orphan := r.git("commit-tree", tree, "-m", "agent orphan")
	bridge := r.git("commit-tree", tree, "-p", human, "-m", "bridge")
	r.git("replace", orphan, bridge)
	require.True(t, r.isAncestor(human, orphan))

	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{}))

	assert.Equal(t, human, r.gitAt(localDir, "rev-parse", "HEAD"))
	assert.Equal(t, human, r.gitAt(localDir, "rev-parse", "main"))
	out, err := runGit(t, localDir, r.env, "replace", "--list")
	require.NoError(t, err)
	assert.Empty(t, out, "replacement refs must not be published")
}

// TestSafeDownload_RequiresGitCheckout accepts a real host checkout and
// preserves its HEAD and worktree.
func TestSafeDownload_RequiresGitCheckout(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")
	head := r.git("rev-parse", "HEAD")

	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{}))

	got, err := runGit(t, localDir, r.env, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, head, got)
	status, err := runGit(t, localDir, r.env, "status", "--porcelain=v1")
	require.NoError(t, err)
	assert.Empty(t, status)
	data, err := os.ReadFile(filepath.Join(localDir, "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "content\n", string(data))
}

// TestSafeDownload_RejectsPlainOutputForCheckoutInput covers the regression
// where a checkout input returned a plain tree: the sandbox can remove .git,
// and publishing that tree would let later host Git discovery resolve metadata
// through a parent repository. The download must fail before publication.
func TestSafeDownload_RejectsPlainOutputForCheckoutInput(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")

	// A real, non-shallow checkout as the remote tree, with the sandbox-side
	// .git removed.
	downloaded := filepath.Join(t.TempDir(), "downloaded")
	require.NoError(t, os.CopyFS(downloaded, os.DirFS(r.dir)))
	require.NoError(t, os.RemoveAll(filepath.Join(downloaded, ".git")))

	reject := func(t *testing.T, localDir string) {
		t.Helper()
		serveDownloadFrom(t, downloaded)
		err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
			RepositorySnapshot{IsCheckout: true})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must remain a git checkout")
		assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	}

	t.Run("absent destination remains absent", func(t *testing.T) {
		localDir := filepath.Join(t.TempDir(), "repo")
		reject(t, localDir)
		_, statErr := os.Lstat(localDir)
		assert.True(t, os.IsNotExist(statErr), "a rejected checkout must not be published")
	})

	t.Run("existing destination is preserved", func(t *testing.T) {
		localDir := filepath.Join(t.TempDir(), "repo")
		sentinel := writeFile(t, localDir, "sentinel.txt", "keep\n")
		reject(t, localDir)
		data, readErr := os.ReadFile(sentinel)
		require.NoError(t, readErr)
		assert.Equal(t, "keep\n", string(data), "a rejected download must not replace the existing destination")
	})
}

// TestSafeDownload_AcceptsCheckoutForCheckoutInput keeps the positive
// checkout-to-checkout path: a required-checkout snapshot accepts a real
// checkout output.
func TestSafeDownload_AcceptsCheckoutForCheckoutInput(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")
	head := r.git("rev-parse", "HEAD")

	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{IsCheckout: true}))

	got, err := runGit(t, localDir, r.env, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, head, got)
}

// TestSafeDownload_PlainInputMayPublishCheckout keeps the plain-to-checkout
// path: without IsCheckout, a plain host input may come back as a valid
// checkout that keeps the normal sanitization and validation.
func TestSafeDownload_PlainInputMayPublishCheckout(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")
	head := r.git("rev-parse", "HEAD")

	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{}))

	got, err := runGit(t, localDir, r.env, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, head, got)
}

func TestSafeDownload_PublishesPlainDirectory(t *testing.T) {
	tree := t.TempDir()
	writeFile(t, tree, "file.txt", "content\n")

	serveDownloadFrom(t, tree)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{}))

	data, err := os.ReadFile(filepath.Join(localDir, "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "content\n", string(data))
	_, err = os.Lstat(filepath.Join(localDir, ".git"))
	assert.True(t, os.IsNotExist(err), "a plain download must not gain git metadata")
}

// TestSafeDownload_RejectsMissingOrInvalidRootCheckout rejects, without
// publishing, roots that look like a repository but are neither a checkout nor
// a plain directory; there is no discovery fallback to the bare root.
func TestSafeDownload_RejectsMissingOrInvalidRootCheckout(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")

	reject := func(t *testing.T, src string) error {
		t.Helper()
		serveDownloadFrom(t, src)
		localDir := filepath.Join(t.TempDir(), "repo")
		err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
			RepositorySnapshot{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "checking git checkout")
		assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
		_, statErr := os.Lstat(localDir)
		assert.True(t, os.IsNotExist(statErr), "a rejected checkout must not be published")
		return err
	}

	t.Run("root git directory metadata", func(t *testing.T) {
		tree := t.TempDir()
		writeFile(t, tree, "HEAD", "ref: refs/heads/main\n")
		writeFile(t, tree, "commondir", "../.git\n")
		err := reject(t, tree)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "a bare-shaped root is detected directly: %v", err)
	})

	t.Run("bare root", func(t *testing.T) {
		bare := filepath.Join(t.TempDir(), "bare.git")
		r.git("clone", "--bare", "--quiet", r.dir, bare)
		assert.Equal(t, "true", r.gitAt(bare, "rev-parse", "--is-bare-repository"),
			"native Git must recognize the bare root")
		err := reject(t, bare)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "a bare root is detected directly: %v", err)
	})

	t.Run("empty .git beside bare root", func(t *testing.T) {
		tree := t.TempDir()
		writeFile(t, tree, "HEAD", r.git("rev-parse", "HEAD")+"\n")
		writeFile(t, tree, "objects/pack/source.idx", "source\n")
		writeFile(t, tree, "refs/heads/main", r.git("rev-parse", "HEAD")+"\n")
		require.NoError(t, os.Mkdir(filepath.Join(tree, ".git"), 0o755))
		err := reject(t, tree)
		assert.False(t, errors.Is(err, ErrRepositoryRejected), "a Git query failure is not a rejection: %v", err)
	})

	t.Run("invalid .git layout", func(t *testing.T) {
		tree := t.TempDir()
		writeFile(t, tree, ".git/HEAD", "ref: refs/heads/main\n")
		err := reject(t, tree)
		assert.False(t, errors.Is(err, ErrRepositoryRejected), "a Git layout query failure is not a rejection: %v", err)
	})
}

// TestSafeDownload_RejectsSpecialHEAD times out a checkout whose .git/HEAD is a
// FIFO instead of publishing it.
func TestSafeDownload_RejectsSpecialHEAD(t *testing.T) {
	requireGit(t)

	tree := t.TempDir()
	writeFile(t, tree, ".git/objects/pack/source.idx", "source\n")
	writeFile(t, tree, ".git/refs/heads/main", strings.Repeat("0", 40)+"\n")
	requireFIFO(t, filepath.Join(tree, ".git"), "HEAD")

	serveDownloadFrom(t, tree)
	localDir := filepath.Join(t.TempDir(), "repo")
	err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "want a deadline error, got %v", err)
	assert.False(t, errors.Is(err, ErrRepositoryRejected), "a timeout is not a rejection: %v", err)
	_, statErr := os.Lstat(localDir)
	assert.True(t, os.IsNotExist(statErr), "a special HEAD must not be published")
}

// TestSafeDownload_CheckoutQueryFailures makes the native layout query fail or
// print the wrong path; every result must be a checkout error, and an
// unqueryable Git must fail closed.
func TestSafeDownload_CheckoutQueryFailures(t *testing.T) {
	requireGit(t)

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")

	installDownloadAndGit := func(t *testing.T, gitBody string) {
		t.Helper()
		binDir := t.TempDir()
		script := fmt.Sprintf("#!/bin/sh\nif [ \"$2\" = \"download\" ]; then\n  /bin/cp -R \"%s/.\" \"$5/\"\nfi\nexit 0\n", r.dir)
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
		if gitBody != "" {
			require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte("#!/bin/sh\n"+gitBody+"\n"), 0o755))
		}
		t.Setenv("PATH", binDir)
	}

	cases := []struct {
		name    string
		gitBody string
		want    string
	}{
		{"status failure", "exit 129", "checking git checkout"},
		{"malformed output", "printf 'junk\\n'", "git directory resolved to"},
		{"git unavailable", "", "checking git checkout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installDownloadAndGit(t, tc.gitBody)
			localDir := filepath.Join(t.TempDir(), "repo")
			err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
				RepositorySnapshot{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			_, statErr := os.Lstat(localDir)
			assert.True(t, os.IsNotExist(statErr), "a failed query must not publish")
		})
	}
}

// TestSafeDownload_CheckoutQueryIgnoresEnvironment proves inherited Git
// variables and repository config cannot redirect or trace the layout query.
func TestSafeDownload_CheckoutQueryIgnoresEnvironment(t *testing.T) {
	requireGit(t)

	decoy := newGitRepo(t)
	decoy.commitFile("decoy.txt", "decoy")

	r := newGitRepo(t)
	r.commitFile("file.txt", "content")
	tracePath := filepath.Join(t.TempDir(), "trace2.log")
	t.Setenv("GIT_DIR", filepath.Join(decoy.dir, ".git"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(decoy.dir, ".git"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(decoy.dir, ".git", "objects"))
	t.Setenv("GIT_TRACE2", tracePath)

	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{}))
	assert.NoFileExists(t, tracePath)
}

// shallowCommitCount returns Git's commit count for HEAD in dir, which reflects
// the root shallow boundary Git is using.
func shallowCommitCount(t *testing.T, dir string, env []string) string {
	t.Helper()
	out, err := runGit(t, dir, env, "rev-list", "--count", "HEAD")
	require.NoError(t, err)
	return out
}

func TestSafeDownload_PreservesTrustedShallowBoundary(t *testing.T) {
	requireGit(t)

	cases := []struct {
		name            string
		trusted         string // "", "head", or "middle"
		downloaded      string // "", "head", or "middle"
		wantCount       string // published count when the change is allowed
		downloadedCount string // baseline count with the untrusted marker
	}{
		{"deeper sandbox boundary is rejected", "head", "middle", "", "2"},
		{"sandbox cutoff in a full repository is rejected", "", "middle", "", "2"},
		{"trusted boundary is recreated when the sandbox removed it", "head", "", "1", ""},
		{"full history stays full when the sandbox removed the marker", "", "", "3", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := gitTestEnv(t.TempDir())
			r := newGitRepo(t)
			r.commitFile("one.txt", "one")
			middle := r.commitFile("two.txt", "two")
			head := r.commitFile("three.txt", "three")
			oid := func(which string) string {
				switch which {
				case "head":
					return head
				case "middle":
					return middle
				}
				t.Fatalf("unknown boundary %q", which)
				return ""
			}
			shallowPath := filepath.Join(r.dir, ".git", "shallow")
			if tc.downloaded != "" {
				require.NoError(t, os.WriteFile(shallowPath, []byte(oid(tc.downloaded)+"\n"), 0o644))
				assert.Equal(t, tc.downloadedCount, shallowCommitCount(t, r.dir, env),
					"fixture must use the untrusted boundary")
			}
			trusted := RepositorySnapshot{}
			if tc.trusted != "" {
				trusted = RepositorySnapshot{Shallow: ShallowState{Present: true, Data: []byte(oid(tc.trusted) + "\n")}}
			}
			serveDownloadFrom(t, r.dir)
			localDir := filepath.Join(t.TempDir(), "repo")

			if tc.wantCount == "" {
				err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, trusted)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unsupported sandbox shallow boundary change")
				assert.Contains(t, err.Error(), "fetch --depth")
				assert.NoDirExists(t, localDir, "a rejected boundary change must not be published")
				return
			}

			require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, trusted))
			assert.Equal(t, tc.wantCount, shallowCommitCount(t, localDir, env))
			got, err := os.ReadFile(filepath.Join(localDir, ".git", "shallow"))
			if !trusted.Shallow.Present {
				assert.True(t, os.IsNotExist(err), "untrusted shallow metadata must not be published")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, trusted.Shallow.Data, got)
		})
	}
}

// TestSafeDownload_RejectsShallowCloneAddedCutoff uses a real local file
// transport clone at depth 1, whose boundary commit is missing its parent, to
// show that a valid Git shallow fetch can add a cutoff absent from the trusted
// snapshot and is rejected before publication.
func TestSafeDownload_RejectsShallowCloneAddedCutoff(t *testing.T) {
	requireGit(t)
	env := gitTestEnv(t.TempDir())

	src := newGitRepo(t)
	src.commitFile("one.txt", "one")
	middle := src.commitFile("two.txt", "two")
	head := src.commitFile("three.txt", "three")

	downloaded := t.TempDir()
	out, err := runGit(t, downloaded, env, "clone", "--quiet", "--depth=1", "file://"+src.dir, ".")
	require.NoError(t, err, out)
	marker, err := os.ReadFile(filepath.Join(downloaded, ".git", "shallow"))
	require.NoError(t, err, "the depth-1 clone must carry a shallow marker")
	require.Equal(t, head+"\n", string(marker), "the clone boundary must be the new tip")
	require.NotEqual(t, middle, strings.TrimSpace(string(marker)), "the trusted boundary must differ")
	r := &gitRepo{t: t, dir: downloaded, env: env}
	assert.Equal(t, "1", r.git("rev-list", "--count", "HEAD"),
		"Git must read the downloaded repository with its own marker")

	trusted := RepositorySnapshot{Shallow: ShallowState{Present: true, Data: []byte(middle + "\n")}}
	serveDownloadFrom(t, downloaded)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(localDir, 0o755))
	prior := filepath.Join(localDir, "prior.txt")
	require.NoError(t, os.WriteFile(prior, []byte("prior\n"), 0o644))

	err = SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, trusted)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	assert.Contains(t, err.Error(), "unsupported sandbox shallow boundary change")
	assert.Contains(t, err.Error(), "fetch --depth")
	got, err := os.ReadFile(prior)
	require.NoError(t, err)
	assert.Equal(t, "prior\n", string(got), "an earlier output must remain untouched")
	_, err = os.Lstat(filepath.Join(localDir, ".git"))
	assert.True(t, os.IsNotExist(err), "the rejected download must not be published")
}

// TestSafeDownload_FailurePhaseMarkers tags failures inside post-download
// validation with the phase marker, leaves transport failures untagged, and
// preserves every original cause.
func TestSafeDownload_FailurePhaseMarkers(t *testing.T) {
	t.Run("download failure", func(t *testing.T) {
		binDir := t.TempDir()
		script := "#!/bin/sh\nif [ \"$2\" = \"download\" ]; then\n  exit 1\nfi\nexit 0\n"
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
		t.Setenv("PATH", binDir)

		localDir := filepath.Join(t.TempDir(), "repo")
		err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrRepositoryValidation), "a failed transfer is not a validation failure: %v", err)
		var exitErr *exec.ExitError
		assert.True(t, errors.As(err, &exitErr), "the transfer cause must stay reachable: %v", err)
	})

	t.Run("git query failure", func(t *testing.T) {
		r := newGitRepo(t)
		r.commitFile("file.txt", "content")

		binDir := t.TempDir()
		download := fmt.Sprintf("#!/bin/sh\nif [ \"$2\" = \"download\" ]; then\n  /bin/cp -R \"%s/.\" \"$5/\"\nfi\nexit 0\n", r.dir)
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(download), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte("#!/bin/sh\nexit 129\n"), 0o755))
		t.Setenv("PATH", binDir)

		localDir := filepath.Join(t.TempDir(), "repo")
		err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "checking git checkout")
		assert.True(t, errors.Is(err, ErrRepositoryValidation), "a Git query failure happens in root validation: %v", err)
		assert.False(t, errors.Is(err, ErrRepositoryRejected), "a Git query failure is not a rejection: %v", err)
	})

	t.Run("filesystem failure during sanitization", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root bypasses permission checks")
		}
		binDir := t.TempDir()
		script := `#!/bin/sh
if [ "$2" = "download" ]; then
  printf 'safe\n' > "$5/safe.txt"
  /bin/ln -s /etc/passwd "$5/dangerous-link"
  /bin/chmod 0555 "$5"
  exit 0
fi
exit 0
`
		require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
		t.Setenv("PATH", binDir)

		parent := t.TempDir()
		t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
		localDir := filepath.Join(parent, "repo")
		err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sanitizing downloaded directory")
		assert.True(t, errors.Is(err, ErrRepositoryValidation), "a sanitization failure happens in the validation phase: %v", err)
		assert.False(t, errors.Is(err, ErrRepositoryRejected), "a filesystem failure is not a rejection: %v", err)
		var symErr *errSymlink
		assert.True(t, errors.As(err, &symErr), "the filesystem cause must stay reachable: %v", err)
	})
}

// TestSafeDownload_RestoresTrustedShallowWhenBoundaryCommitIsRewritten covers a
// sandbox that rewrote history so the trusted boundary commit is no longer on
// the downloaded branch. Restoration stays byte-exact, so the published
// repository carries a shallow entry for a commit the new history does not
// reach; these cases establish that Git still reads the repository, that the
// rewritten candidate history and worktree survive, and that the stale entry
// neither errors nor re-introduces the dropped commit.
func TestSafeDownload_RestoresTrustedShallowWhenBoundaryCommitIsRewritten(t *testing.T) {
	requireGit(t)

	cases := []struct {
		name string
		// rewrite mutates the fixture after the trusted boundary is captured and
		// returns the subjects the rewritten branch should expose, newest first.
		rewrite func(t *testing.T, r *gitRepo, one, middle string) []string
	}{
		{
			name: "squash rewrite leaves the boundary commit unreachable but stored",
			rewrite: func(t *testing.T, r *gitRepo, one, middle string) []string {
				r.git("reset", "--quiet", "--soft", one)
				r.git("commit", "--quiet", "-s", "-m", "squashed")
				_, err := runGit(t, r.dir, r.env, "merge-base", "--is-ancestor", middle, "HEAD")
				require.Error(t, err, "rewritten history must not contain the trusted boundary commit")
				_, err = runGit(t, r.dir, r.env, "cat-file", "-e", middle)
				require.NoError(t, err, "an unpruned squash keeps the old boundary object")
				return []string{"squashed", "one"}
			},
		},
		{
			name: "rebase rewrite prunes the boundary object",
			rewrite: func(t *testing.T, r *gitRepo, one, middle string) []string {
				r.git("checkout", "--quiet", "-b", "upstream", one)
				require.NoError(t, os.WriteFile(filepath.Join(r.dir, "upstream.txt"), []byte("upstream\n"), 0o644))
				r.git("add", "upstream.txt")
				r.git("commit", "--quiet", "-s", "-m", "upstream")
				r.git("checkout", "--quiet", "main")
				r.git("rebase", "--quiet", "upstream")
				r.git("reflog", "expire", "--expire=now", "--all")
				r.git("gc", "--quiet", "--prune=now")
				_, err := runGit(t, r.dir, r.env, "merge-base", "--is-ancestor", middle, "HEAD")
				require.Error(t, err, "rewritten history must not contain the trusted boundary commit")
				_, err = runGit(t, r.dir, r.env, "cat-file", "-e", middle)
				require.Error(t, err, "pruning must drop the trusted boundary object")
				return []string{"three", "two", "upstream", "one"}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newGitRepo(t)
			one := r.commitFile("one.txt", "one")
			middle := r.commitFile("two.txt", "two")
			r.commitFile("three.txt", "three")
			wantSubjects := tc.rewrite(t, r, one, middle)
			head := r.git("rev-parse", "HEAD")

			trusted := RepositorySnapshot{Shallow: ShallowState{Present: true, Data: []byte(middle + "\n")}}
			serveDownloadFrom(t, r.dir)
			localDir := filepath.Join(t.TempDir(), "repo")
			require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, trusted))

			got, err := os.ReadFile(filepath.Join(localDir, ".git", "shallow"))
			require.NoError(t, err)
			assert.Equal(t, trusted.Shallow.Data, got)

			assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "HEAD"),
				"rewritten candidate commit must survive the download")
			assert.Equal(t, strings.Join(wantSubjects, "\n"), r.gitAt(localDir, "log", "--format=%s"))
			assert.Equal(t, fmt.Sprintf("%d", len(wantSubjects)), shallowCommitCount(t, localDir, r.env))

			status, err := runGit(t, localDir, r.env, "status", "--porcelain")
			require.NoError(t, err, "Git must read the published repository")
			assert.Empty(t, status, "the restored branch boundary must not alter the worktree")
			_, err = runGit(t, localDir, r.env, "merge-base", "--is-ancestor", one, "HEAD")
			require.NoError(t, err, "history traversal must work after the stale boundary is restored")
		})
	}
}

func TestSafeDownload_RejectsTrustedShallowWithoutGitMetadata(t *testing.T) {
	binDir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$2\" = \"download\" ]; then\n  printf 'work\\n' > \"$5/file.txt\"\nfi\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir)

	localDir := filepath.Join(t.TempDir(), "repo")
	err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
		RepositorySnapshot{Shallow: ShallowState{Present: true, Data: []byte("0123456789012345678901234567890123456789\n")}})
	require.Error(t, err)
	// A plain output is a supported root, but a trusted shallow boundary needs
	// git metadata, so restoration fails closed rather than creating it.
	assert.Contains(t, err.Error(), "restoring trusted git shallow metadata")
	_, statErr := os.Lstat(localDir)
	assert.True(t, os.IsNotExist(statErr), "unsafe download must not be published")
}

func TestSafeDownload_ReturnsErrorWhenSanitizationCleanupFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}

	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$2" = "download" ]; then
  printf 'safe\n' > "$5/safe.txt"
  /bin/ln -s /etc/passwd "$5/dangerous-link"
  /bin/chmod 0555 "$5"
  staging_parent="${5%/*}"
  /bin/chmod 0555 "$staging_parent"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir)

	parent := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, os.Chmod(parent, 0o755))
	})
	localDir := filepath.Join(parent, "repo")

	err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, RepositorySnapshot{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sanitizing downloaded directory")
	assert.Contains(t, err.Error(), "cleaning staging directory after sanitization failure")
	assert.Contains(t, err.Error(), "removing staging directory")
}

func TestCleanupStagingDir_MissingPath(t *testing.T) {
	require.NoError(t, cleanupStagingDir(filepath.Join(t.TempDir(), "missing")))
}

func TestSanitizeDownload_RemovesAppleDoubleInGitDir(t *testing.T) {
	dir := t.TempDir()

	// Create .git/objects/pack/ with a normal file and an AppleDouble file.
	packDir := filepath.Join(dir, ".git", "objects", "pack")
	require.NoError(t, os.MkdirAll(packDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(packDir, "pack-abc.idx"), []byte("idx"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(packDir, "._pack-abc.idx"), []byte("apple"), 0o644))

	// Create an AppleDouble file outside .git/ — should NOT be removed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "._regular.txt"), []byte("ok"), 0o644))

	err := sanitizeDownload(dir)
	require.NoError(t, err)

	// Normal pack file survives.
	_, err = os.Stat(filepath.Join(packDir, "pack-abc.idx"))
	assert.NoError(t, err, "normal pack file should survive")

	// AppleDouble file inside .git/ should be removed.
	_, err = os.Stat(filepath.Join(packDir, "._pack-abc.idx"))
	assert.True(t, os.IsNotExist(err), "._* file inside .git/ should be removed")

	// AppleDouble file outside .git/ should survive.
	_, err = os.Stat(filepath.Join(dir, "._regular.txt"))
	assert.NoError(t, err, "._* file outside .git/ should survive")
}
