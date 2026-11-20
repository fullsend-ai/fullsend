//go:build !js

package mintcore

import "testing"

// The GCF mint verifies through STS with GitHub Actions' fixed issuer, so an
// API override alone must not start its handler either.
func TestNewHandlerRefusesAnAPIOverrideWithSTS(t *testing.T) {
	t.Setenv("MINT_GITHUB_API_URL", "https://ghes.example.com/api/v3")
	if _, err := NewHandler(nil, &STSVerifier{}); err == nil {
		t.Fatal("NewHandler accepted an API override with the STS verifier")
	}
}
