//go:build !js

package mintcore

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
)

// verifyRS256Signature verifies an RS256 (RSASSA-PKCS1-v1_5 + SHA-256)
// signature using Go's crypto/rsa. The key is provided as a raw JWK entry
// and parsed into an *rsa.PublicKey for verification.
func verifyRS256Signature(signingInput string, signature []byte, key jwkKey) error {
	pub, err := parseRSAPublicKey(key.N, key.E)
	if err != nil {
		return fmt.Errorf("parsing JWK public key: %w", err)
	}
	hashed := sha256.Sum256([]byte(signingInput))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, hashed[:], signature)
}

// signRS256WithPEM signs the signingInput using an RSA private key from
// PEM-encoded data (PKCS1 or PKCS8). Returns the raw signature bytes.
func signRS256WithPEM(pemData []byte, signingInput string) ([]byte, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		pkcs8Key, pkcs8Err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if pkcs8Err != nil {
			return nil, fmt.Errorf("failed to parse private key (PKCS1: %v, PKCS8: %v)", err, pkcs8Err)
		}
		var ok bool
		key, ok = pkcs8Key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS8 key is not RSA")
		}
	}

	hashed := sha256.Sum256([]byte(signingInput))
	return rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
}

// parseRSAPublicKey constructs an *rsa.PublicKey from base64url-encoded
// modulus (N) and exponent (E) values as found in a JWK.
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
