package preset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fetchHTTPSWithTestTLS(ctx context.Context, t *testing.T, srv *httptest.Server, rawURL string, skipIPCheck bool) ([]byte, error) {
	t.Helper()
	transport, ok := srv.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("httptest TLS client did not provide an HTTP transport")
	}
	return fetchHTTPSWithTLSConfig(ctx, rawURL, skipIPCheck, transport.TLSClientConfig)
}

func TestFetch_LocalFile(t *testing.T) {
	dir := t.TempDir()
	content := "version: \"1\"\nruntime: claude\n"
	path := filepath.Join(dir, "preset.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	data, err := Fetch(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestFetch_LocalFileMissing(t *testing.T) {
	_, err := Fetch(context.Background(), "/nonexistent/path/preset.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading preset file")
}

func TestFetch_LocalFileEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yaml")
	require.NoError(t, os.WriteFile(path, []byte{}, 0o644))

	_, err := Fetch(context.Background(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is empty")
}

func TestFetch_HTTPS(t *testing.T) {
	content := "version: \"1\"\nruntime: claude\n"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(content))
	}))
	defer srv.Close()

	data, err := fetchHTTPSWithTestTLS(context.Background(), t, srv, srv.URL+"/preset.yaml", true)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestFetch_HTTPSNotFound(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchHTTPSWithTestTLS(context.Background(), t, srv, srv.URL+"/preset.yaml", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404")
}

func TestFetch_HTTPSEmpty(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := fetchHTTPSWithTestTLS(context.Background(), t, srv, srv.URL+"/empty.yaml", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is empty")
}

func TestFetch_HTTPSUnavailable(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("version: \"1\"\n"))
	}))
	url := srv.URL + "/preset.yaml"
	srv.Close()

	_, err := fetchHTTPSWithTestTLS(context.Background(), t, srv, url, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching preset")
}

func TestFetch_UnsupportedScheme(t *testing.T) {
	_, err := Fetch(context.Background(), "ftp://example.com/preset.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported URL scheme")
}

func TestFetch_HTTPSchemeRejected(t *testing.T) {
	_, err := Fetch(context.Background(), "http://example.com/preset.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported URL scheme")
}

func TestFetch_LocalFileExceedsMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.yaml")
	data := make([]byte, maxSize+1)
	for i := range data {
		data[i] = 'a'
	}
	require.NoError(t, os.WriteFile(path, data, 0o644))

	_, err := Fetch(context.Background(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum size")
}

func TestFetch_HTTPSExceedsMaxSize(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, maxSize+1))
	}))
	defer srv.Close()

	_, err := fetchHTTPSWithTestTLS(context.Background(), t, srv, srv.URL+"/large.yaml", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum size")
}

func TestFetch_HTTPSRedirectToHTTP_Rejected(t *testing.T) {
	httpTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("should not reach here"))
	}))
	defer httpTarget.Close()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpTarget.URL+"/preset.yaml", http.StatusFound)
	}))
	defer srv.Close()

	_, err := fetchHTTPSWithTestTLS(context.Background(), t, srv, srv.URL+"/preset.yaml", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-HTTPS")
}

func TestValidateHash_Match(t *testing.T) {
	data := []byte("hello world")
	hash := sha256.Sum256(data)
	hexHash := hex.EncodeToString(hash[:])

	err := ValidateHash(data, hexHash)
	require.NoError(t, err)
}

func TestValidateHash_MatchUppercase(t *testing.T) {
	data := []byte("hello world")
	hash := sha256.Sum256(data)
	hexHash := strings.ToUpper(hex.EncodeToString(hash[:]))

	err := ValidateHash(data, hexHash)
	require.NoError(t, err)
}

func TestValidateHash_Mismatch(t *testing.T) {
	data := []byte("hello world")
	wrongHash := strings.Repeat("ab", 32)

	err := ValidateHash(data, wrongHash)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preset hash mismatch")
}

func TestValidateHash_InvalidLength(t *testing.T) {
	err := ValidateHash([]byte("data"), "abc123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "64-character")
}

func TestValidateHash_InvalidHex(t *testing.T) {
	err := ValidateHash([]byte("data"), strings.Repeat("zz", 32))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid hex")
}

func TestValidateYAML_Valid(t *testing.T) {
	err := ValidateYAML([]byte("version: \"1\"\nruntime: claude\n"))
	require.NoError(t, err)
}

func TestValidateYAML_Invalid(t *testing.T) {
	err := ValidateYAML([]byte(":\n  - :\n  bad: [unclosed"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid YAML")
}

func TestIsRemote(t *testing.T) {
	assert.True(t, IsRemote("https://example.com/preset.yaml"))
	// Fetch itself only ever treats https:// as remote: any other
	// "://" scheme is an unsupported-scheme error, not a fetch. IsRemote
	// mirrors that so it never disagrees with what Fetch would actually do.
	assert.False(t, IsRemote("http://example.com/preset.yaml"))
	assert.False(t, IsRemote("/local/path/preset.yaml"))
	assert.False(t, IsRemote("relative/path.yaml"))
	assert.False(t, IsRemote("://not-a-url"))
	// Regression: a Windows-style path parses with scheme "C" under
	// url.Parse, which previously made IsRemote misreport it as remote
	// even though Fetch treats it as a local path (no https:// prefix).
	assert.False(t, IsRemote(`C:\presets\org.yaml`))
}

func TestLoad_LocalWithHash(t *testing.T) {
	content := "version: \"1\"\nruntime: claude\n"
	hash := sha256.Sum256([]byte(content))
	path := filepath.Join(t.TempDir(), "preset.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	data, err := Load(context.Background(), path, hex.EncodeToString(hash[:]))
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestLoad_InvalidHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preset.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: \"1\"\n"), 0o644))

	_, err := Load(context.Background(), path, strings.Repeat("ab", 32))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preset hash mismatch")
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preset.yaml")
	require.NoError(t, os.WriteFile(path, []byte(":\n  - :\n"), 0o644))

	_, err := Load(context.Background(), path, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid YAML")
}

func TestLoad_FetchError(t *testing.T) {
	_, err := Load(context.Background(), "/nonexistent/path/preset.yaml", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading preset file")
}

func TestFetch_InvalidHTTPSURL(t *testing.T) {
	_, err := Fetch(context.Background(), "https://example.com/preset.yaml\x00")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching preset")
}

func TestFetch_CanceledContext(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("version: \"1\"\n"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fetchHTTPSWithTestTLS(ctx, t, srv, srv.URL+"/preset.yaml", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetching preset")
}

func TestFetch_HTTPSTooManyRedirects(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.String(), http.StatusFound)
	}))
	defer srv.Close()

	_, err := fetchHTTPSWithTestTLS(context.Background(), t, srv, srv.URL+"/loop.yaml", true)
	require.Error(t, err)
}

func TestApply_ByteForByteInstall(t *testing.T) {
	t.Parallel()
	// Comments and ordering must survive; Apply must not re-encode YAML.
	preset := []byte("# vendor comment\nversion: \"1\"\nruntime: claude\nroles:\n  - triage\n")
	overlay := []byte("version: \"1\"\nruntime: pi\n# repo overlay\n")

	plan := Apply(preset, nil, overlay)
	assert.Equal(t, preset, plan.Base, "base must be the preset bytes, not a merge or remarshal")
	assert.Equal(t, overlay, plan.Overlay, "overlay must be preserved unchanged")
	assert.True(t, plan.BaseChanged)
	assert.Equal(t, BasePath, ".fullsend/config.base.yaml")
	assert.Equal(t, OverlayPath, ".fullsend/config.yaml")
}

func TestApply_ReplacesChangedBase(t *testing.T) {
	t.Parallel()
	old := []byte("version: \"1\"\nruntime: claude\nroles:\n  - triage\n")
	overlay := []byte("version: \"1\"\n# keep me\nkill_switch: true\n")
	next := []byte("version: \"1\"\nruntime: pi\n")

	plan := Apply(next, old, overlay)
	assert.Equal(t, next, plan.Base, "changed source replaces the complete base; leftover keys must not remain")
	assert.Equal(t, overlay, plan.Overlay)
	assert.True(t, plan.BaseChanged)
}

func TestApply_UnchangedBase(t *testing.T) {
	t.Parallel()
	content := []byte("version: \"1\"\nruntime: claude\n")
	overlay := []byte("version: \"1\"\n")

	plan := Apply(content, content, overlay)
	assert.Equal(t, content, plan.Base)
	assert.Equal(t, overlay, plan.Overlay)
	assert.False(t, plan.BaseChanged)
}

func TestApply_DoesNotMutateInputs(t *testing.T) {
	t.Parallel()
	preset := []byte("version: \"1\"\n")
	existing := []byte("old\n")
	overlay := []byte("overlay\n")

	plan := Apply(preset, existing, overlay)
	plan.Base[0] = 'X'
	plan.Overlay[0] = 'Y'
	assert.Equal(t, "version: \"1\"\n", string(preset))
	assert.Equal(t, "overlay\n", string(overlay))
}

// --- SSRF hardening: fetchHTTPS ---

func TestFetchHTTPS_BlocksLoopbackTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("version: \"1\"\n"))
	}))
	defer srv.Close()

	// skipIPCheck=false: production behavior. The test server is bound to
	// loopback, so the resolved-IP validation must reject it exactly as it
	// would reject an attacker-supplied loopback target.
	_, err := fetchHTTPS(context.Background(), srv.URL+"/preset.yaml", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked")
}

func TestFetchHTTPS_BlocksPrivateIPTarget(t *testing.T) {
	// A private-range IP literal never needs a live listener: safeDialContext
	// rejects it before any network I/O occurs.
	_, err := fetchHTTPS(context.Background(), "https://10.1.2.3/preset.yaml", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked")
}

func TestFetchHTTPS_BlocksLinkLocalMetadataTarget(t *testing.T) {
	// 169.254.169.254 is the cloud-metadata address; must be blocked even
	// without a listener present.
	_, err := fetchHTTPS(context.Background(), "https://169.254.169.254/preset.yaml", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked")
}

func TestFetchHTTPS_SkipIPCheckAllowsLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("version: \"1\"\n"))
	}))
	defer srv.Close()

	data, err := fetchHTTPS(context.Background(), srv.URL+"/preset.yaml", true)
	require.NoError(t, err)
	assert.Equal(t, "version: \"1\"\n", string(data))
}

func TestFetchHTTPS_RejectsUserinfo(t *testing.T) {
	_, err := fetchHTTPS(context.Background(), "https://user:pass@example.com/preset.yaml", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "userinfo")
}

func TestFetchHTTPS_RedirectRejectsUserinfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://user:pass@internal.example/preset.yaml", http.StatusFound)
	}))
	defer srv.Close()

	_, err := fetchHTTPS(context.Background(), srv.URL+"/preset.yaml", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "userinfo")
}

func TestFetchHTTPS_IgnoresProxyEnv(t *testing.T) {
	content := "version: \"1\"\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(content))
	}))
	defer srv.Close()

	// Point HTTP_PROXY at an address nothing listens on. If fetchHTTPS
	// honored the environment proxy (the pre-fix behavior), the request
	// would be routed through it and fail to connect. Because Transport.Proxy
	// is forced to nil, the request must go straight to srv and succeed.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")

	data, err := fetchHTTPS(context.Background(), srv.URL+"/preset.yaml", true)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestSafeDialContext_BlocksLoopback(t *testing.T) {
	dial := safeDialContext(&net.Dialer{Timeout: time.Second}, false)
	_, err := dial(context.Background(), "tcp", "127.0.0.1:9999")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked")
}

func TestSafeDialContext_BlocksPrivateIP(t *testing.T) {
	dial := safeDialContext(&net.Dialer{Timeout: time.Second}, false)
	_, err := dial(context.Background(), "tcp", "10.1.2.3:443")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked")
}

func TestSafeDialContext_BlocksHTTPSRedirectTarget(t *testing.T) {
	redirectURL, err := url.Parse("https://10.1.2.3/preset.yaml")
	require.NoError(t, err)

	dial := safeDialContext(&net.Dialer{Timeout: time.Second}, false)
	_, err = dial(context.Background(), "tcp", net.JoinHostPort(redirectURL.Hostname(), "443"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked")
}

func TestSafeDialContext_BlocksLinkLocal(t *testing.T) {
	dial := safeDialContext(&net.Dialer{Timeout: time.Second}, false)
	_, err := dial(context.Background(), "tcp", "169.254.169.254:80")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked")
}

func TestSafeDialContext_SkipIPCheckAllowsLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			conn.Close()
		}
	}()

	dial := safeDialContext(&net.Dialer{Timeout: time.Second}, true)
	conn, err := dial(context.Background(), "tcp", ln.Addr().String())
	require.NoError(t, err)
	conn.Close()
}
