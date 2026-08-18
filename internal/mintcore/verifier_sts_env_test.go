//go:build !js

package mintcore

import (
	"testing"
)

func TestNewSTSVerifierFromEnv(t *testing.T) {
	t.Setenv("OIDC_AUDIENCE", "fullsend-mint")
	t.Setenv("GCP_PROJECT_NUMBER", "123456")
	t.Setenv("WIF_POOL_NAME", "test-pool")
	t.Setenv("WIF_PROVIDER_NAME", "test-provider")
	t.Setenv("PER_REPO_WIF_REPOS", "org/repo-a, Org/Repo-B")

	v, err := NewSTSVerifierFromEnv()
	if err != nil {
		t.Fatalf("NewSTSVerifierFromEnv: %v", err)
	}
	if v == nil {
		t.Fatal("expected non-nil verifier")
	}
	sv, ok := v.(*STSVerifier)
	if !ok {
		t.Fatalf("expected *STSVerifier, got %T", v)
	}
	if sv.oidcAudience != "fullsend-mint" {
		t.Fatalf("expected audience 'fullsend-mint', got %q", sv.oidcAudience)
	}
	if sv.gcpProjectNum != "123456" {
		t.Fatalf("expected gcpProjectNum '123456', got %q", sv.gcpProjectNum)
	}
	if !sv.perRepoWIFRepos["org/repo-a"] {
		t.Fatal("expected org/repo-a in perRepoWIFRepos")
	}
	if !sv.perRepoWIFRepos["org/repo-b"] {
		t.Fatal("expected org/repo-b (lowercased) in perRepoWIFRepos")
	}
}

func TestNewSTSVerifierFromEnv_EmptyAudience(t *testing.T) {
	t.Setenv("OIDC_AUDIENCE", "")

	_, err := NewSTSVerifierFromEnv()
	if err == nil {
		t.Fatal("expected error for empty OIDC_AUDIENCE")
	}
}
