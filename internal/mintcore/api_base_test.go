package mintcore

import "testing"

func TestGitHubAPIBaseURL(t *testing.T) {
	t.Setenv("MINT_GITHUB_API_URL", "")
	t.Setenv("GITHUB_API_URL", "http://ignored.example/api/v3")
	if got := githubAPIBaseURL(); got != defaultGitHubAPIBaseURL {
		t.Fatalf("default = %q, want %q (GITHUB_API_URL must not leak in)", got, defaultGitHubAPIBaseURL)
	}
	t.Setenv("MINT_GITHUB_API_URL", " http://127.0.0.1:18302/api/v3/ ")
	if got := githubAPIBaseURL(); got != "http://127.0.0.1:18302/api/v3" {
		t.Fatalf("base = %q, want the trimmed override", got)
	}
}
