package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testClientID     = "oauth-client-id"
	testClientSecret = "oauth-client-secret-value"
)

// oauthFixture adds an Atlassian token endpoint (/oauth/token) to a
// gatewayFixture. Each successful grant issues access-token-N, N counting
// from 1.
type oauthFixture struct {
	*gatewayFixture
	grants    atomic.Int32
	status    atomic.Int32
	body      atomic.Value // string; overrides the issued token when non-empty
	expiresIn atomic.Int64
	lastGrant atomic.Value // map[string]string
	lastCType atomic.Value // string
	lastAuth  atomic.Value // string
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	f := &oauthFixture{gatewayFixture: newGatewayFixture(t)}
	f.status.Store(http.StatusOK)
	f.body.Store("")
	f.expiresIn.Store(3600)
	f.mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var grant map[string]string
		_ = json.NewDecoder(r.Body).Decode(&grant)
		f.lastGrant.Store(grant)
		f.lastCType.Store(r.Header.Get("Content-Type"))
		f.lastAuth.Store(r.Header.Get("Authorization"))
		n := f.grants.Add(1)
		w.WriteHeader(int(f.status.Load()))
		if b := f.body.Load().(string); b != "" {
			_, _ = w.Write([]byte(b))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"access-token-` + strconv.Itoa(int(n)) +
			`","token_type":"Bearer","expires_in":` + strconv.FormatInt(f.expiresIn.Load(), 10) + `}`))
	})
	return f
}

func (f *oauthFixture) tokenURL() string { return f.srv.URL + "/oauth/token" }

func (f *oauthFixture) client(t *testing.T) *LiveClient {
	t.Helper()
	c, err := NewScoped(ScopedConfig{BaseURL: f.siteURL(), APIURL: f.gatewayURL()},
		Credentials{ClientID: testClientID, ClientSecret: testClientSecret},
		WithOAuthTokenURL(f.tokenURL()))
	require.NoError(t, err)
	return c
}

func TestOAuth_ExchangesClientCredentialsAndUsesBearerToken(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	var gotAuth atomic.Value
	f.handleMyself(t, &gotAuth)

	user, err := f.client(t).GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "acct-1", user.AccountID)
	assert.Equal(t, "Bearer access-token-1", gotAuth.Load())
	assert.Equal(t, map[string]string{
		"grant_type":    "client_credentials",
		"client_id":     testClientID,
		"client_secret": testClientSecret,
	}, f.lastGrant.Load())
	assert.Equal(t, "application/json", f.lastCType.Load())
	assert.Equal(t, "", f.lastAuth.Load(), "the token endpoint must not receive an Authorization header")
	assert.Equal(t, "", f.tenantAuth.Load(), "tenant_info must not receive credentials")
}

func TestOAuth_CachesTokenWithinValidity(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	var gotAuth atomic.Value
	f.handleMyself(t, &gotAuth)
	client := f.client(t)

	for range 3 {
		_, err := client.GetMyself(context.Background())
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), f.grants.Load())
	assert.Equal(t, "Bearer access-token-1", gotAuth.Load())
}

func TestOAuth_ConcurrentRequestsShareOneGrant(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	var gotAuth atomic.Value
	f.handleMyself(t, &gotAuth)
	client := f.client(t)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := client.GetMyself(context.Background())
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), f.grants.Load())
}

func TestOAuth_RefreshesExpiredToken(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	var gotAuth atomic.Value
	f.handleMyself(t, &gotAuth)
	client := f.client(t)

	now := time.Now()
	client.oauth.now = func() time.Time { return now }
	_, err := client.GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer access-token-1", gotAuth.Load())

	// Still valid shortly before the skewed expiry.
	now = now.Add(3600*time.Second - maxOAuthExpirySkew - time.Second)
	_, err = client.GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(1), f.grants.Load())

	// Replaced once within the skew window of the stated expiry.
	now = now.Add(2 * time.Second)
	_, err = client.GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(2), f.grants.Load())
	assert.Equal(t, "Bearer access-token-2", gotAuth.Load())
}

func TestOAuth_ShortLifetimeUsesHalfAsSkew(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	f.expiresIn.Store(10)
	var gotAuth atomic.Value
	f.handleMyself(t, &gotAuth)
	client := f.client(t)

	now := time.Now()
	client.oauth.now = func() time.Time { return now }
	_, err := client.GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, now.Add(5*time.Second), client.oauth.expiry)
}

func TestOAuth_CapsImplausibleLifetime(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	f.expiresIn.Store(1 << 62)
	var gotAuth atomic.Value
	f.handleMyself(t, &gotAuth)
	client := f.client(t)

	now := time.Now()
	client.oauth.now = func() time.Time { return now }
	_, err := client.GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, now.Add(maxOAuthTokenLifetime-maxOAuthExpirySkew), client.oauth.expiry)
}

func TestOAuth_UnauthorizedTriggersExactlyOneRefresh(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	var seen []string
	var mu sync.Mutex
	// The gateway rejects access-token-1 (revoked early) but accepts later
	// tokens.
	f.mux.HandleFunc("/gw/ex/jira/"+testCloudID+"/rest/api/3/issue/PROJ-1/comment", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer access-token-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(t, w, http.StatusCreated, Comment{ID: "c1"})
	})
	client := f.client(t)

	// A POST is retried after a 401 too: the request was not processed.
	_, err := client.CreateComment(context.Background(), "PROJ-1", "hello")
	require.NoError(t, err)
	assert.Equal(t, []string{"Bearer access-token-1", "Bearer access-token-2"}, seen)
	assert.Equal(t, int32(2), f.grants.Load())
}

func TestOAuth_PersistentUnauthorizedIsNotRetriedAgain(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	var hits atomic.Int32
	f.mux.HandleFunc("/gw/ex/jira/"+testCloudID+"/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	client := f.client(t)

	_, err := client.GetMyself(context.Background())
	require.Error(t, err)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
	assert.Equal(t, int32(2), hits.Load())
	assert.Equal(t, int32(2), f.grants.Load())
}

func TestToken_UnauthorizedIsNotRetried(t *testing.T) {
	t.Parallel()
	f := newGatewayFixture(t)
	var hits atomic.Int32
	f.mux.HandleFunc("/gw/ex/jira/"+testCloudID+"/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	client, err := NewScoped(ScopedConfig{BaseURL: f.siteURL(), APIURL: f.gatewayURL()}, Credentials{Token: testToken})
	require.NoError(t, err)

	_, err = client.GetMyself(context.Background())
	require.Error(t, err)
	assert.Equal(t, int32(1), hits.Load())
}

func TestOAuth_TokenEndpointErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
		notErr  string
	}{
		{name: "invalid client", status: http.StatusUnauthorized, body: `{"error":"invalid_client","error_description":"bad secret ` + testClientSecret + `"}`, wantErr: "unexpected status 401 (invalid_client)", notErr: "bad secret"},
		{name: "unsafe error code is not quoted", status: http.StatusBadRequest, body: `{"error":"Injected\nline"}`, wantErr: "unexpected status 400", notErr: "Injected"},
		{name: "non-JSON error body", status: http.StatusBadGateway, body: `<html>oops</html>`, wantErr: "unexpected status 502", notErr: "oops"},
		{name: "malformed JSON", status: http.StatusOK, body: `not json`, wantErr: "malformed response"},
		{name: "missing access token", status: http.StatusOK, body: `{"token_type":"Bearer","expires_in":3600}`, wantErr: "no access_token"},
		{name: "unsupported token type", status: http.StatusOK, body: `{"access_token":"x","token_type":"mac","expires_in":3600}`, wantErr: "unsupported token_type"},
		{name: "missing expiry", status: http.StatusOK, body: `{"access_token":"x","token_type":"Bearer"}`, wantErr: "no positive expires_in"},
		{name: "oversized body", status: http.StatusOK, body: `{"access_token":"` + strings.Repeat("a", maxOAuthTokenBytes) + `"}`, wantErr: "exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newOAuthFixture(t)
			f.status.Store(int32(tt.status))
			f.body.Store(tt.body)
			var gotAuth atomic.Value
			f.handleMyself(t, &gotAuth)

			_, err := f.client(t).GetMyself(context.Background())
			require.Error(t, err)
			msg := err.Error()
			assert.Contains(t, msg, "OAuth token request")
			assert.Contains(t, msg, ClientIDEnvVar)
			assert.Contains(t, msg, ClientSecretEnvVar)
			assert.Contains(t, msg, tt.wantErr)
			assert.NotContains(t, msg, testClientSecret)
			assert.NotContains(t, msg, testClientID)
			if tt.notErr != "" {
				assert.NotContains(t, msg, tt.notErr)
			}
			assert.Nil(t, gotAuth.Load(), "no REST request may be sent without an access token")
		})
	}
}

func TestOAuth_FailureIsNotCached(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	f.status.Store(http.StatusServiceUnavailable)
	f.body.Store(`{}`)
	var gotAuth atomic.Value
	f.handleMyself(t, &gotAuth)
	client := f.client(t)

	_, err := client.GetMyself(context.Background())
	require.Error(t, err)

	f.status.Store(http.StatusOK)
	f.body.Store("")
	_, err = client.GetMyself(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer access-token-2", gotAuth.Load())
}

func TestOAuth_TransportError(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	client := f.client(t)
	// Discover the cloud ID first, so the closed server fails the token
	// request rather than tenant_info.
	client.cloudID = testCloudID
	f.srv.Close()

	_, err := client.GetMyself(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OAuth token request")
	assert.NotContains(t, err.Error(), testClientSecret)
}

func TestOAuth_WaitHonoursContext(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	client := f.client(t)

	// Simulate another caller holding the token request slot.
	client.oauth.sem <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.oauth.token(ctx, client.httpClient, "")
	require.ErrorIs(t, err, context.Canceled)
}

func TestNewScoped_OAuthDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	creds := Credentials{ClientID: testClientID, ClientSecret: testClientSecret}

	client, err := NewScoped(ScopedConfig{BaseURL: "https://example.atlassian.net"}, creds)
	require.NoError(t, err)
	require.NotNil(t, client.oauth)
	assert.Equal(t, DefaultOAuthTokenURL, client.oauth.tokenURL)
	assert.Equal(t, DefaultAPIURL, client.gatewayURL)
	assert.Equal(t, "", client.token)

	_, err = NewScoped(ScopedConfig{BaseURL: "https://example.atlassian.net"}, creds,
		WithOAuthTokenURL("http://auth.example.com/oauth/token"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OAuth token URL")

	_, err = NewScoped(ScopedConfig{BaseURL: "https://example.atlassian.net"},
		Credentials{ClientID: testClientID})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must both be set")

	_, err = NewScoped(ScopedConfig{}, creds)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base URL must be set")
}

func TestWithOAuthTokenURL_IgnoredForToken(t *testing.T) {
	t.Parallel()
	client, err := NewScoped(ScopedConfig{BaseURL: "https://example.atlassian.net"}, Credentials{Token: testToken},
		WithOAuthTokenURL("http://insecure.example.com"))
	require.NoError(t, err)
	assert.Nil(t, client.oauth)
}

func TestCredentialsFromEnv_OAuth(t *testing.T) {
	t.Parallel()
	env := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}
	secretsOf := []string{testToken, testEmail, testClientID, testClientSecret}

	t.Run("client credentials trimmed", func(t *testing.T) {
		t.Parallel()
		creds, err := CredentialsFromEnv(env(map[string]string{
			ClientIDEnvVar:     " " + testClientID + "\n",
			ClientSecretEnvVar: testClientSecret + "\n",
		}))
		require.NoError(t, err)
		assert.Equal(t, Credentials{ClientID: testClientID, ClientSecret: testClientSecret}, creds)
		assert.True(t, creds.OAuth())
	})

	errCases := []struct {
		name    string
		vals    map[string]string
		wantErr string
	}{
		{
			name:    "token and client credentials",
			vals:    map[string]string{TokenEnvVar: testToken, ClientIDEnvVar: testClientID, ClientSecretEnvVar: testClientSecret},
			wantErr: "not both",
		},
		{
			name:    "token and client ID only",
			vals:    map[string]string{TokenEnvVar: testToken, ClientIDEnvVar: testClientID},
			wantErr: "not both",
		},
		{
			name:    "client ID only",
			vals:    map[string]string{ClientIDEnvVar: testClientID},
			wantErr: "must both be set",
		},
		{
			name:    "client secret only",
			vals:    map[string]string{ClientSecretEnvVar: testClientSecret},
			wantErr: "must both be set",
		},
		{
			name:    "whitespace-only client secret",
			vals:    map[string]string{ClientIDEnvVar: testClientID, ClientSecretEnvVar: " \n"},
			wantErr: "must both be set",
		},
		{
			name:    "email with client credentials",
			vals:    map[string]string{EmailEnvVar: testEmail, ClientIDEnvVar: testClientID, ClientSecretEnvVar: testClientSecret},
			wantErr: EmailEnvVar + " applies only to " + TokenEnvVar,
		},
		{
			name:    "nothing set",
			vals:    map[string]string{},
			wantErr: TokenEnvVar + " is required",
		},
	}
	for _, tt := range errCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := CredentialsFromEnv(env(tt.vals))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			for _, s := range secretsOf {
				assert.NotContains(t, err.Error(), s)
			}
		})
	}
}

func TestMintAccessToken_ReturnsTokenAndExpiry(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	f.expiresIn.Store(3600)
	before := time.Now()
	tok, err := MintAccessToken(context.Background(), nil, f.tokenURL(),
		Credentials{ClientID: testClientID, ClientSecret: testClientSecret})
	require.NoError(t, err)
	assert.Equal(t, "access-token-1", tok.Value)
	assert.WithinDuration(t, before.Add(time.Hour), tok.ExpiresAt, 5*time.Second)
	assert.False(t, tok.ExpiresAt.After(time.Now().Add(time.Hour)), "the expiry counts from before the request, never later")
	grant, _ := f.lastGrant.Load().(map[string]string)
	assert.Equal(t, "client_credentials", grant["grant_type"])
	assert.Equal(t, testClientID, grant["client_id"])
	assert.Equal(t, testClientSecret, grant["client_secret"])

	// No caching: each call is a fresh grant.
	tok2, err := MintAccessToken(context.Background(), nil, f.tokenURL(),
		Credentials{ClientID: testClientID, ClientSecret: testClientSecret})
	require.NoError(t, err)
	assert.Equal(t, "access-token-2", tok2.Value)
}

func TestMintAccessToken_Validation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, err := MintAccessToken(ctx, nil, "", Credentials{Token: "static"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), ClientIDEnvVar)

	_, err = MintAccessToken(ctx, nil, "", Credentials{ClientID: testClientID})
	require.Error(t, err)
	assert.Contains(t, err.Error(), ClientSecretEnvVar)

	_, err = MintAccessToken(ctx, nil, "http://auth.example.com/oauth/token",
		Credentials{ClientID: testClientID, ClientSecret: testClientSecret})
	require.Error(t, err, "a non-loopback http token URL is refused")
	assert.NotContains(t, err.Error(), testClientSecret)
}

func TestMintAccessToken_ErrorsNeverCarryTheSecret(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	f.status.Store(http.StatusUnauthorized)
	f.body.Store(`{"error":"invalid_client","error_description":"` + testClientSecret + `"}`)
	_, err := MintAccessToken(context.Background(), nil, f.tokenURL(),
		Credentials{ClientID: testClientID, ClientSecret: testClientSecret})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_client")
	assert.NotContains(t, err.Error(), testClientSecret)
	assert.NotContains(t, err.Error(), testClientID)
}
