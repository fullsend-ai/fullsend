// Package openaiwif exchanges a GitHub Actions OIDC JWT for an OpenAI
// access token via OpenAI's Workload Identity Federation endpoint.
//
// The exchange is two HTTP calls:
//
//  1. GET the GitHub OIDC assertion with the configured audience.
//  2. POST an RFC 8693 token-exchange request to OpenAI's token endpoint.
//
// The returned access token is opaque (never assume a prefix or structure)
// and short-lived: at most an hour, and never longer than the GitHub
// assertion it was exchanged from (minutes). No refresh token is returned;
// the caller re-exchanges a fresh assertion instead.
//
// Both URLs are trusted, fixed channels rather than user or remote
// configuration — the OIDC endpoint is injected by GitHub into the job and
// is a runner-only, deny-listed variable; the token endpoint is a first-party
// constant — so this client is outside the SSRF-hardening scope described in
// docs/contributing/go-code.md ("Secure HTTP clients"). It still applies the
// baseline that section asks of every client: HTTPS only, an explicit
// timeout, and a bounded response body.
package openaiwif

import (
	"bytes"
	"context"
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
	// openAITokenEndpoint is the production OpenAI OAuth2 token endpoint.
	openAITokenEndpoint = "https://auth.openai.com/oauth/token"

	// defaultSubjectTokenType is the OAuth2 subject_token_type for a JWT.
	// OpenAI also accepts "urn:ietf:params:oauth:token-type:id_token";
	// the default here is jwt, overridable via Config.SubjectTokenType for
	// a maintainer's live test.
	defaultSubjectTokenType = "urn:ietf:params:oauth:token-type:jwt"

	// maxResponseBytes bounds how many bytes we accept from either endpoint.
	// Both responses are small JSON; a larger body is an error, not
	// something to parse.
	maxResponseBytes = 1 << 20 // 1 MiB

	// httpTimeout is the per-request timeout for both the OIDC assertion
	// request and the OpenAI exchange.
	httpTimeout = 30 * time.Second

	// maxTokenLifetime is the documented ceiling on an exchanged token.
	maxTokenLifetime = time.Hour

	// oidcHostSuffix is where GitHub serves ACTIONS_ID_TOKEN_REQUEST_URL
	// (github.com only; GHES is not supported by fullsend).
	oidcHostSuffix = ".actions.githubusercontent.com"
)

// Config holds the inputs for a WIF exchange. All fields except
// SubjectTokenType and TokenEndpoint are required.
type Config struct {
	// Audience is the OIDC audience the GitHub assertion is minted for,
	// e.g. "https://auth.openai.com".
	Audience string

	// IdentityProviderID is the OpenAI identity_provider_id.
	IdentityProviderID string

	// ServiceAccountID is the OpenAI service_account_id.
	ServiceAccountID string

	// OIDCRequestURL is the ACTIONS_ID_TOKEN_REQUEST_URL from the runner.
	OIDCRequestURL string

	// OIDCRequestToken is the ACTIONS_ID_TOKEN_REQUEST_TOKEN from the runner.
	OIDCRequestToken string

	// SubjectTokenType overrides the token-exchange subject_token_type.
	// Defaults to "urn:ietf:params:oauth:token-type:jwt".
	SubjectTokenType string

	// TokenEndpoint overrides the OpenAI token endpoint URL. Tests only:
	// production callers leave it empty. Exposing it to user or harness
	// configuration would turn this fixed-endpoint client into an SSRF
	// surface (docs/contributing/go-code.md, "Secure HTTP clients").
	TokenEndpoint string

	// HTTPClient overrides the HTTP client used for both requests. Tests
	// only, for the same reason: a caller-supplied client also bypasses the
	// default redirect refusal.
	HTTPClient *http.Client
}

// Token is the result of a successful exchange.
type Token struct {
	// Value is the opaque access token. Never log, print, or include
	// in error messages.
	Value string

	// ExpiresAt is the absolute expiry derived from the response's
	// expires_in field plus the exchange timestamp. OpenAI caps the token
	// at one hour and never lets it outlive the GitHub assertion it was
	// exchanged from, so in practice this is minutes, not an hour.
	ExpiresAt time.Time

	// Scope is the space-separated permission list the mapping granted
	// (empty when the mapping does not narrow permissions). Not secret.
	Scope string
}

// oidcResponse is the GitHub OIDC endpoint's JSON shape.
type oidcResponse struct {
	Value string `json:"value"`
}

// exchangeRequest is the OpenAI token endpoint's JSON request shape.
type exchangeRequest struct {
	GrantType          string `json:"grant_type"`
	SubjectTokenType   string `json:"subject_token_type"`
	SubjectToken       string `json:"subject_token"`
	IdentityProviderID string `json:"identity_provider_id"`
	ServiceAccountID   string `json:"service_account_id"`
}

// tokenResponse is the OpenAI token endpoint's JSON shape.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

// Exchange performs the two-step WIF exchange:
//  1. Request a GitHub OIDC assertion with the configured audience.
//  2. Exchange the assertion at the OpenAI token endpoint.
//
// Errors never include the assertion or the token value.
func Exchange(ctx context.Context, cfg Config) (*Token, error) {
	if cfg.Audience == "" {
		return nil, fmt.Errorf("openaiwif: audience is required")
	}
	if cfg.IdentityProviderID == "" {
		return nil, fmt.Errorf("openaiwif: identity_provider_id is required")
	}
	if cfg.ServiceAccountID == "" {
		return nil, fmt.Errorf("openaiwif: service_account_id is required")
	}
	if cfg.OIDCRequestURL == "" {
		return nil, fmt.Errorf("openaiwif: ACTIONS_ID_TOKEN_REQUEST_URL is required")
	}
	if cfg.OIDCRequestToken == "" {
		return nil, fmt.Errorf("openaiwif: ACTIONS_ID_TOKEN_REQUEST_TOKEN is required")
	}

	client := cfg.HTTPClient
	if client == nil {
		// Neither endpoint redirects on success; refusing to follow one
		// turns an unexpected 3xx into a visible non-200 error instead of
		// a request to a host we did not choose.
		client = &http.Client{
			Timeout: httpTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	subjectTokenType := cfg.SubjectTokenType
	if subjectTokenType == "" {
		subjectTokenType = defaultSubjectTokenType
	}
	tokenEndpoint := cfg.TokenEndpoint
	if tokenEndpoint == "" {
		tokenEndpoint = openAITokenEndpoint
	}
	if err := requireSecureURL("OIDC request URL", cfg.OIDCRequestURL); err != nil {
		return nil, fmt.Errorf("openaiwif: %w", err)
	}
	if err := requireGitHubOIDCHost(cfg.OIDCRequestURL); err != nil {
		return nil, fmt.Errorf("openaiwif: %w", err)
	}
	if err := requireSecureURL("token endpoint", tokenEndpoint); err != nil {
		return nil, fmt.Errorf("openaiwif: %w", err)
	}

	// Step 1: fetch the GitHub OIDC assertion.
	assertion, err := fetchAssertion(ctx, client, cfg.OIDCRequestURL, cfg.OIDCRequestToken, cfg.Audience)
	if err != nil {
		return nil, fmt.Errorf("openaiwif: assertion request failed: %w", err)
	}

	// Step 2: exchange the assertion for an OpenAI access token.
	tok, err := exchangeToken(ctx, client, tokenEndpoint, assertion, cfg.IdentityProviderID, cfg.ServiceAccountID, subjectTokenType)
	if err != nil {
		return nil, fmt.Errorf("openaiwif: token exchange failed: %w", err)
	}
	return tok, nil
}

// requireSecureURL rejects anything but https, except plain http to a
// loopback address (test servers).
func requireSecureURL(what, raw string) error {
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

// requireGitHubOIDCHost rejects an assertion URL that does not point at
// GitHub's Actions token service (loopback is allowed for tests). The URL
// is runner-injected and deny-listed, so this is defence in depth against a
// rewritten runner environment, not SSRF hardening.
func requireGitHubOIDCHost(raw string) error {
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

// readBounded reads at most maxResponseBytes from r and errors when the
// body is larger, so an oversized response is never parsed.
func readBounded(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return body, nil
}

// fetchAssertion requests a GitHub OIDC JWT from the runner's token endpoint.
func fetchAssertion(ctx context.Context, client *http.Client, oidcURL, oidcToken, audience string) (string, error) {
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
	body, err := readBounded(resp.Body)
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

// exchangeToken performs the RFC 8693 token exchange at the OpenAI endpoint.
func exchangeToken(ctx context.Context, client *http.Client, endpoint, assertion, identityProviderID, serviceAccountID, subjectTokenType string) (*Token, error) {
	exchangeTime := time.Now()

	// OpenAI's token endpoint takes a JSON body (developers.openai.com,
	// "Workload identity token exchange" reference), not the form encoding
	// RFC 8693 examples use.
	payload, err := json.Marshal(exchangeRequest{
		GrantType:          "urn:ietf:params:oauth:grant-type:token-exchange",
		SubjectTokenType:   subjectTokenType,
		SubjectToken:       assertion,
		IdentityProviderID: identityProviderID,
		ServiceAccountID:   serviceAccountID,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	body, err := readBounded(resp.Body)
	if err != nil {
		return nil, err
	}

	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("token endpoint returned empty access_token")
	}
	if tok.ExpiresIn <= 0 {
		return nil, fmt.Errorf("token endpoint returned invalid expires_in: %d", tok.ExpiresIn)
	}
	// The value is sent as `Authorization: Bearer` by the gateway; anything
	// else would be silently wrong, so treat the documented token_type as a
	// checked precondition (RFC 6749 §5.1: case-insensitive).
	if !strings.EqualFold(tok.TokenType, "Bearer") {
		return nil, fmt.Errorf("token endpoint returned unsupported token_type %q (expected Bearer)", tok.TokenType)
	}
	// A header value with whitespace could not be masked per line by the
	// Actions log filter nor sent as a Bearer token.
	if strings.ContainsAny(tok.AccessToken, " \t\r\n") {
		return nil, fmt.Errorf("token endpoint returned an access_token containing whitespace")
	}
	// OpenAI documents at most one hour; a longer claim is treated as an
	// hour so the refresher never sleeps past the real lifetime.
	if tok.ExpiresIn > int(maxTokenLifetime/time.Second) {
		tok.ExpiresIn = int(maxTokenLifetime / time.Second)
	}

	return &Token{
		Value:     tok.AccessToken,
		ExpiresAt: exchangeTime.Add(time.Duration(tok.ExpiresIn) * time.Second),
		Scope:     tok.Scope,
	}, nil
}
