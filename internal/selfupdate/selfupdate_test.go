package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/binary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want version
		ok   bool
	}{
		{"0.53.0", version{0, 53, 0, 0}, true},
		{"v1.2.3", version{1, 2, 3, 0}, true},
		{"v0.54.0-rc.2", version{0, 54, 0, 2}, true},
		{" 0.1.0 ", version{0, 1, 0, 0}, true},
		{"dev", version{}, false},
		{"", version{}, false},
		{"v0.52.0-3-gabc1234-dirty", version{}, false},
		{"v0.52.0-beta.1", version{}, false},
		{"v0.54.0-rc.0", version{}, false},
		{"v0.54.0-rc.01", version{}, false},
		{"1.2", version{}, false},
		{"v99999999999999999999.0.0", version{}, false},
	}
	for _, tt := range tests {
		got, ok := parseVersion(tt.in)
		assert.Equal(t, tt.ok, ok, "parseVersion(%q) ok", tt.in)
		assert.Equal(t, tt.want, got, "parseVersion(%q)", tt.in)
	}
}

func TestVersionCompareAndTag(t *testing.T) {
	order := []string{"v0.9.9", "v0.10.0-rc.1", "v0.10.0-rc.2", "v0.10.0", "v0.10.1", "v0.11.0", "v1.0.0"}
	for i := range order {
		for j := range order {
			a, _ := parseVersion(order[i])
			b, _ := parseVersion(order[j])
			assert.Equal(t, sign(i-j), a.compare(b), "compare(%s, %s)", order[i], order[j])
		}
		v, _ := parseVersion(order[i])
		assert.Equal(t, order[i], v.tag())
	}
}

func TestCheckNotManaged(t *testing.T) {
	for _, p := range []string{
		"/opt/homebrew/Cellar/fullsend/0.53.0/bin/fullsend",
		"/usr/local/Cellar/fullsend/0.53.0/bin/fullsend",
		"/nix/store/abc-fullsend-0.53.0/bin/fullsend",
	} {
		assert.Error(t, checkNotManaged(p), p)
	}
	for _, p := range []string{"/usr/local/bin/fullsend", "/home/me/bin/fullsend", "/opt/homebrew/bin/fullsend"} {
		assert.NoError(t, checkNotManaged(p), p)
	}
}

// fakeRelease serves one release through the Updater's function fields.
type fakeRelease struct {
	tag       string
	archive   []byte
	checksums []byte
	fetched   []string
	verifyErr error
	fetchErr  map[string]error
}

func newFakeRelease(t *testing.T, tag, goos, goarch string, binaryContent []byte) *fakeRelease {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("readme")}, {"fullsend", binaryContent}} {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write(f.body)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	sum := sha256.Sum256(buf.Bytes())
	asset := fmt.Sprintf("fullsend_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), goos, goarch)
	checksums := fmt.Sprintf("%s  %s\n%s  fullsend_other.tar.gz\n", hex.EncodeToString(sum[:]), asset, strings.Repeat("0", 64))
	return &fakeRelease{tag: tag, archive: buf.Bytes(), checksums: []byte(checksums), fetchErr: map[string]error{}}
}

func (f *fakeRelease) fetch(_ context.Context, tag, name string, maxSize int64) ([]byte, error) {
	f.fetched = append(f.fetched, name)
	if err := f.fetchErr[name]; err != nil {
		return nil, err
	}
	if tag != f.tag {
		return nil, fmt.Errorf("no release %s: %w", tag, binary.ErrReleaseFileNotFound)
	}
	switch {
	case name == checksumsAsset:
		return f.checksums, nil
	case name == bundleAsset:
		return []byte("bundle"), nil
	case strings.HasSuffix(name, ".tar.gz"):
		return f.archive, nil
	}
	return nil, fmt.Errorf("no asset %s", name)
}

// setup returns an Updater whose running binary is a file in a temp dir,
// reached through a symlink, and whose release is rel.
func setup(t *testing.T, rel *fakeRelease) (*Updater, string) {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "fullsend")
	require.NoError(t, os.WriteFile(exe, []byte("old binary"), 0o550))
	link := filepath.Join(dir, "fullsend-link")
	require.NoError(t, os.Symlink(exe, link))
	u := &Updater{
		goos:       "linux",
		goarch:     "amd64",
		executable: func() (string, error) { return link, nil },
		latestTag:  func(context.Context) (string, error) { return rel.tag, nil },
		fetchAsset: rel.fetch,
		verifySignature: func(_ context.Context, bundleJSON, checksums []byte, tag string) error {
			if string(bundleJSON) != "bundle" || !bytes.Equal(checksums, rel.checksums) || tag != rel.tag {
				return errors.New("unexpected verification input")
			}
			return rel.verifyErr
		},
	}
	resolved, err := filepath.EvalSymlinks(exe)
	require.NoError(t, err)
	return u, resolved
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

// dirEntries returns the names of the files in dir.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestRun_UpdatesToLatest(t *testing.T) {
	rel := newFakeRelease(t, "v0.53.0", "linux", "amd64", []byte("new binary"))
	u, exe := setup(t, rel)

	res, err := u.Run(context.Background(), Options{CurrentVersion: "0.52.0"})
	require.NoError(t, err)
	assert.Equal(t, Result{Status: StatusUpdated, From: "v0.52.0", To: "v0.53.0", Path: exe}, res)
	assert.Equal(t, "new binary", readFile(t, exe))
	info, err := os.Stat(exe)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o550), info.Mode().Perm(), "keeps the original permissions")
	// Only the binary and the symlink remain: no temp file is left behind.
	assert.ElementsMatch(t, []string{"fullsend", "fullsend-link"}, dirEntries(t, filepath.Dir(exe)))
}

func TestRun_ExplicitOlderVersionRollsBack(t *testing.T) {
	rel := newFakeRelease(t, "v0.51.0", "linux", "amd64", []byte("old release"))
	u, exe := setup(t, rel)

	res, err := u.Run(context.Background(), Options{CurrentVersion: "v0.53.0", TargetVersion: "0.51.0"})
	require.NoError(t, err)
	assert.Equal(t, StatusUpdated, res.Status)
	assert.Equal(t, "v0.51.0", res.To)
	assert.Equal(t, "old release", readFile(t, exe))
}

func TestRun_NoOpCases(t *testing.T) {
	tests := []struct {
		name    string
		current string
		target  string
		dryRun  bool
		want    Status
		fetched []string
	}{
		{"already latest", "0.53.0", "", false, StatusUpToDate, nil},
		{"explicit same version", "0.53.0", "v0.53.0", false, StatusUpToDate, nil},
		{"newer than latest", "0.54.0-rc.1", "", false, StatusAhead, nil},
		{"dry run", "0.52.0", "", true, StatusWouldUpdate, nil},
		// A requested release may not exist, so the dry run checks its checksums.
		{"dry run with version", "0.52.0", "v0.53.0", true, StatusWouldUpdate, []string{checksumsAsset}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel := newFakeRelease(t, "v0.53.0", "linux", "amd64", []byte("new binary"))
			u, exe := setup(t, rel)
			res, err := u.Run(context.Background(), Options{CurrentVersion: tt.current, TargetVersion: tt.target, DryRun: tt.dryRun})
			require.NoError(t, err)
			assert.Equal(t, tt.want, res.Status)
			assert.Equal(t, tt.fetched, rel.fetched, "downloads")
			assert.Equal(t, "old binary", readFile(t, exe))
		})
	}
}

func TestRun_Errors(t *testing.T) {
	tests := []struct {
		name    string
		current string
		target  string
		dryRun  bool
		mutate  func(*fakeRelease, *Updater)
		wantErr string
	}{
		{name: "dev build", current: "dev", wantErr: "development build"},
		{name: "git describe build", current: "v0.52.0-3-gabc1234", wantErr: "development build"},
		{name: "invalid target", current: "0.52.0", target: "latest", wantErr: `invalid version "latest"`},
		{name: "rc.0 target", current: "0.52.0", target: "v0.54.0-rc.0", wantErr: `invalid version "v0.54.0-rc.0"`},
		{name: "target before signed bundles", current: "0.52.0", target: "v0.18.0", wantErr: "releases before v0.19.0"},
		{
			name: "latest lookup fails", current: "0.52.0",
			mutate: func(_ *fakeRelease, u *Updater) {
				u.latestTag = func(context.Context) (string, error) { return "", errors.New("rate limited") }
			},
			wantErr: "finding the latest release: rate limited",
		},
		{
			name: "latest tag unexpected", current: "0.52.0",
			mutate: func(_ *fakeRelease, u *Updater) {
				u.latestTag = func(context.Context) (string, error) { return "nightly", nil }
			},
			wantErr: `unexpected tag "nightly"`,
		},
		{
			name: "executable lookup fails", current: "0.52.0",
			mutate: func(_ *fakeRelease, u *Updater) {
				u.executable = func() (string, error) { return "", errors.New("boom") }
			},
			wantErr: "locating the running fullsend binary",
		},
		{
			name: "executable missing", current: "0.52.0",
			mutate: func(_ *fakeRelease, u *Updater) {
				u.executable = func() (string, error) { return "/nonexistent/fullsend", nil }
			},
			wantErr: "resolving /nonexistent/fullsend",
		},
		{
			name: "signature invalid", current: "0.52.0",
			mutate:  func(r *fakeRelease, _ *Updater) { r.verifyErr = errors.New("no matching identity") },
			wantErr: "failed signature verification, refusing to update: no matching identity",
		},
		{
			name: "checksums missing", current: "0.52.0",
			mutate:  func(r *fakeRelease, _ *Updater) { r.fetchErr[checksumsAsset] = errors.New("404") },
			wantErr: "downloading checksums.txt: 404",
		},
		{
			name: "bundle missing", current: "0.52.0",
			mutate:  func(r *fakeRelease, _ *Updater) { r.fetchErr[bundleAsset] = errors.New("404") },
			wantErr: "downloading checksums.txt.sigstore.json: 404",
		},
		{
			name: "unsupported platform", current: "0.52.0",
			mutate:  func(_ *fakeRelease, u *Updater) { u.goos = "windows" },
			wantErr: "has no build for windows/amd64",
		},
		{name: "dry run, release missing", current: "0.52.0", target: "v9.9.9", dryRun: true, wantErr: "release v9.9.9 not found"},
		{
			name: "dry run, unsupported platform", current: "0.52.0", target: "v0.53.0", dryRun: true,
			mutate:  func(_ *fakeRelease, u *Updater) { u.goos = "windows" },
			wantErr: "has no build for windows/amd64",
		},
		{
			name: "archive download fails", current: "0.52.0",
			mutate: func(r *fakeRelease, _ *Updater) {
				r.fetchErr["fullsend_0.53.0_linux_amd64.tar.gz"] = errors.New("connection reset")
			},
			wantErr: "downloading fullsend_0.53.0_linux_amd64.tar.gz: connection reset",
		},
		{
			name: "archive tampered", current: "0.52.0",
			mutate:  func(r *fakeRelease, _ *Updater) { r.archive = append([]byte{}, r.archive[:len(r.archive)-1]...) },
			wantErr: "checksum mismatch",
		},
		{
			name: "archive without binary", current: "0.52.0",
			mutate: func(r *fakeRelease, _ *Updater) {
				r.archive = []byte("not a gzip")
				sum := sha256.Sum256(r.archive)
				r.checksums = []byte(hex.EncodeToString(sum[:]) + "  fullsend_0.53.0_linux_amd64.tar.gz\n")
			},
			wantErr: "extracting fullsend from the release archive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rel := newFakeRelease(t, "v0.53.0", "linux", "amd64", []byte("new binary"))
			u, exe := setup(t, rel)
			if tt.mutate != nil {
				tt.mutate(rel, u)
			}
			_, err := u.Run(context.Background(), Options{CurrentVersion: tt.current, TargetVersion: tt.target, DryRun: tt.dryRun})
			require.ErrorContains(t, err, tt.wantErr)
			assert.Equal(t, "old binary", readFile(t, exe), "binary must be unchanged")
			assert.ElementsMatch(t, []string{"fullsend", "fullsend-link"}, dirEntries(t, filepath.Dir(exe)), "leftover files")
		})
	}
}

func TestRun_CancelledBeforeReplace(t *testing.T) {
	rel := newFakeRelease(t, "v0.53.0", "linux", "amd64", []byte("new binary"))
	u, exe := setup(t, rel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Ctrl-C during the archive download: the download completes, but the
	// binary must not be replaced.
	u.fetchAsset = func(ctx context.Context, tag, name string, maxSize int64) ([]byte, error) {
		if strings.HasSuffix(name, ".tar.gz") {
			cancel()
		}
		return rel.fetch(ctx, tag, name, maxSize)
	}

	_, err := u.Run(ctx, Options{CurrentVersion: "0.52.0"})
	require.ErrorIs(t, err, context.Canceled)
	assert.ErrorContains(t, err, "self-update cancelled")
	assert.Equal(t, "old binary", readFile(t, exe), "binary must be unchanged")
	assert.ElementsMatch(t, []string{"fullsend", "fullsend-link"}, dirEntries(t, filepath.Dir(exe)), "leftover files")
}

func TestRun_DevBuildIsErrDevBuild(t *testing.T) {
	_, err := New().Run(context.Background(), Options{CurrentVersion: "dev"})
	require.ErrorIs(t, err, ErrDevBuild)
}

func TestRun_ManagedInstallRefused(t *testing.T) {
	// Like Homebrew: the binary on PATH is a symlink into the Cellar.
	prefix := t.TempDir()
	exe := filepath.Join(prefix, "Cellar", "fullsend", "0.52.0", "bin", "fullsend")
	require.NoError(t, os.MkdirAll(filepath.Dir(exe), 0o755))
	require.NoError(t, os.WriteFile(exe, []byte("old binary"), 0o755))
	link := filepath.Join(prefix, "bin", "fullsend")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(exe, link))
	u := New()
	u.executable = func() (string, error) { return link, nil }
	latestCalled := false
	u.latestTag = func(context.Context) (string, error) { latestCalled = true; return "", nil }

	_, err := u.Run(context.Background(), Options{CurrentVersion: "0.52.0"})
	require.ErrorContains(t, err, "managed by Homebrew")
	assert.False(t, latestCalled, "looked up the latest release")
}

func TestRun_UnwritableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	rel := newFakeRelease(t, "v0.53.0", "linux", "amd64", []byte("new binary"))
	u, exe := setup(t, rel)
	dir := filepath.Dir(exe)
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	_, err := u.Run(context.Background(), Options{CurrentVersion: "0.52.0"})
	require.ErrorContains(t, err, "re-run with sudo")
	assert.Empty(t, rel.fetched, "downloaded before checking permissions")
}

func TestNew(t *testing.T) {
	u := New()
	assert.Equal(t, runtime.GOOS, u.goos)
	assert.Equal(t, runtime.GOARCH, u.goarch)
	assert.NotNil(t, u.executable)
	assert.NotNil(t, u.latestTag)
	assert.NotNil(t, u.fetchAsset)
	assert.NotNil(t, u.verifySignature)
}
