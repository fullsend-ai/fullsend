//go:build js

package mintcore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"syscall/js"
)

// hostVerifyRS256Fn and hostSignRS256Fn hold JavaScript callback functions
// registered by the WASM host (Cloudflare Worker) for RSA crypto operations.
// This offloads crypto/rsa, crypto/x509, and math/big from the WASM binary,
// using the host's Web Crypto API (crypto.subtle) instead.
var (
	hostVerifyRS256Fn js.Value
	hostSignRS256Fn   js.Value
)

// RegisterHostCrypto stores the JavaScript callback functions for RSA
// crypto operations. Must be called during WASM initialization before
// any OIDC verification or App JWT signing occurs.
//
// verifyFn signature: verifyRS256(signingInput, signatureB64url, jwkJSON) => Promise<boolean>
// signFn signature:   signRS256(pemPEM, signingInput) => Promise<string(signatureB64url)>
func RegisterHostCrypto(verifyFn, signFn js.Value) error {
	for _, pair := range []struct {
		name string
		fn   js.Value
	}{
		{"verifyRS256", verifyFn},
		{"signRS256", signFn},
	} {
		if pair.fn.IsUndefined() || pair.fn.IsNull() {
			return fmt.Errorf("%s callback must not be null or undefined", pair.name)
		}
		if pair.fn.Type() != js.TypeFunction {
			return fmt.Errorf("%s callback must be a function, got %s", pair.name, pair.fn.Type())
		}
	}
	hostVerifyRS256Fn = verifyFn
	hostSignRS256Fn = signFn
	return nil
}

// verifyRS256Signature verifies an RS256 signature by delegating to the
// host's Web Crypto API via the registered verifyRS256 callback. The JWK
// is serialized to JSON and passed to the host, which imports the key
// with crypto.subtle.importKey and verifies with crypto.subtle.verify.
func verifyRS256Signature(signingInput string, signature []byte, key jwkKey) error {
	jwkJSON, err := json.Marshal(key)
	if err != nil {
		return fmt.Errorf("marshaling JWK for host verify: %w", err)
	}

	sigB64 := base64.RawURLEncoding.EncodeToString(signature)

	result, err := awaitPromise(hostVerifyRS256Fn.Invoke(
		signingInput,
		sigB64,
		string(jwkJSON),
	))
	if err != nil {
		return fmt.Errorf("host verifyRS256 failed: %w", err)
	}

	if !result.Bool() {
		return fmt.Errorf("invalid JWT signature")
	}
	return nil
}

// signRS256WithPEM signs the signingInput by delegating to the host's
// Web Crypto API via the registered signRS256 callback. The PEM-encoded
// private key is passed as a string; the host extracts the DER payload,
// imports with crypto.subtle.importKey, and signs with crypto.subtle.sign.
// Returns the raw signature bytes.
func signRS256WithPEM(pemData []byte, signingInput string) ([]byte, error) {
	result, err := awaitPromise(hostSignRS256Fn.Invoke(
		string(pemData),
		signingInput,
	))
	if err != nil {
		return nil, fmt.Errorf("host signRS256 failed: %w", err)
	}

	sigB64 := result.String()
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, fmt.Errorf("decoding host signature: %w", err)
	}
	return sig, nil
}
