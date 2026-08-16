//go:build js

package mintcore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// JWKSVerifier validates GitHub Actions OIDC JWTs by fetching JWKS from
// the issuer's discovery endpoint. On WASM, it stores raw JWK entries
// and delegates RSA signature verification to the host's Web Crypto API,
// avoiding the crypto/rsa and math/big imports that add ~1 MB gzip.
type JWKSVerifier struct {
	issuerURL  string
	audience   string
	httpClient Doer

	mu            sync.RWMutex
	keys          map[string]jwkKey // raw JWK entries, not parsed RSA keys
	cachedJWKSURI string
	fetchedAt     time.Time
	lastKidMissAt time.Time
	refreshGroup  singleflight.Group
}

// JWKSVerifierConfig configures a new JWKSVerifier.
type JWKSVerifierConfig struct {
	IssuerURL  string
	Audience   string
	HTTPClient Doer
}

// NewJWKSVerifier creates a verifier that validates tokens from issuerURL
// against the given audience.
func NewJWKSVerifier(opts JWKSVerifierConfig) *JWKSVerifier {
	return &JWKSVerifier{
		issuerURL:  opts.IssuerURL,
		audience:   opts.Audience,
		httpClient: opts.HTTPClient,
	}
}

// Verify validates a raw JWT string and returns the parsed claims.
func (v *JWKSVerifier) Verify(ctx context.Context, rawToken string) (*Claims, error) {
	header, claims, err := parseAndValidateJWT(rawToken, v.issuerURL, v.audience)
	if err != nil {
		return nil, err
	}

	jwk, err := v.getKey(ctx, header.Kid)
	if err != nil {
		return nil, fmt.Errorf("getting signing key: %w", err)
	}

	// Verify the signature via host Web Crypto.
	if globalCryptoVerifier == nil {
		return nil, fmt.Errorf("crypto verifier not initialized; call SetCryptoVerifier during init")
	}

	parts := strings.Split(rawToken, ".")
	signingInput := parts[0] + "." + parts[1]
	signatureB64 := parts[2]

	valid, err := globalCryptoVerifier.VerifyRS256(signingInput, signatureB64, jwk.N, jwk.E)
	if err != nil {
		return nil, fmt.Errorf("verifying JWT signature: %w", err)
	}
	if !valid {
		return nil, fmt.Errorf("invalid JWT signature")
	}

	return claims, nil
}

// getKey returns the raw JWK for the given kid, refreshing the cache if needed.
func (v *JWKSVerifier) getKey(ctx context.Context, kid string) (jwkKey, error) {
	v.mu.RLock()
	key, ok := v.keys[kid]
	expired := time.Since(v.fetchedAt) > jwksCacheTTL
	recentKidMiss := time.Since(v.lastKidMissAt) < minRefreshInterval
	v.mu.RUnlock()

	if ok && !expired {
		return key, nil
	}

	if !ok && recentKidMiss {
		return jwkKey{}, fmt.Errorf("key %q not found in JWKS", kid)
	}

	_, err, _ := v.refreshGroup.Do("refresh", func() (interface{}, error) {
		return nil, v.refreshKeys(ctx)
	})
	if err != nil {
		if ok && time.Since(v.fetchedAt) <= maxKeysStaleness {
			return key, nil
		}
		return jwkKey{}, err
	}

	v.mu.RLock()
	key, ok = v.keys[kid]
	v.mu.RUnlock()

	if !ok {
		v.mu.Lock()
		v.lastKidMissAt = time.Now()
		v.mu.Unlock()
		return jwkKey{}, fmt.Errorf("key %q not found in JWKS", kid)
	}
	return key, nil
}

// refreshKeys fetches JWKS from the issuer's endpoint and caches raw JWK entries.
func (v *JWKSVerifier) refreshKeys(ctx context.Context) error {
	v.mu.RLock()
	jwksURI := v.cachedJWKSURI
	v.mu.RUnlock()

	if jwksURI == "" {
		var err error
		jwksURI, err = v.discoverJWKSURI(ctx)
		if err != nil {
			return fmt.Errorf("discovering JWKS URI: %w", err)
		}
	}

	status, body, err := v.httpClient.Do(ctx, "GET", jwksURI, nil, nil)
	if err != nil {
		return fmt.Errorf("fetching JWKS: %w", err)
	}

	if status != statusOK {
		return fmt.Errorf("JWKS endpoint returned status %d", status)
	}

	var jwks jwksResponse
	if err := json.Unmarshal(body, &jwks); err != nil {
		return fmt.Errorf("parsing JWKS: %w", err)
	}

	// Store raw JWK entries — no crypto/rsa parsing.
	// Validate minimum key size using base64 decoded modulus length.
	keys := make(map[string]jwkKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		// Basic validation: modulus must decode and be >= 2048 bits.
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil || len(nBytes)*8 < 2048 {
			continue
		}
		keys[k.Kid] = k
	}

	v.mu.Lock()
	v.keys = keys
	v.cachedJWKSURI = jwksURI
	v.fetchedAt = time.Now()
	v.mu.Unlock()

	return nil
}

func (v *JWKSVerifier) discoverJWKSURI(ctx context.Context) (string, error) {
	discoveryURL := v.issuerURL + "/.well-known/openid-configuration"

	status, body, err := v.httpClient.Do(ctx, "GET", discoveryURL, nil, nil)
	if err != nil {
		return "", fmt.Errorf("fetching discovery document: %w", err)
	}

	if status != statusOK {
		return "", fmt.Errorf("discovery endpoint returned status %d", status)
	}

	var doc discoveryDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("parsing discovery document: %w", err)
	}

	if doc.JWKSURI == "" {
		return "", fmt.Errorf("missing jwks_uri in discovery document")
	}

	if doc.Issuer != v.issuerURL {
		return "", fmt.Errorf("issuer mismatch in discovery document: got %q, want %q", doc.Issuer, v.issuerURL)
	}

	issuerOrigin, err := url.Parse(v.issuerURL)
	if err != nil {
		return "", fmt.Errorf("parsing issuer URL: %w", err)
	}
	jwksOrigin, err := url.Parse(doc.JWKSURI)
	if err != nil {
		return "", fmt.Errorf("parsing jwks_uri: %w", err)
	}
	if jwksOrigin.Scheme != issuerOrigin.Scheme || jwksOrigin.Host != issuerOrigin.Host {
		return "", fmt.Errorf("jwks_uri origin (%s://%s) does not match issuer origin (%s://%s)",
			jwksOrigin.Scheme, jwksOrigin.Host, issuerOrigin.Scheme, issuerOrigin.Host)
	}

	return doc.JWKSURI, nil
}
