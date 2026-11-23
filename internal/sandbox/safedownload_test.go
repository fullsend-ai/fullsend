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

// markerHook writes an executable hook that leaves a marker file when Git runs
// it, which is how these tests prove an agent hook never executes on the host.
func markerHook(t *testing.T, dir, name, marker string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nprintf ran > "+marker+"\n"), 0o755))
	return path
}

// TestSafeDownload_DropsExecutableGitConfig proves a sandbox-written config is
// discarded: identity and origin are restored from the trusted snapshot, every
// executable setting is gone, and real host Git operations do not run anything.
func TestSafeDownload_DropsExecutableGitConfig(t *testing.T) {
	requireGit(t)
	r := newGitRepo(t)
	head := r.commitFile("file.txt", "content")
	r.git("config", "remote.origin.url", "https://example.invalid/org/repo")
	r.git("config", "--add", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	require.True(t, snapshot.isCheckout)
	require.NotEmpty(t, snapshot.config, "trusted identity and origin must be captured")

	marker := filepath.Join(t.TempDir(), "executed")
	hookDir := t.TempDir()
	markerHook(t, hookDir, "pre-commit", marker)
	include := writeFile(t, t.TempDir(), "evil.inc", "[core]\n\thooksPath = "+hookDir+"\n")
	for key, value := range map[string]string{
		"core.hooksPath":               hookDir,
		"core.fsmonitor":               "printf ran > " + marker,
		"filter.evil.clean":            "printf ran > " + marker + "; cat",
		"credential.helper":            "!printf ran > " + marker,
		"diff.external":                "printf ran > " + marker,
		"include.path":                 include,
		"remote.origin.url":            "ext::sh -c true",
		"core.repositoryformatversion": "0",
	} {
		r.git("config", key, value)
	}
	markerHook(t, filepath.Join(r.dir, ".git", "hooks"), "pre-commit", marker)

	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

	assert.Equal(t, "Test", r.gitAt(localDir, "config", "--local", "--get", "user.name"))
	assert.Equal(t, "https://example.invalid/org/repo", r.gitAt(localDir, "config", "--local", "--get", "remote.origin.url"))
	assert.Equal(t, "+refs/heads/*:refs/remotes/origin/*", r.gitAt(localDir, "config", "--local", "--get", "remote.origin.fetch"))
	for _, key := range []string{
		"core.hooksPath", "core.fsmonitor", "filter.evil.clean",
		"credential.helper", "diff.external", "include.path",
	} {
		_, err := runGit(t, localDir, r.env, "config", "--local", "--get", key)
		assert.Error(t, err, "%s must not be restored", key)
	}

	assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "HEAD"))
	assert.Empty(t, r.gitAt(localDir, "status", "--porcelain"))
	r.gitAt(localDir, "commit", "--quiet", "--allow-empty", "-m", "after download")
	assert.NoFileExists(t, marker, "no downloaded hook, filter, fsmonitor, or helper may run")
}

// TestSafeDownload_DropsDownloadedWorktreeAndSSHCommand proves an agent-written
// worktree redirect and SSH command never reach the published checkout.
func TestSafeDownload_DropsDownloadedWorktreeAndSSHCommand(t *testing.T) {
	requireGit(t)
	r := newGitRepo(t)
	head := r.commitFile("file.txt", "content")
	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	outside := t.TempDir()
	marker := filepath.Join(t.TempDir(), "executed")
	r.git("config", "core.worktree", outside)
	r.git("config", "core.sshCommand", "printf ran > "+marker)
	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

	assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "HEAD"))
	assert.Empty(t, r.gitAt(localDir, "status", "--porcelain"),
		"the trusted worktree must be used, not the downloaded core.worktree")
	for _, key := range []string{"core.worktree", "core.sshCommand"} {
		_, err := runGit(t, localDir, r.env, "config", "--local", "--get", key)
		assert.Error(t, err, "%s must not be restored", key)
	}
	assert.NoFileExists(t, marker)
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries, "the downloaded worktree redirect must not touch its target")
}

// TestSanitizeDownload_RemovesHooksAtAnyDepthAndVariant removes hook entries in
// the root .git, module stores, nested checkouts, case variants, and as
// symlinks, without deleting an in-tree symlink target.
func TestSanitizeDownload_RemovesHooksAtAnyDepthAndVariant(t *testing.T) {
	newRoot := func(t *testing.T) string {
		root := t.TempDir()
		writeFile(t, root, ".git/config", "[core]\n")
		writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
		return root
	}
	assertRemoved := func(t *testing.T, root string, rels ...string) {
		t.Helper()
		for _, rel := range rels {
			_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
			assert.True(t, os.IsNotExist(err), "%s must be removed", rel)
		}
	}

	t.Run("case variant and module store", func(t *testing.T) {
		root := newRoot(t)
		writeFile(t, root, ".git/HOOKS/pre-commit", "#!/bin/sh\necho bad\n")
		writeFile(t, root, ".git/modules/dep/config", "[core]\n")
		writeFile(t, root, ".git/modules/dep/HEAD", "ref: refs/heads/main\n")
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "modules", "dep", "objects"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "modules", "dep", "refs"), 0o755))
		writeFile(t, root, ".git/modules/dep/hooks/pre-commit", "#!/bin/sh\necho bad\n")

		require.NoError(t, sanitizeDownload(root, RepositorySnapshot{}))

		assertRemoved(t, root, ".git/HOOKS", ".git/modules/dep/hooks")
		moduleConfig, err := os.ReadFile(filepath.Join(root, ".git", "modules", "dep", "config"))
		require.NoError(t, err)
		assert.Contains(t, string(moduleConfig), "repositoryformatversion", "module store config must be rebuilt")
	})

	t.Run("symlinked hooks entry", func(t *testing.T) {
		root := newRoot(t)
		writeFile(t, root, "shared-hooks/pre-commit", "#!/bin/sh\necho bad\n")
		require.NoError(t, os.Symlink("../shared-hooks", filepath.Join(root, ".git", "hooks")))

		require.NoError(t, sanitizeDownload(root, RepositorySnapshot{}))

		assertRemoved(t, root, ".git/hooks")
		assert.DirExists(t, filepath.Join(root, "shared-hooks"), "an in-tree symlink target must not be deleted")
	})

	t.Run("nested checkout", func(t *testing.T) {
		root := newRoot(t)
		writeFile(t, root, "vendor/nested/.git/config", "[core]\n")
		writeFile(t, root, "vendor/nested/.git/hooks/post-checkout", "#!/bin/sh\necho bad\n")

		require.NoError(t, sanitizeDownload(root, RepositorySnapshot{}))

		assertRemoved(t, root, "vendor/nested/.git/hooks")
	})
}

// TestSafeDownload_RemovesCaseVariantHooksEndToEnd downloads a checkout whose
// git dir and hooks entry use case variants, then proves real Git runs no hook
// marker while HEAD, refs, config, and format survive. It runs only on a
// case-insensitive filesystem, where those variants name the same entries.
func TestSafeDownload_RemovesCaseVariantHooksEndToEnd(t *testing.T) {
	requireGit(t)
	probe := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(probe, "caseprobe"), []byte("x\n"), 0o644))
	if _, err := os.Lstat(filepath.Join(probe, "CASEPROBE")); err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	dirNames := func(t *testing.T, dir string) []string {
		t.Helper()
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return names
	}

	t.Run("root git dir and hooks", func(t *testing.T) {
		r := newGitRepo(t)
		head := r.commitFile("file.txt", "content")
		r.git("config", "remote.origin.url", "https://example.invalid/org/repo")
		refs := r.git("for-each-ref")
		snapshot, err := CaptureRepositorySnapshot(r.dir)
		require.NoError(t, err)

		// Simulate a download whose git dir and hooks entry carry other casing;
		// the filesystem treats them as the same entries.
		require.NoError(t, os.Rename(filepath.Join(r.dir, ".git"), filepath.Join(r.dir, ".GIT")))
		require.Contains(t, dirNames(t, r.dir), ".GIT")
		require.NoError(t, os.RemoveAll(filepath.Join(r.dir, ".GIT", "hooks")))
		marker := filepath.Join(t.TempDir(), "executed")
		markerHook(t, filepath.Join(r.dir, ".GIT", "HOOKS"), "pre-commit", marker)
		markerHook(t, filepath.Join(r.dir, ".GIT", "hooks"), "post-checkout", marker)
		assert.Contains(t, dirNames(t, filepath.Join(r.dir, ".GIT")), "HOOKS",
			"fixture must create an upper-case hooks entry")

		serveDownloadFrom(t, r.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

		assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "HEAD"))
		assert.Equal(t, refs, r.gitAt(localDir, "for-each-ref"))
		assert.Equal(t, "Test", r.gitAt(localDir, "config", "--local", "--get", "user.name"))
		assert.Equal(t, "https://example.invalid/org/repo", r.gitAt(localDir, "config", "--local", "--get", "remote.origin.url"))
		assert.Equal(t, "0", r.gitAt(localDir, "config", "--local", "--get", "core.repositoryformatversion"))
		assert.Equal(t, "sha1", r.gitAt(localDir, "rev-parse", "--show-object-format"))
		_, err = os.Lstat(filepath.Join(localDir, ".git", "HOOKS"))
		assert.True(t, os.IsNotExist(err), "a case-variant hooks entry must be removed")

		r.gitAt(localDir, "commit", "--quiet", "--allow-empty", "-m", "after download")
		r.gitAt(localDir, "checkout", "--quiet", "-b", "after-download")
		assert.NoFileExists(t, marker, "no downloaded case-variant hook may run")
	})

	t.Run("nested module store", func(t *testing.T) {
		dep := newGitRepo(t)
		dep.commitFile("dep.txt", "dependency")
		super := newGitRepo(t)
		super.commitFile("file.txt", "content")
		super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "vendor/dep")
		super.git("commit", "--quiet", "-m", "add submodule")
		submodule := filepath.Join(super.dir, "vendor", "dep")
		subHead := super.gitAt(submodule, "rev-parse", "HEAD")
		storeURL := super.gitAt(submodule, "config", "--local", "--get", "remote.origin.url")
		snapshot, err := CaptureRepositorySnapshot(super.dir)
		require.NoError(t, err)

		require.NoError(t, os.Rename(filepath.Join(super.dir, ".git"), filepath.Join(super.dir, ".GIT")))
		require.NoError(t, os.Rename(filepath.Join(super.dir, ".GIT", "modules"), filepath.Join(super.dir, ".GIT", "MODULES")))
		store := filepath.Join(super.dir, ".GIT", "MODULES", "vendor", "dep")
		require.NoError(t, os.RemoveAll(filepath.Join(store, "hooks")))
		marker := filepath.Join(t.TempDir(), "executed")
		markerHook(t, filepath.Join(store, "HOOKS"), "pre-commit", marker)
		assert.Contains(t, dirNames(t, store), "HOOKS",
			"fixture must create an upper-case module store hooks entry")

		serveDownloadFrom(t, super.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

		localSub := filepath.Join(localDir, "vendor", "dep")
		assert.Equal(t, subHead, super.gitAt(localSub, "rev-parse", "HEAD"))
		assert.Equal(t, storeURL, super.gitAt(localSub, "config", "--local", "--get", "remote.origin.url"))
		localStore := filepath.Join(localDir, ".git", "modules", "vendor", "dep")
		_, err = os.Lstat(filepath.Join(localStore, "HOOKS"))
		assert.True(t, os.IsNotExist(err), "a case-variant module store hooks entry must be removed")

		super.gitAt(localSub, "commit", "--quiet", "--allow-empty", "-m", "after download")
		assert.NoFileExists(t, marker, "no downloaded module store case-variant hook may run")
	})
}

// TestSanitizeDownload_PreservesRefsNamedLikeMetadata keeps branches and tags
// whose names look like git metadata; only real metadata entries are removed.
func TestSanitizeDownload_PreservesRefsNamedLikeMetadata(t *testing.T) {
	requireGit(t)
	r := newGitRepo(t)
	r.commitFile("f.txt", "x")
	for _, name := range []string{"hooks", "feature/config.worktree", "modules"} {
		r.git("branch", name)
	}
	r.git("tag", "hooks")
	before := r.git("show-ref")

	require.NoError(t, sanitizeDownload(r.dir, RepositorySnapshot{}))

	assert.Equal(t, before, r.git("show-ref"), "refs must be preserved byte for byte")
	assert.Empty(t, r.git("status", "--porcelain"))
}

// TestSanitizeDownload_PreservesInitializedSubmodule keeps a normal submodule
// working after sanitization while its store config is rebuilt and its hooks
// are removed.
func TestSanitizeDownload_PreservesInitializedSubmodule(t *testing.T) {
	requireGit(t)
	sub := newGitRepo(t)
	subHead := sub.commitFile("dep.txt", "dependency")
	super := newGitRepo(t)
	super.commitFile("file.txt", "content")
	super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.dir, "vendor/dep")
	super.git("commit", "--quiet", "-m", "add submodule")

	store := filepath.Join(super.dir, ".git", "modules", "vendor", "dep")
	marker := filepath.Join(t.TempDir(), "executed")
	markerHook(t, filepath.Join(store, "hooks"), "pre-commit", marker)

	require.NoError(t, sanitizeDownload(super.dir, RepositorySnapshot{}))

	assert.FileExists(t, filepath.Join(super.dir, "vendor", "dep", ".git"))
	depDir := filepath.Join(super.dir, "vendor", "dep")
	assert.Equal(t, subHead, super.gitAt(depDir, "rev-parse", "HEAD"))
	assert.Empty(t, super.gitAt(depDir, "status", "--porcelain"))
	storeConfig, err := os.ReadFile(filepath.Join(store, "config"))
	require.NoError(t, err)
	assert.Contains(t, string(storeConfig), "repositoryformatversion")
	_, err = os.Lstat(filepath.Join(store, "hooks"))
	assert.True(t, os.IsNotExist(err), "module store hooks must be removed")

	super.gitAt(depDir, "commit", "--quiet", "--allow-empty", "-m", "after sanitize")
	assert.NoFileExists(t, marker)
}

// TestSanitizeDownload_RejectsNestedGitfileRedirect rejects an initialized
// submodule gitfile whose target is not inside the owning checkout's module
// store.
func TestSanitizeDownload_RejectsNestedGitfileRedirect(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".git/config", "[core]\n")
	writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	writeFile(t, root, "outside/config", "[core]\n")
	writeFile(t, root, "outside/HEAD", "ref: refs/heads/main\n")
	writeFile(t, root, "outside/refs/heads/main", testOID+"\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "outside", "objects"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "modules"), 0o755))
	writeFile(t, root, "vendor/dep/.git", "gitdir: ../../outside\n")

	err := sanitizeDownload(root, RepositorySnapshot{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	assert.Contains(t, err.Error(), "redirects outside")
}

// TestSafeDownload_PreservesRepositoryFormat keeps sha256 and reftable storage
// declared after the config rebuild.
func TestSafeDownload_PreservesRepositoryFormat(t *testing.T) {
	requireGit(t)

	t.Run("sha256", func(t *testing.T) {
		dir := t.TempDir()
		r := &gitRepo{t: t, dir: dir, env: gitTestEnv(t.TempDir())}
		out, err := r.tryGit("init", "--quiet", "--initial-branch=main", "--object-format=sha256", ".")
		if err != nil {
			t.Skipf("git does not support sha256: %s", out)
		}
		r.git("config", "user.name", "Test")
		r.git("config", "user.email", "test@example.invalid")
		head := r.commitFile("f.txt", "x")
		snapshot, err := CaptureRepositorySnapshot(r.dir)
		require.NoError(t, err)

		serveDownloadFrom(t, r.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

		assert.Equal(t, "sha256", r.gitAt(localDir, "config", "--local", "--get", "extensions.objectFormat"))
		assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "HEAD"))
		assert.Equal(t, "Test", r.gitAt(localDir, "config", "--local", "--get", "user.name"))
		assert.Empty(t, r.gitAt(localDir, "status", "--porcelain"))
	})

	t.Run("reftable", func(t *testing.T) {
		requireReftableSanitization(t)
		r := newReftableRepo(t)
		head := r.commitFile("f.txt", "x")
		snapshot, err := CaptureRepositorySnapshot(r.dir)
		require.NoError(t, err)

		serveDownloadFrom(t, r.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

		assert.Equal(t, "reftable", r.gitAt(localDir, "config", "--local", "--get", "extensions.refStorage"))
		assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "HEAD"))
		assert.Empty(t, r.gitAt(localDir, "status", "--porcelain"))
	})
}

// TestSanitizeDownload_RejectsUnsafeNestedGitfile fails clearly for nested
// gitfiles that Git cannot resolve, that no checkout owns, or whose owning
// checkout has no module store.
func TestSanitizeDownload_RejectsUnsafeNestedGitfile(t *testing.T) {
	t.Run("not a gitfile", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, ".git/config", "[core]\n")
		writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "modules"), 0o755))
		writeFile(t, root, "vendor/dep/.git", "not a gitdir redirect\n")

		err := sanitizeDownload(root, RepositorySnapshot{})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	})

	// A resolvable gitdir is required to reach the ownership checks.
	makeStore := func(t *testing.T, dir string) {
		writeFile(t, dir, "config", "[core]\n\trepositoryformatversion = 0\n")
		writeFile(t, dir, "HEAD", "ref: refs/heads/main\n")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "objects"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "refs"), 0o755))
	}

	t.Run("no owning checkout", func(t *testing.T) {
		root := t.TempDir()
		makeStore(t, filepath.Join(root, "store"))
		writeFile(t, root, "vendor/dep/.git", "gitdir: ../../store\n")

		err := sanitizeDownload(root, RepositorySnapshot{})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
		assert.Contains(t, err.Error(), "no owning git directory")
	})

	t.Run("no module store", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, ".git/config", "[core]\n")
		writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
		makeStore(t, filepath.Join(root, "store"))
		writeFile(t, root, "vendor/dep/.git", "gitdir: ../../store\n")

		err := sanitizeDownload(root, RepositorySnapshot{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "module store directory")
	})
}

// TestSafeDownload_PreservesOriginPushURL keeps every trusted push destination
// in order while discarding the agent's edits.
func TestSafeDownload_PreservesOriginPushURL(t *testing.T) {
	requireGit(t)
	r := newGitRepo(t)
	r.commitFile("f.txt", "x")
	r.git("config", "remote.origin.url", "https://example.invalid/org/fetch.git")
	r.git("config", "--add", "remote.origin.pushurl", "https://push.invalid/org/one.git")
	r.git("config", "--add", "remote.origin.pushurl", "ssh://git@push.invalid/org/two.git")
	r.git("config", "--add", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"https://push.invalid/org/one.git",
		"ssh://git@push.invalid/org/two.git",
	}, entriesFor(snapshot.config, "remote.origin.pushurl"))

	// The agent redirects push; the trusted destinations must win.
	r.git("config", "--replace-all", "remote.origin.pushurl", "ext::sh -c true")
	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

	assert.Equal(t, []string{
		"https://push.invalid/org/one.git",
		"ssh://git@push.invalid/org/two.git",
	}, strings.Split(r.gitAt(localDir, "config", "--local", "--get-all", "remote.origin.pushurl"), "\n"))
	assert.Equal(t, "https://example.invalid/org/fetch.git", r.gitAt(localDir, "config", "--local", "--get", "remote.origin.url"))
}

// TestSanitizeDownload_RejectsSymlinkedModuleStore refuses a module store or
// module container that reaches Git through a symlink, which would bypass the
// config and hook sanitization this walk performs.
func TestSanitizeDownload_RejectsSymlinkedModuleStore(t *testing.T) {
	makeStore := func(t *testing.T, dir string) {
		writeFile(t, dir, "config", "[core]\n\trepositoryformatversion = 0\n")
		writeFile(t, dir, "HEAD", "ref: refs/heads/main\n")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "objects"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "refs"), 0o755))
	}

	t.Run("symlinked modules container", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, ".git/config", "[core]\n")
		writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
		makeStore(t, filepath.Join(root, "project", "dep"))
		require.NoError(t, os.Symlink(filepath.Join("..", "..", "project"), filepath.Join(root, ".git", "modules")))
		writeFile(t, root, "vendor/dep/.git", "gitdir: ../../.git/modules/dep\n")

		err := sanitizeDownload(root, RepositorySnapshot{})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	})

	t.Run("symlinked module ancestor", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, ".git/config", "[core]\n")
		writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "modules"), 0o755))
		makeStore(t, filepath.Join(root, "src", "dep"))
		require.NoError(t, os.Symlink(filepath.Join("..", "..", "src"), filepath.Join(root, ".git", "modules", "vendor")))
		writeFile(t, root, "vendor/dep/.git", "gitdir: ../../.git/modules/vendor/dep\n")

		err := sanitizeDownload(root, RepositorySnapshot{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink")
	})
}

// TestSanitizeDownload_SanitizesModuleStoreMetadata applies the redirect and
// ancestry rules to module stores, not just the root .git.
func TestSanitizeDownload_SanitizesModuleStoreMetadata(t *testing.T) {
	newRoot := func(t *testing.T) (string, string) {
		root := t.TempDir()
		writeFile(t, root, ".git/config", "[core]\n")
		writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
		store := filepath.Join(root, ".git", "modules", "dep")
		writeFile(t, store, "config", "[core]\n\trepositoryformatversion = 0\n")
		writeFile(t, store, "HEAD", "ref: refs/heads/main\n")
		require.NoError(t, os.MkdirAll(filepath.Join(store, "objects"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(store, "refs", "heads"), 0o755))
		return root, store
	}

	t.Run("commondir", func(t *testing.T) {
		root, store := newRoot(t)
		writeFile(t, store, "commondir", "../elsewhere\n")
		err := sanitizeDownload(root, RepositorySnapshot{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "redirect")
	})

	t.Run("alternates", func(t *testing.T) {
		root, store := newRoot(t)
		writeFile(t, store, "objects/info/alternates", "/elsewhere/objects\n")
		err := sanitizeDownload(root, RepositorySnapshot{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "alternate object store")
	})

	t.Run("ancestry", func(t *testing.T) {
		root, store := newRoot(t)
		writeFile(t, store, "refs/replace/"+testOID, testOID+"\n")
		writeFile(t, store, "refs/heads/main", testOID+"\n")
		writeFile(t, store, "info/grafts", testOID+" "+testOID+"\n")
		writeFile(t, store, "packed-refs", testOID+" refs/replace/"+testOID+"\n^"+testOID+"\n"+testOID+" refs/tags/v1\n")

		require.NoError(t, sanitizeDownload(root, RepositorySnapshot{}))

		for _, rel := range []string{"refs/replace", "info/grafts"} {
			_, err := os.Lstat(filepath.Join(store, filepath.FromSlash(rel)))
			assert.True(t, os.IsNotExist(err), "%s must be removed from a module store", rel)
		}
		assert.FileExists(t, filepath.Join(store, "refs", "heads", "main"))
		packed, err := os.ReadFile(filepath.Join(store, "packed-refs"))
		require.NoError(t, err)
		assert.Equal(t, testOID+" refs/tags/v1\n", string(packed))
	})
}

// TestSafeDownload_PreservesTrustedFormatWithEmptyConfig restores the trusted
// format even when the snapshot has no allowlisted config values.
func TestSafeDownload_PreservesTrustedFormatWithEmptyConfig(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	r := &gitRepo{t: t, dir: dir, env: gitTestEnv(t.TempDir())}
	out, err := r.tryGit("init", "--quiet", "--initial-branch=main", "--object-format=sha256", ".")
	if err != nil {
		t.Skipf("git does not support sha256: %s", out)
	}
	head := r.commitFile("f.txt", "x")
	// Leave only the format declarations so the trusted entry list is empty.
	writeFile(t, dir, ".git/config", "[core]\n\trepositoryformatversion = 1\n[extensions]\n\tobjectFormat = sha256\n")

	snapshot, err := CaptureRepositorySnapshot(dir)
	require.NoError(t, err)
	require.Empty(t, snapshot.config, "no allowlisted defaults may be captured")
	assert.Equal(t, "sha256", snapshot.format.objectFormat)

	require.NoError(t, os.Remove(filepath.Join(dir, ".git", "config")))
	serveDownloadFrom(t, dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

	assert.Equal(t, "sha256", r.gitAt(localDir, "config", "--local", "--get", "extensions.objectFormat"))
	assert.Equal(t, head, r.gitAt(localDir, "rev-parse", "HEAD"))
}

// TestSafeDownload_RejectsAgentFormatChange rejects a download whose root
// format differs from the trusted one before any native reftable processing.
func TestSafeDownload_RejectsAgentFormatChange(t *testing.T) {
	requireGit(t)
	r := newGitRepo(t)
	r.commitFile("f.txt", "x")
	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	require.Equal(t, "files", snapshot.format.refStorage)

	// The agent claims reftable storage and leaves a store behind; the trusted
	// format must be compared first.
	cfgPath := filepath.Join(r.dir, ".git", "config")
	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	changed := strings.Replace(string(data), "repositoryformatversion = 0", "repositoryformatversion = 1", 1) +
		"\n[extensions]\n\trefStorage = reftable\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(changed), 0o644))
	writeFile(t, r.dir, ".git/reftable/bogus", "not a table\n")
	serveDownloadFrom(t, r.dir)

	localDir := filepath.Join(t.TempDir(), "repo")
	err = SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	assert.Contains(t, err.Error(), "trusted checkout is")
	assert.NoDirExists(t, localDir)
}

// TestSanitizeDownload_ReftableModuleStoreCleaned applies the native reftable
// check to a dormant module store, not just the root .git.
func TestSanitizeDownload_ReftableModuleStoreCleaned(t *testing.T) {
	requireReftableSanitization(t)

	root := newGitRepo(t)
	root.commitFile("f.txt", "x")
	dep := newReftableRepo(t)
	depHead := dep.commitFile("dep.txt", "dep")
	tree := dep.git("rev-parse", depHead+"^{tree}")
	orphan := dep.git("commit-tree", tree, "-m", "dep orphan")
	bridge := dep.git("commit-tree", tree, "-p", depHead, "-m", "dep bridge")
	dep.git("replace", orphan, bridge)

	store := filepath.Join(root.dir, ".git", "modules", "dep")
	require.NoError(t, os.MkdirAll(filepath.Dir(store), 0o755))
	require.NoError(t, os.Rename(filepath.Join(dep.dir, ".git"), store))

	require.NoError(t, sanitizeDownload(root.dir, RepositorySnapshot{}))

	out, err := runGit(t, root.dir, root.env, "--git-dir="+store, "replace", "--list")
	require.NoError(t, err, out)
	assert.Empty(t, out, "replacement refs must be removed from a module store")
}

// TestSafeDownload_ReftableDeletedConfigIsRejected proves a reftable checkout
// whose downloaded config was deleted fails closed as a deterministic
// rejection: an undeclared storage format is agent-controlled content, not an
// uncertain validation failure the loop should retry.
func TestSafeDownload_ReftableDeletedConfigIsRejected(t *testing.T) {
	requireReftableSanitization(t)

	repo := newReftableRepo(t)
	repo.commitFile("f.txt", "x")
	snapshot, err := CaptureRepositorySnapshot(repo.dir)
	require.NoError(t, err)

	require.NoError(t, os.Remove(filepath.Join(repo.dir, ".git", "config")))
	serveDownloadFrom(t, repo.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	err = SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	assert.NoDirExists(t, localDir)
}

// TestSanitizeDownload_IgnoresFakeHeadInMetadataDirs keeps a HEAD file planted
// in info/ or a ref namespace from promoting that directory to a git dir, so
// real metadata such as info/grafts cannot hide behind it.
func TestSanitizeDownload_IgnoresFakeHeadInMetadataDirs(t *testing.T) {
	t.Run("root info", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, ".git/config", "[core]\n")
		writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
		writeFile(t, root, ".git/info/HEAD", "planted\n")
		writeFile(t, root, ".git/info/grafts", testOID+" "+testOID+"\n")

		require.NoError(t, sanitizeDownload(root, RepositorySnapshot{}))

		_, err := os.Lstat(filepath.Join(root, ".git", "info", "grafts"))
		assert.True(t, os.IsNotExist(err), "info/grafts must be removed despite info/HEAD")
	})

	t.Run("module store info", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, ".git/config", "[core]\n")
		writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
		store := filepath.Join(root, ".git", "modules", "dep")
		writeFile(t, store, "config", "[core]\n")
		writeFile(t, store, "HEAD", "ref: refs/heads/main\n")
		require.NoError(t, os.MkdirAll(filepath.Join(store, "objects"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(store, "refs"), 0o755))
		writeFile(t, store, "info/HEAD", "planted\n")
		writeFile(t, store, "info/grafts", testOID+" "+testOID+"\n")

		require.NoError(t, sanitizeDownload(root, RepositorySnapshot{}))

		_, err := os.Lstat(filepath.Join(store, "info", "grafts"))
		assert.True(t, os.IsNotExist(err), "module store info/grafts must be removed despite info/HEAD")
	})
}

// TestSanitizeDownload_PreservesModuleRefsNamedHeadAndHooks keeps module-store
// refs whose names look like metadata; a ref named HEAD must not turn its
// namespace into a git dir.
func TestSanitizeDownload_PreservesModuleRefsNamedHeadAndHooks(t *testing.T) {
	requireGit(t)
	dep := newGitRepo(t)
	dep.commitFile("dep.txt", "dependency")
	super := newGitRepo(t)
	super.commitFile("file.txt", "content")
	super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "vendor/dep")
	super.git("commit", "--quiet", "-m", "add submodule")
	store := filepath.Join(super.dir, ".git", "modules", "vendor", "dep")
	for _, name := range []string{"feature/hooks", "feature/HEAD"} {
		out, err := runGit(t, super.dir, super.env, "--git-dir="+store, "branch", name, "HEAD")
		require.NoError(t, err, out)
	}

	require.NoError(t, sanitizeDownload(super.dir, RepositorySnapshot{}))

	for _, name := range []string{"feature/hooks", "feature/HEAD"} {
		out, err := runGit(t, super.dir, super.env, "--git-dir="+store, "rev-parse", "refs/heads/"+name)
		require.NoError(t, err, "module ref %s must survive: %s", name, out)
	}
}

// TestSanitizeDownload_RejectsGitfileWhoseRawTargetEscapes validates the exact
// path Git opens: a symlink followed by ".." must not be textually collapsed
// into the module store.
func TestSanitizeDownload_RejectsGitfileWhoseRawTargetEscapes(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	writeFile(t, root, ".git/config", "[core]\n")
	writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	makeStore := func(dir string) {
		writeFile(t, dir, "config", "[core]\n\trepositoryformatversion = 0\n")
		writeFile(t, dir, "HEAD", "ref: refs/heads/main\n")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "objects"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "refs"), 0o755))
	}
	makeStore(filepath.Join(root, ".git", "modules", "dep"))
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "x"), 0o755))
	makeStore(filepath.Join(outside, ".git", "modules", "dep"))
	// zz-link sorts after vendor/, so it still exists when the gitfile is
	// validated: its target's raw path escapes while filepath.Clean would
	// collapse it into .git/modules/dep.
	require.NoError(t, os.Symlink(filepath.Join(outside, "x"), filepath.Join(root, "zz-link")))
	writeFile(t, root, "vendor/dep/.git", "gitdir: ../../zz-link/../.git/modules/dep\n")

	gitDir, err := runGit(t, filepath.Join(root, "vendor", "dep"), gitTestEnv(t.TempDir()), "rev-parse", "--git-dir")
	require.NoError(t, err, gitDir)
	assert.Contains(t, gitDir, filepath.Join(outside, ".git", "modules", "dep"),
		"real Git must open the target outside the checkout")

	err = sanitizeDownload(root, RepositorySnapshot{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	// Deferred validation runs after the walk has removed absolute symlinks,
	// so the error may be "redirects outside" (one-pass) or "not a valid
	// gitdir redirect" / "target is not reachable" (deferred, symlink gone).
	// Either is a secure rejection; the raw-path escape must not be cleaned.
	assert.True(t, strings.Contains(err.Error(), "redirects outside") || strings.Contains(err.Error(), "not a valid gitdir redirect") || strings.Contains(err.Error(), "target is not reachable"),
		"want symlink-escape rejection, got %v", err)
}

// TestSafeDownload_PreservesRemotesTrackingAndModuleRemotes keeps every trusted
// remote destination (including push URLs and an IPv6 URL), branch tracking,
// and the submodule store's remote, while discarding agent edits.
func TestSafeDownload_PreservesRemotesTrackingAndModuleRemotes(t *testing.T) {
	requireGit(t)
	dep := newGitRepo(t)
	dep.commitFile("dep.txt", "dependency")
	super := newGitRepo(t)
	super.commitFile("file.txt", "content")
	super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "vendor/dep")
	super.git("commit", "--quiet", "-m", "add submodule")
	depDir := filepath.Join(super.dir, "vendor", "dep")
	storeURL := super.gitAt(depDir, "config", "--local", "--get", "remote.origin.url")
	ipv6 := "https://[2001:db8::1]/org/up.git"

	super.git("config", "remote.upstream.url", ipv6)
	super.git("config", "--add", "remote.upstream.fetch", "+refs/heads/*:refs/remotes/upstream/*")
	super.git("config", "--add", "remote.origin.pushurl", "https://push.invalid/org/one.git")
	super.git("config", "--add", "remote.origin.pushurl", "ssh://git@push.invalid/org/two.git")
	super.git("config", "branch.main.remote", "upstream")
	super.git("config", "branch.main.merge", "refs/heads/up-main")

	snapshot, err := CaptureRepositorySnapshot(super.dir)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"https://push.invalid/org/one.git",
		"ssh://git@push.invalid/org/two.git",
	}, entriesFor(snapshot.config, "remote.origin.pushurl"))
	assert.Equal(t, []string{ipv6}, entriesFor(snapshot.config, "remote.upstream.url"),
		"an IPv6 URL must be captured, not mistaken for a transport helper")
	assert.Equal(t, []string{"upstream"}, entriesFor(snapshot.config, "branch.main.remote"))
	assert.Contains(t, snapshot.stores[".git/modules/vendor/dep"].config,
		configEntry{Key: "remote.origin.url", Value: storeURL})

	// Agent edits every destination; trusted values must win.
	super.git("config", "--replace-all", "remote.origin.pushurl", "ext::sh -c true")
	super.git("config", "remote.upstream.url", "https://evil.invalid/up.git")
	super.git("config", "branch.main.remote", "origin")
	super.gitAt(depDir, "config", "remote.origin.url", "ext::sh -c true")
	serveDownloadFrom(t, super.dir)

	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

	assert.Equal(t, []string{
		"https://push.invalid/org/one.git",
		"ssh://git@push.invalid/org/two.git",
	}, strings.Split(super.gitAt(localDir, "config", "--local", "--get-all", "remote.origin.pushurl"), "\n"))
	assert.Equal(t, ipv6, super.gitAt(localDir, "config", "--local", "--get", "remote.upstream.url"))
	assert.Equal(t, ipv6, super.gitAt(localDir, "remote", "get-url", "upstream"),
		"real Git must accept the restored IPv6 destination")
	assert.Equal(t, "upstream", super.gitAt(localDir, "config", "--local", "--get", "branch.main.remote"))
	assert.Equal(t, "refs/heads/up-main", super.gitAt(localDir, "config", "--local", "--get", "branch.main.merge"))
	localDep := filepath.Join(localDir, "vendor", "dep")
	assert.Equal(t, storeURL, super.gitAt(localDep, "config", "--local", "--get", "remote.origin.url"),
		"the submodule store remote must be restored")
	assert.Empty(t, super.gitAt(localDep, "status", "--porcelain"))
}

// TestSanitizeDownload_RejectsGitfileTargetNotSanitized refuses a nested gitfile
// whose target the walk never recorded as a sanitized git dir, and proves an
// ordinary initialized submodule still works.
func TestSanitizeDownload_RejectsGitfileTargetNotSanitized(t *testing.T) {
	requireGit(t)
	writeStore := func(t *testing.T, dir string, withConfig bool) {
		t.Helper()
		if withConfig {
			writeFile(t, dir, "config", "[core]\n\trepositoryformatversion = 0\n")
		}
		writeFile(t, dir, "HEAD", "ref: refs/heads/main\n")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "objects"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "refs"), 0o755))
	}
	newRoot := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		writeFile(t, root, ".git/config", "[core]\n")
		writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
		return root
	}

	t.Run("under another store objects", func(t *testing.T) {
		root := newRoot(t)
		writeStore(t, filepath.Join(root, ".git", "modules", "parent"), true)
		evil := filepath.Join(root, ".git", "modules", "parent", "objects", "evil")
		writeStore(t, evil, true)
		marker := filepath.Join(t.TempDir(), "executed")
		markerHook(t, filepath.Join(evil, "hooks"), "pre-commit", marker)
		writeFile(t, root, "vendor/dep/.git", "gitdir: ../../.git/modules/parent/objects/evil\n")

		gitDir, err := runGit(t, filepath.Join(root, "vendor", "dep"), gitTestEnv(t.TempDir()), "rev-parse", "--git-dir")
		require.NoError(t, err, gitDir)
		assert.Contains(t, gitDir, filepath.Join("parent", "objects", "evil"),
			"real Git must use the unsanitized target")

		err = sanitizeDownload(root, RepositorySnapshot{})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
		assert.Contains(t, err.Error(), "not a sanitized git dir")
		assert.NoFileExists(t, marker, "no agent hook may run while rejecting the download")
	})

	t.Run("configless target", func(t *testing.T) {
		root := newRoot(t)
		writeStore(t, filepath.Join(root, ".git", "modules", "dep"), false)
		writeFile(t, root, "vendor/dep/.git", "gitdir: ../../.git/modules/dep\n")

		err := sanitizeDownload(root, RepositorySnapshot{})
		require.Error(t, err)
		// The walk rejects the configless store before the deferred gitfile
		// validation runs: git adopts a store without a config file.
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
		assert.Contains(t, err.Error(), "module store layout")
	})

	t.Run("ordinary initialized submodule", func(t *testing.T) {
		dep := newGitRepo(t)
		dep.commitFile("dep.txt", "dependency")
		super := newGitRepo(t)
		super.commitFile("f.txt", "x")
		super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "vendor/dep")
		super.git("commit", "--quiet", "-m", "add submodule")

		require.NoError(t, sanitizeDownload(super.dir, RepositorySnapshot{}))
		assert.Empty(t, super.gitAt(filepath.Join(super.dir, "vendor", "dep"), "status", "--porcelain"))
	})
}

// TestSafeDownload_PreservesPushSettings keeps the trusted push configuration
// (push refspecs, pushDefault, pushRemote, push.default) and discards agent
// edits, so push behaviour is not silently changed.
func TestSafeDownload_PreservesPushSettings(t *testing.T) {
	requireGit(t)
	r := newGitRepo(t)
	r.commitFile("f.txt", "x")
	r.git("config", "--add", "remote.origin.push", "refs/heads/main:refs/heads/main")
	r.git("config", "--add", "remote.origin.push", ":refs/heads/old")
	r.git("config", "remote.pushDefault", "origin")
	r.git("config", "branch.main.pushRemote", "origin")
	r.git("config", "push.default", "upstream")
	r.git("config", "remote.origin.mirror", "true")

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"refs/heads/main:refs/heads/main", ":refs/heads/old"},
		entriesFor(snapshot.config, "remote.origin.push"))
	assert.Equal(t, []string{"origin"}, entriesFor(snapshot.config, "remote.pushdefault"))
	assert.Equal(t, []string{"upstream"}, entriesFor(snapshot.config, "push.default"))
	assert.Equal(t, []string{"origin"}, entriesFor(snapshot.config, "branch.main.pushremote"))
	assert.Equal(t, []string{"true"}, entriesFor(snapshot.config, "remote.origin.mirror"))

	r.git("config", "--replace-all", "remote.origin.push", ":refs/heads/main")
	r.git("config", "remote.pushDefault", "evil")
	r.git("config", "branch.main.pushRemote", "evil")
	r.git("config", "push.default", "matching")
	r.git("config", "remote.origin.mirror", "false")
	serveDownloadFrom(t, r.dir)

	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

	assert.Equal(t, []string{"refs/heads/main:refs/heads/main", ":refs/heads/old"},
		strings.Split(r.gitAt(localDir, "config", "--local", "--get-all", "remote.origin.push"), "\n"))
	assert.Equal(t, "origin", r.gitAt(localDir, "config", "--local", "--get", "remote.pushDefault"))
	assert.Equal(t, "origin", r.gitAt(localDir, "config", "--local", "--get", "branch.main.pushRemote"))
	assert.Equal(t, "upstream", r.gitAt(localDir, "config", "--local", "--get", "push.default"))
	assert.Equal(t, "true", r.gitAt(localDir, "config", "--local", "--get", "remote.origin.mirror"))
}

// TestSafeDownload_PreservesBareMirror keeps Git's boolean mirror semantics: a
// valueless key is true and a later empty value is false, so neither is dropped
// or replayed with the wrong meaning.
func TestSafeDownload_PreservesBareMirror(t *testing.T) {
	requireGit(t)
	appendSection := func(t *testing.T, repo *gitRepo, section string) {
		t.Helper()
		path := filepath.Join(repo.dir, ".git", "config")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(data, []byte("\n"+section)...), 0o644))
	}

	r := newGitRepo(t)
	r.commitFile("f.txt", "x")
	r.git("config", "remote.origin.url", "https://example.invalid/org/repo")
	appendSection(t, r, "[remote \"origin\"]\n\tmirror\n")
	assert.Equal(t, "true", r.git("config", "--get", "--bool", "remote.origin.mirror"),
		"native Git treats a bare mirror key as true")

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"true"}, entriesFor(snapshot.config, "remote.origin.mirror"))

	// The agent turns mirroring off; the trusted bare key must still mean true.
	r.git("config", "remote.origin.mirror", "false")
	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))
	assert.Equal(t, "true", r.gitAt(localDir, "config", "--get", "--bool", "remote.origin.mirror"))

	t.Run("empty worktree value overrides to false", func(t *testing.T) {
		r := newGitRepo(t)
		r.commitFile("f.txt", "x")
		r.git("config", "remote.origin.url", "https://example.invalid/org/repo")
		r.git("config", "extensions.worktreeConfig", "true")
		appendSection(t, r, "[remote \"origin\"]\n\tmirror\n")
		writeFile(t, r.dir, ".git/config.worktree", "[remote \"origin\"]\n\tmirror =\n")
		assert.Equal(t, "false", r.git("config", "--get", "--bool", "remote.origin.mirror"),
			"native Git treats an empty boolean as false")

		snapshot, err := CaptureRepositorySnapshot(r.dir)
		require.NoError(t, err)
		assert.Equal(t, []string{"false"}, entriesFor(snapshot.config, "remote.origin.mirror"),
			"a single-valued boolean captures only its final value")

		writeFile(t, r.dir, ".git/config.worktree", "[remote \"origin\"]\n\tmirror = true\n")
		serveDownloadFrom(t, r.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))
		assert.Equal(t, "false", r.gitAt(localDir, "config", "--get", "--bool", "remote.origin.mirror"))
	})
}

// TestSafeDownload_PreservesBareAndEmptyWorkingCopyBooleans keeps native Git
// boolean meaning for working-copy hints: an empty value is false and a bare
// key is true.
func TestSafeDownload_PreservesBareAndEmptyWorkingCopyBooleans(t *testing.T) {
	requireGit(t)
	r := newGitRepo(t)
	r.commitFile("f.txt", "x")
	_, _ = r.tryGit("config", "--unset-all", "core.ignorecase")
	r.git("config", "core.filemode", "")
	configPath := filepath.Join(r.dir, ".git", "config")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, append(data, []byte("\n[core]\n\tignorecase\n")...), 0o644))
	assert.Equal(t, "false", r.git("config", "--local", "--bool", "--get", "core.filemode"))
	assert.Equal(t, "true", r.git("config", "--local", "--bool", "--get", "core.ignorecase"))

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"false"}, entriesFor(snapshot.config, "core.filemode"))
	assert.Equal(t, []string{"true"}, entriesFor(snapshot.config, "core.ignorecase"))

	r.git("config", "core.filemode", "true")
	r.git("config", "core.ignorecase", "false")
	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

	assert.Equal(t, "false", r.gitAt(localDir, "config", "--local", "--get", "core.filemode"))
	assert.Equal(t, "true", r.gitAt(localDir, "config", "--local", "--get", "core.ignorecase"))
	assert.Equal(t, "false", r.gitAt(localDir, "config", "--local", "--bool", "--get", "core.filemode"))
	assert.Equal(t, "true", r.gitAt(localDir, "config", "--local", "--bool", "--get", "core.ignorecase"))
}

// TestSafeDownload_PreservesDistinctMirrorSubsectionCase keeps Git's
// case-sensitive remote subsection names apart when both set mirrors.
func TestSafeDownload_PreservesDistinctMirrorSubsectionCase(t *testing.T) {
	requireGit(t)
	r := newGitRepo(t)
	r.commitFile("f.txt", "x")
	r.git("config", "remote.ReviewProbe.mirror", "true")
	r.git("config", "remote.reviewprobe.mirror", "false")

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"true"}, entriesFor(snapshot.config, "remote.ReviewProbe.mirror"))
	assert.Equal(t, []string{"false"}, entriesFor(snapshot.config, "remote.reviewprobe.mirror"),
		"distinct case-sensitive remote subsections must not be conflated")

	r.git("config", "--replace-all", "remote.ReviewProbe.mirror", "false")
	r.git("config", "--replace-all", "remote.reviewprobe.mirror", "true")
	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

	assert.Equal(t, "true", r.gitAt(localDir, "config", "--local", "--get", "remote.ReviewProbe.mirror"))
	assert.Equal(t, "false", r.gitAt(localDir, "config", "--local", "--get", "remote.reviewprobe.mirror"))
}

// TestSafeDownload_RejectsModuleStoreFormatChange compares every initialized
// module store format with the trusted snapshot before native reftable
// handling, and fails closed when the download declares another one.
func TestSafeDownload_RejectsModuleStoreFormatChange(t *testing.T) {
	requireGit(t)
	newSubmodule := func(t *testing.T, dep *gitRepo) (*gitRepo, string) {
		t.Helper()
		super := newGitRepo(t)
		super.commitFile("f.txt", "x")
		super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "vendor/dep")
		super.git("commit", "--quiet", "-m", "add submodule")
		return super, filepath.Join(super.dir, ".git", "modules", "vendor", "dep", "config")
	}

	t.Run("sha1 store declares sha256", func(t *testing.T) {
		dep := newGitRepo(t)
		dep.commitFile("dep.txt", "dep")
		super, storeConfig := newSubmodule(t, dep)
		snapshot, err := CaptureRepositorySnapshot(super.dir)
		require.NoError(t, err)
		assert.Equal(t, "sha1", snapshot.stores[".git/modules/vendor/dep"].format.objectFormat)

		writeFile(t, filepath.Dir(storeConfig), "config",
			"[core]\n\trepositoryformatversion = 1\n[extensions]\n\tobjectFormat = sha256\n")
		serveDownloadFrom(t, super.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		err = SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
		assert.Contains(t, err.Error(), "trusted checkout is")
		assert.NoDirExists(t, localDir)
	})

	t.Run("sha1 store declares reftable", func(t *testing.T) {
		dep := newGitRepo(t)
		dep.commitFile("dep.txt", "dep")
		super, storeConfig := newSubmodule(t, dep)
		snapshot, err := CaptureRepositorySnapshot(super.dir)
		require.NoError(t, err)

		storeDir := filepath.Dir(storeConfig)
		writeFile(t, storeDir, "config",
			"[core]\n\trepositoryformatversion = 1\n[extensions]\n\trefStorage = reftable\n")
		// A malformed store would fail native reftable handling; the trusted
		// format comparison must reject first.
		writeFile(t, storeDir, "reftable/bogus", "not a table\n")
		serveDownloadFrom(t, super.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		err = SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "trusted checkout is")
		assert.NoDirExists(t, localDir)
	})

	t.Run("sha256 store declares sha1", func(t *testing.T) {
		// Git requires a submodule and its superproject to share the object
		// format, so the sha256 store is created in a sha256 checkout.
		dep := &gitRepo{t: t, dir: t.TempDir(), env: gitTestEnv(t.TempDir())}
		out, err := dep.tryGit("init", "--quiet", "--initial-branch=main", "--object-format=sha256", ".")
		if err != nil {
			t.Skipf("git does not support sha256: %s", out)
		}
		dep.git("config", "user.name", "Test")
		dep.git("config", "user.email", "test@example.invalid")
		dep.commitFile("dep.txt", "dep")

		super := &gitRepo{t: t, dir: t.TempDir(), env: gitTestEnv(t.TempDir())}
		out, err = super.tryGit("init", "--quiet", "--initial-branch=main", "--object-format=sha256", ".")
		require.NoError(t, err, out)
		super.git("config", "user.name", "Test")
		super.git("config", "user.email", "test@example.invalid")
		super.commitFile("f.txt", "x")
		if out, err = super.tryGit("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "vendor/dep"); err != nil {
			t.Skipf("cannot create a sha256 submodule store: %s (%v)", out, err)
		}
		super.git("commit", "--quiet", "-m", "add submodule")
		storeConfig := filepath.Join(super.dir, ".git", "modules", "vendor", "dep", "config")

		snapshot, err := CaptureRepositorySnapshot(super.dir)
		require.NoError(t, err)
		assert.Equal(t, "sha256", snapshot.stores[".git/modules/vendor/dep"].format.objectFormat)

		writeFile(t, filepath.Dir(storeConfig), "config",
			"[core]\n\trepositoryformatversion = 1\n[extensions]\n\tobjectFormat = sha1\n")
		serveDownloadFrom(t, super.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		err = SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
		assert.NoDirExists(t, localDir)
	})
}

// TestSafeDownload_RejectsCaseVariantModuleFormatChange proves a case-varied
// module path still resolves to the trusted store format: a changed agent
// format is rejected before native Git processing, while an ordinary submodule
// and its refs still round-trip.
func TestSafeDownload_RejectsCaseVariantModuleFormatChange(t *testing.T) {
	requireGit(t)
	probe := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(probe, "caseprobe"), []byte("x\n"), 0o644))
	if _, err := os.Lstat(filepath.Join(probe, "CASEPROBE")); err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	newSubmodule := func(t *testing.T) *gitRepo {
		t.Helper()
		dep := newGitRepo(t)
		dep.commitFile("dep.txt", "dependency")
		super := newGitRepo(t)
		super.commitFile("file.txt", "content")
		super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "vendor/dep")
		super.git("commit", "--quiet", "-m", "add submodule")
		return super
	}

	t.Run("case-varied module format change is rejected", func(t *testing.T) {
		super := newSubmodule(t)
		snapshot, err := CaptureRepositorySnapshot(super.dir)
		require.NoError(t, err)
		require.Equal(t, "sha1", snapshot.stores[".git/modules/vendor/dep"].format.objectFormat)

		require.NoError(t, os.Rename(filepath.Join(super.dir, ".git"), filepath.Join(super.dir, ".GIT")))
		require.NoError(t, os.Rename(filepath.Join(super.dir, ".GIT", "modules"), filepath.Join(super.dir, ".GIT", "MODULES")))
		caseStore := filepath.Join(super.dir, ".GIT", "MODULES", "vendor", "dep")
		require.DirExists(t, caseStore)
		writeFile(t, caseStore, "config",
			"[core]\n\trepositoryformatversion = 1\n[extensions]\n\tobjectFormat = sha256\n")

		serveDownloadFrom(t, super.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		err = SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
		assert.Contains(t, err.Error(), "trusted checkout is")
		assert.NoDirExists(t, localDir, "rejection must happen before publishing native Git output")
	})

	t.Run("ordinary submodule and refs are preserved", func(t *testing.T) {
		super := newSubmodule(t)
		superRefs := super.git("for-each-ref")
		sub := filepath.Join(super.dir, "vendor", "dep")
		subRefs := super.gitAt(sub, "for-each-ref")
		subHead := super.gitAt(sub, "rev-parse", "HEAD")
		snapshot, err := CaptureRepositorySnapshot(super.dir)
		require.NoError(t, err)

		serveDownloadFrom(t, super.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

		assert.Equal(t, superRefs, super.gitAt(localDir, "for-each-ref"))
		localSub := filepath.Join(localDir, "vendor", "dep")
		assert.Equal(t, subRefs, super.gitAt(localSub, "for-each-ref"))
		assert.Equal(t, subHead, super.gitAt(localSub, "rev-parse", "HEAD"))
		assert.Empty(t, super.gitAt(localSub, "status", "--porcelain"))
	})
}

// TestSafeDownload_PreservesWorktreePushDestination merges the trusted
// per-worktree config in Git's precedence order, so a push destination it sets
// is restored instead of falling back to the origin URL.
func TestSafeDownload_PreservesWorktreePushDestination(t *testing.T) {
	requireGit(t)
	r := newGitRepo(t)
	r.commitFile("f.txt", "x")
	r.git("config", "extensions.worktreeConfig", "true")
	r.git("config", "remote.origin.url", "https://example.invalid/main.git")
	r.git("config", "remote.origin.pushurl", "https://push.invalid/main.git")
	writeFile(t, r.dir, ".git/config.worktree",
		"[remote \"origin\"]\n\tpushurl = https://push.invalid/worktree.git\n")

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"https://push.invalid/main.git",
		"https://push.invalid/worktree.git",
	}, entriesFor(snapshot.config, "remote.origin.pushurl"),
		"worktree config values are appended in Git's precedence order")

	// The agent replaces the destinations; the trusted list must win.
	r.git("config", "--replace-all", "remote.origin.pushurl", "ext::sh -c true")
	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

	assert.Equal(t, []string{
		"https://push.invalid/main.git",
		"https://push.invalid/worktree.git",
	}, strings.Split(r.gitAt(localDir, "config", "--local", "--get-all", "remote.origin.pushurl"), "\n"))

	t.Run("disabled extension is ignored", func(t *testing.T) {
		r := newGitRepo(t)
		r.commitFile("f.txt", "x")
		writeFile(t, r.dir, ".git/config.worktree",
			"[remote \"origin\"]\n\tpushurl = https://push.invalid/ignored.git\n")
		snapshot, err := CaptureRepositorySnapshot(r.dir)
		require.NoError(t, err)
		assert.Empty(t, entriesFor(snapshot.config, "remote.origin.pushurl"),
			"Git does not read worktree config without extensions.worktreeConfig")
	})

	t.Run("non-regular worktree config fails closed", func(t *testing.T) {
		r := newGitRepo(t)
		r.commitFile("f.txt", "x")
		r.git("config", "extensions.worktreeConfig", "true")
		require.NoError(t, os.Mkdir(filepath.Join(r.dir, ".git", "config.worktree"), 0o755))
		// Git's own checkout validation already refuses a directory named
		// config.worktree while the extension is enabled; capture must not
		// fall back to another destination.
		_, err := CaptureRepositorySnapshot(r.dir)
		require.Error(t, err)
	})

	t.Run("module store worktree push destination", func(t *testing.T) {
		dep := newGitRepo(t)
		dep.commitFile("dep.txt", "dep")
		super := newGitRepo(t)
		super.commitFile("f.txt", "x")
		super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "vendor/dep")
		super.git("commit", "--quiet", "-m", "add dep")
		store := filepath.Join(super.dir, ".git", "modules", "vendor", "dep")
		submodule := filepath.Join(super.dir, "vendor", "dep")
		super.gitAt(submodule, "config", "extensions.worktreeConfig", "true")
		writeFile(t, store, "config.worktree",
			"[remote \"origin\"]\n\tpushurl = https://push.invalid/module-worktree.git\n")

		snapshot, err := CaptureRepositorySnapshot(super.dir)
		require.NoError(t, err)
		assert.Contains(t, snapshot.stores[".git/modules/vendor/dep"].config,
			configEntry{Key: "remote.origin.pushurl", Value: "https://push.invalid/module-worktree.git"},
			"a module store worktree config must be captured")

		// The agent replaces the value; the trusted one must win and the
		// downloaded worktree config must be discarded.
		writeFile(t, store, "config.worktree", "[remote \"origin\"]\n\tpushurl = ext::sh -c true\n")
		serveDownloadFrom(t, super.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

		localStore := filepath.Join(localDir, ".git", "modules", "vendor", "dep")
		assert.Equal(t, "https://push.invalid/module-worktree.git",
			super.gitAt(filepath.Join(localDir, "vendor", "dep"), "config", "--get", "remote.origin.pushurl"))
		assert.NoFileExists(t, filepath.Join(localStore, "config.worktree"),
			"the downloaded module worktree config must be discarded")
	})
}

// TestSafeDownload_FailsClosedWhenTrustedStorePresentButNotSanitized proves
// finding 1: a trusted module store whose config is deleted in the download
// still leaves hooks and ancestry unsanitized, so the checkout must be
// rejected before the trusted config is restored. Even if the worktree gitfile
// is removed, the store's malicious metadata survives. Legitimate deletions
// (store absent) are preserved.
func TestSafeDownload_FailsClosedWhenTrustedStorePresentButNotSanitized(t *testing.T) {
	requireGit(t)
	dep := newGitRepo(t)
	dep.commitFile("dep.txt", "dependency")
	super := newGitRepo(t)
	super.commitFile("file.txt", "content")
	super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "vendor/dep")
	super.git("commit", "--quiet", "-m", "add submodule")
	snapshot, err := CaptureRepositorySnapshot(super.dir)
	require.NoError(t, err)
	require.Contains(t, snapshot.stores, ".git/modules/vendor/dep")

	// Legitimate deletion: store removed entirely should be preserved.
	t.Run("legitimate deletion preserved", func(t *testing.T) {
		dep2 := newGitRepo(t)
		dep2.commitFile("dep.txt", "dependency")
		super2 := newGitRepo(t)
		super2.commitFile("file.txt", "content")
		super2.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep2.dir, "vendor/dep")
		super2.git("commit", "--quiet", "-m", "add submodule")
		snapshot2, err := CaptureRepositorySnapshot(super2.dir)
		require.NoError(t, err)
		require.Contains(t, snapshot2.stores, ".git/modules/vendor/dep")
		require.NoError(t, os.RemoveAll(filepath.Join(super2.dir, "vendor/dep")))
		require.NoError(t, os.RemoveAll(filepath.Join(super2.dir, ".git", "modules", "vendor", "dep")))
		serveDownloadFrom(t, super2.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot2))
		_, err = os.Lstat(filepath.Join(localDir, ".git", "modules", "vendor", "dep"))
		assert.True(t, os.IsNotExist(err), "legitimately deleted store must stay deleted")
		_, err = os.Lstat(filepath.Join(localDir, "vendor", "dep", ".git"))
		assert.True(t, os.IsNotExist(err))
	})

	// Adversarial: store present with deleted config plus malicious metadata,
	// even if worktree gitfile is removed.
	store := filepath.Join(super.dir, ".git", "modules", "vendor", "dep")
	require.NoError(t, os.Remove(filepath.Join(store, "config")))
	// Empty/deleted layout: leave store dir present but config missing.
	// Modified layout: also test with config replaced by directory.
	marker := filepath.Join(t.TempDir(), "executed")
	markerHook(t, filepath.Join(store, "hooks"), "pre-commit", marker)
	writeFile(t, store, "info/grafts", testOID+" "+testOID+"\n")
	writeFile(t, store, "refs/replace/"+testOID, testOID+"\n")
	writeFile(t, store, "objects/info/commit-graph", "malicious\n")
	// Remove worktree gitfile to simulate hidden store.
	require.NoError(t, os.Remove(filepath.Join(super.dir, "vendor", "dep", ".git")))

	serveDownloadFrom(t, super.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	err = SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot)
	require.Error(t, err, "a present trusted store with deleted config must fail closed before restoration")
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	// The walk rejects the configless store before the post-walk trusted
	// check can name it: git adopts a store without a config file.
	assert.Contains(t, err.Error(), "module store layout")
	assert.NoDirExists(t, localDir)
	assert.NoFileExists(t, marker)
}

// TestSanitizeDownload_ValidHiddenDirSubmodule proves finding 3: a valid
// submodule at a dot-prefixed path `.config/dep/.git` sorts before
// `.git/modules`, so one-pass gitfile validation would reject before the
// module store is known. Deferring validation until the entire walk has
// collected gitDirs must keep it valid.
func TestSanitizeDownload_ValidHiddenDirSubmodule(t *testing.T) {
	requireGit(t)
	dep := newGitRepo(t)
	depHead := dep.commitFile("dep.txt", "dependency")
	super := newGitRepo(t)
	super.commitFile("file.txt", "content")
	super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, ".config/dep")
	super.git("commit", "--quiet", "-m", "add submodule")
	// Direct sanitize should succeed even though .config/dep/.git sorts before .git/modules.
	require.NoError(t, sanitizeDownload(super.dir, RepositorySnapshot{}))
	snapshot, err := CaptureRepositorySnapshot(super.dir)
	require.NoError(t, err)
	require.Contains(t, snapshot.stores, ".git/modules/.config/dep")
	serveDownloadFrom(t, super.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))
	localDep := filepath.Join(localDir, ".config", "dep")
	assert.Equal(t, depHead, super.gitAt(localDep, "rev-parse", "HEAD"))
	assert.Empty(t, super.gitAt(localDep, "status", "--porcelain"))
	assert.Equal(t, depHead, super.gitAt(filepath.Join(localDir, ".config", "dep"), "rev-parse", "HEAD"))
}

// TestSanitizeDownload_PlantedGitUnderMetadataExclusionIsSanitizedOrRejected
// proves finding 2: a .git directory planted under a metadata exclusion
// (e.g., .git/modules/x/objects/.git) can be recognized as a root, but its
// direct config, hooks, and ancestry must still be sanitized or the checkout
// rejected.
func TestSanitizeDownload_PlantedGitUnderMetadataExclusionIsSanitizedOrRejected(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	writeFile(t, root, ".git/config", "[core]\n\trepositoryformatversion = 0\n")
	writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	// Create a legitimate store shell so the reported path is relevant.
	storeShell := filepath.Join(root, ".git", "modules", "x")
	writeFile(t, storeShell, "config", "[core]\n\trepositoryformatversion = 0\n")
	writeFile(t, storeShell, "HEAD", "ref: refs/heads/main\n")
	require.NoError(t, os.MkdirAll(filepath.Join(storeShell, "objects"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(storeShell, "refs"), 0o755))
	// Plant a valid git directory inside the store's objects metadata.
	planted := filepath.Join(storeShell, "objects", ".git")
	writeFile(t, planted, "config", "[core]\n\thooksPath = /evil\n[filter \"evil\"]\n\tclean = cat\n")
	writeFile(t, planted, "HEAD", "ref: refs/heads/main\n")
	require.NoError(t, os.MkdirAll(filepath.Join(planted, "objects"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(planted, "refs"), 0o755))
	marker := filepath.Join(t.TempDir(), "executed")
	markerHook(t, filepath.Join(planted, "hooks"), "pre-commit", marker)
	writeFile(t, planted, "info/grafts", testOID+" "+testOID+"\n")
	writeFile(t, planted, "refs/replace/"+testOID, testOID+"\n")
	require.NoError(t, os.MkdirAll(filepath.Join(planted, "objects", "info"), 0o755))
	writeFile(t, planted, "objects/info/commit-graph", "malicious\n")
	writeFile(t, planted, "packed-refs", testOID+" refs/replace/"+testOID+"\n")
	// In-checkout gitfile targeting the planted directory makes it a valid
	// worktree-style target; it must be sanitized or rejected.
	gitfile := filepath.Join(root, "vendor", "x", ".git")
	writeFile(t, root, "vendor/x/.git", "gitdir: ../../.git/modules/x/objects/.git\n")

	// Prove native Git resolves fixture's gitfile to the planted .git before sanitization.
	env := gitTestEnv(t.TempDir())
	out, err := runGit(t, filepath.Join(root, "vendor", "x"), env, "rev-parse", "--resolve-git-dir", gitfile)
	require.NoError(t, err, "fixture gitfile must be resolvable by native Git before sanitization: %s", out)
	assert.Contains(t, out, filepath.Join("modules", "x", "objects", ".git"), "native Git must resolve gitfile to planted .git, got %q", out)

	err = sanitizeDownload(root, RepositorySnapshot{})
	if err != nil {
		require.True(t, errors.Is(err, ErrRepositoryRejected), "planted .git under metadata exclusion must be rejected as ErrRepositoryRejected, got %v", err)
		lower := strings.ToLower(err.Error())
		// Relevant rejections for this layout are about unsanitized target or outside module store, not arbitrary Git failure.
		assert.True(t, strings.Contains(lower, "sanitized") || strings.Contains(lower, "redirects outside") || strings.Contains(lower, "module store"),
			"rejection must be relevant to planted store, got %v", err)
		return
	}
	// If accepted, direct metadata must be sanitized. Use Lstat/NoDirExists
	// so directories are not falsely passing.
	_, err = os.Lstat(filepath.Join(planted, "hooks"))
	assert.True(t, os.IsNotExist(err), "planted hooks must be removed")
	assert.NoDirExists(t, filepath.Join(planted, "hooks"))
	_, err = os.Lstat(filepath.Join(planted, "info", "grafts"))
	assert.True(t, os.IsNotExist(err), "planted grafts must be removed")
	_, err = os.Lstat(filepath.Join(planted, "refs", "replace"))
	assert.True(t, os.IsNotExist(err), "planted refs/replace must be removed")
	_, err = os.Lstat(filepath.Join(planted, "objects", "info", "commit-graph"))
	assert.True(t, os.IsNotExist(err), "planted commit-graph must be removed")
	cfg, err := os.ReadFile(filepath.Join(planted, "config"))
	require.NoError(t, err)
	assert.NotContains(t, string(cfg), "hooksPath", "planted config must be rebuilt")
	assert.NotContains(t, string(cfg), "filter", "planted filter must be rebuilt")
	assert.Contains(t, string(cfg), "repositoryformatversion")
	// Packed refs replacement must be filtered.
	if data, err := os.ReadFile(filepath.Join(planted, "packed-refs")); err == nil {
		assert.NotContains(t, string(data), "refs/replace", "planted packed replacement ref must be removed")
	} else {
		assert.True(t, os.IsNotExist(err), "packed-refs should be sanitized or absent")
	}
}

// TestSafeDownload_MissingRootGitDiagnostic verifies finding 5: when the
// downloaded checkout has its root .git deleted, the error is a clear
// targeted diagnostic that the input was a checkout and the downloaded .git
// is missing, preserves ErrRepositoryRejected, and does not publish.
func TestSafeDownload_MissingRootGitDiagnostic(t *testing.T) {
	requireGit(t)
	r := newGitRepo(t)
	r.commitFile("file.txt", "content")
	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	require.True(t, snapshot.isCheckout)
	require.NoError(t, os.RemoveAll(filepath.Join(r.dir, ".git")))
	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	err = SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	lower := strings.ToLower(err.Error())
	assert.Contains(t, lower, "downloaded .git")
	assert.Contains(t, lower, "missing")
	assert.Contains(t, lower, "input was a git checkout")
	assert.NoDirExists(t, localDir)
}

func TestSanitizeDownload_RemovesAbsoluteSymlinks(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "real.txt"), []byte("ok"), 0o644))
	require.NoError(t, os.Symlink("/nonexistent/target", filepath.Join(dir, "danger")))

	err := sanitizeDownload(dir, RepositorySnapshot{})
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

	err := sanitizeDownload(dir, RepositorySnapshot{})
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

	err := sanitizeDownload(dir, RepositorySnapshot{})
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

	err := sanitizeDownload(dir, RepositorySnapshot{})
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

	require.NoError(t, sanitizeDownload(repo, RepositorySnapshot{}))

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

	err := sanitizeDownload(dir, RepositorySnapshot{})
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

	err := sanitizeDownload(dir, RepositorySnapshot{})
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

	err := sanitizeDownload(dir, RepositorySnapshot{})
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
	err := sanitizeDownload(dir, RepositorySnapshot{})
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
	writeFile(t, root, ".git/logs/wt/HEAD", "ref: refs/heads/wt\n")

	require.NoError(t, sanitizeDownload(root, RepositorySnapshot{}))

	for _, rel := range []string{
		".git/HEAD", ".git/config", ".git/index",
		".git/objects/ab/object", ".git/objects/pack/pack-abc.idx",
		".git/refs/heads/main", ".git/refs/remotes/origin/main",
		".git/refs/tags/v1", ".git/refs/stash", ".git/refs/notes/commits",
		".git/refs/bisect/good", ".git/refs/custom/x",
		".git/info/exclude", ".git/info/refs", ".git/objects/info/packs",
		".git/modules/dep/config", ".git/logs/wt/HEAD",
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

	require.NoError(t, sanitizeDownload(root, RepositorySnapshot{}))

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

			err := sanitizeDownload(root, RepositorySnapshot{})
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
			require.Error(t, sanitizeDownload(r.dir, RepositorySnapshot{}), "redirected Git metadata must be rejected")
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

	require.NoError(t, sanitizeDownload(r.dir, RepositorySnapshot{}))

	assert.Equal(t, head, r.git("rev-parse", "HEAD"))
	assert.Equal(t, "main", r.git("symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, head, r.git("rev-parse", "main"))
	_, err := os.Stat(filepath.Join(r.dir, ".git", "reftable", "tables.list"))
	require.NoError(t, err, "reftable tables must survive sanitization")
	configAfter, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.NotContains(t, string(configAfter), "user", "untrusted config values must be dropped")
	assert.Equal(t, "reftable", r.git("config", "--local", "--get", "extensions.refStorage"),
		"the declared ref storage must survive the rebuild")
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
	require.NoError(t, sanitizeDownload(r.dir, RepositorySnapshot{}))

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

	require.NoError(t, sanitizeDownload(r.dir, RepositorySnapshot{}))

	_, err := r.tryGit("show-ref", "--verify", link)
	require.Error(t, err, "symbolic replacement ref must be removed")
	assert.Equal(t, head, r.git("rev-parse", "refs/heads/main"), "target branch must be unchanged")
}

// TestSanitizeDownload_ReftableUnbornChecksOut accepts an unborn reftable
// repository with no refs yet.
func TestSanitizeDownload_ReftableUnbornChecksOut(t *testing.T) {
	requireReftableSanitization(t)
	r := newReftableRepo(t)

	require.NoError(t, sanitizeDownload(r.dir, RepositorySnapshot{}))

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

	require.NoError(t, sanitizeDownload(r.dir, RepositorySnapshot{}))

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
	assert.NoFileExists(t, marker, "hook must not run during setup")

	require.NoError(t, sanitizeDownload(r.dir, RepositorySnapshot{}))

	assert.NoFileExists(t, marker, "repository hook must never execute")
	assert.Empty(t, r.git("replace", "--list"), "repository fsck config must not weaken cleanup")
	configAfter, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.NotContains(t, string(configAfter), "hooksPath", "executable config must be dropped")
	assert.NotContains(t, string(configAfter), "fsck", "repository fsck overrides must be dropped")
	assert.Equal(t, "reftable", r.git("config", "--local", "--get", "extensions.refStorage"))
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

	require.Error(t, sanitizeDownload(r.dir, RepositorySnapshot{}), "corrupt reftable bytes must fail closed")

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

			require.Error(t, sanitizeDownload(r.dir, RepositorySnapshot{}))
		})
	}
}

// TestSanitizeDownload_ReftableListingFailureFailsClosed rejects a repository
// whose replacement refs cannot be listed, before any ref is modified.
func TestSanitizeDownload_ReftableListingFailureFailsClosed(t *testing.T) {
	r := newReftableRepo(t)
	r.commitFile("file.txt", "original")
	installGitShim(t, "for a in \"$@\"; do\n  if [ \"$a\" = replace ]; then exit 2; fi\ndone")

	require.Error(t, sanitizeDownload(r.dir, RepositorySnapshot{}))
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

	require.Error(t, sanitizeDownload(r.dir, RepositorySnapshot{}))
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

			err := sanitizeDownload(root, RepositorySnapshot{})
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

			err := sanitizeDownload(r.dir, RepositorySnapshot{})
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

// TestSafeDownload_ReconcilesStoreShallowBoundaries proves a module store's
// shallow boundary round-trips from the trusted snapshot, an agent-added
// cutoff is rejected, and a store the snapshot does not name loses its
// boundary.
func TestSafeDownload_ReconcilesStoreShallowBoundaries(t *testing.T) {
	newSetup := func(t *testing.T) (*gitRepo, string) {
		dep := newGitRepo(t)
		head := dep.commitFile("dep.txt", "dep")
		dep.commitFile("dep2.txt", "dep2")
		super := newGitRepo(t)
		super.commitFile("file.txt", "x")
		super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "dep")
		super.git("commit", "--quiet", "-m", "add dep")
		// A shallow submodule store: the boundary names the checked-out commit.
		writeFile(t, super.dir, ".git/modules/dep/shallow", head+"\n")
		return super, head
	}

	t.Run("trusted boundary round-trips", func(t *testing.T) {
		super, head := newSetup(t)
		snapshot, err := CaptureRepositorySnapshot(super.dir)
		require.NoError(t, err)
		require.True(t, snapshot.stores[".git/modules/dep"].shallow.Present,
			"a store boundary must be captured")
		serveDownloadFrom(t, super.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))
		data, err := os.ReadFile(filepath.Join(localDir, ".git", "modules", "dep", "shallow"))
		require.NoError(t, err)
		assert.Equal(t, head+"\n", string(data))
	})

	t.Run("agent-added cutoff is rejected", func(t *testing.T) {
		super, _ := newSetup(t)
		snapshot, err := CaptureRepositorySnapshot(super.dir)
		require.NoError(t, err)
		// The sandbox adds a boundary commit the host never trusted.
		other := newGitRepo(t)
		otherHead := other.commitFile("other.txt", "other")
		writeFile(t, super.dir, ".git/modules/dep/shallow", otherHead+"\n")
		serveDownloadFrom(t, super.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		err = SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
		assert.Contains(t, err.Error(), "shallow boundary")
		assert.NoDirExists(t, localDir)
	})

	t.Run("store initialized during the run is rejected, not corrupted", func(t *testing.T) {
		dep := newGitRepo(t)
		dep.commitFile("dep.txt", "dep")
		dep.commitFile("dep2.txt", "dep2")
		super := newGitRepo(t)
		super.commitFile("file.txt", "x")
		snapshot, err := CaptureRepositorySnapshot(super.dir)
		require.NoError(t, err)
		require.Empty(t, snapshot.stores, "the submodule is not initialized on the host")

		// The agent initializes the submodule shallowly during the run. A
		// module store is a bare-style git dir, so the shallow boundary
		// lands directly inside it.
		store := filepath.Join(super.dir, ".git", "modules", "dep")
		require.NoError(t, os.MkdirAll(filepath.Join(super.dir, ".git", "modules"), 0o755))
		_, err = runGit(t, filepath.Join(super.dir, ".git", "modules"), super.env,
			"clone", "--quiet", "--bare", "--depth", "1", "file://"+dep.dir, "dep")
		require.NoError(t, err)
		require.FileExists(t, filepath.Join(store, "shallow"), "fixture must be genuinely shallow")
		writeFile(t, super.dir, "dep/.git", "gitdir: ../.git/modules/dep\n")

		serveDownloadFrom(t, super.dir)
		localDir := filepath.Join(t.TempDir(), "repo")
		err = SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
		assert.Contains(t, err.Error(), "shallow boundary")
		assert.NoDirExists(t, localDir)
	})
}

// TestSanitizeDownload_RejectsBareShapedDirectory rejects an agent-returned
// Git-shaped directory wherever it sits — outside .git, inside .git, or
// named with a lowercase head — because Git adopts it as a repository and
// its executable config would reach a post-script that runs Git inside it.
func TestSanitizeDownload_RejectsBareShapedDirectory(t *testing.T) {
	for _, tc := range []struct{ name, dir, head string }{
		{"outside git", "vendor/payload", "HEAD"},
		{"inside git", ".git/payload", "HEAD"},
		{"lowercase head", "vendor/lower", "head"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newGitRepo(t)
			r.commitFile("file.txt", "x")
			bare := filepath.Join(r.dir, filepath.FromSlash(tc.dir))
			require.NoError(t, os.MkdirAll(filepath.Join(bare, "objects"), 0o755))
			require.NoError(t, os.MkdirAll(filepath.Join(bare, "refs"), 0o755))
			writeFile(t, r.dir, tc.dir+"/"+tc.head, "ref: refs/heads/main\n")
			writeFile(t, r.dir, tc.dir+"/config", "[core]\n\tfsmonitor = /tmp/pwn\n")
			writeFile(t, r.dir, tc.dir+"/hooks/pre-commit", "#!/bin/sh\n")

			err := sanitizeDownload(r.dir, RepositorySnapshot{})
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
			assert.Contains(t, err.Error(), "bare-style Git directory")
		})
	}

	t.Run("worktree symlink to it", func(t *testing.T) {
		r := newGitRepo(t)
		r.commitFile("file.txt", "x")
		bare := filepath.Join(r.dir, ".git", "payload")
		require.NoError(t, os.MkdirAll(filepath.Join(bare, "objects"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(bare, "refs"), 0o755))
		writeFile(t, r.dir, ".git/payload/HEAD", "ref: refs/heads/main\n")
		require.NoError(t, os.Symlink("../.git/payload", filepath.Join(r.dir, "evil")))

		err := sanitizeDownload(r.dir, RepositorySnapshot{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bare-style Git directory")
	})
}

// TestSanitizeDownload_NestedCheckoutKeepsBoundary locks the regression a
// review found: a nested checkout's shallow file is ordinary worktree
// content and must survive the walk, not be stripped as an untrusted module
// store boundary. A host-present nested checkout is rejected before upload,
// so the nested checkout here is agent-added.
func TestSanitizeDownload_NestedCheckoutKeepsBoundary(t *testing.T) {
	r := newGitRepo(t)
	human := r.commitFile("file.txt", "x")
	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	nested := filepath.Join(r.dir, "vendor", "x", ".git")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	writeFile(t, r.dir, "vendor/x/.git/HEAD", "ref: refs/heads/main\n")
	writeFile(t, r.dir, "vendor/x/.git/shallow", human+"\n")

	require.NoError(t, sanitizeDownload(r.dir, snapshot))
	assert.FileExists(t, filepath.Join(nested, "shallow"), "a nested checkout keeps its boundary")
}

// TestCaptureRepositorySnapshot_RejectsBareFixtureAndForeignGitfile fails
// before upload for the layouts the download walk rejects: a committed bare
// fixture such as testdata/x.git, and a gitfile whose target lies outside
// the target's own module store.
func TestCaptureRepositorySnapshot_RejectsBareFixtureAndForeignGitfile(t *testing.T) {
	t.Run("bare fixture in a checkout", func(t *testing.T) {
		r := newGitRepo(t)
		r.commitFile("file.txt", "x")
		bare := filepath.Join(r.dir, "testdata", "x.git")
		require.NoError(t, os.MkdirAll(filepath.Join(bare, "objects"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(bare, "refs"), 0o755))
		writeFile(t, r.dir, "testdata/x.git/HEAD", "ref: refs/heads/main\n")

		_, err := CaptureRepositorySnapshot(r.dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bare-style Git directory")
	})

	t.Run("bare fixture in a plain target", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "plain.txt", "x")
		bare := filepath.Join(root, "testdata", "x.git")
		require.NoError(t, os.MkdirAll(filepath.Join(bare, "objects"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(bare, "refs"), 0o755))
		writeFile(t, root, "testdata/x.git/HEAD", "ref: refs/heads/main\n")

		_, err := CaptureRepositorySnapshot(root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bare-style Git directory")
	})

	t.Run("foreign gitfile", func(t *testing.T) {
		dep := newGitRepo(t)
		dep.commitFile("dep.txt", "dep")
		r := newGitRepo(t)
		r.commitFile("file.txt", "x")
		require.NoError(t, os.MkdirAll(filepath.Join(r.dir, "vendor"), 0o755))
		writeFile(t, r.dir, "vendor/dep/.git", "gitdir: "+filepath.Join(dep.dir, ".git")+"\n")

		_, err := CaptureRepositorySnapshot(r.dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "module store")
	})

	t.Run("submodule gitfile still captures", func(t *testing.T) {
		dep := newGitRepo(t)
		dep.commitFile("dep.txt", "dep")
		r := newGitRepo(t)
		r.commitFile("file.txt", "x")
		r.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "vendor/dep")
		r.git("commit", "--quiet", "-m", "add submodule")
		snapshot, err := CaptureRepositorySnapshot(r.dir)
		require.NoError(t, err)
		assert.Contains(t, snapshot.stores, ".git/modules/vendor/dep")
	})
}

// TestCaptureRepositorySnapshot_RejectsNestedCheckoutInCheckoutTarget fails
// before upload when a checkout target already contains a nested .git
// directory, like a vendored clone: the round trip would strip its config
// and corrupt its shallow state, so it gets the same early rejection a
// plain target gets. Gitfile submodules are the supported layout.
func TestCaptureRepositorySnapshot_RejectsNestedCheckoutInCheckoutTarget(t *testing.T) {
	dep := newGitRepo(t)
	dep.commitFile("dep.txt", "dep")
	super := newGitRepo(t)
	super.commitFile("file.txt", "x")
	require.NoError(t, os.MkdirAll(filepath.Join(super.dir, "vendor"), 0o755))
	clone := filepath.Join(super.dir, "vendor", "x")
	_, err := runGit(t, filepath.Join(super.dir, "vendor"), super.env, "clone", "--quiet", "file://"+dep.dir, "x")
	require.NoError(t, err)
	require.DirExists(t, filepath.Join(clone, ".git"), "fixture must contain a nested checkout")

	_, err = CaptureRepositorySnapshot(super.dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested Git directory")

	// A gitfile submodule in the same position still captures.
	dep2 := newGitRepo(t)
	dep2.commitFile("dep.txt", "dep")
	super2 := newGitRepo(t)
	super2.commitFile("file.txt", "x")
	super2.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep2.dir, "vendor/dep")
	super2.git("commit", "--quiet", "-m", "add submodule")
	snapshot, err := CaptureRepositorySnapshot(super2.dir)
	require.NoError(t, err)
	assert.Contains(t, snapshot.stores, ".git/modules/vendor/dep")
}

// TestSanitizeDownload_RejectsUnrecognizedModuleStore proves a directory
// under modules/ that this walk does not recognize as a store cannot keep
// its metadata: git adopts a store with HEAD, objects, and refs alone (config
// is optional), so an agent-planted store would otherwise keep its hooks
// until a host `git submodule update --init` runs them. Store names derive
// from the submodule path and may contain slashes.
func TestSanitizeDownload_RejectsUnrecognizedModuleStore(t *testing.T) {
	for _, name := range []string{"dep", "a/b", "a/b/c", "vendor/dep", "a/.git/b", "a/.GIT/b"} {
		t.Run(name, func(t *testing.T) {
			r := newGitRepo(t)
			human := r.commitFile("file.txt", "x")
			writeFile(t, r.dir, ".gitmodules", "[submodule \"dep\"]\n\tpath = dep\n\turl = https://example.invalid/dep.git\n")
			writeFile(t, r.dir, ".git/modules/"+name+"/HEAD", "ref: refs/heads/main\n")
			writeFile(t, r.dir, ".git/modules/"+name+"/commondir", "../..\n")
			writeFile(t, r.dir, ".git/modules/"+name+"/objects/info/exclude", "")
			writeFile(t, r.dir, ".git/modules/"+name+"/refs/heads/main", human+"\n")
			writeFile(t, r.dir, ".git/modules/"+name+"/hooks/post-checkout", "#!/bin/sh\n")

			err := sanitizeDownload(r.dir, RepositorySnapshot{})
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
			assert.Contains(t, err.Error(), "module store layout")
		})
	}
}

// TestSanitizeDownload_RemovesLegacyRemoteFiles removes the legacy remotes/
// and branches/ files, which define remotes for names missing from the
// rebuilt config.
func TestSanitizeDownload_RemovesLegacyRemoteFiles(t *testing.T) {
	r := newGitRepo(t)
	r.commitFile("file.txt", "x")
	writeFile(t, r.dir, ".git/remotes/origin", "https://example.invalid/origin.git\n")
	writeFile(t, r.dir, ".git/branches/main", "https://example.invalid/main.git\n")

	require.NoError(t, sanitizeDownload(r.dir, RepositorySnapshot{}))
	assert.NoFileExists(t, filepath.Join(r.dir, ".git", "remotes"))
	assert.NoFileExists(t, filepath.Join(r.dir, ".git", "branches"))
}

// TestSafeDownload_PreservesSubmoduleActivation proves an initialised
// submodule is still initialised after the round trip: the store and gitfile
// survive sanitization, and the restored activation keys keep git from
// reporting the submodule as inactive.
func TestSafeDownload_PreservesSubmoduleActivation(t *testing.T) {
	dep := newGitRepo(t)
	dep.commitFile("dep.txt", "dep")
	r := newGitRepo(t)
	r.commitFile("file.txt", "x")
	r.git("-c", "protocol.file.allow=always", "submodule", "add", "--quiet", "file://"+dep.dir, "dep")
	r.git("commit", "--quiet", "-m", "add dep")
	require.FileExists(t, filepath.Join(r.dir, "dep", ".git"), "submodule gitfile must exist")

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

	out, err := runGit(t, localDir, gitTestEnv(t.TempDir()), "submodule", "status")
	require.NoError(t, err, out)
	assert.Contains(t, out, "dep")
	assert.NotContains(t, out, "-", "submodule must not come back inactive")
}

// TestSanitizeDownload_RejectsReftableWorktreeMetadata rejects registered
// worktree metadata for reftable, where extra ref stores could live.
func TestSanitizeDownload_RejectsReftableWorktreeMetadata(t *testing.T) {
	r := newReftableRepo(t)
	r.commitFile("file.txt", "original")
	writeFile(t, r.dir, ".git/worktrees/wt/gitdir", "../outside\n")
	sentinel := filepath.Join(t.TempDir(), "outside")
	require.NoFileExists(t, sentinel)

	err := sanitizeDownload(r.dir, RepositorySnapshot{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worktree")
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	assert.NoFileExists(t, sentinel, "worktree manifest must not be followed")
}

// TestSanitizeDownload_RejectsWorktreeMetadata rejects registered worktree
// metadata in files-based git dirs too: the private directories under
// .git/worktrees/ carry hooks, per-worktree config, and commondir redirects
// that the walk does not sanitize.
func TestSanitizeDownload_RejectsWorktreeMetadata(t *testing.T) {
	r := newGitRepo(t)
	r.commitFile("file.txt", "original")
	writeFile(t, r.dir, ".git/worktrees/wt/gitdir", "../outside\n")
	sentinel := filepath.Join(t.TempDir(), "outside")
	require.NoFileExists(t, sentinel)

	err := sanitizeDownload(r.dir, RepositorySnapshot{})
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

	require.NoError(t, sanitizeDownload(root.dir, RepositorySnapshot{}))

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
			require.NoError(t, sanitizeDownload(root, RepositorySnapshot{}))
			got, err := os.ReadFile(filepath.Join(root, ".git", "packed-refs"))
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
		})
	}

	t.Run("orphan peeled line is preserved", func(t *testing.T) {
		root := t.TempDir()
		content := "^" + testOID + "\n"
		writeFile(t, root, ".git/packed-refs", content)
		require.NoError(t, sanitizeDownload(root, RepositorySnapshot{}))
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

		require.NoError(t, sanitizeDownload(root, RepositorySnapshot{}))
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

		require.NoError(t, sanitizeDownload(root, RepositorySnapshot{}))
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

		err := sanitizeDownload(root, RepositorySnapshot{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "packed-refs")
	})

	t.Run("directory is rejected", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "packed-refs"), 0o755))
		err := sanitizeDownload(root, RepositorySnapshot{})
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
			err := sanitizeDownload(root, RepositorySnapshot{})
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
		require.Error(t, sanitizeDownload(cloneDir, RepositorySnapshot{}), "alternate object stores must be rejected")
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
	writeFile(t, root, ".git/modules/other/HEAD", "ref: refs/heads/main\n")
	writeFile(t, root, ".git/modules/other/objects/info/exclude", "")
	writeFile(t, root, ".git/modules/other/refs/heads/main", testOID+"\n")
	writeFile(t, root, ".git/logs/wt/HEAD", "ref: refs/heads/wt\n")

	require.NoError(t, sanitizeDownload(root, RepositorySnapshot{}))

	_, err := os.Lstat(filepath.Join(root, "vendor", "dep", ".git", "refs", "replace"))
	assert.True(t, os.IsNotExist(err), "nested replace refs must be filtered")
	data, err := os.ReadFile(filepath.Join(root, "vendor", "dep", ".git", "config"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "repositoryformatversion", "nested config must be rebuilt")

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
	require.NoError(t, sanitizeDownload(r.dir, RepositorySnapshot{}))
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
			require.NoError(t, sanitizeDownload(r.dir, RepositorySnapshot{}))
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

	require.NoError(t, sanitizeDownload(r.dir, RepositorySnapshot{}))
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

	err := sanitizeDownload(dir, RepositorySnapshot{})
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
			RepositorySnapshot{isCheckout: true})
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
		RepositorySnapshot{isCheckout: true}))

	got, err := runGit(t, localDir, r.env, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, head, got)
}

// TestSafeDownload_PlainInputMayPublishCheckout keeps the plain-to-checkout
// path: without isCheckout, a plain host input may come back as a valid
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
			installGitQueryShim(t, binDir, gitBody)
			t.Setenv("PATH", binDir+string(filepath.ListSeparator)+os.Getenv("PATH"))
			return
		}
		// No git at all: config sanitization fails closed before the layout
		// query, so only publication must be checked.
		t.Setenv("PATH", binDir)
	}

	cases := []struct {
		name    string
		gitBody string
		want    string
	}{
		{"status failure", "exit 129", "checking git checkout"},
		{"malformed output", "printf 'junk\\n'; exit 0", "git directory resolved to"},
		{"git unavailable", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installDownloadAndGit(t, tc.gitBody)
			localDir := filepath.Join(t.TempDir(), "repo")
			err := SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir,
				RepositorySnapshot{})
			require.Error(t, err)
			if tc.want != "" {
				assert.Contains(t, err.Error(), tc.want)
			}
			assert.True(t, errors.Is(err, ErrRepositoryValidation), "want ErrRepositoryValidation, got %v", err)
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
				trusted = RepositorySnapshot{shallow: shallowState{Present: true, Data: []byte(oid(tc.trusted) + "\n")}}
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
			if !trusted.shallow.Present {
				assert.True(t, os.IsNotExist(err), "untrusted shallow metadata must not be published")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, trusted.shallow.Data, got)
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

	trusted := RepositorySnapshot{shallow: shallowState{Present: true, Data: []byte(middle + "\n")}}
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
		installGitQueryShim(t, binDir, "exit 129")
		t.Setenv("PATH", binDir+string(filepath.ListSeparator)+os.Getenv("PATH"))

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

			trusted := RepositorySnapshot{shallow: shallowState{Present: true, Data: []byte(middle + "\n")}}
			serveDownloadFrom(t, r.dir)
			localDir := filepath.Join(t.TempDir(), "repo")
			require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, trusted))

			got, err := os.ReadFile(filepath.Join(localDir, ".git", "shallow"))
			require.NoError(t, err)
			assert.Equal(t, trusted.shallow.Data, got)

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
		RepositorySnapshot{shallow: shallowState{Present: true, Data: []byte("0123456789012345678901234567890123456789\n")}})
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

	err := sanitizeDownload(dir, RepositorySnapshot{})
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
