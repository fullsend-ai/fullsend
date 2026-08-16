//go:build js

package mintcore

import "fmt"

// GenerateAppJWT creates a signed RS256 JWT for GitHub App authentication
// by delegating the RSA signing to the host's Web Crypto API. The PEM key
// data is passed to the host, which parses it and signs using
// crypto.subtle.sign("RSASSA-PKCS1-v1_5", ...).
//
// This avoids importing crypto/rsa, crypto/x509, and encoding/pem in the
// WASM build, which together with net/http's crypto/tls dependency tree
// account for ~1 MB gzip in the binary.
func GenerateAppJWT(appID string, pemData []byte) (string, error) {
	if globalCryptoSigner == nil {
		return "", fmt.Errorf("crypto signer not initialized; call SetCryptoSigner during init")
	}

	signingInput, err := GenerateAppJWTPayload(appID)
	if err != nil {
		return "", err
	}

	signatureB64, err := globalCryptoSigner.SignRS256(signingInput, pemData)
	if err != nil {
		return "", fmt.Errorf("signing JWT via host crypto: %w", err)
	}

	return signingInput + "." + signatureB64, nil
}
