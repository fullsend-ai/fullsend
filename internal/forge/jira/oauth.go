package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DefaultOAuthTokenURL is the Atlassian token endpoint that exchanges a
// service account's OAuth 2.0 client credentials for an access token.
const DefaultOAuthTokenURL = "https://auth.atlassian.com/oauth/token"

// maxOAuthTokenBytes bounds the token endpoint response body.
const maxOAuthTokenBytes = 64 << 10

// maxOAuthTokenLifetime caps the lifetime taken from expires_in, so an
// implausible value neither overflows nor pins a token indefinitely.
// Atlassian service-account access tokens are valid for 60 minutes.
const maxOAuthTokenLifetime = 24 * time.Hour

// maxOAuthExpirySkew is how long before its stated expiry a cached access
// token is replaced, so a token never expires in flight.
const maxOAuthExpirySkew = 60 * time.Second

// oauthErrorCodeRe matches an RFC 6749 error code (for example
// invalid_client). Only a matching code is quoted in errors, so a token
// endpoint response cannot inject arbitrary text into logs.
var oauthErrorCodeRe = regexp.MustCompile(`^[a-z_]{1,64}$`)

// WithOAuthTokenURL overrides the token endpoint used with OAuth 2.0
// client credentials (DefaultOAuthTokenURL). It has no effect on a client
// built with a token. The URL must use https unless it is a loopback
// address.
func WithOAuthTokenURL(tokenURL string) Option {
	return func(c *LiveClient) {
		c.oauthTokenURL = strings.TrimSpace(tokenURL)
	}
}

// withOAuthClientCredentials makes the client authenticate with a Bearer
// access token obtained through the client_credentials grant. New sets
// the token endpoint once all options are applied.
func withOAuthClientCredentials(clientID, clientSecret string) Option {
	return func(c *LiveClient) {
		c.oauth = &oauthTokenSource{
			clientID:     clientID,
			clientSecret: clientSecret,
			sem:          make(chan struct{}, 1),
			now:          time.Now,
		}
	}
}

// oauthTokenSource obtains and caches access tokens for a service
// account's OAuth 2.0 client credentials.
type oauthTokenSource struct {
	tokenURL     string
	clientID     string
	clientSecret string

	// sem serializes token requests; accessToken and expiry cache the
	// last successful one. A failed request is not cached.
	sem         chan struct{}
	accessToken string
	expiry      time.Time
	now         func() time.Time
}

// token returns a valid access token, requesting a new one when none is
// cached, the cached one is about to expire, or the cached one equals
// stale (a token the API just rejected). Concurrent callers wait for a
// single request; waiting honours ctx.
func (s *oauthTokenSource) token(ctx context.Context, hc *http.Client, stale string) (string, error) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.sem }()
	if s.accessToken != "" && s.accessToken != stale && s.now().Before(s.expiry) {
		return s.accessToken, nil
	}
	s.accessToken = ""
	tok, lifetime, err := s.fetch(ctx, hc)
	if err != nil {
		return "", err
	}
	skew := min(maxOAuthExpirySkew, lifetime/2)
	s.accessToken = tok
	s.expiry = s.now().Add(lifetime - skew)
	return tok, nil
}

// fetch performs the client_credentials grant and returns the access token
// and its lifetime. Errors name the credential variables and the token
// endpoint but never the client ID, client secret or response body.
func (s *oauthTokenSource) fetch(ctx context.Context, hc *http.Client) (string, time.Duration, error) {
	errPrefix := fmt.Sprintf("jira: OAuth token request to %q with %s and %s", s.tokenURL, ClientIDEnvVar, ClientSecretEnvVar)
	body, err := json.Marshal(map[string]string{
		"grant_type":    "client_credentials",
		"client_id":     s.clientID,
		"client_secret": s.clientSecret,
	})
	if err != nil {
		return "", 0, fmt.Errorf("%s: encode request: %w", errPrefix, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("%s: create request: %w", errPrefix, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("%s: %w", errPrefix, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthTokenBytes+1))
	if err != nil {
		return "", 0, fmt.Errorf("%s: read response: %w", errPrefix, err)
	}
	if len(data) > maxOAuthTokenBytes {
		return "", 0, fmt.Errorf("%s: response exceeds %d bytes", errPrefix, maxOAuthTokenBytes)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
	}
	parseErr := json.Unmarshal(data, &tr)
	if resp.StatusCode != http.StatusOK {
		if parseErr == nil && oauthErrorCodeRe.MatchString(tr.Error) {
			return "", 0, fmt.Errorf("%s: unexpected status %d (%s)", errPrefix, resp.StatusCode, tr.Error)
		}
		return "", 0, fmt.Errorf("%s: unexpected status %d", errPrefix, resp.StatusCode)
	}
	if parseErr != nil {
		return "", 0, fmt.Errorf("%s: malformed response", errPrefix)
	}
	if tr.AccessToken == "" {
		return "", 0, fmt.Errorf("%s: response has no access_token", errPrefix)
	}
	if tr.TokenType != "" && !strings.EqualFold(tr.TokenType, "Bearer") {
		return "", 0, fmt.Errorf("%s: unsupported token_type, want Bearer", errPrefix)
	}
	if tr.ExpiresIn <= 0 {
		return "", 0, fmt.Errorf("%s: response has no positive expires_in", errPrefix)
	}
	lifetime := maxOAuthTokenLifetime
	if tr.ExpiresIn < int64(maxOAuthTokenLifetime/time.Second) {
		lifetime = time.Duration(tr.ExpiresIn) * time.Second
	}
	return tr.AccessToken, lifetime, nil
}

// AccessToken is an OAuth 2.0 access token and the instant it expires.
type AccessToken struct {
	Value     string
	ExpiresAt time.Time
}

// MintAccessToken performs one client_credentials grant with creds, which
// must be OAuth 2.0 client credentials, and returns the access token with
// its expiry. It is for a caller that hands the token on instead of calling
// Jira itself: fullsend run gives it to the sandbox's Jira provider and
// mints a new one before it expires (fullsend#9220). The client secret
// never leaves this call. tokenURL empty means DefaultOAuthTokenURL; hc nil
// means a client with the same timeout and redirect policy as New. Errors
// name the credential variables and the token endpoint but never the
// client ID, client secret or response body.
func MintAccessToken(ctx context.Context, hc *http.Client, tokenURL string, creds Credentials) (AccessToken, error) {
	if !creds.OAuth() {
		return AccessToken{}, fmt.Errorf("jira: minting an access token needs %s and %s", ClientIDEnvVar, ClientSecretEnvVar)
	}
	if err := creds.validate(); err != nil {
		return AccessToken{}, err
	}
	if tokenURL = strings.TrimSpace(tokenURL); tokenURL == "" {
		tokenURL = DefaultOAuthTokenURL
	}
	if err := ValidateBaseURL(tokenURL); err != nil {
		return AccessToken{}, fmt.Errorf("jira: OAuth token URL: %w", err)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second, CheckRedirect: checkRedirect}
	}
	src := &oauthTokenSource{tokenURL: tokenURL, clientID: creds.ClientID, clientSecret: creds.ClientSecret}
	issued := time.Now()
	tok, lifetime, err := src.fetch(ctx, hc)
	if err != nil {
		return AccessToken{}, err
	}
	return AccessToken{Value: tok, ExpiresAt: issued.Add(lifetime)}, nil
}
