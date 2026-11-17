package main

import "testing"

func TestOIDCIssuerURL(t *testing.T) {
	t.Setenv("MINT_OIDC_ISSUER_URL", "")
	if got, err := oidcIssuerURL(); err != nil || got != defaultOIDCIssuerURL {
		t.Fatalf("default issuer = %q, %v; want %q", got, err, defaultOIDCIssuerURL)
	}
	t.Setenv("MINT_OIDC_ISSUER_URL", " http://127.0.0.1:18302/ ")
	if got, err := oidcIssuerURL(); err != nil || got != "http://127.0.0.1:18302" {
		t.Fatalf("issuer = %q, %v; want the trimmed override", got, err)
	}
}

func TestOIDCIssuerURLRefusesInsecureValues(t *testing.T) {
	for _, in := range []string{"http://issuer.example", "https://user@issuer.example", "issuer.example"} {
		t.Setenv("MINT_OIDC_ISSUER_URL", in)
		if got, err := oidcIssuerURL(); err == nil {
			t.Errorf("MINT_OIDC_ISSUER_URL=%q accepted as %q", in, got)
		}
	}
}
