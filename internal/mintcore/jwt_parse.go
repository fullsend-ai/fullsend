package mintcore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	jwksCacheTTL       = 1 * time.Hour
	maxKeysStaleness   = 24 * time.Hour
	minRefreshInterval = 30 * time.Second
	maxJWKSResponseLen = 512 * 1024
)

// jwtHeader represents the JOSE header of a JWT.
type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// jwksResponse represents a JSON Web Key Set response.
type jwksResponse struct {
	Keys []jwkKey `json:"keys"`
}

// jwkKey represents a single RSA public key in a JWKS.
type jwkKey struct {
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// discoveryDoc represents an OpenID Connect discovery document.
type discoveryDoc struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// parseAndValidateJWT parses a raw JWT string, validates the header and
// standard claims (issuer, audience, expiry, iat), and returns the parsed
// header and claims. It does NOT verify the cryptographic signature — that
// is platform-specific (Go crypto on non-WASM, host Web Crypto on WASM).
func parseAndValidateJWT(rawToken string, issuerURL, audience string) (*jwtHeader, *Claims, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return nil, nil, fmt.Errorf("invalid JWT format: expected 3 segments, got %d", len(parts))
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, fmt.Errorf("decoding JWT header: %w", err)
	}
	var header jwtHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, nil, fmt.Errorf("parsing JWT header: %w", err)
	}
	if header.Alg != "RS256" {
		return nil, nil, fmt.Errorf("unsupported signing algorithm: %s", header.Alg)
	}
	if header.Kid == "" {
		return nil, nil, fmt.Errorf("missing kid in JWT header")
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, fmt.Errorf("decoding JWT claims: %w", err)
	}
	var claims Claims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, nil, fmt.Errorf("parsing JWT claims: %w", err)
	}

	if claims.Issuer != issuerURL {
		return nil, nil, fmt.Errorf("unexpected issuer: %s", claims.Issuer)
	}
	if audience == "" {
		return nil, nil, fmt.Errorf("OIDC audience must be configured")
	}
	if !claims.Audience.Contains(audience) {
		return nil, nil, fmt.Errorf("audience mismatch")
	}

	now := time.Now().Unix()
	skew := int64(maxClockSkew.Seconds())
	if claims.Expiry <= now-skew {
		return nil, nil, fmt.Errorf("token expired")
	}
	if claims.IssuedAt == 0 {
		return nil, nil, fmt.Errorf("missing iat claim")
	}
	if claims.IssuedAt > now+skew {
		return nil, nil, fmt.Errorf("token issued in the future")
	}
	if claims.Repository == "" {
		return nil, nil, fmt.Errorf("missing repository claim")
	}

	return &header, &claims, nil
}
