package vertexauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testWIFProvider = "projects/123456789/locations/global/workloadIdentityPools/fullsend/providers/github"

// providerAllowedAudiences mirrors the allowedAudiences that
// internal/dispatch/gcf provisions on a provider: iamAudience() plus the
// mint audience. Google STS accepts a GitHub OIDC token only for these.
func providerAllowedAudiences(projectNumber, poolID, providerID string) []string {
	return []string{
		fmt.Sprintf("https://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
			projectNumber, poolID, providerID),
		"fullsend-mint",
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestPrepareGitHubWIF(t *testing.T) {
	const (
		requestToken = "runner-request-secret"
		subjectToken = "github-oidc-secret"
	)
	allowed := providerAllowedAudiences("123456789", "fullsend", "github")
	var oidcCalled, stsCalled bool
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oidcCalled = true
		assert.Equal(t, "Bearer "+requestToken, r.Header.Get("Authorization"))
		// A token minted for any other audience would be refused by STS, so
		// the fake refuses to mint it.
		if !slices.Contains(allowed, r.URL.Query().Get("audience")) {
			http.Error(w, "audience not allowed by provider", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"value":"`+subjectToken+`"}`)
	}))
	t.Cleanup(oidc.Close)

	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stsCalled = true
		require.NoError(t, r.ParseForm())
		assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", r.Form.Get("grant_type"))
		assert.Equal(t, "//iam.googleapis.com/"+testWIFProvider, r.Form.Get("audience"))
		assert.Equal(t, "https://www.googleapis.com/auth/cloud-platform", r.Form.Get("scope"))
		assert.Equal(t, "urn:ietf:params:oauth:token-type:jwt", r.Form.Get("subject_token_type"))
		assert.Equal(t, subjectToken, r.Form.Get("subject_token"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"google-access-token","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(sts.Close)

	var seenTokens []string
	env, cleanup, err := prepareGitHubWIF(context.Background(), Config{
		ProjectID:                "example-project",
		WorkloadIdentityProvider: testWIFProvider,
		OIDCRequestURL:           oidc.URL + "?api-version=2.0",
		OIDCRequestToken:         requestToken,
		TempDir:                  t.TempDir(),
		OnSubjectToken:           func(token string) { seenTokens = append(seenTokens, token) },
	}, prepareOptions{stsEndpoint: sts.URL, httpClient: oidc.Client()})
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	assert.Equal(t, []string{subjectToken}, seenTokens)
	assert.True(t, oidcCalled)
	assert.True(t, stsCalled)

	credentialsPath := env["GOOGLE_APPLICATION_CREDENTIALS"]
	tokenPath := env["GCP_OIDC_TOKEN_FILE"]
	authPath := env["FULLSEND_GCP_OIDC_AUTH_FILE"]
	require.FileExists(t, credentialsPath)
	require.FileExists(t, tokenPath)
	require.FileExists(t, authPath)
	assert.Equal(t, credentialsPath, env["GOOGLE_GHA_CREDS_PATH"])
	assert.Equal(t, credentialsPath, env["CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE"])
	for _, key := range []string{"CLOUDSDK_CORE_PROJECT", "CLOUDSDK_PROJECT", "GCLOUD_PROJECT", "GCP_PROJECT", "GOOGLE_CLOUD_PROJECT"} {
		assert.Equal(t, "example-project", env[key], key)
	}

	// The refresh loop re-requests with this URL, so it must carry an
	// audience the provider accepts.
	oidcURL, err := url.Parse(env["FULLSEND_GCP_OIDC_URL"])
	require.NoError(t, err)
	assert.Equal(t, "https://iam.googleapis.com/"+testWIFProvider, oidcURL.Query().Get("audience"))
	assert.Contains(t, allowed, oidcURL.Query().Get("audience"))

	for _, path := range []string{credentialsPath, tokenPath, authPath} {
		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), path)
	}

	var credentials map[string]any
	credentialsData, err := os.ReadFile(credentialsPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(credentialsData, &credentials))
	assert.Equal(t, "external_account", credentials["type"])
	assert.Equal(t, "//iam.googleapis.com/"+testWIFProvider, credentials["audience"])
	assert.Equal(t, sts.URL, credentials["token_url"])
	source := credentials["credential_source"].(map[string]any)
	assert.Equal(t, "/sandbox/workspace/.gcp-oidc-token", source["file"])
	format := source["format"].(map[string]any)
	assert.Equal(t, "json", format["type"])
	assert.Equal(t, "value", format["subject_token_field_name"])
	assert.NotContains(t, string(credentialsData), requestToken)
	assert.NotContains(t, string(credentialsData), subjectToken)

	tokenData, err := os.ReadFile(tokenPath)
	require.NoError(t, err)
	assert.JSONEq(t, `{"value":"`+subjectToken+`"}`, string(tokenData))
	authData, err := os.ReadFile(authPath)
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+requestToken, string(authData))

	root := filepath.Dir(credentialsPath)
	cleanup()
	cleanup()
	assert.NoDirExists(t, root)
}

func TestPrepareGitHubWIFRejectsInvalidInputs(t *testing.T) {
	valid := Config{
		ProjectID:                "example-project",
		WorkloadIdentityProvider: testWIFProvider,
		OIDCRequestURL:           "https://token.actions.githubusercontent.com/token",
		OIDCRequestToken:         "request-token",
		TempDir:                  t.TempDir(),
	}
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "project", mutate: func(c *Config) { c.ProjectID = "" }, wantErr: "FULLSEND_GCP_PROJECT_ID"},
		{name: "provider", mutate: func(c *Config) { c.WorkloadIdentityProvider = "" }, wantErr: "FULLSEND_GCP_WIF_PROVIDER"},
		{name: "provider format", mutate: func(c *Config) { c.WorkloadIdentityProvider = "pools/example" }, wantErr: "provider resource name"},
		{name: "OIDC URL", mutate: func(c *Config) { c.OIDCRequestURL = "" }, wantErr: "ACTIONS_ID_TOKEN_REQUEST_URL"},
		{name: "OIDC token", mutate: func(c *Config) { c.OIDCRequestToken = "" }, wantErr: "ACTIONS_ID_TOKEN_REQUEST_TOKEN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			_, _, err := prepareGitHubWIF(context.Background(), cfg, prepareOptions{})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestPrepareGitHubWIFExportedValidation(t *testing.T) {
	_, _, err := PrepareGitHubWIF(context.Background(), Config{})
	require.ErrorContains(t, err, "FULLSEND_GCP_PROJECT_ID")
}

func TestRequireGitHubOIDCURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "GitHub", raw: "https://token.actions.githubusercontent.com/token"},
		{name: "loopback test server", raw: "http://127.0.0.1/token"},
		{name: "malformed", raw: "https://%gh", wantErr: "parsing"},
		{name: "insecure", raw: "http://token.actions.githubusercontent.com/token", wantErr: "https"},
		{name: "wrong host", raw: "https://example.com/token", wantErr: "not GitHub"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := requireGitHubOIDCURL(tt.raw)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestFetchOIDCTokenRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    []byte
		wantErr string
	}{
		{name: "HTTP error", status: http.StatusUnauthorized, body: []byte("secret body"), wantErr: "HTTP 401"},
		{name: "malformed JSON", status: http.StatusOK, body: []byte("not-json"), wantErr: "invalid token response"},
		{name: "empty token", status: http.StatusOK, body: []byte(`{"value":""}`), wantErr: "invalid token response"},
		{name: "oversized", status: http.StatusOK, body: bytes.Repeat([]byte("x"), maxResponseBytes+1), wantErr: "exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tt.status,
					Body:       io.NopCloser(bytes.NewReader(tt.body)),
					Header:     make(http.Header),
				}, nil
			})}
			_, _, err := fetchOIDCToken(context.Background(), client, "https://token.actions.githubusercontent.com/token", "request-secret")
			require.ErrorContains(t, err, tt.wantErr)
			assert.NotContains(t, err.Error(), "secret body")
			assert.NotContains(t, err.Error(), "request-secret")
		})
	}
}

func TestPrepareGitHubWIFErrorsDoNotLeakSecrets(t *testing.T) {
	const secret = "never-print-this-secret"
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, secret, http.StatusUnauthorized)
	}))
	t.Cleanup(oidc.Close)

	_, _, err := prepareGitHubWIF(context.Background(), Config{
		ProjectID:                "example-project",
		WorkloadIdentityProvider: testWIFProvider,
		OIDCRequestURL:           oidc.URL,
		OIDCRequestToken:         secret,
		TempDir:                  t.TempDir(),
	}, prepareOptions{stsEndpoint: "http://127.0.0.1/unused", httpClient: oidc.Client()})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)

	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, secret, http.StatusBadRequest)
	}))
	t.Cleanup(sts.Close)
	successOIDC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"value":"`+secret+`"}`)
	}))
	t.Cleanup(successOIDC.Close)

	_, _, err = prepareGitHubWIF(context.Background(), Config{
		ProjectID:                "example-project",
		WorkloadIdentityProvider: testWIFProvider,
		OIDCRequestURL:           successOIDC.URL,
		OIDCRequestToken:         secret,
		TempDir:                  t.TempDir(),
	}, prepareOptions{stsEndpoint: sts.URL, httpClient: sts.Client()})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	assert.False(t, strings.Contains(err.Error(), "response_body"))
}
