package mintcore

// NewJWKSVerifierFromEnv constructs a JWKSVerifier wired to the
// package-level mintEnv and mintHTTP accessors. It reads OIDC_AUDIENCE
// from the environment and obtains an HTTP client from mintHTTP, then
// delegates to NewJWKSVerifier with plain data — keeping heavy
// constructors free of environment coupling.
//
// Use this as the VerifierFactory argument to NewHandler in entrypoints
// that validate tokens via direct JWKS (standalone binary, CF Worker
// WASM, devmint).
func NewJWKSVerifierFromEnv() (OIDCVerifier, error) {
	return NewJWKSVerifier(JWKSVerifierConfig{
		IssuerURL:  "https://token.actions.githubusercontent.com",
		Audience:   mintEnv("OIDC_AUDIENCE"),
		HTTPClient: mintHTTP(),
	})
}
