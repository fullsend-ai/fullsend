package binary

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type redirectTransport struct {
	srvURL string
	base   http.RoundTripper
}

func (t redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = strings.TrimPrefix(strings.TrimPrefix(t.srvURL, "https://"), "http://")
	if t.base == nil {
		t.base = http.DefaultTransport
	}
	return t.base.RoundTrip(clone)
}

func withTestReleaseServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	origClient := HTTPClient
	origBaseURL := ReleaseBaseURL
	HTTPClient = &http.Client{
		Transport: redirectTransport{srvURL: srv.URL},
		Timeout:   120 * time.Second,
	}
	ReleaseBaseURL = srv.URL
	t.Cleanup(func() {
		HTTPClient = origClient
		ReleaseBaseURL = origBaseURL
	})
}

func TestExtractFullsendFromTarGz_PathTraversal(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	content := []byte("malicious binary content")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "../../../tmp/fullsend",
		Size:     int64(len(content)),
		Mode:     0o755,
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	destPath := filepath.Join(t.TempDir(), "fullsend")
	err = ExtractFullsendFromTarGz(&buf, destPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found in archive")
}

func TestExtractFullsendFromTarGz_ValidEntry(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	content := []byte("valid binary content")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "fullsend_0.4.0_linux_amd64/fullsend",
		Size:     int64(len(content)),
		Mode:     0o755,
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	destPath := filepath.Join(t.TempDir(), "fullsend")
	err = ExtractFullsendFromTarGz(&buf, destPath)
	require.NoError(t, err)

	data, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, "valid binary content", string(data))
}

func TestDownloadChecksumForAsset_ParsesLine(t *testing.T) {
	body := "1b4f0e9851971998e732078544c96b36c3d01cedf7caa332359d6f1d83567014  fullsend_1.0.0_linux_arm64.tar.gz\n" +
		"60303ae22b998861bce3b28f33eec1be758a213c86c93c076dbe9f558c11c752  fullsend_1.0.0_linux_amd64.tar.gz\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	origBaseURL := ReleaseBaseURL
	ReleaseBaseURL = srv.URL
	defer func() { ReleaseBaseURL = origBaseURL }()

	hash, err := downloadChecksumForAsset("1.0.0", "fullsend_1.0.0_linux_amd64.tar.gz")
	require.NoError(t, err)
	assert.Equal(t, "60303ae22b998861bce3b28f33eec1be758a213c86c93c076dbe9f558c11c752", hash)
}

func TestDownloadChecksumForAsset_AssetNotFound(t *testing.T) {
	body := "60303ae22b998861bce3b28f33eec1be758a213c86c93c076dbe9f558c11c752  fullsend_1.0.0_linux_amd64.tar.gz\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	origBaseURL := ReleaseBaseURL
	ReleaseBaseURL = srv.URL
	defer func() { ReleaseBaseURL = origBaseURL }()

	_, err := downloadChecksumForAsset("1.0.0", "fullsend_1.0.0_linux_arm64.tar.gz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in checksums.txt")
}

func TestDownloadChecksumForAsset_InvalidHex(t *testing.T) {
	body := "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ  fullsend_1.0.0_linux_amd64.tar.gz\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	origBaseURL := ReleaseBaseURL
	ReleaseBaseURL = srv.URL
	defer func() { ReleaseBaseURL = origBaseURL }()

	_, err := downloadChecksumForAsset("1.0.0", "fullsend_1.0.0_linux_amd64.tar.gz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid hex hash")
}

func TestDownloadReleaseBinary_ChecksumMismatch(t *testing.T) {
	var tarBuf bytes.Buffer
	gw := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gw)
	content := []byte("fake binary")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "fullsend",
		Size:     int64(len(content)),
		Mode:     0o755,
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"
	checksumBody := fmt.Sprintf("%s  fullsend_1.0.0_linux_amd64.tar.gz\n", wrongHash)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1.0.0/checksums.txt" {
			fmt.Fprint(w, checksumBody)
		} else if r.URL.Path == "/v1.0.0/fullsend_1.0.0_linux_amd64.tar.gz" {
			w.Write(tarBuf.Bytes())
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origBaseURL := ReleaseBaseURL
	ReleaseBaseURL = srv.URL
	defer func() { ReleaseBaseURL = origBaseURL }()

	destPath := filepath.Join(t.TempDir(), "fullsend")
	err = DownloadRelease("1.0.0", "amd64", destPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
}

func TestDownloadReleaseBinary_ChecksumMatch(t *testing.T) {
	var tarBuf bytes.Buffer
	gw := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gw)
	content := []byte("good binary")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "fullsend",
		Size:     int64(len(content)),
		Mode:     0o755,
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	tarBytes := tarBuf.Bytes()
	h := sha256.Sum256(tarBytes)
	correctHash := hex.EncodeToString(h[:])
	checksumBody := fmt.Sprintf("%s  fullsend_2.0.0_linux_amd64.tar.gz\n", correctHash)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2.0.0/checksums.txt" {
			fmt.Fprint(w, checksumBody)
		} else if r.URL.Path == "/v2.0.0/fullsend_2.0.0_linux_amd64.tar.gz" {
			w.Write(tarBytes)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origBaseURL := ReleaseBaseURL
	ReleaseBaseURL = srv.URL
	defer func() { ReleaseBaseURL = origBaseURL }()

	destPath := filepath.Join(t.TempDir(), "fullsend")
	err = DownloadRelease("2.0.0", "amd64", destPath)
	require.NoError(t, err)

	data, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, "good binary", string(data))
}

func TestDownloadRelease_Live(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping download test in short mode")
	}

	destPath := filepath.Join(t.TempDir(), "fullsend")
	err := DownloadRelease("0.4.0", "amd64", destPath)
	require.NoError(t, err)

	info, err := os.Stat(destPath)
	require.NoError(t, err)
	assert.True(t, info.Size() > 0)
}

func TestCrossCompile_ProducesBinary(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("cross-compilation test only meaningful on non-Linux hosts")
	}
	if testing.Short() {
		t.Skip("skipping cross-compilation in short mode")
	}

	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "fullsend")
	err := CrossCompile(CrossCompileOpts{
		Version:      "dev",
		Arch:         runtime.GOARCH,
		DestPath:     binPath,
		VersionStamp: "-crosscompiled",
	})
	require.NoError(t, err)

	info, err := os.Stat(binPath)
	require.NoError(t, err)
	assert.True(t, info.Size() > 0)
}

func TestValidateLinuxBinary_RejectsNonELF(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "not-elf")
	require.NoError(t, os.WriteFile(tmp, []byte("#!/bin/sh\necho hello"), 0o755))
	err := ValidateLinuxBinary(tmp, "amd64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid ELF binary")
}

func TestValidateLinuxBinary_RejectsMissing(t *testing.T) {
	err := ValidateLinuxBinary("/tmp/nonexistent-fullsend-binary-12345", "amd64")
	require.Error(t, err)
}

func TestValidateLinuxBinary_AcceptsHostBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host binary is only ELF on Linux")
	}
	exe, err := os.Executable()
	require.NoError(t, err)
	assert.NoError(t, ValidateLinuxBinary(exe, runtime.GOARCH))
}

func TestResolveForVendor_DevNoCheckoutFails(t *testing.T) {
	// Force no module by running from a temp dir without go.mod.
	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	_, err = ResolveForVendor(VendorOpts{Version: "dev", Arch: "amd64"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dev build")
}

func TestResolveForVendor_NoLatestFallback(t *testing.T) {
	var latestCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/releases/latest") {
			latestCalls.Add(1)
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	origClient := HTTPClient
	origBaseURL := ReleaseBaseURL
	HTTPClient = srv.Client()
	ReleaseBaseURL = srv.URL
	defer func() {
		HTTPClient = origClient
		ReleaseBaseURL = origBaseURL
	}()

	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	_, err = ResolveForVendor(VendorOpts{Version: "0.4.0", Arch: "amd64"})
	require.Error(t, err)
	assert.Equal(t, int32(0), latestCalls.Load(), "vendor path must not call latest release API")
	assert.NotContains(t, err.Error(), "latest")
}

func TestResolveForVendor_ReleaseFallback(t *testing.T) {
	var tarBuf bytes.Buffer
	gw := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gw)
	content := []byte("release binary")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "fullsend",
		Size:     int64(len(content)),
		Mode:     0o755,
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	tarBytes := tarBuf.Bytes()
	h := sha256.Sum256(tarBytes)
	correctHash := hex.EncodeToString(h[:])
	checksumBody := fmt.Sprintf("%s  fullsend_0.4.0_linux_amd64.tar.gz\n", correctHash)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0.4.0/checksums.txt" {
			fmt.Fprint(w, checksumBody)
		} else if r.URL.Path == "/v0.4.0/fullsend_0.4.0_linux_amd64.tar.gz" {
			w.Write(tarBytes)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origBaseURL := ReleaseBaseURL
	ReleaseBaseURL = srv.URL
	defer func() { ReleaseBaseURL = origBaseURL }()

	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	result, err := ResolveForVendor(VendorOpts{Version: "0.4.0", Arch: "amd64"})
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(result.TmpDir) })
	assert.Equal(t, SourceReleaseDownload, result.Source)

	data, err := os.ReadFile(result.Path)
	require.NoError(t, err)
	assert.Equal(t, "release binary", string(data))
}

func TestResolveForRun_PrefersReleaseBeforeCrossCompile(t *testing.T) {
	// Build mock release assets.
	var tarBuf bytes.Buffer
	gw := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gw)
	content := []byte("release binary")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "fullsend",
		Size:     int64(len(content)),
		Mode:     0o755,
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	tarBytes := tarBuf.Bytes()
	h := sha256.Sum256(tarBytes)
	correctHash := hex.EncodeToString(h[:])
	checksumBody := fmt.Sprintf("%s  fullsend_0.4.0_linux_amd64.tar.gz\n", correctHash)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0.4.0/checksums.txt" {
			fmt.Fprint(w, checksumBody)
		} else if r.URL.Path == "/v0.4.0/fullsend_0.4.0_linux_amd64.tar.gz" {
			w.Write(tarBytes)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	origBaseURL := ReleaseBaseURL
	ReleaseBaseURL = srv.URL
	defer func() { ReleaseBaseURL = origBaseURL }()

	// Run from non-module dir — cross-compile would fail if attempted after release.
	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	result, err := ResolveForRun("0.4.0", "amd64")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(result.TmpDir) })
	assert.Equal(t, SourceReleaseDownload, result.Source)
}

func TestDownloadRelease_ExceedsMaxSize(t *testing.T) {
	origLimit := maxDownloadSize
	maxDownloadSize = 512
	t.Cleanup(func() { maxDownloadSize = origLimit })

	content := bytes.Repeat([]byte("x"), 2000)

	var tarBuf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&tarBuf, gzip.NoCompression)
	require.NoError(t, err)
	tw := tar.NewWriter(gw)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "fullsend",
		Size:     int64(len(content)),
		Mode:     0o755,
		Typeflag: tar.TypeReg,
	}))
	_, err = tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	tarBytes := tarBuf.Bytes()
	h := sha256.Sum256(tarBytes)
	checksumBody := fmt.Sprintf("%s  fullsend_1.0.0_linux_amd64.tar.gz\n", hex.EncodeToString(h[:]))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1.0.0/checksums.txt" {
			fmt.Fprint(w, checksumBody)
		} else if r.URL.Path == "/v1.0.0/fullsend_1.0.0_linux_amd64.tar.gz" {
			w.Write(tarBytes)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	withTestReleaseServer(t, srv)

	destPath := filepath.Join(t.TempDir(), "fullsend")
	err = DownloadRelease("1.0.0", "amd64", destPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum size")
}

func TestResolveForRun_CrossCompileFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cross-compilation in short mode")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	withTestReleaseServer(t, srv)

	result, err := ResolveForRun("0.4.0", "amd64")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(result.TmpDir) })
	assert.Equal(t, SourceCheckoutBuild, result.Source)
}

func TestResolveForRun_LatestReleaseFallback(t *testing.T) {
	var tarBuf bytes.Buffer
	gw := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gw)
	content := []byte("latest release binary")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "fullsend",
		Size:     int64(len(content)),
		Mode:     0o755,
		Typeflag: tar.TypeReg,
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	tarBytes := tarBuf.Bytes()
	h := sha256.Sum256(tarBytes)
	correctHash := hex.EncodeToString(h[:])
	checksumBody := fmt.Sprintf("%s  fullsend_9.9.9_linux_amd64.tar.gz\n", correctHash)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/fullsend-ai/fullsend/releases/latest" {
			fmt.Fprint(w, `{"tag_name":"v9.9.9"}`)
		} else if r.URL.Path == "/v9.9.9/checksums.txt" {
			fmt.Fprint(w, checksumBody)
		} else if r.URL.Path == "/v9.9.9/fullsend_9.9.9_linux_amd64.tar.gz" {
			w.Write(tarBytes)
		} else {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	withTestReleaseServer(t, srv)

	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	result, err := ResolveForRun("dev", "amd64")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(result.TmpDir) })
	assert.Equal(t, SourceReleaseDownload, result.Source)
}

func TestResolveForRun_AllStrategiesFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	withTestReleaseServer(t, srv)

	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	_, err = ResolveForRun("dev", "amd64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "all strategies failed")
}

func TestResolveExplicit_ValidatesELF(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "not-elf")
	require.NoError(t, os.WriteFile(tmp, []byte("not binary"), 0o644))
	err := ResolveExplicit(tmp, "amd64")
	require.Error(t, err)
}

func TestExtractSourceTreeRejectsOversizedFile(t *testing.T) {
	origMax := maxDownloadSize
	maxDownloadSize = 64
	t.Cleanup(func() { maxDownloadSize = origMax })

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "fullsend-repo/large.bin",
		Typeflag: tar.TypeReg,
		Size:     128,
		Mode:     0o644,
	}))
	_, err := tw.Write(bytes.Repeat([]byte("x"), 128))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	dest := t.TempDir()
	err = extractSourceTree(bytes.NewReader(buf.Bytes()), dest)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum size")
}

func TestExtractSourceTreeExtractsSmallFile(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	content := []byte("hello")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "fullsend-repo/README.md",
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
		Mode:     0o644,
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	dest := t.TempDir()
	require.NoError(t, extractSourceTree(bytes.NewReader(buf.Bytes()), dest))

	data, err := os.ReadFile(filepath.Join(dest, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestCopyDirContentsPreservesMode(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	script := filepath.Join(src, "run.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755))

	require.NoError(t, copyDirContents(src, dst))

	info, err := os.Stat(filepath.Join(dst, "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}

func TestPathWithinDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "extract")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	assert.True(t, pathWithinDir(dir, dir))
	assert.True(t, pathWithinDir(dir, filepath.Join(dir, "nested", "file.txt")))
	assert.False(t, pathWithinDir(dir, filepath.Join(filepath.Dir(dir), "escape.txt")))
	assert.False(t, pathWithinDir(dir, "/etc/passwd"))
}

func TestExtractSourceTreeAggregateSizeLimit(t *testing.T) {
	origMax := maxDownloadSize
	maxDownloadSize = 512
	t.Cleanup(func() { maxDownloadSize = origMax })

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	chunk := bytes.Repeat([]byte("x"), 300)
	for i := range 3 {
		name := fmt.Sprintf("fullsend-repo/part-%d.bin", i)
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Size:     int64(len(chunk)),
			Mode:     0o644,
		}))
		_, err := tw.Write(chunk)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	dest := t.TempDir()
	err := extractSourceTree(bytes.NewReader(buf.Bytes()), dest)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "aggregate extracted size exceeds maximum")
}

func TestFetchSourceTree_ExtractsArchive(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	content := []byte("module root")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "fullsend-1.0.0/go.mod",
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
		Mode:     0o644,
	}))
	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1.0.0.tar.gz" {
			w.Write(buf.Bytes())
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	origBase := SourceArchiveBaseURL
	SourceArchiveBaseURL = srv.URL
	t.Cleanup(func() { SourceArchiveBaseURL = origBase })

	dest := t.TempDir()
	require.NoError(t, FetchSourceTree("1.0.0", dest))

	data, err := os.ReadFile(filepath.Join(dest, "go.mod"))
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestFetchSourceTree_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	origBase := SourceArchiveBaseURL
	SourceArchiveBaseURL = srv.URL
	t.Cleanup(func() { SourceArchiveBaseURL = origBase })

	err := FetchSourceTree("9.9.9", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned 404")
}

// Ensure io is used in download tests.
var _ = io.Discard
