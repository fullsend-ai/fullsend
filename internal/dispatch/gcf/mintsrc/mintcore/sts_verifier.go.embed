package mintcore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultGitHubOIDCIssuer = "https://token.actions.githubusercontent.com"

// stsResponse is the relevant fields from the GCP STS token exchange response.
type stsResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// STSVerifierConfig configures a new STSVerifier.
type STSVerifierConfig struct {
	HTTPClient         HTTPDoer
	STSURL             string
	GCPProjectNum      string
	WIFPoolName        string
	DefaultWIFProvider string
	AllowedOrgs        []string
	AllowedWorkflows   []string
	PerRepoWIFRepos    map[string]bool
	OIDCAudience       string
}

// STSVerifier validates OIDC tokens by exchanging them with GCP STS
// (Workload Identity Federation). It performs lightweight JWT pre-validation
// before the STS exchange.
type STSVerifier struct {
	httpClient         HTTPDoer
	stsBaseURL         string
	gcpProjectNum      string
	wifPoolName        string
	defaultWIFProvider string
	allowedOrgs        []string
	allowedWorkflows   []string
	perRepoWIFRepos    map[string]bool
	oidcAudience       string
}

// NewSTSVerifier creates a verifier that validates tokens via GCP STS exchange.
func NewSTSVerifier(opts STSVerifierConfig) *STSVerifier {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	stsURL := opts.STSURL
	if stsURL == "" {
		stsURL = "https://sts.googleapis.com"
	}
	perRepo := opts.PerRepoWIFRepos
	if perRepo == nil {
		perRepo = make(map[string]bool)
	}
	return &STSVerifier{
		httpClient:         httpClient,
		stsBaseURL:         stsURL,
		gcpProjectNum:      opts.GCPProjectNum,
		wifPoolName:        opts.WIFPoolName,
		defaultWIFProvider: opts.DefaultWIFProvider,
		allowedOrgs:        opts.AllowedOrgs,
		allowedWorkflows:   opts.AllowedWorkflows,
		perRepoWIFRepos:    perRepo,
		oidcAudience:       opts.OIDCAudience,
	}
}

// Verify pre-validates the JWT claims, then exchanges the token with GCP STS.
func (v *STSVerifier) Verify(ctx context.Context, rawToken string) (*Claims, error) {
	claims, err := v.prevalidate(rawToken)
	if err != nil {
		return nil, err
	}

	providerName := v.resolveWIFProvider(claims.Repository)
	if err := v.exchangeSTS(ctx, rawToken, providerName); err != nil {
		return nil, err
	}

	return claims, nil
}

// prevalidate performs lightweight validation of the OIDC JWT before
// sending it to STS. This is defense-in-depth — STS performs the
// authoritative cryptographic validation.
func (v *STSVerifier) prevalidate(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format: expected 3 segments, got %d", len(parts))
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding JWT claims: %w", err)
	}

	var claims Claims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("parsing JWT claims: %w", err)
	}

	if claims.Issuer != defaultGitHubOIDCIssuer {
		return nil, fmt.Errorf("unexpected issuer: %s", claims.Issuer)
	}

	if v.oidcAudience == "" {
		return nil, fmt.Errorf("OIDC_AUDIENCE must be configured")
	}
	if !claims.Audience.Contains(v.oidcAudience) {
		return nil, fmt.Errorf("audience mismatch")
	}

	now := time.Now().Unix()
	skew := int64(maxClockSkew.Seconds())
	if claims.Expiry <= now-skew {
		return nil, fmt.Errorf("token expired")
	}
	if claims.IssuedAt == 0 {
		return nil, fmt.Errorf("missing iat claim")
	}
	if claims.IssuedAt > now+skew {
		return nil, fmt.Errorf("token issued in the future")
	}

	if claims.Repository == "" {
		return nil, fmt.Errorf("missing repository claim")
	}

	if err := ValidateOrgAllowed(claims.RepositoryOwner, v.allowedOrgs); err != nil {
		return nil, err
	}

	if err := ValidateWorkflowRef(claims.JobWorkflowRef, claims.Repository, v.perRepoWIFRepos, v.allowedWorkflows); err != nil {
		return nil, err
	}

	return &claims, nil
}

// resolveWIFProvider returns the WIF provider name to use for STS validation.
// Repos in the perRepoWIFRepos registry use a dedicated per-repo provider;
// all others (including .fullsend) use the default.
func (v *STSVerifier) resolveWIFProvider(repository string) string {
	parts := strings.SplitN(repository, "/", 2)
	if len(parts) != 2 {
		return v.defaultWIFProvider
	}
	if parts[1] == ".fullsend" {
		return v.defaultWIFProvider
	}
	if v.perRepoWIFRepos[strings.ToLower(repository)] {
		return BuildRepoProviderID(parts[0], parts[1])
	}
	return v.defaultWIFProvider
}

func (v *STSVerifier) exchangeSTS(ctx context.Context, oidcToken, providerName string) error {
	aud := fmt.Sprintf("//iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		v.gcpProjectNum, v.wifPoolName, providerName)

	formValues := url.Values{
		"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"audience":             {aud},
		"scope":                {"https://www.googleapis.com/auth/cloud-platform"},
		"requested_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
		"subject_token_type":   {"urn:ietf:params:oauth:token-type:jwt"},
		"subject_token":        {oidcToken},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		v.stsBaseURL+"/v1/token",
		strings.NewReader(formValues.Encode()))
	if err != nil {
		return fmt.Errorf("creating STS request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("STS request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("STS returned status %d", resp.StatusCode)
	}

	var stsResp stsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stsResp); err != nil {
		return fmt.Errorf("decoding STS response: %w", err)
	}

	if stsResp.AccessToken == "" {
		return fmt.Errorf("STS returned empty access token")
	}

	return nil
}
