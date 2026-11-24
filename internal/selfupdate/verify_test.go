package selfupdate

import (
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testdata holds the real checksums.txt and checksums.txt.sigstore.json of
// fullsend v0.53.0 and a snapshot of the Sigstore trusted root, so these
// tests verify a real release offline.
const fixtureTag = "v0.53.0"

func loadFixtures(t *testing.T) (root.TrustedMaterial, []byte, []byte) {
	t.Helper()
	trusted, err := root.NewTrustedRootFromPath(filepath.Join("testdata", "trusted_root.json"))
	require.NoError(t, err, "loading trusted root")
	bundleJSON, err := os.ReadFile(filepath.Join("testdata", "checksums.txt.sigstore.json"))
	require.NoError(t, err)
	checksums, err := os.ReadFile(filepath.Join("testdata", "checksums.txt"))
	require.NoError(t, err)
	return trusted, bundleJSON, checksums
}

func TestVerifyChecksums_AcceptsRealRelease(t *testing.T) {
	trusted, bundleJSON, checksums := loadFixtures(t)
	require.NoError(t, verifyChecksums(trusted, bundleJSON, checksums, fixtureTag))
}

func TestVerifyChecksums_RejectsOtherTag(t *testing.T) {
	trusted, bundleJSON, checksums := loadFixtures(t)
	// A genuine signature for v0.53.0 must not vouch for another release.
	require.Error(t, verifyChecksums(trusted, bundleJSON, checksums, "v0.54.0"))
}

func TestVerifyChecksums_RejectsTamperedChecksums(t *testing.T) {
	trusted, bundleJSON, checksums := loadFixtures(t)
	tampered := bytes.Replace(checksums, []byte("4620788534"), []byte("0000000000"), 1)
	require.NotEqual(t, checksums, tampered, "fixture did not contain the expected hash prefix")
	require.Error(t, verifyChecksums(trusted, bundleJSON, tampered, fixtureTag))
}

func TestVerifyChecksums_RejectsMalformedBundle(t *testing.T) {
	trusted, _, checksums := loadFixtures(t)
	err := verifyChecksums(trusted, []byte(`{"not":"a bundle"}`), checksums, fixtureTag)
	require.ErrorContains(t, err, "parsing signature bundle")
}

func TestSignerIdentity(t *testing.T) {
	id, err := signerIdentity("v1.2.3")
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/fullsend-ai/fullsend/.github/workflows/release.yml@refs/tags/v1.2.3",
		id.SubjectAlternativeName.SubjectAlternativeName)
	assert.Equal(t, githubOIDCIssuer, id.Issuer.Issuer)
	assert.Equal(t, "refs/tags/v1.2.3", id.Extensions.SourceRepositoryRef)
}

// Installed CLIs only accept signatures made by this workflow, so it must
// exist and run GoReleaser itself.
func TestReleaseWorkflowRunsGoReleaser(t *testing.T) {
	path := strings.TrimPrefix(releaseWorkflow, releaseRepoURI+"/")
	content, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(path)))
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(content), "uses: goreleaser/goreleaser-action@"),
		"%s must run GoReleaser itself", path)
}

func TestVerifyReleaseSignature(t *testing.T) {
	trusted, bundleJSON, checksums := loadFixtures(t)
	orig := loadTrustedRootFn
	t.Cleanup(func() { loadTrustedRootFn = orig })

	loadTrustedRootFn = func(context.Context) (root.TrustedMaterial, error) { return trusted, nil }
	require.NoError(t, verifyReleaseSignature(context.Background(), bundleJSON, checksums, fixtureTag))

	loadTrustedRootFn = func(context.Context) (root.TrustedMaterial, error) { return nil, os.ErrNotExist }
	require.ErrorIs(t, verifyReleaseSignature(context.Background(), bundleJSON, checksums, fixtureTag), os.ErrNotExist)
}

// useMirror points the TUF client at a test server running handler, with
// its cache at cache, and returns a counter of the requests it received.
func useMirror(t *testing.T, cache string, handler http.HandlerFunc) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	orig := tufOptionsFn
	t.Cleanup(func() { tufOptionsFn = orig })
	tufOptionsFn = func() *tuf.Options {
		return tuf.DefaultOptions().WithRepositoryBaseURL(srv.URL).WithCachePath(cache)
	}
	return &calls
}

// stalledMirror is a TUF mirror that accepts requests and never answers
// them, like a broken proxy.
func stalledMirror(t *testing.T) *atomic.Int32 {
	t.Helper()
	release := make(chan struct{})
	calls := useMirror(t, t.TempDir(), func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })
	return calls
}

func TestFetchTrustedRoot_FailsWhenMirrorUnreachable(t *testing.T) {
	calls := useMirror(t, t.TempDir(), http.NotFound)
	_, err := fetchTrustedRoot(context.Background())
	require.ErrorContains(t, err, "fetching Sigstore trusted root")
	assert.Positive(t, calls.Load(), "mirror handler was not called")
}

func TestFetchTrustedRoot_TimesOutOnStalledMirror(t *testing.T) {
	calls := stalledMirror(t)
	origTimeout := tufHTTPTimeout
	t.Cleanup(func() { tufHTTPTimeout = origTimeout })
	tufHTTPTimeout = 100 * time.Millisecond

	start := time.Now()
	_, err := fetchTrustedRoot(context.Background())
	require.Error(t, err)
	assert.Less(t, time.Since(start), 10*time.Second, "bounded by the HTTP timeout")
	// A timeout is not retried without the cache: one stalled request only.
	assert.Equal(t, int32(1), calls.Load(), "requests to the stalled mirror")
}

func TestFetchTrustedRoot_StopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := useMirror(t, t.TempDir(), func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	})

	start := time.Now()
	_, err := fetchTrustedRoot(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 10*time.Second, "stops without waiting for the HTTP timeout")
	assert.Positive(t, calls.Load(), "mirror handler was not called")
}

func TestFetchTrustedRoot_RetriesWithoutUnusableCache(t *testing.T) {
	tests := []struct {
		name  string
		cache func(t *testing.T) string
	}{
		{"cache path is a file", func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "not-a-dir")
			require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
			return p
		}},
		{"read-only cache directory", func(t *testing.T) string {
			if runtime.GOOS == "windows" || os.Geteuid() == 0 {
				t.Skip("needs POSIX permissions and a non-root user")
			}
			p := t.TempDir()
			require.NoError(t, os.Chmod(p, 0o555))
			t.Cleanup(func() { os.Chmod(p, 0o755) })
			return p
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := useMirror(t, tt.cache(t), http.NotFound)

			_, err := fetchTrustedRoot(context.Background())
			require.Error(t, err)
			// The cached attempt fails on the cache, so every request comes
			// from the retry without it.
			assert.Positive(t, calls.Load(), "the retry did not reach the mirror")
			var pathErr *fs.PathError
			assert.ErrorAs(t, err, &pathErr, "the cache error is reported")
			assert.ErrorContains(t, err, "404", "the retry error is reported")
		})
	}
}
