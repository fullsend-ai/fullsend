package appsetup

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPEMKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der := x509.MarshalPKCS1PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
}

func TestGenerateAppJWT(t *testing.T) {
	pemKey := testPEMKey(t)

	token, err := generateAppJWT(12345, pemKey)
	require.NoError(t, err)

	// JWT has three dot-separated segments.
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3, "JWT must have 3 parts")

	// Decode and verify header.
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var header map[string]string
	require.NoError(t, json.Unmarshal(headerJSON, &header))
	assert.Equal(t, "RS256", header["alg"])
	assert.Equal(t, "JWT", header["typ"])

	// Decode and verify claims.
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]interface{}
	require.NoError(t, json.Unmarshal(claimsJSON, &claims))
	assert.Equal(t, float64(12345), claims["iss"])
	assert.Contains(t, claims, "iat")
	assert.Contains(t, claims, "exp")

	// exp should be after iat.
	iat := claims["iat"].(float64)
	exp := claims["exp"].(float64)
	assert.Greater(t, exp, iat)
}

func TestGenerateAppJWT_InvalidPEM(t *testing.T) {
	_, err := generateAppJWT(1, []byte("not a pem key"))
	assert.Error(t, err)
}
