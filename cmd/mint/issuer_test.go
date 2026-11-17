package main

import "testing"

func TestOIDCIssuerURL(t *testing.T) {
	t.Setenv("MINT_OIDC_ISSUER_URL", "")
	t.Setenv("MINT_GITHUB_API_URL", "")
	if got, err := oidcIssuerURL(); err != nil || got != defaultOIDCIssuerURL {
		t.Fatalf("default issuer = %q, %v; want %q", got, err, defaultOIDCIssuerURL)
	}
	t.Setenv("MINT_GITHUB_API_URL", "http://127.0.0.1:18302/api/v3")
	for in, want := range map[string]string{
		" http://127.0.0.1:18302 ":  "http://127.0.0.1:18302",
		"https://issuer.example/x/": "https://issuer.example/x/",
	} {
		t.Setenv("MINT_OIDC_ISSUER_URL", in)
		if got, err := oidcIssuerURL(); err != nil || got != want {
			t.Fatalf("MINT_OIDC_ISSUER_URL=%q: issuer = %q, %v; want %q (kept as given)", in, got, err, want)
		}
	}
}

func TestOIDCIssuerURLNeedsBothOverrides(t *testing.T) {
	for _, tc := range []struct{ issuer, api string }{
		{"", "https://ghes.example.com/api/v3"},
		{"https://ghes.example.com/_services/token", ""},
	} {
		t.Setenv("MINT_OIDC_ISSUER_URL", tc.issuer)
		t.Setenv("MINT_GITHUB_API_URL", tc.api)
		if got, err := oidcIssuerURL(); err == nil {
			t.Errorf("issuer=%q api=%q accepted as %q", tc.issuer, tc.api, got)
		}
	}
}

func TestOIDCIssuerURLRefusesInsecureValues(t *testing.T) {
	t.Setenv("MINT_GITHUB_API_URL", "https://ghes.example.com/api/v3")
	for _, in := range []string{"http://issuer.example", "https://user@issuer.example", "issuer.example"} {
		t.Setenv("MINT_OIDC_ISSUER_URL", in)
		if got, err := oidcIssuerURL(); err == nil {
			t.Errorf("MINT_OIDC_ISSUER_URL=%q accepted as %q", in, got)
		}
	}
}
