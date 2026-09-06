package mintcore

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// cfAccessKeyCache holds cached JWKS keys from the Cloudflare Access
// certificate endpoint, using the same TTL and staleness rules as
// JWKSVerifier.
type cfAccessKeyCache struct {
	mu            sync.RWMutex
	keys          map[string]*rsa.PublicKey
	fetchedAt     time.Time
	lastKidMissAt time.Time
	refreshGroup  singleflight.Group
}

// cfAccessKeys is the package-level JWKS cache for CF Access tokens.
var cfAccessKeys cfAccessKeyCache

// cfAccessClaims holds the subset of Cloudflare Access JWT claims
// validated by the status endpoint.
type cfAccessClaims struct {
	Issuer   string   `json:"iss"`
	Audience Audience `json:"aud"`
	IssuedAt int64    `json:"iat"`
	Expiry   int64    `json:"exp"`
	Email    string   `json:"email"`
	Type     string   `json:"type"`
	Sub      string   `json:"sub"`
}

// validateStatusCFAccess authenticates a /v1/status request using a
// Cloudflare Access JWT (Managed OAuth for Cloudflare Access). It:
//
//  1. Checks that StatusCFAccessAud and StatusCFAccessTeam are configured.
//  2. Extracts the JWT from the Cf-Access-Jwt-Assertion header.
//  3. Validates the JWT signature against the CF Access JWKS endpoint.
//  4. Checks issuer, audience, and token timestamps.
//
// Returns nil on success. Returns errStatusAuthSkip when the validator
// is not configured or when no Cf-Access-Jwt-Assertion header is present.
// Returns an error on positive rejection (invalid token, expired, wrong
// audience). All status-auth failures collapse to 401.
func validateStatusCFAccess(ctx context.Context, r *http.Request) error {
	if StatusCFAccessAud == "" || StatusCFAccessTeam == "" {
		return errStatusAuthSkip
	}

	rawJWT := r.Header.Get("Cf-Access-Jwt-Assertion")
	if rawJWT == "" {
		return errStatusAuthSkip
	}

	issuer := "https://" + StatusCFAccessTeam + ".cloudflareaccess.com"

	parts := strings.Split(rawJWT, ".")
	if len(parts) != 3 {
		return fmt.Errorf("invalid CF Access JWT format: expected 3 segments, got %d", len(parts))
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("decoding CF Access JWT header: %w", err)
	}
	var header jwtHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return fmt.Errorf("parsing CF Access JWT header: %w", err)
	}
	if header.Alg != "RS256" {
		return fmt.Errorf("unsupported CF Access signing algorithm: %s", header.Alg)
	}
	if header.Kid == "" {
		return fmt.Errorf("missing kid in CF Access JWT header")
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("decoding CF Access JWT claims: %w", err)
	}
	var claims cfAccessClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return fmt.Errorf("parsing CF Access JWT claims: %w", err)
	}

	if claims.Issuer != issuer {
		return fmt.Errorf("CF Access issuer mismatch: got %q, want %q", claims.Issuer, issuer)
	}
	if !claims.Audience.Contains(StatusCFAccessAud) {
		return fmt.Errorf("CF Access audience mismatch")
	}

	now := time.Now().Unix()
	skew := int64(maxClockSkew.Seconds())
	if claims.Expiry <= now-skew {
		return fmt.Errorf("CF Access token expired")
	}
	if claims.IssuedAt == 0 {
		return fmt.Errorf("missing iat in CF Access token")
	}
	if claims.IssuedAt > now+skew {
		return fmt.Errorf("CF Access token issued in the future")
	}

	jwksURL := issuer + "/cdn-cgi/access/certs"
	key, err := cfAccessKeys.getKey(ctx, header.Kid, jwksURL)
	if err != nil {
		return fmt.Errorf("getting CF Access signing key: %w", err)
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("decoding CF Access JWT signature: %w", err)
	}
	signingInput := parts[0] + "." + parts[1]
	hashed := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hashed[:], signature); err != nil {
		return fmt.Errorf("invalid CF Access JWT signature")
	}

	log.Printf("status auth: CF Access authenticated user %q via team %s", claims.Email, StatusCFAccessTeam)
	return nil
}

// getKey returns the RSA public key for the given kid from the CF
// Access JWKS cache, refreshing from jwksURL if the kid is not found
// or the cache has expired. Uses the same TTL and refresh-throttle
// constants as JWKSVerifier.
func (c *cfAccessKeyCache) getKey(ctx context.Context, kid, jwksURL string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	key, ok := c.keys[kid]
	expired := time.Since(c.fetchedAt) > jwksCacheTTL
	recentKidMiss := time.Since(c.lastKidMissAt) < minRefreshInterval
	c.mu.RUnlock()

	if ok && !expired {
		return key, nil
	}

	if !ok && recentKidMiss {
		return nil, fmt.Errorf("CF Access key %q not found in JWKS", kid)
	}

	_, err, _ := c.refreshGroup.Do("refresh", func() (any, error) {
		return nil, c.refreshKeys(ctx, jwksURL)
	})
	if err != nil {
		if ok && time.Since(c.fetchedAt) <= maxKeysStaleness {
			return key, nil
		}
		return nil, err
	}

	c.mu.RLock()
	key, ok = c.keys[kid]
	c.mu.RUnlock()

	if !ok {
		c.mu.Lock()
		c.lastKidMissAt = time.Now()
		c.mu.Unlock()
		return nil, fmt.Errorf("CF Access key %q not found in JWKS", kid)
	}
	return key, nil
}

// refreshKeys fetches JWKS from the Cloudflare Access certificate
// endpoint. CF Access publishes its signing keys at
// https://<team>.cloudflareaccess.com/cdn-cgi/access/certs — no
// OIDC discovery is needed.
func (c *cfAccessKeyCache) refreshKeys(ctx context.Context, jwksURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return fmt.Errorf("creating CF Access JWKS request: %w", err)
	}

	resp, err := mintHTTP(req)
	if err != nil {
		return fmt.Errorf("fetching CF Access JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("CF Access JWKS endpoint returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSResponseLen))
	if err != nil {
		return fmt.Errorf("reading CF Access JWKS response: %w", err)
	}

	var jwks jwksResponse
	if err := json.Unmarshal(body, &jwks); err != nil {
		return fmt.Errorf("parsing CF Access JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		pub, err := parseRSAPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}

	c.mu.Lock()
	c.keys = keys
	c.fetchedAt = time.Now()
	c.mu.Unlock()

	return nil
}
