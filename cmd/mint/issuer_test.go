package main

import "testing"

func TestOIDCIssuerURL(t *testing.T) {
	t.Setenv("MINT_OIDC_ISSUER_URL", "")
	if got := oidcIssuerURL(); got != defaultOIDCIssuerURL {
		t.Fatalf("default issuer = %q, want %q", got, defaultOIDCIssuerURL)
	}
	t.Setenv("MINT_OIDC_ISSUER_URL", " http://127.0.0.1:18302/ ")
	if got := oidcIssuerURL(); got != "http://127.0.0.1:18302" {
		t.Fatalf("issuer = %q, want the trimmed override", got)
	}
}
