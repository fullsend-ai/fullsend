package cf

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient returns a LiveCloudflareAPIClient pointed at the given
// httptest.Server and sets CLOUDFLARE_API_TOKEN for the duration of t.
func newTestClient(t *testing.T, srv *httptest.Server) *LiveCloudflareAPIClient {
	t.Helper()
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	return &LiveCloudflareAPIClient{
		httpClient: srv.Client(),
		baseURL:    srv.URL,
	}
}

// --- cfAPIRequest tests ---

func TestCfAPIRequest_Success_NoBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	body, err := client.cfAPIRequest(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(body))
}

func TestCfAPIRequest_Success_WithBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		reqBody, _ := io.ReadAll(r.Body)
		var parsed map[string]string
		_ = json.Unmarshal(reqBody, &parsed)
		assert.Equal(t, "bar", parsed["foo"])
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.cfAPIRequest(context.Background(), http.MethodPost, srv.URL+"/test", map[string]string{"foo": "bar"})
	require.NoError(t, err)
}

func TestCfAPIRequest_NonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.cfAPIRequest(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cloudflare API returned 403")
}

func TestCfAPIRequest_MissingTokenAndWranglerFails(t *testing.T) {
	// When CLOUDFLARE_API_TOKEN is unset and wrangler auth token fails,
	// cfAPIRequest should return an error from resolveAPIToken.
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	oldFn := WranglerAuthTokenFn
	WranglerAuthTokenFn = func(_ context.Context) (string, error) {
		return "", assert.AnError
	}
	defer func() { WranglerAuthTokenFn = oldFn }()

	client := &LiveCloudflareAPIClient{httpClient: http.DefaultClient}
	_, err := client.cfAPIRequest(context.Background(), http.MethodGet, "http://unused", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CLOUDFLARE_API_TOKEN is not set")
	assert.Contains(t, err.Error(), "wrangler auth token failed")
}

func TestCfAPIRequest_WranglerAuthFallback(t *testing.T) {
	// When CLOUDFLARE_API_TOKEN is unset, cfAPIRequest should fall back
	// to wrangler auth token. This is the core auth unification fix:
	// custom-domain operations (attach, remove, zone lookup) now work
	// with only `wrangler login`.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer wrangler-oauth-token", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	oldFn := WranglerAuthTokenFn
	WranglerAuthTokenFn = func(_ context.Context) (string, error) {
		return "wrangler-oauth-token", nil
	}
	defer func() { WranglerAuthTokenFn = oldFn }()

	client := &LiveCloudflareAPIClient{
		httpClient: srv.Client(),
		baseURL:    srv.URL,
	}
	body, err := client.cfAPIRequest(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(body))
}

// --- NewLiveCloudflareAPIClient ---

func TestNewLiveCloudflareAPIClient(t *testing.T) {
	client := NewLiveCloudflareAPIClient()
	require.NotNil(t, client)
	assert.NotNil(t, client.httpClient)
	assert.Empty(t, client.baseURL, "production client should use default base URL")
}

// --- cfBaseURL ---

func TestCfBaseURL_Default(t *testing.T) {
	client := NewLiveCloudflareAPIClient()
	assert.Equal(t, "https://api.cloudflare.com/client/v4", client.cfBaseURL())
}

func TestCfBaseURL_Override(t *testing.T) {
	client := &LiveCloudflareAPIClient{baseURL: "http://localhost:9999"}
	assert.Equal(t, "http://localhost:9999", client.cfBaseURL())
}

// --- AttachCustomDomain ---

func TestAttachCustomDomain_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/accounts/acc-123/workers/domains", r.URL.Path)

		reqBody, _ := io.ReadAll(r.Body)
		var parsed map[string]string
		require.NoError(t, json.Unmarshal(reqBody, &parsed))
		assert.Equal(t, "mint.example.com", parsed["hostname"])
		assert.Equal(t, "zone-456", parsed["zone_id"])
		assert.Equal(t, "my-worker", parsed["service"])
		assert.Equal(t, "production", parsed["environment"])

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.AttachCustomDomain(context.Background(), "acc-123", "my-worker", "zone-456", "mint.example.com")
	require.NoError(t, err)
}

func TestAttachCustomDomain_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"domain already in use"}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.AttachCustomDomain(context.Background(), "acc-123", "my-worker", "zone-456", "mint.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "attaching custom domain")
}

func TestAttachCustomDomain_WranglerAuthFallback(t *testing.T) {
	// AttachCustomDomain should work with wrangler auth when
	// CLOUDFLARE_API_TOKEN is unset.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer wrangler-token", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	oldFn := WranglerAuthTokenFn
	WranglerAuthTokenFn = func(_ context.Context) (string, error) {
		return "wrangler-token", nil
	}
	defer func() { WranglerAuthTokenFn = oldFn }()

	client := &LiveCloudflareAPIClient{
		httpClient: srv.Client(),
		baseURL:    srv.URL,
	}
	err := client.AttachCustomDomain(context.Background(), "acc-123", "my-worker", "zone-456", "mint.example.com")
	require.NoError(t, err)
}

// --- RemoveCustomDomain ---

func TestRemoveCustomDomain_Found(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1:
			// List domains.
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Contains(t, r.URL.Path, "/accounts/acc-1/workers/domains")
			assert.Equal(t, "mint.example.com", r.URL.Query().Get("hostname"))
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result":[{"id":"dom-42","hostname":"mint.example.com"}]}`))
		case 2:
			// Delete domain.
			assert.Equal(t, http.MethodDelete, r.Method)
			assert.Equal(t, "/accounts/acc-1/workers/domains/dom-42", r.URL.Path)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.RemoveCustomDomain(context.Background(), "acc-1", "mint.example.com")
	require.NoError(t, err)
	assert.Equal(t, 2, callCount)
}

func TestRemoveCustomDomain_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":[]}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.RemoveCustomDomain(context.Background(), "acc-1", "missing.example.com")
	require.NoError(t, err, "should be no-op when domain not found")
}

func TestRemoveCustomDomain_LookupError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"server error"}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.RemoveCustomDomain(context.Background(), "acc-1", "mint.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "looking up custom domain")
}

func TestRemoveCustomDomain_DeleteError(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		if callCount == 1 {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result":[{"id":"dom-42","hostname":"mint.example.com"}]}`))
		} else {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"forbidden"}`))
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	err := client.RemoveCustomDomain(context.Background(), "acc-1", "mint.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "removing custom domain")
}

// --- findCustomDomainID ---

func TestFindCustomDomainID_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/accounts/acc-1/workers/domains")
		assert.Equal(t, "mint.example.com", r.URL.Query().Get("hostname"))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":[{"id":"dom-1","hostname":"mint.example.com"}]}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	id, err := client.findCustomDomainID(context.Background(), "acc-1", "mint.example.com")
	require.NoError(t, err)
	assert.Equal(t, "dom-1", id)
}

func TestFindCustomDomainID_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":[]}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	id, err := client.findCustomDomainID(context.Background(), "acc-1", "missing.example.com")
	require.NoError(t, err)
	assert.Empty(t, id)
}

func TestFindCustomDomainID_NoHostnameMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":[{"id":"dom-1","hostname":"other.example.com"}]}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	id, err := client.findCustomDomainID(context.Background(), "acc-1", "mint.example.com")
	require.NoError(t, err)
	assert.Empty(t, id)
}

func TestFindCustomDomainID_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{bad-json`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.findCustomDomainID(context.Background(), "acc-1", "mint.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing domains response")
}

// --- URL encoding test for hostname ---

func TestFindCustomDomainID_URLEncodesHostname(t *testing.T) {
	// Verify that the hostname is URL-encoded in the query parameter.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The query parameter should be properly encoded.
		assert.Equal(t, "host+with+spaces", r.URL.Query().Get("hostname"))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":[]}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.findCustomDomainID(context.Background(), "acc-1", "host+with+spaces")
	require.NoError(t, err)
}

// --- LookupZoneID tests ---

func TestLookupZoneID_MatchesRootDomain(t *testing.T) {
	// "mint.fullsend.sh" should find zone "fullsend.sh".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		w.WriteHeader(http.StatusOK)
		if name == "fullsend.sh" {
			w.Write([]byte(`{"result":[{"id":"zone-123","name":"fullsend.sh"}]}`))
		} else {
			w.Write([]byte(`{"result":[]}`))
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	zoneID, err := client.LookupZoneID(context.Background(), "mint.fullsend.sh")
	require.NoError(t, err)
	assert.Equal(t, "zone-123", zoneID)
}

func TestLookupZoneID_MatchesExactDomain(t *testing.T) {
	// "example.com" should find zone "example.com" directly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		w.WriteHeader(http.StatusOK)
		if name == "example.com" {
			w.Write([]byte(`{"result":[{"id":"zone-abc","name":"example.com"}]}`))
		} else {
			w.Write([]byte(`{"result":[]}`))
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	zoneID, err := client.LookupZoneID(context.Background(), "example.com")
	require.NoError(t, err)
	assert.Equal(t, "zone-abc", zoneID)
}

func TestLookupZoneID_NotFound(t *testing.T) {
	// Domain not in account should return a clear error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":[]}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.LookupZoneID(context.Background(), "unknown.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zone not found")
	assert.Contains(t, err.Error(), "Cloudflare account")
}

func TestLookupZoneID_InvalidDomain(t *testing.T) {
	client := &LiveCloudflareAPIClient{}
	_, err := client.LookupZoneID(context.Background(), "localhost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must have at least two labels")
}

func TestLookupZoneID_DeepSubdomain(t *testing.T) {
	// "a.b.c.example.com" should walk up and find zone "example.com".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		w.WriteHeader(http.StatusOK)
		if name == "example.com" {
			w.Write([]byte(`{"result":[{"id":"zone-deep","name":"example.com"}]}`))
		} else {
			w.Write([]byte(`{"result":[]}`))
		}
	}))
	defer srv.Close()

	client := newTestClient(t, srv)
	zoneID, err := client.LookupZoneID(context.Background(), "a.b.c.example.com")
	require.NoError(t, err)
	assert.Equal(t, "zone-deep", zoneID)
}

// --- resolveAPIToken tests ---

func TestResolveAPIToken_EnvVar(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "env-token")
	token, err := resolveAPIToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "env-token", token)
}

func TestResolveAPIToken_WranglerFallback(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	oldFn := WranglerAuthTokenFn
	WranglerAuthTokenFn = func(_ context.Context) (string, error) {
		return "wrangler-token", nil
	}
	defer func() { WranglerAuthTokenFn = oldFn }()

	token, err := resolveAPIToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "wrangler-token", token)
}

func TestResolveAPIToken_BothFail(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	oldFn := WranglerAuthTokenFn
	WranglerAuthTokenFn = func(_ context.Context) (string, error) {
		return "", assert.AnError
	}
	defer func() { WranglerAuthTokenFn = oldFn }()

	_, err := resolveAPIToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CLOUDFLARE_API_TOKEN is not set")
	assert.Contains(t, err.Error(), "wrangler auth token failed")
}

func TestValidateHostname(t *testing.T) {
	tests := []struct {
		hostname string
		valid    bool
	}{
		{"mint.fullsend.sh", true},
		{"stage.mint.fullsend.sh", true},
		{"my-mint.fullsend.sh", true},
		{"a.co", true},
		{"foo.example.com", true},
		{"localhost", false},
		{"", false},
		{".fullsend.sh", false},
		{"fullsend.sh.", false},
		{"mint fullsend.sh", false},
		{"mint!@#.fullsend.sh", false},
		{"-mint.fullsend.sh", false},
	}

	for _, tc := range tests {
		t.Run(tc.hostname, func(t *testing.T) {
			assert.Equal(t, tc.valid, ValidateHostname(tc.hostname))
		})
	}
}

func TestTruncateErrorBody(t *testing.T) {
	short := "short error"
	assert.Equal(t, short, truncateErrorBody(short))

	long := strings.Repeat("x", 600)
	truncated := truncateErrorBody(long)
	assert.Len(t, truncated, maxErrorBodyLen+len("…[truncated]"))
	assert.True(t, strings.HasSuffix(truncated, "…[truncated]"))
}

func TestCfAPIRequest_ErrorResponseTruncated(t *testing.T) {
	// Verify that large error responses are truncated in the error message.
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")

	largeBody := strings.Repeat("x", 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(largeBody))
	}))
	defer srv.Close()

	client := &LiveCloudflareAPIClient{
		httpClient: srv.Client(),
		baseURL:    srv.URL,
	}

	_, err := client.cfAPIRequest(context.Background(), http.MethodGet, srv.URL+"/test", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "…[truncated]")
	// Error message should not contain the full 1000-char body.
	assert.Less(t, len(err.Error()), 700)
}
