package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCaptureRepositorySnapshot captures the narrow trusted state and returns a
// zero snapshot for a plain directory.
func TestCaptureRepositorySnapshot(t *testing.T) {
	requireGit(t)
	r := newGitRepo(t)
	r.commitFile("f.txt", "x")
	r.git("config", "remote.origin.url", "https://example.invalid/org/repo")

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	assert.True(t, snapshot.isCheckout)
	assert.Equal(t, "0", snapshot.format.version)
	assert.Equal(t, "sha1", snapshot.format.objectFormat)
	assert.Equal(t, "files", snapshot.format.refStorage)
	keys := make([]string, 0, len(snapshot.config))
	for _, entry := range snapshot.config {
		keys = append(keys, entry.Key)
	}
	assert.Contains(t, strings.Join(keys, ","), "user.name")
	assert.Contains(t, strings.Join(keys, ","), "remote.origin.url")

	plain, err := CaptureRepositorySnapshot(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, RepositorySnapshot{}, plain)
}

// TestCaptureRepositorySnapshot_RejectsNestedCheckoutInPlainRoot refuses to
// snapshot a plain target that already contains a nested checkout, while
// keeping empty plain targets and normal root-checkout submodules working.
func TestCaptureRepositorySnapshot_RejectsNestedCheckoutInPlainRoot(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	out, err := runGit(t, nested, gitTestEnv(t.TempDir()), "init", "--quiet", "--initial-branch=main", ".")
	require.NoError(t, err, out)

	_, err = CaptureRepositorySnapshot(root)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	assert.Contains(t, err.Error(), "contains nested Git metadata")

	target := t.TempDir()
	symlinkedNested := filepath.Join(target, "nested")
	require.NoError(t, os.MkdirAll(symlinkedNested, 0o755))
	out, err = runGit(t, symlinkedNested, gitTestEnv(t.TempDir()), "init", "--quiet", "--initial-branch=main", ".")
	require.NoError(t, err, out)
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(target, link))
	_, err = CaptureRepositorySnapshot(link)
	require.Error(t, err, "a symlinked plain root must not hide nested Git metadata")
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	assert.Contains(t, err.Error(), "contains nested Git metadata")

	snapshot, err := CaptureRepositorySnapshot(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, RepositorySnapshot{}, snapshot, "an empty plain target stays a zero snapshot")

	dep := newGitRepo(t)
	dep.commitFile("dep.txt", "dep")
	super := newGitRepo(t)
	super.commitFile("file.txt", "content")
	super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "vendor/dep")
	super.git("commit", "--quiet", "-m", "add submodule")
	snapshot, err = CaptureRepositorySnapshot(super.dir)
	require.NoError(t, err, "a root checkout's own submodule is not a nested plain checkout")
	assert.True(t, snapshot.isCheckout)
	assert.Contains(t, snapshot.stores, ".git/modules/vendor/dep")
}

// TestCaptureRepositorySnapshot_ErrorBranches fails closed on an unsafe shallow
// marker and an unknown repository format.
func TestCaptureRepositorySnapshot_ErrorBranches(t *testing.T) {
	requireGit(t)

	t.Run("non-regular shallow", func(t *testing.T) {
		r := newGitRepo(t)
		r.commitFile("f.txt", "x")
		require.NoError(t, os.Mkdir(filepath.Join(r.dir, ".git", "shallow"), 0o755))
		_, err := CaptureRepositorySnapshot(r.dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a regular file")
	})

	t.Run("oversized shallow", func(t *testing.T) {
		r := newGitRepo(t)
		r.commitFile("f.txt", "x")
		require.NoError(t, os.WriteFile(filepath.Join(r.dir, ".git", "shallow"), bytes.Repeat([]byte("a"), maxShallowBytes+1), 0o644))
		_, err := CaptureRepositorySnapshot(r.dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "larger than")
	})

	t.Run("unsupported format", func(t *testing.T) {
		path := writeFile(t, t.TempDir(), "config", "[core]\n\trepositoryformatversion = 1\n[extensions]\n\trefStorage = fancy\n")
		_, err := captureGitConfigFormat(t.TempDir(), path)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	})

	t.Run("git unavailable", func(t *testing.T) {
		path := writeFile(t, t.TempDir(), "config", "[core]\n	repositoryformatversion = 0\n")
		t.Setenv("PATH", "")
		_, err := captureGitConfigFormat(t.TempDir(), path)
		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrRepositoryRejected), "a missing Git is not a content rejection")
	})
}

// TestCaptureRepositorySnapshot_ShallowBoundaryAndLayout covers the shallow
// boundary plus the layout and failure cases around it: inherited selectors,
// empty markers, linked worktrees, plain roots, bare roots, and unreadable or
// malformed metadata.
func TestCaptureRepositorySnapshot_ShallowBoundaryAndLayout(t *testing.T) {
	requireGit(t)
	newShallowTestRepo := func(t *testing.T) string {
		t.Helper()
		r := newGitRepo(t)
		for _, name := range []string{"one", "two", "three"} {
			r.commitFile(name+".txt", name)
		}
		return r.dir
	}
	gitAt := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		out, err := runGit(t, dir, gitTestEnv(t.TempDir()), args...)
		require.NoErrorf(t, err, "git %v: %s", args, out)
		return out
	}

	t.Run("full history has no marker", func(t *testing.T) {
		snapshot, err := CaptureRepositorySnapshot(newShallowTestRepo(t))
		require.NoError(t, err)
		assert.False(t, snapshot.shallow.Present)
		assert.Nil(t, snapshot.shallow.Data)
		assert.True(t, snapshot.isCheckout)
	})

	t.Run("shallow clone is captured and disappears after unshallow", func(t *testing.T) {
		src := newShallowTestRepo(t)
		clone := filepath.Join(t.TempDir(), "clone")
		gitAt(t, t.TempDir(), "clone", "--quiet", "--depth=1", "file://"+src, clone)

		want, err := os.ReadFile(filepath.Join(clone, ".git", "shallow"))
		require.NoError(t, err)
		snapshot, err := CaptureRepositorySnapshot(clone)
		require.NoError(t, err)
		require.True(t, snapshot.shallow.Present)
		assert.Equal(t, want, snapshot.shallow.Data)
		assert.True(t, snapshot.isCheckout)
		assert.Equal(t, "1", gitAt(t, clone, "rev-list", "--count", "HEAD"))

		// A pre-script may legitimately unshallow the host checkout before
		// upload; the captured state must then carry no boundary.
		gitAt(t, clone, "fetch", "--quiet", "--unshallow")
		snapshot, err = CaptureRepositorySnapshot(clone)
		require.NoError(t, err)
		assert.False(t, snapshot.shallow.Present)
		assert.True(t, snapshot.isCheckout)
		assert.Equal(t, "3", gitAt(t, clone, "rev-list", "--count", "HEAD"))
	})

	t.Run("inherited selectors cannot choose the shallow boundary", func(t *testing.T) {
		decoySrc := newShallowTestRepo(t)
		require.NoError(t, os.WriteFile(filepath.Join(decoySrc, "decoy.txt"), []byte("decoy\n"), 0o644))
		gitAt(t, decoySrc, "add", "decoy.txt")
		gitAt(t, decoySrc, "commit", "--quiet", "-m", "decoy")
		decoy := filepath.Join(t.TempDir(), "decoy")
		gitAt(t, t.TempDir(), "clone", "--quiet", "--depth=1", "file://"+decoySrc, decoy)
		decoyMarker, err := os.ReadFile(filepath.Join(decoy, ".git", "shallow"))
		require.NoError(t, err)

		trusted := filepath.Join(t.TempDir(), "trusted")
		gitAt(t, t.TempDir(), "clone", "--quiet", "--depth=1", "file://"+newShallowTestRepo(t), trusted)
		trustedMarker, err := os.ReadFile(filepath.Join(trusted, ".git", "shallow"))
		require.NoError(t, err)
		require.NotEqual(t, decoyMarker, trustedMarker,
			"fixtures must have distinct boundaries for this test to discriminate")

		t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
		t.Setenv("GIT_WORK_TREE", decoy)
		t.Setenv("GIT_COMMON_DIR", filepath.Join(decoy, ".git"))

		snapshot, err := CaptureRepositorySnapshot(trusted)
		require.NoError(t, err)
		require.True(t, snapshot.shallow.Present)
		assert.Equal(t, trustedMarker, snapshot.shallow.Data,
			"inherited selectors must not substitute another repository's boundary")
		assert.True(t, snapshot.isCheckout)

		// Unshallowing the trusted checkout must clear its boundary even while
		// the inherited selectors name a shallow repository.
		gitAt(t, trusted, "fetch", "--quiet", "--unshallow")
		snapshot, err = CaptureRepositorySnapshot(trusted)
		require.NoError(t, err)
		assert.False(t, snapshot.shallow.Present)
		assert.Nil(t, snapshot.shallow.Data)
		assert.True(t, snapshot.isCheckout)
	})

	t.Run("empty marker is distinct from absence", func(t *testing.T) {
		repo := newShallowTestRepo(t)
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "shallow"), nil, 0o644))
		snapshot, err := CaptureRepositorySnapshot(repo)
		require.NoError(t, err)
		assert.True(t, snapshot.shallow.Present)
		assert.Empty(t, snapshot.shallow.Data)
		assert.True(t, snapshot.isCheckout)
	})

	t.Run("linked worktree gitfile is unsupported", func(t *testing.T) {
		repo := newShallowTestRepo(t)
		wt := filepath.Join(t.TempDir(), "wt")
		gitAt(t, repo, "worktree", "add", "--quiet", wt, "-b", "wt-branch")

		snapshot, err := CaptureRepositorySnapshot(wt)
		require.Error(t, err)
		assert.Equal(t, RepositorySnapshot{}, snapshot)
	})

	t.Run("plain directory without .git is accepted", func(t *testing.T) {
		snapshot, err := CaptureRepositorySnapshot(t.TempDir())
		require.NoError(t, err)
		assert.Equal(t, RepositorySnapshot{}, snapshot)
	})

	t.Run("plain directory does not capture a parent shallow checkout", func(t *testing.T) {
		src := newShallowTestRepo(t)
		parent := filepath.Join(t.TempDir(), "parent")
		gitAt(t, t.TempDir(), "clone", "--quiet", "--depth=1", "file://"+src, parent)
		plain := filepath.Join(parent, "plain")
		require.NoError(t, os.Mkdir(plain, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(plain, "file.txt"), []byte("content\n"), 0o644))

		snapshot, err := CaptureRepositorySnapshot(plain)
		require.NoError(t, err)
		assert.Equal(t, RepositorySnapshot{}, snapshot,
			"plain input must not inherit the parent checkout's shallow boundary")
	})

	t.Run("bare repository root is rejected", func(t *testing.T) {
		bare := filepath.Join(t.TempDir(), "bare.git")
		gitAt(t, t.TempDir(), "init", "--quiet", "--bare", bare)

		snapshot, err := CaptureRepositorySnapshot(bare)
		require.Error(t, err)
		assert.Equal(t, RepositorySnapshot{}, snapshot)
	})

	t.Run("broken git metadata fails closed", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
		_, err := CaptureRepositorySnapshot(dir)
		require.Error(t, err)
	})

	t.Run("non-regular marker is rejected", func(t *testing.T) {
		repo := newShallowTestRepo(t)
		require.NoError(t, os.Mkdir(filepath.Join(repo, ".git", "shallow"), 0o755))
		_, err := CaptureRepositorySnapshot(repo)
		require.Error(t, err)
	})

	t.Run("non-directory repo path fails closed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "repo")
		require.NoError(t, os.WriteFile(path, []byte("x\n"), 0o644))
		_, err := CaptureRepositorySnapshot(path)
		require.Error(t, err)
	})

	t.Run("unreadable marker fails closed", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root bypasses permission checks")
		}
		repo := newShallowTestRepo(t)
		marker := filepath.Join(repo, ".git", "shallow")
		require.NoError(t, os.WriteFile(marker, []byte("0123456789012345678901234567890123456789\n"), 0o644))
		require.NoError(t, os.Chmod(marker, 0o000))
		t.Cleanup(func() { _ = os.Chmod(marker, 0o644) })

		_, err := CaptureRepositorySnapshot(repo)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading git shallow metadata")
	})
}

// TestWriteGitConfig_RoundTrip writes values with native Git, keeps their
// order, and refuses a non-regular config path.
func TestWriteGitConfig_RoundTrip(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	format := gitConfigFormat{version: "1", objectFormat: "sha256", refStorage: "files"}
	entries := []configEntry{
		{Key: "user.name", Value: `a"b\c`},
		{Key: "remote.origin.url", Value: "https://example.invalid/one"},
		{Key: "remote.origin.url", Value: "https://example.invalid/two"},
	}
	require.NoError(t, writeGitConfig(t.TempDir(), path, format, entries))

	getAll := func(key string) []string {
		out, err := runGit(t, dir, gitTestEnv(t.TempDir()), "config", "--file", path, "--get-all", key)
		require.NoError(t, err, out)
		return strings.Split(out, "\n")
	}
	assert.Equal(t, []string{`a"b\c`}, getAll("user.name"))
	assert.Equal(t, []string{"https://example.invalid/one", "https://example.invalid/two"}, getAll("remote.origin.url"))
	assert.Equal(t, []string{"sha256"}, getAll("extensions.objectFormat"))

	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o755))
	err := writeGitConfig(t.TempDir(), path, format, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")
}

// TestCaptureTrustedConfig_CapturesEmptyValuesAndFailsClosedOnGitErrors
// captures an explicit empty value — it is a real Git value with real
// precedence — and reports a Git query failure.
func TestCaptureTrustedConfig_CapturesEmptyValuesAndFailsClosedOnGitErrors(t *testing.T) {
	requireGit(t)

	path := writeFile(t, t.TempDir(), "config", "[core]\n\trepositoryformatversion = 0\n[user]\n\tname =\n")
	entries, err := captureTrustedConfig(t.TempDir(), t.TempDir(), path)
	require.NoError(t, err)
	assert.Equal(t, []configEntry{{Key: "user.name", Value: ""}}, entries,
		"an empty value is a real value and must be captured")

	t.Setenv("PATH", "")
	_, err = captureTrustedConfig(t.TempDir(), t.TempDir(), path)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrRepositoryRejected), "a missing Git is not a content rejection")
}

// entriesFor returns the captured values for one trusted key.
func entriesFor(entries []configEntry, key string) []string {
	var values []string
	for _, entry := range entries {
		if entry.Key == key {
			values = append(values, entry.Value)
		}
	}
	return values
}

// TestCaptureRepositorySnapshot_ReftableCheckout pins the reftable capture
// invariants in-package: the internal/cli tests see only exported fields and
// cannot assert them directly.
func TestCaptureRepositorySnapshot_ReftableCheckout(t *testing.T) {
	requireReftableSanitization(t)

	repo := newReftableRepo(t)
	repo.commitFile("f.txt", "x")
	snapshot, err := CaptureRepositorySnapshot(repo.dir)
	require.NoError(t, err)
	assert.True(t, snapshot.isCheckout, "a reftable checkout must capture as a checkout")
	assert.Equal(t, "reftable", snapshot.format.refStorage)
	assert.False(t, snapshot.shallow.Present)
}

// TestCaptureRepositorySnapshot_SubmoduleActivation captures the keys that
// keep an initialised submodule initialised, and fails closed on an
// executable transport helper in a submodule destination.
func TestCaptureRepositorySnapshot_SubmoduleActivation(t *testing.T) {
	r := newGitRepo(t)
	r.commitFile("file.txt", "x")
	r.git("config", "submodule.dep.url", "https://example.invalid/dep.git")
	r.git("config", "submodule.dep.active", "true")
	r.git("config", "submodule.dep.update", "none")

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	values := make(map[string]string)
	for _, entry := range snapshot.config {
		values[strings.ToLower(entry.Key)] = entry.Value
	}
	assert.Equal(t, "https://example.invalid/dep.git", values["submodule.dep.url"])
	assert.Equal(t, "true", values["submodule.dep.active"])
	assert.NotContains(t, values, "submodule.dep.update", "non-activation submodule settings stay dropped")

	r2 := newGitRepo(t)
	r2.commitFile("file.txt", "x")
	r2.git("config", "submodule.dep.url", "ext::sh -c id")
	_, err = CaptureRepositorySnapshot(r2.dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "executable transport")
}

// TestCaptureRepositorySnapshot_SubmoduleActiveBooleans proves activation
// booleans survive capture in every Git encoding: a bare key means true and
// an empty value means false, so a deliberately inactive submodule stays
// inactive instead of being reactivated by its restored URL.
func TestCaptureRepositorySnapshot_SubmoduleActiveBooleans(t *testing.T) {
	r := newGitRepo(t)
	r.commitFile("file.txt", "x")
	r.git("config", "submodule.dep.url", "https://example.invalid/dep.git")
	r.git("config", "submodule.dep.active", "")
	// A bare activation key means true.
	f, err := os.OpenFile(filepath.Join(r.dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("[submodule \"on\"]\n\turl = https://example.invalid/on.git\n\tactive\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	values := make(map[string]string)
	for _, entry := range snapshot.config {
		values[strings.ToLower(entry.Key)] = entry.Value
	}
	assert.Equal(t, "false", values["submodule.dep.active"], "empty activation must stay false")
	assert.Equal(t, "true", values["submodule.on.active"], "bare activation must stay true")
}

// TestCaptureRepositorySnapshot_RejectsInRepoCAPaths proves a CA setting the
// downloaded checkout could supply fails capture: Git resolves a relative
// value against the working tree, and an absolute path can land inside the
// target repository directly or through a symlink. An absolute path outside
// the repository and an empty value are captured verbatim.
func TestCaptureRepositorySnapshot_RejectsInRepoCAPaths(t *testing.T) {
	requireGit(t)
	const caInfo = "http.https://gitlab.example.invalid/.sslCAInfo"
	const caPath = "http.https://gitlab.example.invalid/.sslCAPath"

	rejected := []struct {
		name  string
		key   string
		value func(r *gitRepo) string
		want  string
	}{
		{
			name: "relative file",
			key:  caInfo,
			value: func(r *gitRepo) string {
				writeFile(t, r.dir, "certs/ca.pem", "ca")
				return "certs/ca.pem"
			},
			want: "relative CA path",
		},
		{
			name: "relative directory",
			key:  caPath,
			value: func(r *gitRepo) string {
				writeFile(t, r.dir, "certs/ca.pem", "ca")
				return "certs"
			},
			want: "relative CA path",
		},
		{
			name:  "absolute file inside the repository",
			key:   caInfo,
			value: func(r *gitRepo) string { return writeFile(t, r.dir, "certs/ca.pem", "ca") },
			want:  "inside the target repository",
		},
		{
			name:  "absolute missing file inside the repository",
			key:   caInfo,
			value: func(r *gitRepo) string { return filepath.Join(r.dir, "certs", "missing", "ca.pem") },
			want:  "inside the target repository",
		},
		{
			name:  "repository root",
			key:   caPath,
			value: func(r *gitRepo) string { return r.dir },
			want:  "inside the target repository",
		},
		{
			name: "absolute path through a symlink into the repository",
			key:  caInfo,
			value: func(r *gitRepo) string {
				writeFile(t, r.dir, "certs/ca.pem", "ca")
				link := filepath.Join(t.TempDir(), "link")
				require.NoError(t, os.Symlink(filepath.Join(r.dir, "certs"), link))
				return filepath.Join(link, "missing.pem")
			},
			want: "inside the target repository",
		},
		{
			name: "external dangling file symlink into the repository",
			key:  caInfo,
			value: func(r *gitRepo) string {
				link := filepath.Join(t.TempDir(), "host-ca.pem")
				require.NoError(t, os.Symlink(filepath.Join(r.dir, "certs", "ca.pem"), link))
				return link
			},
			want: "inside the target repository",
		},
		{
			name: "external dangling directory symlink into the repository",
			key:  caPath,
			value: func(r *gitRepo) string {
				link := filepath.Join(t.TempDir(), "host-certs")
				require.NoError(t, os.Symlink(filepath.Join(r.dir, "certs"), link))
				return filepath.Join(link, "ca.pem")
			},
			want: "inside the target repository",
		},
		{
			name: "relative dangling symlink into the repository",
			key:  caInfo,
			value: func(r *gitRepo) string {
				dir := t.TempDir()
				rel, err := filepath.Rel(dir, filepath.Join(r.dir, "certs", "ca.pem"))
				require.NoError(t, err)
				link := filepath.Join(dir, "host-ca.pem")
				require.NoError(t, os.Symlink(rel, link))
				return link
			},
			want: "inside the target repository",
		},
		{
			name: "path that enters the repository and leaves through dotdot",
			key:  caInfo,
			value: func(r *gitRepo) string {
				return filepath.Join(r.dir, "link") + "/../../ca.pem"
			},
			want: "inside the target repository",
		},
		{
			name: "symlink loop",
			key:  caInfo,
			value: func(r *gitRepo) string {
				dir := t.TempDir()
				require.NoError(t, os.Symlink(filepath.Join(dir, "b"), filepath.Join(dir, "a")))
				require.NoError(t, os.Symlink(filepath.Join(dir, "a"), filepath.Join(dir, "b")))
				return filepath.Join(dir, "a")
			},
			want: "cannot be established",
		},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			r := newGitRepo(t)
			r.commitFile("file.txt", "x")
			r.git("config", tc.key, tc.value(r))
			_, err := CaptureRepositorySnapshot(r.dir)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	t.Run("module store", func(t *testing.T) {
		dep := newGitRepo(t)
		dep.commitFile("dep.txt", "dependency")
		super := newGitRepo(t)
		super.commitFile("file.txt", "content")
		super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "vendor/dep")
		super.gitAt(filepath.Join(super.dir, "vendor", "dep"), "config", caInfo, "certs/ca.pem")
		_, err := CaptureRepositorySnapshot(super.dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "relative CA path")
	})

	t.Run("differently-cased alias of the repository", func(t *testing.T) {
		r := newGitRepo(t)
		r.commitFile("file.txt", "x")
		alias := filepath.Join(filepath.Dir(r.dir), strings.ToUpper(filepath.Base(r.dir)))
		if alias == r.dir {
			alias = filepath.Join(filepath.Dir(r.dir), strings.ToLower(filepath.Base(r.dir)))
		}
		if _, err := os.Stat(alias); err != nil {
			t.Skip("filesystem is case-sensitive; no alias spelling of the repository exists")
		}
		r.git("config", caInfo, filepath.Join(alias, "certs", "ca.pem"))
		_, err := CaptureRepositorySnapshot(r.dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inside the target repository")
	})

	t.Run("outside the repository and empty are kept", func(t *testing.T) {
		r := newGitRepo(t)
		r.commitFile("file.txt", "x")
		outside := writeFile(t, t.TempDir(), "ca.pem", "ca")
		r.git("config", caInfo, outside)
		r.git("config", caPath, "")
		snapshot, err := CaptureRepositorySnapshot(r.dir)
		require.NoError(t, err)
		values := make(map[string]string)
		for _, entry := range snapshot.config {
			values[strings.ToLower(entry.Key)] = entry.Value
		}
		assert.Equal(t, outside, values[strings.ToLower(caInfo)], "an absolute path outside the repository is kept verbatim")
		empty, ok := values[strings.ToLower(caPath)]
		assert.True(t, ok, "an empty CA setting is kept")
		assert.Empty(t, empty)
	})
}

// TestCaptureRepositorySnapshot_RejectsWorktreeRegistry fails the run before
// upload when the root or a module store holds registered worktrees, instead
// of rejecting the download after the agent has run.
func TestCaptureRepositorySnapshot_RejectsWorktreeRegistry(t *testing.T) {
	r := newGitRepo(t)
	r.commitFile("file.txt", "x")
	require.NoError(t, os.MkdirAll(filepath.Join(r.dir, ".git", "worktrees", "wt"), 0o755))
	_, err := CaptureRepositorySnapshot(r.dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worktree")

	storeHost := newGitRepo(t)
	storeHost.commitFile("file.txt", "x")
	store := filepath.Join(storeHost.dir, ".git", "modules", "dep")
	require.NoError(t, os.MkdirAll(filepath.Join(store, "objects"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(store, "refs"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(store, "worktrees", "wt"), 0o755))
	writeFile(t, storeHost.dir, ".git/modules/dep/config", "[core]\n")
	writeFile(t, storeHost.dir, ".git/modules/dep/HEAD", "ref: refs/heads/main\n")
	_, err = CaptureRepositorySnapshot(storeHost.dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worktree")
}

// TestCaptureRepositorySnapshot_ResolvesRelativeSubmoduleDestination proves
// a relative local submodule url is resolved against the trusted checkout:
// kept verbatim it would name a path relative to the download's temporary
// directory after publication, and a module store's relative destination
// fails closed.
func TestCaptureRepositorySnapshot_ResolvesRelativeSubmoduleDestination(t *testing.T) {
	parent := t.TempDir()
	newAt := func(t *testing.T, dir string) *gitRepo {
		t.Helper()
		require.NoError(t, os.MkdirAll(dir, 0o755))
		r := &gitRepo{t: t, dir: dir, env: gitTestEnv(t.TempDir())}
		_, err := runGit(t, r.dir, r.env, "init", "--quiet", "--initial-branch=main", ".")
		require.NoError(t, err)
		r.git("config", "user.name", "Test")
		r.git("config", "user.email", "test@example.invalid")
		return r
	}
	depDir := filepath.Join(parent, "dep")
	dep := newAt(t, depDir)
	dep.commitFile("dep.txt", "dep")
	superDir := filepath.Join(parent, "super")
	super := newAt(t, superDir)
	super.commitFile("file.txt", "x")
	super.git("config", "submodule.dep.url", "../dep")
	super.git("config", "submodule.dep.active", "true")

	snapshot, err := CaptureRepositorySnapshot(superDir)
	require.NoError(t, err)
	values := make(map[string]string)
	for _, entry := range snapshot.config {
		values[strings.ToLower(entry.Key)] = entry.Value
	}
	resolved, err := filepath.EvalSymlinks(depDir)
	require.NoError(t, err)
	assert.Equal(t, resolved, values["submodule.dep.url"],
		"a relative local submodule destination must be resolved against the trusted checkout")

	// A destination that names nothing on the host fails closed.
	broken := newGitRepo(t)
	broken.commitFile("file.txt", "x")
	broken.git("config", "submodule.dep.url", "../missing")
	_, err = CaptureRepositorySnapshot(broken.dir)
	require.Error(t, err)

	// A store's own config has no owning worktree to resolve against, so a
	// relative destination there fails the capture.
	storeHost := newGitRepo(t)
	storeHost.commitFile("file.txt", "x")
	store := filepath.Join(storeHost.dir, ".git", "modules", "st")
	require.NoError(t, os.MkdirAll(filepath.Join(store, "objects"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(store, "refs"), 0o755))
	writeFile(t, storeHost.dir, ".git/modules/st/config", "[submodule \"inner\"]\n\turl = ../inner\n")
	writeFile(t, storeHost.dir, ".git/modules/st/HEAD", "ref: refs/heads/main\n")
	_, err = CaptureRepositorySnapshot(storeHost.dir)
	require.Error(t, err)
}

// TestSafeDownload_PreservesSparseCheckout proves sparse-checkout settings
// and patterns survive the round trip: the booleans are restored and the
// pattern file under .git/info/ is ordinary content the walk keeps.
func TestSafeDownload_PreservesSparseCheckout(t *testing.T) {
	r := newGitRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(r.dir, "sub"), 0o755))
	r.commitFile("sub/included.txt", "x")
	r.commitFile("excluded.txt", "x")
	r.git("sparse-checkout", "set", "sub")
	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)

	serveDownloadFrom(t, r.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

	env := gitTestEnv(t.TempDir())
	out, err := runGit(t, localDir, env, "config", "--get", "--bool", "core.sparseCheckout")
	require.NoError(t, err, out)
	assert.Equal(t, "true", strings.TrimSpace(out), "sparse checkout must stay enabled")
	require.FileExists(t, filepath.Join(localDir, ".git", "info", "sparse-checkout"))
	listing, err := runGit(t, localDir, env, "sparse-checkout", "list")
	require.NoError(t, err, listing)
	assert.Contains(t, listing, "sub")
}

// TestCaptureRepositorySnapshot_RejectsRemoteFormPartialClone fails closed
// for the remote-based form of a partial clone, which may appear without the
// extensions.partialClone marker and would come back with its lazy fetch
// broken.
func TestCaptureRepositorySnapshot_RejectsRemoteFormPartialClone(t *testing.T) {
	r := newGitRepo(t)
	r.commitFile("file.txt", "x")
	r.git("config", "remote.origin.url", "https://example.invalid/x.git")
	r.git("config", "remote.origin.promisor", "true")
	r.git("config", "remote.origin.partialclonefilter", "blob:none")

	_, err := CaptureRepositorySnapshot(r.dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "partial clone")
}

// TestCaptureRepositorySnapshot_StoreNamedObjects proves the capture walk's
// metadata-directory skip does not swallow a store literally named after one.
func TestCaptureRepositorySnapshot_StoreNamedObjects(t *testing.T) {
	r := newGitRepo(t)
	r.commitFile("file.txt", "x")
	store := filepath.Join(r.dir, ".git", "modules", "objects")
	require.NoError(t, os.MkdirAll(filepath.Join(store, "objects"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(store, "refs"), 0o755))
	writeFile(t, r.dir, ".git/modules/objects/config", "[core]\n")
	writeFile(t, r.dir, ".git/modules/objects/HEAD", "ref: refs/heads/main\n")

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	assert.Contains(t, snapshot.stores, ".git/modules/objects")
}

// TestCaptureRepositorySnapshot_AllowsNamedHelperTransport proves a named
// helper destination such as codecommit:: is captured like any other
// host-chosen destination: only the ext helper, whose address is an
// arbitrary command, is rejected.
func TestCaptureRepositorySnapshot_AllowsNamedHelperTransport(t *testing.T) {
	r := newGitRepo(t)
	r.commitFile("file.txt", "x")
	r.git("config", "remote.origin.url", "codecommit::MyRepo")
	r.git("config", "remote.origin.pushurl", "persistent-https::test.example.invalid")

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"codecommit::MyRepo"}, entriesFor(snapshot.config, "remote.origin.url"))
	assert.Equal(t, []string{"persistent-https::test.example.invalid"}, entriesFor(snapshot.config, "remote.origin.pushurl"))
}

// TestCaptureRepositorySnapshot_RejectsExecutableRemoteHelper fails clearly for
// a trusted destination that would execute a program instead of being retained.
func TestCaptureRepositorySnapshot_RejectsExecutableRemoteHelper(t *testing.T) {
	for _, key := range []string{"remote.origin.url", "remote.origin.pushurl"} {
		t.Run(key, func(t *testing.T) {
			requireGit(t)
			r := newGitRepo(t)
			r.commitFile("f.txt", "x")
			r.git("config", key, "ext::sh -c true")

			_, err := CaptureRepositorySnapshot(r.dir)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "executable transport helper")
			assert.Contains(t, err.Error(), key)
		})
	}
}

// TestUnsupportedExtensionsAreRejected rejects an extension the sanitizer
// cannot reproduce instead of silently dropping it, which would damage a
// partial clone.
// TestCaptureRepositorySnapshot_RejectsSymlinkedModuleStore proves capture
// fails closed when .git/modules is a symlink: the store configs cannot be
// captured, and silently skipping would drop the trusted submodule remotes
// from every later restore.
func TestCaptureRepositorySnapshot_RejectsSymlinkedModuleStore(t *testing.T) {
	root := newGitRepo(t)
	root.commitFile("f.txt", "x")
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root.dir, ".git", "modules")))
	_, err := CaptureRepositorySnapshot(root.dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is a symlink")
}

// TestCaptureTrustedConfig_DedupesAcrossFiles proves a tracking entry that
// appears in both config and config.worktree is captured once: replaying it
// twice would make Git apply the same refspec twice.
func TestCaptureTrustedConfig_DedupesAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "config", "[remote \"origin\"]\n\turl = https://example.com/x.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n")
	writeFile(t, dir, "config.worktree", "[remote \"origin\"]\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n")
	entries, err := captureTrustedConfig(dir, "", filepath.Join(dir, "config"), filepath.Join(dir, "config.worktree"))
	require.NoError(t, err)
	var fetches []string
	for _, entry := range entries {
		if entry.Key == "remote.origin.fetch" {
			fetches = append(fetches, entry.Value)
		}
	}
	assert.Equal(t, []string{"+refs/heads/*:refs/remotes/origin/*"}, fetches)
}

func TestUnsupportedExtensionsAreRejected(t *testing.T) {
	requireGit(t)

	t.Run("capture", func(t *testing.T) {
		r := newGitRepo(t)
		r.commitFile("f.txt", "x")
		r.git("config", "core.repositoryformatversion", "1")
		r.git("config", "extensions.partialClone", "origin")
		_, err := CaptureRepositorySnapshot(r.dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported repository extension")
	})

	t.Run("sanitize", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
		writeFile(t, root, ".git/config", "[core]\n\trepositoryformatversion = 1\n[extensions]\n\tpartialClone = origin\n")
		err := sanitizeDownload(root, RepositorySnapshot{})
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	})

	t.Run("worktree config stays supported", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n")
		writeFile(t, root, ".git/config", "[core]\n\trepositoryformatversion = 1\n[extensions]\n\tworktreeConfig = true\n")
		require.NoError(t, sanitizeDownload(root, RepositorySnapshot{}))
	})
}

// TestCaptureRepositorySnapshot_RejectsExecutableRemoteHelperOnAnyRemote fails
// clearly for an executable transport helper on a non-origin remote or a module
// store, not only origin.
func TestCaptureRepositorySnapshot_RejectsExecutableRemoteHelperOnAnyRemote(t *testing.T) {
	requireGit(t)
	t.Run("non-origin remote", func(t *testing.T) {
		r := newGitRepo(t)
		r.commitFile("f.txt", "x")
		r.git("config", "remote.upstream.url", "ext::sh -c true")
		_, err := CaptureRepositorySnapshot(r.dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "executable transport helper")
	})

	t.Run("module store remote", func(t *testing.T) {
		dep := newGitRepo(t)
		dep.commitFile("dep.txt", "dependency")
		super := newGitRepo(t)
		super.commitFile("file.txt", "content")
		super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "vendor/dep")
		super.gitAt(filepath.Join(super.dir, "vendor", "dep"), "config", "remote.origin.url", "ext::sh -c true")
		_, err := CaptureRepositorySnapshot(super.dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "executable transport helper")
	})
}

// TestCaptureRepositorySnapshot_ResolvesRelativeLocalDestination pins a relative
// local origin/push destination to its trusted absolute target, so relocation
// cannot silently point it at different content.
func TestCaptureRepositorySnapshot_ResolvesRelativeLocalDestination(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	dep := initRepoAt(t, filepath.Join(base, "dep"))
	dep.commitFile("dep.txt", "dep")
	super := initRepoAt(t, filepath.Join(base, "super"))
	super.commitFile("f.txt", "x")
	super.git("config", "remote.origin.url", "../dep")
	super.git("config", "remote.origin.pushurl", "../dep")
	want, err := filepath.EvalSymlinks(filepath.Join(base, "dep"))
	require.NoError(t, err)

	snapshot, err := CaptureRepositorySnapshot(super.dir)
	require.NoError(t, err)
	assert.Equal(t, []string{want}, entriesFor(snapshot.config, "remote.origin.url"),
		"a relative local destination must be captured as its trusted absolute target")
	assert.Equal(t, []string{want}, entriesFor(snapshot.config, "remote.origin.pushurl"))

	// The agent repoints origin at content inside the sandbox tree.
	super.git("config", "remote.origin.url", "/tmp/agent-written")
	serveDownloadFrom(t, super.dir)
	localDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, SafeDownload("test-sandbox", "/sandbox/workspace/repo", localDir, snapshot))

	assert.Equal(t, want, super.gitAt(localDir, "remote", "get-url", "origin"))
	assert.Equal(t, want, super.gitAt(localDir, "remote", "get-url", "--push", "origin"))

	t.Run("unresolvable relative destination fails closed", func(t *testing.T) {
		r := initRepoAt(t, filepath.Join(t.TempDir(), "repo"))
		r.commitFile("f.txt", "x")
		r.git("config", "remote.origin.url", "../missing")
		_, err := CaptureRepositorySnapshot(r.dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot be resolved")
	})

	t.Run("url scp and absolute destinations are unchanged", func(t *testing.T) {
		r := initRepoAt(t, filepath.Join(t.TempDir(), "repo"))
		r.commitFile("f.txt", "x")
		for _, value := range []string{
			"https://[2001:db8::1]/org/repo.git",
			"git@example.invalid:org/repo.git",
			"/srv/git/repo.git",
		} {
			r.git("config", "--replace-all", "remote.origin.url", value)
			snapshot, err := CaptureRepositorySnapshot(r.dir)
			require.NoError(t, err)
			assert.Equal(t, []string{value}, entriesFor(snapshot.config, "remote.origin.url"),
				"%q must be captured unchanged", value)
		}
	})

	t.Run("symlink before dotdot is resolved physically", func(t *testing.T) {
		root := t.TempDir()
		super := initRepoAt(t, filepath.Join(root, "super"))
		super.commitFile("f.txt", "x")
		physical := initRepoAt(t, filepath.Join(root, "target", "dep"))
		physical.commitFile("dep.txt", "physical")
		physical.git("branch", "physical-marker")
		// The lexical decoy lives outside the capture target: a bare clone
		// inside a checkout is rejected before upload now, and the
		// physical-versus-lexical distinction only needs somewhere Git can
		// be run from, not a supported target.
		lexicalSrc := initRepoAt(t, filepath.Join(root, "lexical-src"))
		lexicalSrc.commitFile("dep.txt", "lexical")
		lexicalSrc.git("branch", "lexical-marker")
		_, err := runGit(t, root, super.env, "clone", "--quiet", "--bare", lexicalSrc.dir, "dep")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "target", "real"), 0o755))
		require.NoError(t, os.Symlink("target/real", filepath.Join(root, "dep-link")))
		require.NoError(t, os.Symlink("../target/real", filepath.Join(super.dir, "link")))
		super.git("config", "remote.origin.url", "link/../dep")

		// Real Git follows the symlink before "..", so it contacts the repo in
		// target/dep, not the one lexically beside the link. The proof runs
		// in its own scratch repo so the decoy never sits inside a supported
		// target.
		scratch := initRepoAt(t, filepath.Join(root, "scratch"))
		scratch.commitFile("s.txt", "s")
		scratch.git("config", "remote.origin.url", "../dep-link/../dep")
		out, err := runGit(t, scratch.dir, scratch.env, "ls-remote", "--heads", "origin")
		require.NoError(t, err)
		assert.Contains(t, out, "refs/heads/physical-marker")
		assert.NotContains(t, out, "refs/heads/lexical-marker")

		want, err := filepath.EvalSymlinks(filepath.Join(root, "target", "dep"))
		require.NoError(t, err)
		snapshot, err := CaptureRepositorySnapshot(super.dir)
		require.NoError(t, err)
		assert.Equal(t, []string{want}, entriesFor(snapshot.config, "remote.origin.url"))
	})
}

// TestCaptureRepositorySnapshot_NestedModuleDestination preserves the absolute
// destination Git writes for a nested module store, and fails closed for a
// relative module destination, which Git resolves against the submodule
// worktree rather than the superproject root.
func TestCaptureRepositorySnapshot_NestedModuleDestination(t *testing.T) {
	requireGit(t)
	dep := newGitRepo(t)
	dep.commitFile("dep.txt", "dep")
	mid := newGitRepo(t)
	mid.commitFile("mid.txt", "mid")
	mid.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", dep.dir, "sub/dep")
	mid.git("commit", "--quiet", "-m", "add dep")
	super := newGitRepo(t)
	super.commitFile("f.txt", "x")
	super.git("-c", "protocol.file.allow=always", "submodule", "add", "-q", mid.dir, "vendor/mid")
	super.git("commit", "--quiet", "-m", "add mid")
	super.git("-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive", "--quiet")

	nestedStore := filepath.Join(super.dir, ".git", "modules", "vendor", "mid", "modules", "sub", "dep")
	want, err := runGit(t, super.dir, super.env, "config", "--file", filepath.Join(nestedStore, "config"), "--get", "remote.origin.url")
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(want), "Git stores an absolute nested module destination, got %q", want)

	snapshot, err := CaptureRepositorySnapshot(super.dir)
	require.NoError(t, err)
	assert.Contains(t, snapshot.stores[".git/modules/vendor/mid/modules/sub/dep"].config,
		configEntry{Key: "remote.origin.url", Value: want})

	relativeStore := filepath.Join(super.dir, ".git", "modules", "vendor", "mid", "config")
	super.gitAt(super.dir, "config", "--file", relativeStore, "remote.origin.url", "../mid")
	_, err = CaptureRepositorySnapshot(super.dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be resolved without its owning worktree")
}

// TestCaptureRepositorySnapshot_RepeatedTrackingValueKeepsFinal proves a
// single-valued tracking setting restores its effective final value when an
// earlier value repeats after an override: A,B,A restores A, not B.
func TestCaptureRepositorySnapshot_RepeatedTrackingValueKeepsFinal(t *testing.T) {
	r := newGitRepo(t)
	r.commitFile("f.txt", "x")
	f, err := os.OpenFile(filepath.Join(r.dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("[branch \"main\"]\n\tremote = up1\n\tremote = up2\n\tremote = up1\n" +
		"[submodule \"dep\"]\n\turl = https://a.example/1\n\turl = https://a.example/2\n\turl = https://a.example/1\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// Native Git resolves the same effective final values; the capture must
	// match it, or the classification has drifted from Git's semantics.
	assert.Equal(t, "up1", r.git("config", "--get", "branch.main.remote"))
	assert.Equal(t, "https://a.example/1", r.git("config", "--get", "submodule.dep.url"))

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	values := make(map[string]string)
	for _, entry := range snapshot.config {
		values[strings.ToLower(entry.Key)] = entry.Value
	}
	assert.Equal(t, "up1", values["branch.main.remote"], "A,B,A restores the final A")
	assert.Equal(t, "https://a.example/1", values["submodule.dep.url"], "A,B,A restores the final A")
}

// TestCaptureRepositorySnapshot_EmptyOverrideTakesEffect proves an explicit
// empty value is a real Git value with real precedence: a later empty
// override clears an earlier setting instead of being dropped for it.
func TestCaptureRepositorySnapshot_EmptyOverrideTakesEffect(t *testing.T) {
	r := newGitRepo(t)
	r.commitFile("f.txt", "x")
	r.git("config", "user.email", "first@example.invalid")
	r.git("config", "user.email", "")
	f, err := os.OpenFile(filepath.Join(r.dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("[branch \"main\"]\n\tremote = up\n\tremote =\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.Equal(t, "", r.git("config", "--get", "user.email"),
		"native Git resolves the empty override")

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	values := make(map[string]string)
	for _, entry := range snapshot.config {
		values[strings.ToLower(entry.Key)] = entry.Value
	}
	assert.Equal(t, "", values["user.email"], "the empty override must clear the earlier value")
	assert.Equal(t, "", values["branch.main.remote"], "the empty override must clear the earlier value")
}

// TestCaptureRepositorySnapshot_SymlinkedCheckoutRoot rejects nested layouts
// reached through a symlinked target path: WalkDir does not follow a
// symlinked root, so the walks resolve it first, the way the plain target
// walk does.
func TestCaptureRepositorySnapshot_SymlinkedCheckoutRoot(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	r := initRepoAt(t, real)
	r.commitFile("file.txt", "x")
	dep := newGitRepo(t)
	dep.commitFile("dep.txt", "dep")
	require.NoError(t, os.MkdirAll(filepath.Join(real, "vendor"), 0o755))
	_, err := runGit(t, filepath.Join(real, "vendor"), r.env, "clone", "--quiet", "file://"+dep.dir, "x")
	require.NoError(t, err)
	require.DirExists(t, filepath.Join(real, "vendor", "x", ".git"))
	require.NoError(t, os.Symlink("real", filepath.Join(root, "link")))

	_, err = CaptureRepositorySnapshot(filepath.Join(root, "link"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nested Git directory")
}

// TestCaptureRepositorySnapshot_DuplicateBooleansKeepGitPrecedence proves a
// repeated remote mirror and repeated working-copy hints survive the capture
// with Git's last-one-wins precedence, including a bare key and an empty value.
func TestCaptureRepositorySnapshot_DuplicateBooleansKeepGitPrecedence(t *testing.T) {
	requireGit(t)
	r := newGitRepo(t)
	r.commitFile("f.txt", "x")
	f, err := os.OpenFile(filepath.Join(r.dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("[remote \"origin\"]\n\turl = https://example.invalid/org/repo\n" +
		"\tmirror = true\n\tmirror = false\n[core]\n\tfilemode = true\n\tfilemode = false\n" +
		"\tsymlinks\n\tignorecase =\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	snapshot, err := CaptureRepositorySnapshot(r.dir)
	require.NoError(t, err)
	restored := filepath.Join(t.TempDir(), "config")
	require.NoError(t, writeGitConfig(t.TempDir(), restored, snapshot.format, snapshot.config))
	gitAt := func(args ...string) string {
		t.Helper()
		out, err := runGit(t, t.TempDir(), gitTestEnv(t.TempDir()), append([]string{"config", "--file", restored}, args...)...)
		require.NoError(t, err, out)
		return out
	}

	assert.Equal(t, "false", gitAt("--bool", "--get-all", "remote.origin.mirror"),
		"a single-valued boolean restores only its final value")
	for _, tc := range []struct{ key, want string }{
		{"remote.origin.mirror", "false"},
		{"core.filemode", "false"},
		{"core.symlinks", "true"},
		{"core.ignorecase", "false"},
	} {
		assert.Equal(t, tc.want, gitAt("--bool", "--get", tc.key),
			"%s must keep Git's precedence after the round trip", tc.key)
	}

	// A longer sequence and a worktree override that repeats an earlier
	// value must both restore the effective final value, not a deduplicated
	// sequence whose last entry would flip it.
	r2 := newGitRepo(t)
	r2.commitFile("f.txt", "x")
	f2, err := os.OpenFile(filepath.Join(r2.dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f2.WriteString("[remote \"origin\"]\n\turl = https://example.invalid/org/repo\n" +
		"\tmirror = false\n\tmirror = true\n\tmirror = false\n")
	require.NoError(t, err)
	require.NoError(t, f2.Close())
	snapshot2, err := CaptureRepositorySnapshot(r2.dir)
	require.NoError(t, err)
	for _, entry := range snapshot2.config {
		if strings.EqualFold(entry.Key, "remote.origin.mirror") {
			assert.Equal(t, "false", entry.Value, "false,true,false must restore the final false")
		}
	}

	r3 := newGitRepo(t)
	r3.commitFile("f.txt", "x")
	f3, err := os.OpenFile(filepath.Join(r3.dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f3.WriteString("[remote \"origin\"]\n\turl = https://example.invalid/org/repo\n\tmirror = true\n")
	require.NoError(t, err)
	require.NoError(t, f3.Close())
	// extensions.worktreeConfig is off, so config.worktree is not read; the
	// repeating override lives in the same file instead.
	f3b, err := os.OpenFile(filepath.Join(r3.dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f3b.WriteString("\tmirror = false\n\tmirror = true\n")
	require.NoError(t, err)
	require.NoError(t, f3b.Close())
	snapshot3, err := CaptureRepositorySnapshot(r3.dir)
	require.NoError(t, err)
	for _, entry := range snapshot3.config {
		if strings.EqualFold(entry.Key, "remote.origin.mirror") {
			assert.Equal(t, "true", entry.Value, "true,false,true must restore the final true")
		}
	}
}

// TestCaptureRepositorySnapshot_RejectsInvalidBooleanConfig fails closed when a
// trusted boolean-shaped value is not a boolean, instead of dropping it.
func TestCaptureRepositorySnapshot_RejectsInvalidBooleanConfig(t *testing.T) {
	for _, tc := range []struct{ key, entry string }{
		{"core.filemode", "[core]\n\tfilemode = maybe\n"},
		{"remote.origin.mirror", "[remote \"origin\"]\n\tmirror = maybe\n"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			requireGit(t)
			r := newGitRepo(t)
			r.commitFile("f.txt", "x")
			f, err := os.OpenFile(filepath.Join(r.dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
			require.NoError(t, err)
			_, err = f.WriteString(tc.entry)
			require.NoError(t, err)
			require.NoError(t, f.Close())

			_, err = CaptureRepositorySnapshot(r.dir)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.key)
		})
	}
}

// TestCaptureRepositorySnapshot_MissingPlainTargetFailsClosed rejects a plain
// target that does not exist instead of returning an empty snapshot.
func TestCaptureRepositorySnapshot_MissingPlainTargetFailsClosed(t *testing.T) {
	_, err := CaptureRepositorySnapshot(filepath.Join(t.TempDir(), "missing"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

// TestCaptureRepositorySnapshot_UnreadablePlainTreeFailsClosed refuses a plain
// target it cannot fully walk instead of snapshotting it as plain.
func TestCaptureRepositorySnapshot_UnreadablePlainTreeFailsClosed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	writeFile(t, blocked, "f.txt", "x\n")
	require.NoError(t, os.Chmod(blocked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	_, err := CaptureRepositorySnapshot(root)
	require.ErrorIs(t, err, os.ErrPermission)
}

// TestCaptureRepositorySnapshot_IgnoresCaseVariantGitMetadata keeps a plain
// target plain when a .git case variant is not the metadata Git itself would
// open. It needs a case-sensitive filesystem to create the variant.
func TestCaptureRepositorySnapshot_IgnoresCaseVariantGitMetadata(t *testing.T) {
	probe := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(probe, "caseprobe"), []byte("x\n"), 0o644))
	if _, err := os.Lstat(filepath.Join(probe, "CASEPROBE")); err == nil {
		t.Skip("filesystem is case-insensitive")
	}

	root := t.TempDir()
	writeFile(t, root, ".GIT/config", "[core]\n")
	snapshot, err := CaptureRepositorySnapshot(root)
	require.NoError(t, err)
	assert.Equal(t, RepositorySnapshot{}, snapshot)
}

// TestRestoreTrustedShallow_RejectsOversizedMarker rejects a marker above the
// whole-file limit and leaves it untouched.
func TestRestoreTrustedShallow_RejectsOversizedMarker(t *testing.T) {
	localDir := t.TempDir()
	gitDir := filepath.Join(localDir, ".git")
	require.NoError(t, os.Mkdir(gitDir, 0o755))
	shallowPath := writeFile(t, gitDir, "shallow", "")
	require.NoError(t, os.Truncate(shallowPath, maxShallowBytes+1))

	err := restoreTrustedShallow(localDir, shallowState{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	assert.Contains(t, err.Error(), fmt.Sprintf("larger than %d bytes", maxShallowBytes))
	info, statErr := os.Stat(shallowPath)
	require.NoError(t, statErr)
	assert.Equal(t, int64(maxShallowBytes+1), info.Size(), "a rejected marker must be left untouched")
}

// TestRestoreTrustedShallow_EntrySets checks the boundary-entry contract:
// returned entries must be a subset of the trusted set, while order,
// duplicates, removals, and an empty marker are tolerated.
func TestRestoreTrustedShallow_EntrySets(t *testing.T) {
	first := strings.Repeat("1", 40)
	second := strings.Repeat("2", 40)
	root := newGitRepo(t).commitFile("root.txt", "root")
	present := func(entries ...string) shallowState {
		return shallowState{Present: true, Data: []byte(strings.Join(entries, "\n") + "\n")}
	}
	cases := []struct {
		name     string
		trusted  shallowState
		returned string
		absent   bool
		wantErr  bool
	}{
		{name: "unchanged marker restores the trusted bytes", trusted: present(first), returned: first + "\n"},
		{name: "reordered and duplicated entries are allowed", trusted: present(first, second), returned: second + "\n" + first + "\n" + second + "\n"},
		{name: "removal is allowed", trusted: present(first, second), returned: first + "\n"},
		{name: "empty returned file is allowed when trusted is absent", returned: ""},
		{name: "absent returned marker is allowed when trusted is absent", absent: true},
		{name: "added cutoff is rejected", trusted: present(first), returned: first + "\n" + second + "\n", wantErr: true},
		{name: "malformed added entry is rejected", trusted: present(first), returned: first + "\njunk\n", wantErr: true},
		{name: "new zero-parent root cutoff is rejected", returned: root + "\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			localDir := t.TempDir()
			gitDir := filepath.Join(localDir, ".git")
			require.NoError(t, os.Mkdir(gitDir, 0o755))
			shallowPath := filepath.Join(gitDir, "shallow")
			if !tc.absent {
				require.NoError(t, os.WriteFile(shallowPath, []byte(tc.returned), 0o644))
			}

			err := restoreTrustedShallow(localDir, tc.trusted)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unsupported sandbox shallow boundary change")
				assert.Contains(t, err.Error(), "fetch --depth")
				got, readErr := os.ReadFile(shallowPath)
				require.NoError(t, readErr)
				assert.Equal(t, tc.returned, string(got), "a rejected boundary must be left untouched")
				return
			}
			require.NoError(t, err)
			got, readErr := os.ReadFile(shallowPath)
			if !tc.trusted.Present {
				assert.True(t, os.IsNotExist(readErr), "trusted absence must remove the marker")
				return
			}
			require.NoError(t, readErr)
			assert.Equal(t, tc.trusted.Data, got, "trusted bytes must be restored exactly")
		})
	}
}

func TestRestoreTrustedShallow_ErrorPaths(t *testing.T) {
	t.Run("shallow path cannot be inspected", func(t *testing.T) {
		localDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(localDir, ".git"), []byte("gitdir: elsewhere\n"), 0o644))

		err := restoreTrustedShallow(localDir, shallowState{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inspecting git shallow metadata")
	})

	t.Run("git entry is not a directory", func(t *testing.T) {
		localDir := t.TempDir()
		require.NoError(t, os.Symlink("missing", filepath.Join(localDir, ".git")))

		err := restoreTrustedShallow(localDir, shallowState{Present: true, Data: []byte("0123456789012345678901234567890123456789\n")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not a directory")
	})
}

func TestRestoreTrustedShallow_PermissionFailures(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}

	t.Run("removing untrusted shallow fails", func(t *testing.T) {
		localDir := t.TempDir()
		gitDir := filepath.Join(localDir, ".git")
		require.NoError(t, os.Mkdir(gitDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "shallow"), nil, 0o644))
		require.NoError(t, os.Chmod(gitDir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })

		err := restoreTrustedShallow(localDir, shallowState{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "removing untrusted git shallow metadata")
	})

	t.Run("reading downloaded shallow fails", func(t *testing.T) {
		localDir := t.TempDir()
		gitDir := filepath.Join(localDir, ".git")
		require.NoError(t, os.Mkdir(gitDir, 0o755))
		shallowPath := filepath.Join(gitDir, "shallow")
		require.NoError(t, os.WriteFile(shallowPath, []byte("0123456789012345678901234567890123456789\n"), 0o644))
		require.NoError(t, os.Chmod(shallowPath, 0))
		t.Cleanup(func() { _ = os.Chmod(shallowPath, 0o644) })

		err := restoreTrustedShallow(localDir, shallowState{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading downloaded git shallow metadata")
	})

	t.Run("writing trusted shallow fails", func(t *testing.T) {
		localDir := t.TempDir()
		gitDir := filepath.Join(localDir, ".git")
		require.NoError(t, os.Mkdir(gitDir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })

		err := restoreTrustedShallow(localDir, shallowState{Present: true, Data: []byte("0123456789012345678901234567890123456789\n")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "writing trusted git shallow metadata")
	})
}
