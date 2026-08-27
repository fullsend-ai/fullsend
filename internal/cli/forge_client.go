package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/spf13/cobra"
)

var errGitLabTokenMissing = errors.New("no GitLab token found: set GITLAB_TOKEN or pass --gitlab-token")

// resolveGitLabToken returns a GitLab personal or project access token
// from the environment. Unlike GitHub tokens, there is no `glab auth
// token` fallback — the token must be set explicitly.
func resolveGitLabToken() (string, error) {
	if token := os.Getenv("GITLAB_TOKEN"); token != "" {
		return token, nil
	}
	return "", errGitLabTokenMissing
}

// newForgeClient creates a forge.Client for the given forge type.
// For GitHub, it uses the standard token resolution chain (GH_TOKEN,
// GITHUB_TOKEN, gh auth token). For GitLab, it uses GITLAB_TOKEN or
// the provided gitlabToken override.
//
// The baseURL parameter, when non-empty, sets the forge instance URL
// (from the manifest's forge section). It takes precedence over the
// GITLAB_API_URL / GITHUB_API_URL environment variables, which are
// kept as a fallback for callers that don't have a manifest yet
// (e.g., repos migrate).
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
		// Base URL precedence: explicit arg > env vars (via gl.URLEnvVars).
		// The env-var precedence is shared with gl.ResolveForgeHostPort().
		var opts []gl.Option
		if baseURL != "" {
			opts = append(opts, gl.WithBaseURL(baseURL))
		} else {
			for _, env := range gl.URLEnvVars {
				if envURL := strings.TrimSpace(os.Getenv(env)); envURL != "" {
					opts = append(opts, gl.WithBaseURL(envURL))
					break
				}
			}
		}
		opts = append(opts, glOpts...)
		return gl.New(token, opts...)
	case repos.ForgeGitHub, "":
		token, err := resolveToken()
		if err != nil {
			return nil, err
		}
		return newGitHubLiveClient(token, baseURL), nil
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
