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
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	jwksCacheTTL       = 1 * time.Hour
	maxKeysStaleness   = 24 * time.Hour
	minRefreshInterval = 30 * time.Second
	maxJWKSResponseLen = 512 * 1024
)

// JWKSVerifier validates GitHub Actions OIDC JWTs by fetching JWKS from
// the issuer's discovery endpoint and verifying RS256 signatures directly.
type JWKSVerifier struct {
	issuerURL            string
	audience             string
	httpClient           HTTPDoer
	allowedOrgs          []string
	allowedWorkflowFiles []string
	perRepoWIFRepos      map[string]bool

	mu            sync.RWMutex
	keys          map[string]*rsa.PublicKey
	cachedJWKSURI string
	fetchedAt     time.Time
	lastKidMissAt time.Time
	refreshGroup  singleflight.Group
}

// JWKSVerifierConfig configures a new JWKSVerifier.
type JWKSVerifierConfig struct {
	IssuerURL            string
	Audience             string
	HTTPClient           HTTPDoer
	AllowedOrgs          []string
	AllowedWorkflowFiles []string
	PerRepoWIFRepos      map[string]bool
}

// NewJWKSVerifier creates a verifier that validates tokens from issuerURL
// against the given audience. If httpClient is nil, http.DefaultClient is used.
func NewJWKSVerifier(opts JWKSVerifierConfig) *JWKSVerifier {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	perRepo := opts.PerRepoWIFRepos
	if perRepo == nil {
		perRepo = make(map[string]bool)
	}
	return &JWKSVerifier{
		issuerURL:            opts.IssuerURL,
		audience:             opts.Audience,
		httpClient:           httpClient,
		allowedOrgs:          opts.AllowedOrgs,
		allowedWorkflowFiles: opts.AllowedWorkflowFiles,
		perRepoWIFRepos:      perRepo,
	}
}

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

// Verify validates a raw JWT string and returns the parsed claims.
func (v *JWKSVerifier) Verify(ctx context.Context, rawToken string) (*Claims, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format: expected 3 segments, got %d", len(parts))
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decoding JWT header: %w", err)
	}
	var header jwtHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("parsing JWT header: %w", err)
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported signing algorithm: %s", header.Alg)
	}
	if header.Kid == "" {
		return nil, fmt.Errorf("missing kid in JWT header")
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding JWT claims: %w", err)
	}
	var claims Claims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, fmt.Errorf("parsing JWT claims: %w", err)
	}

	if claims.Issuer != v.issuerURL {
		return nil, fmt.Errorf("unexpected issuer: %s", claims.Issuer)
	}
	if v.audience == "" {
		return nil, fmt.Errorf("OIDC audience must be configured")
	}
	if !claims.Audience.Contains(v.audience) {
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

	key, err := v.getKey(ctx, header.Kid)
	if err != nil {
		return nil, fmt.Errorf("getting signing key: %w", err)
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decoding JWT signature: %w", err)
	}
	signingInput := parts[0] + "." + parts[1]
	hashed := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hashed[:], signature); err != nil {
		return nil, fmt.Errorf("invalid JWT signature")
	}

	if err := ValidateOrgAllowed(claims.RepositoryOwner, v.allowedOrgs); err != nil {
		return nil, err
	}
	if err := ValidateWorkflowRef(claims.JobWorkflowRef, claims.Repository, v.perRepoWIFRepos, v.allowedWorkflowFiles); err != nil {
		return nil, err
	}

	return &claims, nil
}

// getKey returns the RSA public key for the given kid, refreshing the
// JWKS cache if the kid is not found or the cache has expired.
// A minimum interval between kid-miss refreshes prevents thundering-herd
// JWKS fetches from tokens with unknown or random kid values.
func (v *JWKSVerifier) getKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	key, ok := v.keys[kid]
	expired := time.Since(v.fetchedAt) > jwksCacheTTL
	recentKidMiss := time.Since(v.lastKidMissAt) < minRefreshInterval
	v.mu.RUnlock()

	if ok && !expired {
		return key, nil
	}

	if !ok && recentKidMiss {
		return nil, fmt.Errorf("key %q not found in JWKS", kid)
	}

	_, err, _ := v.refreshGroup.Do("refresh", func() (interface{}, error) {
		return nil, v.refreshKeys(ctx)
	})
	if err != nil {
		if ok && time.Since(v.fetchedAt) <= maxKeysStaleness {
			return key, nil
		}
		return nil, err
	}

	v.mu.RLock()
	key, ok = v.keys[kid]
	v.mu.RUnlock()

	if !ok {
		v.mu.Lock()
		v.lastKidMissAt = time.Now()
		v.mu.Unlock()
		return nil, fmt.Errorf("key %q not found in JWKS", kid)
	}
	return key, nil
}

// refreshKeys fetches JWKS from the issuer's endpoint. The discovered
// jwks_uri is cached permanently (never re-discovered); a process restart
// is required if the issuer's JWKS URI changes. This avoids a second HTTP
// request on every refresh cycle.
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURI, nil)
	if err != nil {
		return fmt.Errorf("creating JWKS request: %w", err)
	}

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("JWKS endpoint returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSResponseLen))
	if err != nil {
		return fmt.Errorf("reading JWKS response: %w", err)
	}

	var jwks jwksResponse
	if err := json.Unmarshal(body, &jwks); err != nil {
		return fmt.Errorf("parsing JWKS: %w", err)
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

	v.mu.Lock()
	v.keys = keys
	v.cachedJWKSURI = jwksURI
	v.fetchedAt = time.Now()
	v.mu.Unlock()

	return nil
}

func (v *JWKSVerifier) discoverJWKSURI(ctx context.Context) (string, error) {
	discoveryURL := v.issuerURL + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating discovery request: %w", err)
	}

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching discovery document: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("discovery endpoint returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSResponseLen))
	if err != nil {
		return "", fmt.Errorf("reading discovery document: %w", err)
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

func parseRSAPublicKey(nB64, eB64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, fmt.Errorf("decoding modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, fmt.Errorf("decoding exponent: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	if n.BitLen() < 2048 {
		return nil, fmt.Errorf("RSA key too small: %d bits (minimum 2048)", n.BitLen())
	}
	e := new(big.Int).SetBytes(eBytes)
	if !e.IsInt64() {
		return nil, fmt.Errorf("exponent too large")
	}

	return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
}
