package actionsoidc

import "testing"

func envWith(server string) func(string) string {
	return func(k string) string {
		if k == "GITHUB_SERVER_URL" {
			return server
		}
		return ""
	}
}

func TestRequireGitHubOIDCHostAcceptsTheConfiguredServer(t *testing.T) {
	cases := []struct{ server, oidcURL string }{
		{"https://ghes.example.com", "https://ghes.example.com/_services/token?api-version=2.0"},
		{"https://GHES.example.com/", "https://ghes.example.com/_services/token"},
		{"https://ghemu.localhost:443", "https://ghemu.localhost:443/actions/oidc/token"},
		{"https://ghemu.localhost", "https://ghemu.localhost/actions/oidc/token"},
	}
	for _, c := range cases {
		if err := requireGitHubOIDCHost(c.oidcURL, envWith(c.server)); err != nil {
			t.Errorf("GITHUB_SERVER_URL=%q refused %q: %v", c.server, c.oidcURL, err)
		}
	}
}

func TestRequireGitHubOIDCHostRefusesOtherHosts(t *testing.T) {
	if err := requireGitHubOIDCHost("https://elsewhere.example/token", envWith("https://ghes.example.com")); err == nil {
		t.Error("a host other than the configured server was accepted")
	}
	if err := requireGitHubOIDCHost("https://ghes.example.com.evil.test/token", envWith("https://ghes.example.com")); err == nil {
		t.Error("a look-alike host was accepted")
	}
	if err := requireGitHubOIDCHost("http://ghes.example.com/token", envWith("https://ghes.example.com")); err == nil {
		t.Error("the configured server on port 80 was accepted for an https server URL")
	}
	if err := requireGitHubOIDCHost("https://ghes.example.com:8443/token", envWith("https://ghes.example.com")); err == nil {
		t.Error("another port on the configured server was accepted")
	}
	for _, server := range []string{"http://ghes.example.com", "https://user@ghes.example.com", "https://ghes.example.com/sub"} {
		if err := requireGitHubOIDCHost("https://ghes.example.com/token", envWith(server)); err == nil {
			t.Errorf("GITHUB_SERVER_URL=%q is not a valid server URL but its host was accepted", server)
		}
	}
}

func TestRequireGitHubOIDCHostOnGitHubCom(t *testing.T) {
	for _, server := range []string{"", "https://github.com", "https://GitHub.com/"} {
		if err := requireGitHubOIDCHost("https://github.com/token", envWith(server)); err == nil {
			t.Errorf("GITHUB_SERVER_URL=%q accepted github.com itself as a token service", server)
		}
		if err := requireGitHubOIDCHost("https://pipelines.actions.githubusercontent.com/abc/token", envWith(server)); err != nil {
			t.Errorf("GITHUB_SERVER_URL=%q refused GitHub's token service: %v", server, err)
		}
	}
}
