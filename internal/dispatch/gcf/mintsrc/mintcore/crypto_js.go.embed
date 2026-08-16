//go:build js

package mintcore

import (
	"fmt"
	"syscall/js"
)

// HostCryptoSigner signs JWTs via the host's Web Crypto API.
// The callback signature is:
//
//	signFn(headerPayload, pemBase64) => Promise<signatureBase64url>
//
// The host decodes the PEM, imports the RSA private key via
// crypto.subtle.importKey, and signs with RSASSA-PKCS1-v1_5 / SHA-256.
type HostCryptoSigner struct {
	signFn js.Value
}

// HostCryptoVerifier verifies RS256 signatures via the host's Web Crypto API.
// The callback signature is:
//
//	verifyFn(signingInput, signatureBase64url, jwkN, jwkE) => Promise<bool>
//
// The host constructs an RSA public key from the JWK parameters via
// crypto.subtle.importKey and verifies with RSASSA-PKCS1-v1_5 / SHA-256.
type HostCryptoVerifier struct {
	verifyFn js.Value
}

var (
	globalCryptoSigner   *HostCryptoSigner
	globalCryptoVerifier *HostCryptoVerifier
)

// SetCryptoSigner sets the global crypto signer for WASM builds.
// Must be called during initialization before any mint operations.
func SetCryptoSigner(signer *HostCryptoSigner) {
	globalCryptoSigner = signer
}

// SetCryptoVerifier sets the global crypto verifier for WASM builds.
// Must be called during initialization before any mint operations.
func SetCryptoVerifier(verifier *HostCryptoVerifier) {
	globalCryptoVerifier = verifier
}

// NewHostCryptoSigner wraps a JavaScript function as a crypto signer.
func NewHostCryptoSigner(signFn js.Value) (*HostCryptoSigner, error) {
	if signFn.IsUndefined() || signFn.IsNull() {
		return nil, fmt.Errorf("sign callback must not be null or undefined")
	}
	if signFn.Type() != js.TypeFunction {
		return nil, fmt.Errorf("sign callback must be a function, got %s", signFn.Type())
	}
	return &HostCryptoSigner{signFn: signFn}, nil
}

// NewHostCryptoVerifier wraps a JavaScript function as a crypto verifier.
func NewHostCryptoVerifier(verifyFn js.Value) (*HostCryptoVerifier, error) {
	if verifyFn.IsUndefined() || verifyFn.IsNull() {
		return nil, fmt.Errorf("verify callback must not be null or undefined")
	}
	if verifyFn.Type() != js.TypeFunction {
		return nil, fmt.Errorf("verify callback must be a function, got %s", verifyFn.Type())
	}
	return &HostCryptoVerifier{verifyFn: verifyFn}, nil
}

// SignRS256 signs a JWT header.payload string using the given PEM key data.
// Returns the base64url-encoded signature.
func (s *HostCryptoSigner) SignRS256(headerPayload string, pemData []byte) (string, error) {
	result, err := awaitPromise(s.signFn.Invoke(headerPayload, string(pemData)))
	if err != nil {
		return "", fmt.Errorf("host crypto sign failed: %w", err)
	}
	return result.String(), nil
}

// VerifyRS256 verifies an RS256 signature using JWK public key parameters.
func (v *HostCryptoVerifier) VerifyRS256(signingInput, signatureB64url, jwkN, jwkE string) (bool, error) {
	result, err := awaitPromise(v.verifyFn.Invoke(signingInput, signatureB64url, jwkN, jwkE))
	if err != nil {
		return false, fmt.Errorf("host crypto verify failed: %w", err)
	}
	return result.Bool(), nil
}
