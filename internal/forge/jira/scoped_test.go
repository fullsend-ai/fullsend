package jira

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testCloudID = "11111111-2222-3333-4444-555555555555"
	testToken   = "scoped-secret-token"
	testEmail   = "bot@example.com"
)

// gatewayFixture serves both the Jira site (tenant_info) and the API
// gateway from one httptest server. The gateway lives under gatewayPrefix
// so tests can tell the two apart.
type gatewayFixture struct {
	srv          *httptest.Server
	mux          *http.ServeMux
	tenantHits   atomic.Int32
	tenantAuth   atomic.Value // Authorization header seen by tenant_info
	tenantStatus atomic.Int32
	tenantBody   atomic.Value // string
}

func newGatewayFixture(t *testing.T) *gatewayFixture {
	t.Helper()
	f := &gatewayFixture{mux: http.NewServeMux()}
	f.tenantStatus.Store(http.StatusOK)
	f.tenantBody.Store(`{"cloudId":"` + testCloudID + `"}`)
	f.srv = httptest.NewServer(f.mux)
	t.Cleanup(f.srv.Close)
	f.mux.HandleFunc("/site/_edge/tenant_info", func(w http.ResponseWriter, r *http.Request) {
		f.tenantHits.Add(1)
		f.tenantAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(int(f.tenantStatus.Load()))
		_, _ = w.Write([]byte(f.tenantBody.Load().(string)))
	})
	return f
}

func (f *gatewayFixture) siteURL() string    { return f.srv.URL + "/site" }
func (f *gatewayFixture) gatewayURL() string { return f.srv.URL + "/gw" }

// handleMyself registers /myself under the gateway path layout and records
// the Authorization header it received.
func (f *gatewayFixture) handleMyself(t *testing.T, gotAuth *atomic.Value) {
	t.Helper()
	f.mux.HandleFunc("/gw/ex/jira/"+testCloudID+"/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		writeJSON(t, w, http.StatusOK, User{AccountID: "acct-1"})
	})
}

func TestNewScoped_BasicAuthWithEmail(t *testing.T) {
	t.Parallel()
	f := newGatewayFixture(t)
	var gotAuth atomic.Value
	f.handleMyself(t, &gotAuth)

	client, err := NewScoped(ScopedConfig{BaseURL: f.siteURL(), APIURL: f.gatewayURL()},
		Credentials{Token: testToken, Email: testEmail})
	require.NoError(t, err)

	user, err := client.GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "acct-1", user.AccountID)
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(testEmail+":"+testToken))
	assert.Equal(t, want, gotAuth.Load())
	assert.Equal(t, "", f.tenantAuth.Load(), "tenant_info must not receive credentials")
}

func TestNewScoped_BearerAuthWithoutEmail(t *testing.T) {
	t.Parallel()
	f := newGatewayFixture(t)
	var gotAuth atomic.Value
	f.handleMyself(t, &gotAuth)

	client, err := NewScoped(ScopedConfig{BaseURL: f.siteURL(), APIURL: f.gatewayURL()},
		Credentials{Token: testToken})
	require.NoError(t, err)

	_, err = client.GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+testToken, gotAuth.Load())
}

func TestNewScoped_DefaultAPIURL(t *testing.T) {
	t.Parallel()
	client, err := NewScoped(ScopedConfig{BaseURL: "https://example.atlassian.net"}, Credentials{Token: testToken})
	require.NoError(t, err)
	assert.Equal(t, DefaultAPIURL, client.gatewayURL)
	assert.Equal(t, "https://example.atlassian.net", client.baseURL)
}

func TestNewScoped_APIURLOverrideKeepsPathLayout(t *testing.T) {
	t.Parallel()
	f := newGatewayFixture(t)
	var gotAuth atomic.Value
	f.handleMyself(t, &gotAuth)

	// A trailing slash on the override must not produce a double slash.
	client, err := NewScoped(ScopedConfig{BaseURL: f.siteURL() + "/", APIURL: f.gatewayURL() + "/"},
		Credentials{Token: testToken})
	require.NoError(t, err)

	got, err := client.apiURL(context.Background(), "/issue/PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, f.gatewayURL()+"/ex/jira/"+testCloudID+"/rest/api/3/issue/PROJ-1", got)

	_, err = client.GetMyself(context.Background())
	require.NoError(t, err)
}

func TestNewScoped_RejectsInsecureAPIURL(t *testing.T) {
	t.Parallel()
	_, err := NewScoped(ScopedConfig{BaseURL: "https://example.atlassian.net", APIURL: "http://gateway.example.com"},
		Credentials{Token: testToken})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API gateway URL")
}

func TestNewScoped_RejectsMissingBaseURL(t *testing.T) {
	t.Parallel()
	_, err := NewScoped(ScopedConfig{}, Credentials{Token: testToken})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base URL must be set")
}

func TestNewScoped_RejectsEmptyToken(t *testing.T) {
	t.Parallel()
	_, err := NewScoped(ScopedConfig{BaseURL: "https://example.atlassian.net"}, Credentials{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token must not be empty")
}

func TestCloudID_CachedPerClient(t *testing.T) {
	t.Parallel()
	f := newGatewayFixture(t)
	var gotAuth atomic.Value
	f.handleMyself(t, &gotAuth)

	client, err := NewScoped(ScopedConfig{BaseURL: f.siteURL(), APIURL: f.gatewayURL()}, Credentials{Token: testToken})
	require.NoError(t, err)

	for range 3 {
		_, err := client.GetMyself(context.Background())
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), f.tenantHits.Load())
}

func TestCloudID_ConcurrentRequestsDiscoverOnce(t *testing.T) {
	t.Parallel()
	f := newGatewayFixture(t)
	var gotAuth atomic.Value
	f.handleMyself(t, &gotAuth)

	client, err := NewScoped(ScopedConfig{BaseURL: f.siteURL(), APIURL: f.gatewayURL()}, Credentials{Token: testToken})
	require.NoError(t, err)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := client.GetMyself(context.Background())
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), f.tenantHits.Load())
}

func TestCloudID_DiscoveryErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "non-200 status", status: http.StatusNotFound, body: `{}`, wantErr: "unexpected status 404"},
		{name: "malformed JSON", status: http.StatusOK, body: `not json`, wantErr: "malformed response"},
		{name: "missing cloudId", status: http.StatusOK, body: `{"other":"x"}`, wantErr: "no valid cloudId"},
		{name: "cloudId with path characters", status: http.StatusOK, body: `{"cloudId":"../../admin"}`, wantErr: "no valid cloudId"},
		{name: "oversized body", status: http.StatusOK, body: `{"cloudId":"` + strings.Repeat("a", maxTenantInfoBytes) + `"}`, wantErr: "exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newGatewayFixture(t)
			f.tenantStatus.Store(int32(tt.status))
			f.tenantBody.Store(tt.body)
			var gotAuth atomic.Value
			f.handleMyself(t, &gotAuth)

			client, err := NewScoped(ScopedConfig{BaseURL: f.siteURL(), APIURL: f.gatewayURL()},
				Credentials{Token: testToken, Email: testEmail})
			require.NoError(t, err)

			_, err = client.GetMyself(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "discover cloud ID")
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.NotContains(t, err.Error(), testToken)
			assert.NotContains(t, err.Error(), testEmail)
			assert.Nil(t, gotAuth.Load(), "no REST request may be sent without a cloud ID")
		})
	}
}

func TestCloudID_FailureIsNotCached(t *testing.T) {
	t.Parallel()
	f := newGatewayFixture(t)
	var gotAuth atomic.Value
	f.handleMyself(t, &gotAuth)
	f.tenantStatus.Store(http.StatusServiceUnavailable)

	client, err := NewScoped(ScopedConfig{BaseURL: f.siteURL(), APIURL: f.gatewayURL()}, Credentials{Token: testToken})
	require.NoError(t, err)

	_, err = client.GetMyself(context.Background())
	require.Error(t, err)

	f.tenantStatus.Store(http.StatusOK)
	_, err = client.GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(2), f.tenantHits.Load())
}

func TestCloudID_TransportError(t *testing.T) {
	t.Parallel()
	f := newGatewayFixture(t)
	site := f.siteURL()
	f.srv.Close()

	client, err := NewScoped(ScopedConfig{BaseURL: site, APIURL: site}, Credentials{Token: testToken})
	require.NoError(t, err)
	_, err = client.GetMyself(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discover cloud ID")
	assert.NotContains(t, err.Error(), testToken)
}

func TestCloudID_WaitHonoursContext(t *testing.T) {
	t.Parallel()
	client, err := NewScoped(ScopedConfig{BaseURL: "https://example.atlassian.net"}, Credentials{Token: testToken})
	require.NoError(t, err)

	// Simulate another caller holding the discovery slot.
	client.cloudSem <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.resolveCloudID(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSiteMode_UnchangedWithoutGateway(t *testing.T) {
	t.Parallel()
	client, err := New(testToken, WithBaseURL("https://example.atlassian.net"))
	require.NoError(t, err)
	got, err := client.apiURL(context.Background(), "/myself")
	require.NoError(t, err)
	assert.Equal(t, "https://example.atlassian.net/rest/api/3/myself", got)
}

func TestCredentialsFromEnv(t *testing.T) {
	t.Parallel()
	env := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}

	t.Run("token and email", func(t *testing.T) {
		t.Parallel()
		creds, err := CredentialsFromEnv(env(map[string]string{TokenEnvVar: " " + testToken + "\n", EmailEnvVar: testEmail + "\n"}))
		require.NoError(t, err)
		assert.Equal(t, Credentials{Token: testToken, Email: testEmail}, creds)
	})

	t.Run("token only selects service-account auth", func(t *testing.T) {
		t.Parallel()
		creds, err := CredentialsFromEnv(env(map[string]string{TokenEnvVar: testToken}))
		require.NoError(t, err)
		assert.Equal(t, Credentials{Token: testToken}, creds)
	})

	t.Run("missing token", func(t *testing.T) {
		t.Parallel()
		_, err := CredentialsFromEnv(env(map[string]string{EmailEnvVar: testEmail}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), TokenEnvVar)
		assert.NotContains(t, err.Error(), testEmail)
	})
}

func TestCredentialsFromEnv_DefaultsToOSEnv(t *testing.T) {
	t.Setenv(TokenEnvVar, testToken)
	t.Setenv(EmailEnvVar, "")
	creds, err := CredentialsFromEnv(nil)
	require.NoError(t, err)
	assert.Equal(t, Credentials{Token: testToken}, creds)
}
