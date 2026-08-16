package mintcore

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyRS256Signature(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	nB64 := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	eB64 := base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1})
	jwk := jwkKey{
		Kty: "RSA",
		Alg: "RS256",
		Kid: "test-key",
		N:   nB64,
		E:   eB64,
	}

	signingInput := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0"
	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:]) // crypto.SHA256 = crypto.SHA256
	require.NoError(t, err)

	err = verifyRS256Signature(signingInput, sig, jwk)
	assert.NoError(t, err)
}

func TestVerifyRS256Signature_InvalidSignature(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	nB64 := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	eB64 := base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1})
	jwk := jwkKey{
		Kty: "RSA",
		Alg: "RS256",
		Kid: "test-key",
		N:   nB64,
		E:   eB64,
	}

	err = verifyRS256Signature("some.data", []byte("bad-signature"), jwk)
	assert.Error(t, err)
}

func TestSignRS256WithPEM(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	signingInput := "header.payload"
	sig, err := signRS256WithPEM(pemData, signingInput)
	require.NoError(t, err)
	assert.NotEmpty(t, sig)

	// Verify the signature with the public key.
	hashed := sha256.Sum256([]byte(signingInput))
	err = rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hashed[:], sig)
	assert.NoError(t, err, "signature should verify with the corresponding public key")
}

func TestSignRS256WithPEM_InvalidPEM(t *testing.T) {
	_, err := signRS256WithPEM([]byte("not a pem"), "header.payload")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "PEM")
}

func TestSignRS256WithPEM_PKCS8(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8Bytes,
	})

	signingInput := "header.payload"
	sig, err := signRS256WithPEM(pemData, signingInput)
	require.NoError(t, err)
	assert.NotEmpty(t, sig)

	// Verify the signature with the public key.
	hashed := sha256.Sum256([]byte(signingInput))
	err = rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hashed[:], sig)
	assert.NoError(t, err, "PKCS8 signature should verify")
}
