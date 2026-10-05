package cf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// maxResponseSize limits the amount of data read from Cloudflare API
// responses. 1 MB is far larger than any expected response, but
// prevents unbounded memory consumption if an intermediary returns
// an unexpectedly large body.
const maxResponseSize = 1 << 20 // 1 MB

// maxErrorBodyLen limits the length of response bodies included in
// error messages to avoid leaking unexpectedly large payloads into
// logs.
const maxErrorBodyLen = 512

// hostnamePattern validates DNS hostnames used as custom domain values.
// Allows standard domain labels (alphanumeric + hyphens) separated by dots,
// with at least two labels.
var hostnamePattern = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)

// ValidateHostname checks whether s is a syntactically valid DNS hostname
// suitable for use as a Workers Custom Domain value. It does not verify
// that the domain exists or is reachable.
func ValidateHostname(s string) bool {
	return len(s) <= 253 && hostnamePattern.MatchString(s)
}

// truncateErrorBody returns s truncated to maxErrorBodyLen, appending
// "…[truncated]" if the string was shortened.
func truncateErrorBody(s string) string {
	if len(s) <= maxErrorBodyLen {
		return s
	}
	return s[:maxErrorBodyLen] + "…[truncated]"
}

// CloudflareAPIClient abstracts direct Cloudflare API calls for
// operations not covered by wrangler: custom domain attachment.
// Implementations must be safe for concurrent use from multiple
// goroutines.
type CloudflareAPIClient interface {
	// AttachCustomDomain registers a custom domain for a Worker via
	// the Cloudflare Workers Custom Domains API. If the domain is
	// already attached, this is a no-op.
	AttachCustomDomain(ctx context.Context, accountID, workerName, zoneID, hostname string) error

	// RemoveCustomDomain removes a Worker's custom domain binding
	// by hostname.
	RemoveCustomDomain(ctx context.Context, accountID, hostname string) error

	// LookupZoneID resolves the Cloudflare zone ID for a given domain
	// name by walking up the domain hierarchy. For example, given
	// "mint.fullsend.sh", it tries "mint.fullsend.sh" then "fullsend.sh"
	// until a matching zone is found. Returns an error if the zone is
	// not in the account.
	LookupZoneID(ctx context.Context, domain string) (string, error)
}

// --- LiveCloudflareAPIClient ---

// LiveCloudflareAPIClient implements CloudflareAPIClient using the
// Cloudflare REST API. Authentication is resolved via resolveAPIToken:
// CLOUDFLARE_API_TOKEN env var first, then wrangler auth token fallback.
type LiveCloudflareAPIClient struct {
	httpClient *http.Client
	// baseURL overrides the Cloudflare API base URL. When empty
	// (default), production URL https://api.cloudflare.com/client/v4
	// is used. Set in tests to point at httptest servers.
	baseURL string
}

// NewLiveCloudflareAPIClient creates a client that calls the
// Cloudflare REST API.
func NewLiveCloudflareAPIClient() *LiveCloudflareAPIClient {
	return &LiveCloudflareAPIClient{
		httpClient: http.DefaultClient,
	}
}

// cfBaseURL returns the base URL for the Cloudflare API. When
// baseURL is set (e.g. in tests), it is used instead of the
// production URL.
func (c *LiveCloudflareAPIClient) cfBaseURL() string {
	if c.baseURL != "" {
		return c.baseURL
	}
	return "https://api.cloudflare.com/client/v4"
}

// WranglerAuthTokenFn is the function used to run `wrangler auth token`.
// Override in tests to avoid needing a real wrangler installation.
var WranglerAuthTokenFn = runWranglerAuthToken

// runWranglerAuthToken executes `npx wrangler auth token` and returns
// the token string.
func runWranglerAuthToken(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "npx", "wrangler", "auth", "token")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("wrangler auth token failed: %w\n%s", err, string(out))
	}
	// The output may contain multiple lines; the token is typically
	// the last non-empty line.
	token := ""
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			token = line
		}
	}
	if token == "" {
		return "", fmt.Errorf("wrangler auth token returned empty output")
	}
	return token, nil
}

// resolveAPIToken returns a Cloudflare API token for direct API calls.
// It tries CLOUDFLARE_API_TOKEN env var first, then falls back to
// `wrangler auth token` to obtain a token from the wrangler OAuth session.
func resolveAPIToken(ctx context.Context) (string, error) {
	token := os.Getenv("CLOUDFLARE_API_TOKEN")
	if token != "" {
		return token, nil
	}
	// Fall back to wrangler auth token.
	wranglerToken, err := WranglerAuthTokenFn(ctx)
	if err != nil {
		return "", fmt.Errorf("CLOUDFLARE_API_TOKEN is not set and wrangler auth token failed: %w\nSet CLOUDFLARE_API_TOKEN or run 'wrangler login' first", err)
	}
	return wranglerToken, nil
}

// cfAPIRequest is a helper that makes an authenticated Cloudflare API
// request and returns the response body. It handles setting the
// Authorization header and Content-Type. Authentication is resolved
// via resolveAPIToken: CLOUDFLARE_API_TOKEN env var first, then
// wrangler auth token fallback (from `wrangler login`).
func (c *LiveCloudflareAPIClient) cfAPIRequest(ctx context.Context, method, reqURL string, body any) ([]byte, error) {
	token, err := resolveAPIToken(ctx)
	if err != nil {
		return nil, err
	}

	var reqBody io.Reader
	if body != nil {
		data, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return nil, fmt.Errorf("marshaling request body: %w", marshalErr)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, reqBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Cloudflare API: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cloudflare API returned %d: %s", resp.StatusCode, truncateErrorBody(string(respBody)))
	}
	return respBody, nil
}

// AttachCustomDomain registers a custom domain for a Worker via
// PUT /accounts/{account_id}/workers/domains.
func (c *LiveCloudflareAPIClient) AttachCustomDomain(ctx context.Context, accountID, workerName, zoneID, hostname string) error {
	reqURL := fmt.Sprintf("%s/accounts/%s/workers/domains", c.cfBaseURL(), url.PathEscape(accountID))
	body := map[string]string{
		"hostname":    hostname,
		"zone_id":     zoneID,
		"service":     workerName,
		"environment": "production",
	}

	_, err := c.cfAPIRequest(ctx, http.MethodPut, reqURL, body)
	if err != nil {
		return fmt.Errorf("attaching custom domain %s: %w", hostname, err)
	}
	return nil
}

// RemoveCustomDomain removes a Worker's custom domain binding by
// first looking up the domain ID, then deleting it.
func (c *LiveCloudflareAPIClient) RemoveCustomDomain(ctx context.Context, accountID, hostname string) error {
	domainID, err := c.findCustomDomainID(ctx, accountID, hostname)
	if err != nil {
		return fmt.Errorf("looking up custom domain %s: %w", hostname, err)
	}
	if domainID == "" {
		// Domain not found — nothing to remove.
		return nil
	}

	reqURL := fmt.Sprintf("%s/accounts/%s/workers/domains/%s", c.cfBaseURL(), url.PathEscape(accountID), url.PathEscape(domainID))
	if _, err := c.cfAPIRequest(ctx, http.MethodDelete, reqURL, nil); err != nil {
		return fmt.Errorf("removing custom domain %s: %w", hostname, err)
	}
	return nil
}

// findCustomDomainID looks up a Worker custom domain by hostname.
// Returns the domain ID if found, or empty string if not.
func (c *LiveCloudflareAPIClient) findCustomDomainID(ctx context.Context, accountID, hostname string) (string, error) {
	params := url.Values{"hostname": {hostname}}
	reqURL := fmt.Sprintf("%s/accounts/%s/workers/domains?%s", c.cfBaseURL(), url.PathEscape(accountID), params.Encode())
	respBody, err := c.cfAPIRequest(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}

	var result struct {
		Result []struct {
			ID       string `json:"id"`
			Hostname string `json:"hostname"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing domains response: %w", err)
	}

	for _, d := range result.Result {
		if d.Hostname == hostname {
			return d.ID, nil
		}
	}
	return "", nil
}

// LookupZoneID resolves the Cloudflare zone ID for a domain by walking
// up the domain hierarchy. For "mint.fullsend.sh" it tries
// "mint.fullsend.sh", then "fullsend.sh". Returns an error if no
// matching zone is found.
func (c *LiveCloudflareAPIClient) LookupZoneID(ctx context.Context, domain string) (string, error) {
	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid domain %q: must have at least two labels", domain)
	}

	// Walk up the domain hierarchy. For "mint.fullsend.sh":
	//   try "mint.fullsend.sh", then "fullsend.sh"
	for i := 0; i <= len(parts)-2; i++ {
		candidate := strings.Join(parts[i:], ".")
		zoneID, err := c.lookupZoneByName(ctx, candidate)
		if err != nil {
			return "", fmt.Errorf("looking up zone for %s: %w", candidate, err)
		}
		if zoneID != "" {
			return zoneID, nil
		}
	}

	return "", fmt.Errorf("zone not found for domain %q — ensure the domain's zone exists in your Cloudflare account", domain)
}

// lookupZoneByName queries the Cloudflare Zones API for a zone with
// the given name. Returns the zone ID if found, or empty string if
// no match.
func (c *LiveCloudflareAPIClient) lookupZoneByName(ctx context.Context, name string) (string, error) {
	params := url.Values{"name": {name}}
	reqURL := fmt.Sprintf("%s/zones?%s", c.cfBaseURL(), params.Encode())
	respBody, err := c.cfAPIRequest(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}

	var result struct {
		Result []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing zones response: %w", err)
	}

	for _, z := range result.Result {
		if z.Name == name {
			return z.ID, nil
		}
	}
	return "", nil
}

// ResolveZoneIDForDomain is a convenience function that creates a
// CloudflareAPIClient and resolves the zone ID for a domain. It
// resolves the API token from CLOUDFLARE_API_TOKEN or wrangler auth
// token (via resolveAPIToken, which cfAPIRequest also uses). This is
// intended for CLI-level use before building the provisioner config.
func ResolveZoneIDForDomain(ctx context.Context, domain string) (string, error) {
	client := NewLiveCloudflareAPIClient()
	return client.LookupZoneID(ctx, domain)
}

// ResolveZoneIDForDomainFn is the function used to resolve zone IDs
// from domain names. Override in tests to avoid real API calls.
var ResolveZoneIDForDomainFn = ResolveZoneIDForDomain
