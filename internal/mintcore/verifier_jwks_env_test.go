package mintcore

import (
	"testing"
)

func TestNewJWKSVerifierFromEnv(t *testing.T) {
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")

	v, err := NewJWKSVerifierFromEnv()
	if err != nil {
		t.Fatalf("NewJWKSVerifierFromEnv: %v", err)
	}
	if v == nil {
		t.Fatal("expected non-nil verifier")
	}
	jv, ok := v.(*JWKSVerifier)
	if !ok {
		t.Fatalf("expected *JWKSVerifier, got %T", v)
	}
	if jv.audience != "fullsend-mint" {
		t.Fatalf("expected audience 'fullsend-mint', got %q", jv.audience)
	}
}

func TestNewJWKSVerifierFromEnv_EmptyAudience(t *testing.T) {
	t.Setenv("OIDC_AUDIENCE", "")

	_, err := NewJWKSVerifierFromEnv()
	if err == nil {
		t.Fatal("expected error for empty OIDC_AUDIENCE")
	}
}
