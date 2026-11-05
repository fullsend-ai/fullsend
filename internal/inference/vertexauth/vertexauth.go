// Package vertexauth prepares GitHub Actions Workload Identity Federation
// credentials for a single Vertex inference run.
package vertexauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	googleSTSEndpoint  = "https://sts.googleapis.com/v1/token"
	cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"
	subjectTokenType   = "urn:ietf:params:oauth:token-type:jwt"
	sandboxTokenPath   = "/sandbox/workspace/.gcp-oidc-token"
	maxResponseBytes   = 1 << 20
	httpTimeout        = 30 * time.Second
	oidcHostSuffix     = ".actions.githubusercontent.com"
)

var providerResourcePattern = regexp.MustCompile(
	`^projects/[1-9][0-9]*/locations/global/workloadIdentityPools/[A-Za-z0-9._-]+/providers/[A-Za-z0-9._-]+$`,
)

// Config contains the trusted runner inputs needed to prepare credentials.
type Config struct {
	ProjectID                string
	WorkloadIdentityProvider string
	OIDCRequestURL           string
	OIDCRequestToken         string
	TempDir                  string
	// OnSubjectToken, when set, receives the fetched GitHub OIDC token so
	// the caller can mask it in its log before the token is used.
	OnSubjectToken func(token string)
}

type prepareOptions struct {
	stsEndpoint string
	httpClient  *http.Client
}

type oidcResponse struct {
	Value string `json:"value"`
}

type externalAccountCredentials struct {
	Type             string           `json:"type"`
	Audience         string           `json:"audience"`
	SubjectTokenType string           `json:"subject_token_type"`
	TokenURL         string           `json:"token_url"`
	CredentialSource credentialSource `json:"credential_source"`
}

type credentialSource struct {
	File   string           `json:"file"`
	Format credentialFormat `json:"format"`
}

type credentialFormat struct {
	Type                  string `json:"type"`
	SubjectTokenFieldName string `json:"subject_token_field_name"`
}

// PrepareGitHubWIF creates and validates direct GitHub-to-Google WIF
// credentials. The returned cleanup function is safe to call more than once.
func PrepareGitHubWIF(ctx context.Context, cfg Config) (map[string]string, func(), error) {
	return prepareGitHubWIF(ctx, cfg, prepareOptions{})
}

func prepareGitHubWIF(ctx context.Context, cfg Config, opts prepareOptions) (map[string]string, func(), error) {
	if strings.TrimSpace(cfg.ProjectID) == "" {
		return nil, nil, fmt.Errorf("Vertex inference requires FULLSEND_GCP_PROJECT_ID")
	}
	if strings.TrimSpace(cfg.WorkloadIdentityProvider) == "" {
		return nil, nil, fmt.Errorf("Vertex inference requires FULLSEND_GCP_WIF_PROVIDER")
	}
	if !providerResourcePattern.MatchString(cfg.WorkloadIdentityProvider) {
		return nil, nil, fmt.Errorf("FULLSEND_GCP_WIF_PROVIDER must be a full workload identity provider resource name")
	}
	if cfg.OIDCRequestURL == "" {
		return nil, nil, fmt.Errorf("Vertex inference requires ACTIONS_ID_TOKEN_REQUEST_URL")
	}
	if cfg.OIDCRequestToken == "" {
		return nil, nil, fmt.Errorf("Vertex inference requires ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	}
	if err := requireGitHubOIDCURL(cfg.OIDCRequestURL); err != nil {
		return nil, nil, err
	}

	stsEndpoint := opts.stsEndpoint
	if stsEndpoint == "" {
		stsEndpoint = googleSTSEndpoint
	}
	client := opts.httpClient
	if client == nil {
		client = &http.Client{
			Timeout: httpTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}

	// The OIDC token's aud claim must match the provider's allowedAudiences,
	// which list the https form; STS takes the scheme-relative resource name.
	oidcAudience := "https://iam.googleapis.com/" + cfg.WorkloadIdentityProvider
	audience := "//iam.googleapis.com/" + cfg.WorkloadIdentityProvider
	oidcURL, err := addAudience(cfg.OIDCRequestURL, oidcAudience)
	if err != nil {
		return nil, nil, fmt.Errorf("preparing GitHub OIDC request: %w", err)
	}
	subjectToken, tokenJSON, err := fetchOIDCToken(ctx, client, oidcURL, cfg.OIDCRequestToken)
	if err != nil {
		return nil, nil, err
	}
	if cfg.OnSubjectToken != nil {
		cfg.OnSubjectToken(subjectToken)
	}

	tempBase := cfg.TempDir
	if tempBase == "" {
		tempBase = os.TempDir()
	}
	root, err := os.MkdirTemp(tempBase, "fullsend-gcp-wif-")
	if err != nil {
		return nil, nil, fmt.Errorf("creating temporary credential directory: %w", err)
	}
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() { _ = os.RemoveAll(root) })
	}
	keep := false
	defer func() {
		if !keep {
			cleanup()
		}
	}()

	tokenPath := filepath.Join(root, "gcp-oidc-token.json")
	authPath := filepath.Join(root, "gcp-oidc-auth")
	credentialsPath := filepath.Join(root, "sandbox-gcp-credentials.json")
	if err := os.WriteFile(tokenPath, tokenJSON, 0o600); err != nil {
		return nil, nil, fmt.Errorf("writing temporary OIDC token: %w", err)
	}
	if err := os.WriteFile(authPath, []byte("Bearer "+cfg.OIDCRequestToken), 0o600); err != nil {
		return nil, nil, fmt.Errorf("writing temporary OIDC authorization: %w", err)
	}

	hostCredentials := externalAccountConfig(audience, stsEndpoint, tokenPath)
	hostJSON, err := json.Marshal(hostCredentials)
	if err != nil {
		return nil, nil, fmt.Errorf("encoding temporary Google credentials: %w", err)
	}
	validationCtx := context.WithValue(ctx, oauth2.HTTPClient, client)
	creds, err := google.CredentialsFromJSONWithTypeAndParams(validationCtx, hostJSON, google.ExternalAccount, google.CredentialsParams{
		Scopes: []string{cloudPlatformScope},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("constructing Google STS credentials failed")
	}
	if _, err := creds.TokenSource.Token(); err != nil {
		return nil, nil, fmt.Errorf("validating Google STS exchange failed")
	}

	sandboxCredentials := externalAccountConfig(audience, stsEndpoint, sandboxTokenPath)
	sandboxJSON, err := json.Marshal(sandboxCredentials)
	if err != nil {
		return nil, nil, fmt.Errorf("encoding sandbox Google credentials: %w", err)
	}
	if err := os.WriteFile(credentialsPath, sandboxJSON, 0o600); err != nil {
		return nil, nil, fmt.Errorf("writing sandbox Google credentials: %w", err)
	}

	keep = true
	env := map[string]string{
		"GOOGLE_GHA_CREDS_PATH":                  credentialsPath,
		"GOOGLE_APPLICATION_CREDENTIALS":         credentialsPath,
		"CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE": credentialsPath,
		"CLOUDSDK_CORE_PROJECT":                  cfg.ProjectID,
		"CLOUDSDK_PROJECT":                       cfg.ProjectID,
		"GCLOUD_PROJECT":                         cfg.ProjectID,
		"GCP_PROJECT":                            cfg.ProjectID,
		"GOOGLE_CLOUD_PROJECT":                   cfg.ProjectID,
		"GCP_OIDC_TOKEN_FILE":                    tokenPath,
		"FULLSEND_GCP_OIDC_URL":                  oidcURL,
		"FULLSEND_GCP_OIDC_AUTH_FILE":            authPath,
	}
	return env, cleanup, nil
}

func externalAccountConfig(audience, tokenURL, tokenFile string) externalAccountCredentials {
	return externalAccountCredentials{
		Type:             "external_account",
		Audience:         audience,
		SubjectTokenType: subjectTokenType,
		TokenURL:         tokenURL,
		CredentialSource: credentialSource{
			File: tokenFile,
			Format: credentialFormat{
				Type:                  "json",
				SubjectTokenFieldName: "value",
			},
		},
	}
}

func addAudience(raw, audience string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("audience", audience)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func requireGitHubOIDCURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parsing ACTIONS_ID_TOKEN_REQUEST_URL: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	isLoopback := host == "localhost"
	if ip := net.ParseIP(host); ip != nil {
		isLoopback = ip.IsLoopback()
	}
	if u.Scheme == "http" && isLoopback {
		return nil
	}
	if u.Scheme != "https" {
		return fmt.Errorf("ACTIONS_ID_TOKEN_REQUEST_URL must use https")
	}
	if !strings.HasSuffix(host, oidcHostSuffix) {
		return fmt.Errorf("ACTIONS_ID_TOKEN_REQUEST_URL is not GitHub's Actions token service")
	}
	return nil
}

func fetchOIDCToken(ctx context.Context, client *http.Client, endpoint, requestToken string) (string, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", nil, fmt.Errorf("creating GitHub OIDC request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+requestToken)
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("requesting GitHub OIDC token failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("GitHub OIDC endpoint returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return "", nil, fmt.Errorf("reading GitHub OIDC response failed")
	}
	if len(body) > maxResponseBytes {
		return "", nil, fmt.Errorf("GitHub OIDC response exceeds %d bytes", maxResponseBytes)
	}
	var response oidcResponse
	if err := json.Unmarshal(body, &response); err != nil || response.Value == "" {
		return "", nil, fmt.Errorf("GitHub OIDC endpoint returned an invalid token response")
	}
	tokenJSON, err := json.Marshal(response)
	if err != nil {
		return "", nil, fmt.Errorf("encoding temporary OIDC token failed")
	}
	return response.Value, tokenJSON, nil
}
