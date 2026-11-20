package mintcore

import "testing"

func TestGitHubAPIBaseURL(t *testing.T) {
	t.Setenv("MINT_GITHUB_API_URL", "")
	t.Setenv("GITHUB_API_URL", "http://ignored.example/api/v3")
	if got, err := githubAPIBaseURL(); err != nil || got != defaultGitHubAPIBaseURL {
		t.Fatalf("default = %q, %v; want %q (GITHUB_API_URL must not leak in)", got, err, defaultGitHubAPIBaseURL)
	}
	for in, want := range map[string]string{
		" http://127.0.0.1:18302/api/v3/ ": "http://127.0.0.1:18302/api/v3",
		"https://ghes.example.com/api/v3":  "https://ghes.example.com/api/v3",
	} {
		t.Setenv("MINT_GITHUB_API_URL", in)
		if got, err := githubAPIBaseURL(); err != nil || got != want {
			t.Fatalf("MINT_GITHUB_API_URL=%q: base = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestGitHubAPIBaseURLRefusesInsecureValues(t *testing.T) {
	for _, in := range []string{
		"/",
		" // ",
		"http://ghes.example.com/api/v3",
		"https://user:pass@ghes.example.com/api/v3",
		"https://ghes.example.com/api/v3?x=1",
		"https://ghes.example.com/api/v3#f",
		"ftp://ghes.example.com",
		"ghes.example.com/api/v3",
		"http://[::1]:8080",
	} {
		t.Setenv("MINT_GITHUB_API_URL", in)
		if got, err := githubAPIBaseURL(); err == nil {
			t.Errorf("MINT_GITHUB_API_URL=%q accepted as %q", in, got)
		}
	}
}

func TestNewHandlerRefusesAnInsecureGitHubAPI(t *testing.T) {
	t.Setenv("MINT_GITHUB_API_URL", "http://ghes.example.com/api/v3")
	v, _ := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: "https://ghes.example.com/_services/token"})
	if _, err := NewHandler(nil, v); err == nil {
		t.Fatal("NewHandler accepted a plain-http GitHub API on a non-loopback host")
	}
}

func TestNewHandlerUsesTheConfiguredGitHubAPI(t *testing.T) {
	t.Setenv("MINT_GITHUB_API_URL", "https://ghes.example.com/api/v3/")
	v, _ := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: "https://ghes.example.com/_services/token"})
	h, err := NewHandler(nil, v)
	if err != nil {
		t.Fatal(err)
	}
	if h.githubBaseURL != "https://ghes.example.com/api/v3" {
		t.Fatalf("githubBaseURL = %q", h.githubBaseURL)
	}
}

// The Worker and GCF mints keep github.com's Actions issuer, so an API
// override alone must not start a handler there.
func TestNewHandlerRefusesAnAPIOverrideWithTheGitHubComIssuer(t *testing.T) {
	t.Setenv("MINT_GITHUB_API_URL", "https://ghes.example.com/api/v3")
	jwks, _ := NewJWKSVerifier(JWKSVerifierConfig{IssuerURL: githubActionsOIDCIssuer})
	for name, v := range map[string]OIDCVerifier{
		"Worker (JWKS, github.com issuer)": jwks,
		"other verifier":                   &fakeOIDCVerifier{},
	} {
		if _, err := NewHandler(nil, v); err == nil {
			t.Errorf("%s: NewHandler accepted an API override with the github.com issuer", name)
		}
	}
}
