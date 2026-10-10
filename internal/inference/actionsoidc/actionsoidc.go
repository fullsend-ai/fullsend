// Package actionsoidc fetches a GitHub Actions OIDC assertion (a JWT) from
// the runner's token service. It is provider-neutral: the inference
// gateway route (ADR 0137) uses the assertion as its credential directly,
// and package openaiwif exchanges one for an OpenAI access token.
//
// The OIDC endpoint is a trusted, fixed channel rather than user or remote
// configuration: GitHub injects it into the job as a runner-only,
// deny-listed variable. So this client is outside the SSRF-hardening scope
// described in docs/contributing/go-code.md ("Secure HTTP clients"). It
// still applies the baseline that section asks of every client: HTTPS
// only, an explicit timeout, and a bounded response body.
package actionsoidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// MaxResponseBytes bounds how many bytes a response may carry. The
	// responses are small JSON; a larger body is an error, not something
	// to parse.
	MaxResponseBytes = 1 << 20 // 1 MiB

	// HTTPTimeout is the per-request timeout of the default client.
	HTTPTimeout = 30 * time.Second

	// oidcHostSuffix is where GitHub serves ACTIONS_ID_TOKEN_REQUEST_URL
	// (github.com only; GHES is not supported by fullsend).
	oidcHostSuffix = ".actions.githubusercontent.com"
)

// AssertionConfig holds the inputs for a bare GitHub OIDC assertion fetch:
// no exchange follows, so the assertion itself is the credential (the
// inference gateway route, ADR 0137).
type AssertionConfig struct {
	// Audience is the OIDC audience the assertion is minted for.
	Audience string

	// OIDCRequestURL is the ACTIONS_ID_TOKEN_REQUEST_URL from the runner.
	OIDCRequestURL string

	// OIDCRequestToken is the ACTIONS_ID_TOKEN_REQUEST_TOKEN from the runner.
	OIDCRequestToken string

	// HTTPClient overrides the HTTP client. Tests only: a caller-supplied
	// client bypasses the default redirect refusal.
	HTTPClient *http.Client
}

// Assertion is a fetched GitHub OIDC JWT with the lifetime claims read
// from its payload. The claims are decoded without verifying the
// signature: the gateway verifies the token, and the runner only uses them
// to schedule a re-fetch before the token expires.
type Assertion struct {
	// Value is the JWT. Never log, print, or include in error messages.
	Value string

	// IssuedAt is the token's iat claim.
	IssuedAt time.Time

	// ExpiresAt is the token's exp claim.
	ExpiresAt time.Time
}

// Lifetime returns exp - iat.
func (a *Assertion) Lifetime() time.Duration {
	return a.ExpiresAt.Sub(a.IssuedAt)
}

// oidcResponse is the GitHub OIDC endpoint's JSON shape.
type oidcResponse struct {
	Value string `json:"value"`
}

// FetchAssertion requests a GitHub OIDC assertion for cfg.Audience and
// returns it with its iat and exp claims. Errors never include the
// assertion value.
func FetchAssertion(ctx context.Context, cfg AssertionConfig) (*Assertion, error) {
	if cfg.Audience == "" {
		return nil, fmt.Errorf("actionsoidc: audience is required")
	}
	if cfg.OIDCRequestURL == "" {
		return nil, fmt.Errorf("actionsoidc: ACTIONS_ID_TOKEN_REQUEST_URL is required")
	}
	if cfg.OIDCRequestToken == "" {
		return nil, fmt.Errorf("actionsoidc: ACTIONS_ID_TOKEN_REQUEST_TOKEN is required")
	}
	if err := RequireSecureURL("OIDC request URL", cfg.OIDCRequestURL); err != nil {
		return nil, fmt.Errorf("actionsoidc: %w", err)
	}
	if err := RequireGitHubOIDCHost(cfg.OIDCRequestURL); err != nil {
		return nil, fmt.Errorf("actionsoidc: %w", err)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: HTTPTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	value, err := Fetch(ctx, client, cfg.OIDCRequestURL, cfg.OIDCRequestToken, cfg.Audience)
	if err != nil {
		return nil, fmt.Errorf("actionsoidc: assertion request failed: %w", err)
	}
	iat, exp, err := ParseJWTLifetime(value)
	if err != nil {
		return nil, fmt.Errorf("actionsoidc: %w", err)
	}
	return &Assertion{Value: value, IssuedAt: iat, ExpiresAt: exp}, nil
}

// ParseJWTLifetime decodes the iat and exp claims from a JWT's payload
// without verifying its signature. Both claims are required, and exp must
// be after iat. Errors never include the token.
func ParseJWTLifetime(token string) (iat, exp time.Time, err error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, time.Time{}, fmt.Errorf("assertion is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("decoding assertion payload: invalid base64")
	}
	var claims struct {
		IssuedAt  *json.Number `json:"iat"`
		ExpiresAt *json.Number `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("decoding assertion payload: invalid JSON")
	}
	if claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("assertion has no iat or exp claim")
	}
	i, err1 := claims.IssuedAt.Int64()
	e, err2 := claims.ExpiresAt.Int64()
	if err1 != nil || err2 != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("assertion iat or exp claim is not an integer")
	}
	if e <= i {
		return time.Time{}, time.Time{}, fmt.Errorf("assertion exp is not after iat")
	}
	return time.Unix(i, 0), time.Unix(e, 0), nil
}

// RequireSecureURL rejects anything but https, except plain http to a
// loopback address (test servers).
func RequireSecureURL(what, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", what, err)
	}
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && isLoopback(u.Hostname()):
		return nil
	}
	return fmt.Errorf("%s must use https (got scheme %q)", what, u.Scheme)
}

// RequireGitHubOIDCHost rejects an assertion URL that does not point at
// GitHub's Actions token service (loopback is allowed for tests). The URL
// is runner-injected and deny-listed, so this is defence in depth against a
// rewritten runner environment, not SSRF hardening.
func RequireGitHubOIDCHost(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parsing OIDC request URL: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	if isLoopback(host) || strings.HasSuffix(host, oidcHostSuffix) {
		return nil
	}
	return fmt.Errorf("OIDC request URL host %q is not GitHub's Actions token service (*%s)", u.Hostname(), oidcHostSuffix)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ReadBounded reads at most MaxResponseBytes from r and errors when the
// body is larger, so an oversized response is never parsed.
func ReadBounded(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if len(body) > MaxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", MaxResponseBytes)
	}
	return body, nil
}

// Fetch requests a GitHub OIDC JWT from the runner's token endpoint with
// client and returns it unparsed. It does not check the URL: callers run
// RequireSecureURL and RequireGitHubOIDCHost first, as FetchAssertion does.
// Errors never include the assertion.
func Fetch(ctx context.Context, client *http.Client, oidcURL, oidcToken, audience string) (string, error) {
	// GitHub hands the runner a URL that already carries api-version; the
	// audience is one more query parameter, added through url.Values so it
	// is encoded correctly whether or not a query string is present.
	u, err := url.Parse(oidcURL)
	if err != nil {
		return "", fmt.Errorf("parsing OIDC request URL: %w", err)
	}
	q := u.Query()
	q.Set("audience", audience)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "bearer "+oidcToken)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OIDC endpoint returned %d", resp.StatusCode)
	}
	body, err := ReadBounded(resp.Body)
	if err != nil {
		return "", err
	}

	var oidc oidcResponse
	if err := json.Unmarshal(body, &oidc); err != nil {
		return "", fmt.Errorf("parsing response: %w", err)
	}
	if oidc.Value == "" {
		return "", fmt.Errorf("OIDC endpoint returned empty assertion")
	}
	return oidc.Value, nil
}
