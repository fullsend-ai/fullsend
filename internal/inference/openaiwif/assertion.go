package openaiwif

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
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

// FetchAssertion requests a GitHub OIDC assertion for cfg.Audience and
// returns it with its iat and exp claims. Errors never include the
// assertion value.
func FetchAssertion(ctx context.Context, cfg AssertionConfig) (*Assertion, error) {
	if cfg.Audience == "" {
		return nil, fmt.Errorf("openaiwif: audience is required")
	}
	if cfg.OIDCRequestURL == "" {
		return nil, fmt.Errorf("openaiwif: ACTIONS_ID_TOKEN_REQUEST_URL is required")
	}
	if cfg.OIDCRequestToken == "" {
		return nil, fmt.Errorf("openaiwif: ACTIONS_ID_TOKEN_REQUEST_TOKEN is required")
	}
	if err := requireSecureURL("OIDC request URL", cfg.OIDCRequestURL); err != nil {
		return nil, fmt.Errorf("openaiwif: %w", err)
	}
	if err := requireGitHubOIDCHost(cfg.OIDCRequestURL); err != nil {
		return nil, fmt.Errorf("openaiwif: %w", err)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: httpTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	value, err := fetchAssertion(ctx, client, cfg.OIDCRequestURL, cfg.OIDCRequestToken, cfg.Audience)
	if err != nil {
		return nil, fmt.Errorf("openaiwif: assertion request failed: %w", err)
	}
	iat, exp, err := ParseJWTLifetime(value)
	if err != nil {
		return nil, fmt.Errorf("openaiwif: %w", err)
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
