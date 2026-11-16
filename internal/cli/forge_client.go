package cli

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/forge"
	fj "github.com/fullsend-ai/fullsend/internal/forge/forgejo"
	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/spf13/cobra"
)

var errGitLabTokenMissing = errors.New("no GitLab token found: set GITLAB_TOKEN or pass --gitlab-token")
var errForgejoTokenMissing = errors.New("no Forgejo token found: set FORGEJO_TOKEN or GITEA_TOKEN")

// resolveGitLabToken returns a GitLab personal or project access token
// from the environment. Unlike GitHub tokens, there is no `glab auth
// token` fallback — the token must be set explicitly.
func resolveGitLabToken() (string, error) {
	if token := os.Getenv("GITLAB_TOKEN"); token != "" {
		return token, nil
	}
	return "", errGitLabTokenMissing
}

// resolveForgejoToken returns a Forgejo/Gitea access token from the
// environment (FORGEJO_TOKEN preferred, then GITEA_TOKEN).
func resolveForgejoToken() (string, error) {
	if token := os.Getenv("FORGEJO_TOKEN"); token != "" {
		return token, nil
	}
	if token := os.Getenv("GITEA_TOKEN"); token != "" {
		return token, nil
	}
	return "", errForgejoTokenMissing
}

// newForgeClient creates a forge.Client for the given forge type.
// For GitHub, it delegates to newAuthenticatedGitHubClient (GH_TOKEN,
// GITHUB_TOKEN, gh auth token). For GitLab, it uses GITLAB_TOKEN or
// the provided gitlabToken override. For Forgejo, it uses FORGEJO_TOKEN
// or GITEA_TOKEN (gitlabToken override is ignored).
//
// The baseURL parameter, when non-empty, sets the forge instance URL
// (from the manifest's forge section). It takes precedence over the
// GITLAB_API_URL / GITHUB_API_URL / FORGEJO_API_URL environment variables,
// which are kept as a fallback for callers that don't have a manifest yet.
func newForgeClient(forgeName, gitlabToken, baseURL string, glOpts ...gl.Option) (forge.Client, error) {
	switch forgeName {
	case repos.ForgeGitLab:
		token := gitlabToken
		if token == "" {
			var err error
			token, err = resolveGitLabToken()
			if err != nil {
				return nil, err
			}
		}
		// Base URL precedence: explicit arg > first non-empty env var of
		// gl.URLEnvVars. gl.ResolveBaseURL is shared with the credential
		// profile binding in run (resolveGitLabCredentialForgeHost), so
		// both use the same URL.
		var opts []gl.Option
		if raw, _ := gl.ResolveBaseURL(baseURL); raw != "" {
			opts = append(opts, gl.WithBaseURL(raw))
		}
		opts = append(opts, glOpts...)
		return gl.New(token, opts...)
	case repos.ForgeForgejo:
		token, err := resolveForgejoToken()
		if err != nil {
			return nil, err
		}
		var opts []fj.Option
		raw := baseURL
		if raw == "" {
			raw = os.Getenv("FORGEJO_API_URL")
		}
		if raw == "" {
			raw = os.Getenv("GITEA_API_URL")
		}
		if raw == "" {
			return nil, fmt.Errorf("forgejo API URL required: set FORGEJO_API_URL or GITEA_API_URL, or pass a manifest URL")
		}
		opts = append(opts, fj.WithBaseURL(raw))
		return fj.New(token, opts...), nil
	case repos.ForgeGitHub, "":
		return newAuthenticatedGitHubClient("", baseURL)
	default:
		return nil, fmt.Errorf("unsupported forge %q", forgeName)
	}
}

// forgeClientFactory lazily creates and caches per-forge API clients.
// Each client is created on first use and reused for subsequent calls
// with the same forge name. The sync.Mutex protects the client cache
// for concurrent goroutines in per-repo batch loops.
type forgeClientFactory struct {
	gitlabToken string
	githubURL   string
	gitlabURL   string
	mu          sync.Mutex
	clients     map[string]forge.Client
}

// newForgeClientFactory returns a ForgeClientFactory that lazily creates
// and caches forge clients. The manifest carries per-platform URLs;
// when a URL is set it takes precedence over the GITLAB_API_URL /
// GITHUB_API_URL environment variables.
//
// A GitLab token is only resolved if the factory is asked for a GitLab
// client, so single-forge GitHub manifests never require GITLAB_TOKEN.
func newForgeClientFactory(gitlabToken string, m *repos.Manifest) repos.ForgeClientFactory {
	var githubURL, gitlabURL string
	if m != nil {
		if m.GitHub != nil {
			githubURL = m.GitHub.URL
		}
		if m.GitLab != nil {
			gitlabURL = m.GitLab.URL
		}
	}
	return &forgeClientFactory{
		gitlabToken: gitlabToken,
		githubURL:   githubURL,
		gitlabURL:   gitlabURL,
		clients:     make(map[string]forge.Client),
	}
}

// ConfigFor returns a ForgeConfig with a live Client for the named forge.
// Clients are created lazily and cached — at most 2 clients per command
// invocation (one GitHub, one GitLab).
func (f *forgeClientFactory) ConfigFor(forgeName string) (repos.ForgeConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Normalize empty forge name to github (backward compat).
	if forgeName == "" {
		forgeName = repos.ForgeGitHub
	}

	client, ok := f.clients[forgeName]
	if !ok {
		baseURL := f.forgeURL(forgeName)
		var err error
		client, err = newForgeClient(forgeName, f.gitlabToken, baseURL)
		if err != nil {
			return repos.ForgeConfig{}, err
		}
		f.clients[forgeName] = client
	}

	cfg := repos.ForgeConfigFor(forgeName)
	cfg.Client = client
	return cfg, nil
}

// forgeURL returns the manifest-configured URL for the given forge.
// Returns "" when no URL is set, letting newForgeClient fall back to
// env vars or built-in defaults.
func (f *forgeClientFactory) forgeURL(forgeName string) string {
	switch forgeName {
	case repos.ForgeGitLab:
		return f.gitlabURL
	case repos.ForgeGitHub:
		return f.githubURL
	default:
		return ""
	}
}

// singleClientFactory wraps a single forge.Client as a ForgeClientFactory,
// returning the same client for any forge name. Used in tests and CLI
// test-override paths where a single FakeClient backs all operations.
type singleClientFactory struct {
	client forge.Client
}

func newSingleClientFactory(client forge.Client) repos.ForgeClientFactory {
	return &singleClientFactory{client: client}
}

func (f *singleClientFactory) ConfigFor(forgeName string) (repos.ForgeConfig, error) {
	if forgeName == "" {
		forgeName = repos.ForgeGitHub
	}
	cfg := repos.ForgeConfigFor(forgeName)
	cfg.Client = f.client
	return cfg, nil
}

// getGitLabToken extracts the --gitlab-token flag from the command chain.
func getGitLabToken(cmd *cobra.Command) string {
	token, _ := cmd.Flags().GetString("gitlab-token")
	if token == "" {
		token, _ = cmd.InheritedFlags().GetString("gitlab-token")
	}
	return token
}
