package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DefaultGatewayURL is the Atlassian Platform API Gateway, which serves every
// Jira Cloud site at https://api.atlassian.com/ex/jira/{cloudId}/rest/api/3/...
// (see GatewayAPIURL). Atlassian-managed service accounts (Atlassian
// Administration > Directory > Service accounts) authenticate only through
// the gateway: their scoped API tokens are rejected by the site URL
// (https://<site>.atlassian.net/rest/api/3/...). A regular account's classic
// API token is accepted at both, with Basic auth, so sending every call
// through the gateway serves both kinds of account (#9007). See
// https://support.atlassian.com/user-management/docs/manage-api-tokens-for-service-accounts/.
const DefaultGatewayURL = "https://api.atlassian.com"

// validCloudID bounds a cloud ID to the characters Atlassian uses (a UUID),
// so a misconfigured value cannot inject path segments into the gateway URL.
var validCloudID = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// ValidateCloudID checks that id is usable as an Atlassian cloud ID in a
// gateway URL path.
func ValidateCloudID(id string) error {
	if !validCloudID.MatchString(id) {
		return fmt.Errorf("invalid Atlassian cloud ID %q: expected letters, digits, and hyphens (a UUID)", id)
	}
	return nil
}

// GatewayAPIURL returns the Platform API Gateway base for a site's cloud ID,
// <gatewayURL>/ex/jira/<cloudID>, for use with WithAPIURL.
func GatewayAPIURL(gatewayURL, cloudID string) (string, error) {
	if err := ValidateCloudID(cloudID); err != nil {
		return "", err
	}
	if err := ValidateBaseURL(gatewayURL); err != nil {
		return "", fmt.Errorf("gateway URL: %w", err)
	}
	return strings.TrimRight(gatewayURL, "/") + "/ex/jira/" + cloudID, nil
}

// newDiscoveryHTTPClient returns the HTTP client used for unauthenticated
// cloud ID discovery. It refuses off-origin redirects for the same reason
// the API client does (see checkRedirect). Like the API client it does not
// apply the internal-address dial checks from docs/contributing/go-code.md:
// the site URL is operator configuration (a repository variable or flag set
// by a repository admin), not untrusted input, and ValidateBaseURL already
// pins the scheme to HTTPS outside loopback.
func newDiscoveryHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: checkRedirect,
	}
}

// FetchCloudID returns the cloud ID of a Jira Cloud site, read from the
// unauthenticated https://<site>.atlassian.net/_edge/tenant_info endpoint.
// A nil httpClient uses a default client with a 30-second timeout.
func FetchCloudID(ctx context.Context, httpClient *http.Client, siteURL string) (string, error) {
	if err := ValidateBaseURL(siteURL); err != nil {
		return "", err
	}
	if httpClient == nil {
		httpClient = newDiscoveryHTTPClient()
	}
	reqURL := strings.TrimRight(siteURL, "/") + "/_edge/tenant_info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("create tenant_info request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch Jira cloud ID from %s: %w", reqURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch Jira cloud ID from %s: unexpected status %d", reqURL, resp.StatusCode)
	}
	var info struct {
		CloudID string `json:"cloudId"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&info); err != nil {
		return "", fmt.Errorf("decode Jira cloud ID from %s: %w", reqURL, err)
	}
	if info.CloudID == "" {
		return "", fmt.Errorf("fetch Jira cloud ID from %s: response has no cloudId", reqURL)
	}
	if err := ValidateCloudID(info.CloudID); err != nil {
		return "", err
	}
	return info.CloudID, nil
}

// DiscoverAPIURL returns the Platform API Gateway base for the site at
// siteURL, discovering the site's cloud ID with FetchCloudID. gatewayURL is
// normally DefaultGatewayURL; tests point it at a local server.
func DiscoverAPIURL(ctx context.Context, httpClient *http.Client, siteURL, gatewayURL string) (string, error) {
	cloudID, err := FetchCloudID(ctx, httpClient, siteURL)
	if err != nil {
		return "", err
	}
	return GatewayAPIURL(gatewayURL, cloudID)
}
