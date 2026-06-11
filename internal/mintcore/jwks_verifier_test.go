package mintcore

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testOIDCServer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	kid    string
}

func newTestOIDCServer(t *testing.T) *testOIDCServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	kid := "test-key-1"
	s := &testOIDCServer{key: key, kid: kid}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"issuer":   s.server.URL,
			"jwks_uri": s.server.URL + "/.well-known/jwks",
		})
	})
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{
				{
					"kty": "RSA",
					"alg": "RS256",
					"use": "sig",
					"kid": kid,
					"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
					"e":   base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
				},
			},
		})
	})

	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *testOIDCServer) signJWT(t *testing.T, headerOverrides, claimsOverrides map[string]interface{}) string {
	t.Helper()

	header := map[string]interface{}{
		"alg": "RS256",
		"typ": "JWT",
		"kid": s.kid,
	}
	for k, v := range headerOverrides {
		header[k] = v
	}

	now := time.Now()
	claims := map[string]interface{}{
		"iss":              s.server.URL,
		"aud":              "fullsend-mint",
		"iat":              now.Unix(),
		"exp":              now.Add(10 * time.Minute).Unix(),
		"repository":       "myorg/my-repo",
		"repository_owner": "myorg",
		"job_workflow_ref": "myorg/.fullsend/.github/workflows/dispatch.yml@refs/heads/main",
	}
	for k, v := range claimsOverrides {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}

	headerJSON, err := json.Marshal(header)
	require.NoError(t, err)
	claimsJSON, err := json.Marshal(claims)
	require.NoError(t, err)

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerB64 + "." + claimsB64

	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, hashed[:])
	require.NoError(t, err)

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestJWKSVerifier_ValidToken(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:            s.server.URL,
		Audience:             "fullsend-mint",
		AllowedOrgs:          []string{"myorg"},
		AllowedWorkflowFiles: []string{"*"},
	})
	token := s.signJWT(t, nil, nil)

	claims, err := v.Verify(t.Context(), token)
	require.NoError(t, err)
	assert.Equal(t, s.server.URL, claims.Issuer)
	assert.True(t, claims.Audience.Contains("fullsend-mint"))
	assert.Equal(t, "myorg/my-repo", claims.Repository)
	assert.Equal(t, "myorg", claims.RepositoryOwner)
}

func TestJWKSVerifier_InvalidFormat(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: s.server.URL, Audience: "fullsend-mint"})

	_, err := v.Verify(t.Context(), "not-a-jwt")
	assert.ErrorContains(t, err, "expected 3 segments")
}

func TestJWKSVerifier_WrongAlgorithm(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: s.server.URL, Audience: "fullsend-mint"})
	token := s.signJWT(t, map[string]interface{}{"alg": "HS256"}, nil)

	_, err := v.Verify(t.Context(), token)
	assert.ErrorContains(t, err, "unsupported signing algorithm")
}

func TestJWKSVerifier_WrongIssuer(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: s.server.URL, Audience: "fullsend-mint"})
	token := s.signJWT(t, nil, map[string]interface{}{"iss": "https://evil.com"})

	_, err := v.Verify(t.Context(), token)
	assert.ErrorContains(t, err, "unexpected issuer")
}

func TestJWKSVerifier_WrongAudience(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: s.server.URL, Audience: "fullsend-mint"})
	token := s.signJWT(t, nil, map[string]interface{}{"aud": "wrong-audience"})

	_, err := v.Verify(t.Context(), token)
	assert.ErrorContains(t, err, "audience mismatch")
}

func TestJWKSVerifier_ExpiredToken(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: s.server.URL, Audience: "fullsend-mint"})
	past := time.Now().Add(-10 * time.Minute).Unix()
	token := s.signJWT(t, nil, map[string]interface{}{
		"iat": past - 600,
		"exp": past,
	})

	_, err := v.Verify(t.Context(), token)
	assert.ErrorContains(t, err, "token expired")
}

func TestJWKSVerifier_FutureToken(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: s.server.URL, Audience: "fullsend-mint"})
	future := time.Now().Add(10 * time.Minute).Unix()
	token := s.signJWT(t, nil, map[string]interface{}{"iat": future})

	_, err := v.Verify(t.Context(), token)
	assert.ErrorContains(t, err, "token issued in the future")
}

func TestJWKSVerifier_MissingRepository(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: s.server.URL, Audience: "fullsend-mint"})
	token := s.signJWT(t, nil, map[string]interface{}{"repository": ""})

	_, err := v.Verify(t.Context(), token)
	assert.ErrorContains(t, err, "missing repository claim")
}

func TestJWKSVerifier_InvalidSignature(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: s.server.URL, Audience: "fullsend-mint"})
	token := s.signJWT(t, nil, nil)

	// Tamper with the signature
	parts := token[:len(token)-4] + "XXXX"
	_, err := v.Verify(t.Context(), parts)
	assert.ErrorContains(t, err, "invalid JWT signature")
}

func TestJWKSVerifier_UnknownKid(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: s.server.URL, Audience: "fullsend-mint"})
	token := s.signJWT(t, map[string]interface{}{"kid": "unknown-key"}, nil)

	_, err := v.Verify(t.Context(), token)
	assert.ErrorContains(t, err, "not found in JWKS")
}

func TestJWKSVerifier_KeyRotation(t *testing.T) {
	key1, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	key2, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	currentKey := key1
	currentKid := "key-1"

	mux := http.NewServeMux()
	var server *httptest.Server

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"issuer":   server.URL,
			"jwks_uri": server.URL + "/.well-known/jwks",
		})
	})
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{
				{
					"kty": "RSA", "alg": "RS256", "use": "sig",
					"kid": currentKid,
					"n":   base64.RawURLEncoding.EncodeToString(currentKey.N.Bytes()),
					"e":   base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
				},
			},
		})
	})

	server = httptest.NewServer(mux)
	defer server.Close()

	v := NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:            server.URL,
		Audience:             "fullsend-mint",
		AllowedOrgs:          []string{"myorg"},
		AllowedWorkflowFiles: []string{"*"},
	})

	signToken := func(kid string, key *rsa.PrivateKey) string {
		now := time.Now()
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
		claims, _ := json.Marshal(map[string]interface{}{
			"iss": server.URL, "aud": "fullsend-mint",
			"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
			"repository": "myorg/my-repo", "repository_owner": "myorg",
			"job_workflow_ref": "myorg/.fullsend/.github/workflows/dispatch.yml@refs/heads/main",
		})
		hB64 := base64.RawURLEncoding.EncodeToString(header)
		cB64 := base64.RawURLEncoding.EncodeToString(claims)
		input := hB64 + "." + cB64
		hashed := sha256.Sum256([]byte(input))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
		return fmt.Sprintf("%s.%s", input, base64.RawURLEncoding.EncodeToString(sig))
	}

	// First verify with key-1
	token1 := signToken("key-1", key1)
	_, err = v.Verify(t.Context(), token1)
	require.NoError(t, err)

	// Rotate to key-2 — the cache should refresh on kid miss
	currentKey = key2
	currentKid = "key-2"

	token2 := signToken("key-2", key2)
	_, err = v.Verify(t.Context(), token2)
	require.NoError(t, err)
}

func TestJWKSVerifier_AudienceArray(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:            s.server.URL,
		Audience:             "fullsend-mint",
		AllowedOrgs:          []string{"myorg"},
		AllowedWorkflowFiles: []string{"*"},
	})
	token := s.signJWT(t, nil, map[string]interface{}{
		"aud": []string{"other", "fullsend-mint"},
	})

	claims, err := v.Verify(t.Context(), token)
	require.NoError(t, err)
	assert.True(t, claims.Audience.Contains("fullsend-mint"))
}

func TestJWKSVerifier_JWKSURIOriginMismatch(t *testing.T) {
	mux := http.NewServeMux()
	var server *httptest.Server

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"issuer":   server.URL,
			"jwks_uri": "https://evil.com/.well-known/jwks",
		})
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	v := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: server.URL, Audience: "fullsend-mint"})
	err := v.refreshKeys(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match issuer origin")
}

func TestJWKSVerifier_DiscoveryIssuerMismatch(t *testing.T) {
	mux := http.NewServeMux()
	var server *httptest.Server

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"issuer":   "https://evil.com",
			"jwks_uri": server.URL + "/.well-known/jwks",
		})
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	v := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: server.URL, Audience: "fullsend-mint"})
	err := v.refreshKeys(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "issuer mismatch in discovery document")
}

func TestJWKSVerifier_OrgNotAllowed(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:            s.server.URL,
		Audience:             "fullsend-mint",
		AllowedOrgs:          []string{"allowed-org"},
		AllowedWorkflowFiles: []string{"*"},
	})
	token := s.signJWT(t, nil, nil) // default claims use "myorg"

	_, err := v.Verify(t.Context(), token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in allowed orgs")
}

func TestJWKSVerifier_WorkflowNotAllowed(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:            s.server.URL,
		Audience:             "fullsend-mint",
		AllowedOrgs:          []string{"myorg"},
		AllowedWorkflowFiles: []string{"only-this.yml"},
	})
	token := s.signJWT(t, nil, nil) // default workflow is dispatch.yml

	_, err := v.Verify(t.Context(), token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in allowed list")
}

func TestJWKSVerifier_EmptyOrgList_FailsClosed(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:            s.server.URL,
		Audience:             "fullsend-mint",
		AllowedWorkflowFiles: []string{"*"},
	})
	token := s.signJWT(t, nil, nil)

	_, err := v.Verify(t.Context(), token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in allowed orgs")
}

func TestJWKSVerifier_EmptyAudience_FailsClosed(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:            s.server.URL,
		AllowedOrgs:          []string{"myorg"},
		AllowedWorkflowFiles: []string{"*"},
	})
	token := s.signJWT(t, nil, nil)

	_, err := v.Verify(t.Context(), token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OIDC audience must be configured")
}

func TestJWKSVerifier_MissingIat(t *testing.T) {
	s := newTestOIDCServer(t)
	v := NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:            s.server.URL,
		Audience:             "fullsend-mint",
		AllowedOrgs:          []string{"myorg"},
		AllowedWorkflowFiles: []string{"*"},
	})
	token := s.signJWT(t, nil, map[string]interface{}{"iat": 0})

	_, err := v.Verify(t.Context(), token)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing iat claim")
}

func TestJWKSVerifier_StaleKeyFallback(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	kid := "test-key-1"
	fetchCount := 0

	mux := http.NewServeMux()
	var server *httptest.Server

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"issuer":   server.URL,
			"jwks_uri": server.URL + "/.well-known/jwks",
		})
	})
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
		fetchCount++
		if fetchCount > 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{{
				"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
			}},
		})
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	v := NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:            server.URL,
		Audience:             "fullsend-mint",
		AllowedOrgs:          []string{"myorg"},
		AllowedWorkflowFiles: []string{"*"},
	})

	signToken := func() string {
		now := time.Now()
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
		claims, _ := json.Marshal(map[string]interface{}{
			"iss": server.URL, "aud": "fullsend-mint",
			"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
			"repository": "myorg/my-repo", "repository_owner": "myorg",
			"job_workflow_ref": "myorg/.fullsend/.github/workflows/dispatch.yml@refs/heads/main",
		})
		hB64 := base64.RawURLEncoding.EncodeToString(header)
		cB64 := base64.RawURLEncoding.EncodeToString(claims)
		input := hB64 + "." + cB64
		hashed := sha256.Sum256([]byte(input))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
		return fmt.Sprintf("%s.%s", input, base64.RawURLEncoding.EncodeToString(sig))
	}

	// First verify populates the cache
	_, err = v.Verify(t.Context(), signToken())
	require.NoError(t, err)

	// Expire the cache to force a refresh attempt
	v.mu.Lock()
	v.fetchedAt = time.Now().Add(-2 * jwksCacheTTL)
	v.mu.Unlock()

	// Second verify should fall back to stale key when JWKS endpoint fails
	_, err = v.Verify(t.Context(), signToken())
	require.NoError(t, err, "should fall back to stale cached key when refresh fails")
	assert.Equal(t, 2, fetchCount, "should have attempted a refresh")
}

func TestJWKSVerifier_StaleKeyRejectedAfterMaxStaleness(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	kid := "test-key-1"
	fetchCount := 0

	mux := http.NewServeMux()
	var server *httptest.Server

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"issuer":   server.URL,
			"jwks_uri": server.URL + "/.well-known/jwks",
		})
	})
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
		fetchCount++
		if fetchCount > 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"keys": []map[string]string{{
				"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
			}},
		})
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	v := NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:            server.URL,
		Audience:             "fullsend-mint",
		AllowedOrgs:          []string{"myorg"},
		AllowedWorkflowFiles: []string{"*"},
	})

	signToken := func() string {
		now := time.Now()
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
		claims, _ := json.Marshal(map[string]interface{}{
			"iss": server.URL, "aud": "fullsend-mint",
			"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
			"repository": "myorg/my-repo", "repository_owner": "myorg",
			"job_workflow_ref": "myorg/.fullsend/.github/workflows/dispatch.yml@refs/heads/main",
		})
		hB64 := base64.RawURLEncoding.EncodeToString(header)
		cB64 := base64.RawURLEncoding.EncodeToString(claims)
		input := hB64 + "." + cB64
		hashed := sha256.Sum256([]byte(input))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
		return fmt.Sprintf("%s.%s", input, base64.RawURLEncoding.EncodeToString(sig))
	}

	_, err = v.Verify(t.Context(), signToken())
	require.NoError(t, err)

	// Set fetchedAt beyond the maximum staleness window
	v.mu.Lock()
	v.fetchedAt = time.Now().Add(-maxKeysStaleness - time.Hour)
	v.mu.Unlock()

	_, err = v.Verify(t.Context(), signToken())
	require.Error(t, err, "should reject stale keys beyond max staleness window")
}

func TestParseRSAPublicKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	nB64 := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	eB64 := base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1})

	pub, err := parseRSAPublicKey(nB64, eB64)
	require.NoError(t, err)
	assert.Equal(t, key.N, pub.N)
	assert.Equal(t, 65537, pub.E)
}
