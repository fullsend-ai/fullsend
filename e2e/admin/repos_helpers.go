//go:build e2e

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

const (
	defaultReposE2EGitHubOrg   = "fullsend-repos-e2e-gh"
	defaultReposE2EGitLabGroup = "fullsend-repos-e2e-gl"

	ephemeralRepoPrefix = "repos-e2e-"
)

func reposE2EGitHubOrg() string {
	if v := os.Getenv("REPOS_E2E_GITHUB_ORG"); v != "" {
		return v
	}
	return defaultReposE2EGitHubOrg
}

func reposE2EGitLabGroup() string {
	if v := os.Getenv("REPOS_E2E_GITLAB_GROUP"); v != "" {
		return v
	}
	return defaultReposE2EGitLabGroup
}

// reposTestEnv holds shared state for a repos lifecycle e2e test run.
type reposTestEnv struct {
	binary  string
	ghToken string
	glToken string
	ghOrg   string
	glGroup string

	ghClient *gh.LiveClient
	glClient *gl.LiveClient

	manifestDir string
	runID       string
}

func (e *reposTestEnv) cliEnv() map[string]string {
	return map[string]string{
		"GH_TOKEN":     e.ghToken,
		"GITHUB_TOKEN": e.ghToken,
		"GITLAB_TOKEN": e.glToken,
	}
}

func setupReposTest(t *testing.T) *reposTestEnv {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping repos e2e test in short mode")
	}

	binary := e2etest.BuildCLIBinary(t)

	ghToken := os.Getenv("REPOS_E2E_GITHUB_TOKEN")
	if ghToken == "" {
		t.Skip("REPOS_E2E_GITHUB_TOKEN not set, skipping repos e2e test")
	}
	glToken := os.Getenv("GITLAB_TOKEN")
	if glToken == "" {
		t.Skip("GITLAB_TOKEN not set, skipping repos e2e test")
	}

	ghClient := e2etest.NewLiveClient(ghToken)

	glClient, err := gl.New(glToken)
	require.NoError(t, err, "creating GitLab client")

	ghOrg := reposE2EGitHubOrg()
	glGroup := reposE2EGitLabGroup()

	cleanupStaleEphemeralRepos(t, ghClient, glClient, ghToken, glToken, ghOrg, glGroup)

	env := &reposTestEnv{
		binary:      binary,
		ghToken:     ghToken,
		glToken:     glToken,
		ghOrg:       ghOrg,
		glGroup:     glGroup,
		ghClient:    ghClient,
		glClient:    glClient,
		manifestDir: t.TempDir(),
		runID:       uuid.New().String()[:8],
	}

	return env
}

func createEphemeralGitHubRepo(t *testing.T, client forge.Client, org, suffix string) string {
	t.Helper()
	ctx := context.Background()
	name := ephemeralRepoPrefix + suffix

	t.Logf("[setup] Creating GitHub repo %s/%s", org, name)
	_, err := client.CreateRepo(ctx, org, name, "repos e2e test (auto-deleted)", false)
	require.NoError(t, err, "creating GitHub repo %s/%s", org, name)

	t.Cleanup(func() {
		t.Logf("[cleanup] Deleting GitHub repo %s/%s", org, name)
		if delErr := client.DeleteRepo(context.Background(), org, name); delErr != nil {
			t.Logf("[cleanup] failed to delete GitHub repo %s/%s: %v", org, name, delErr)
		}
	})

	return name
}

func createEphemeralGitLabRepo(t *testing.T, client forge.Client, group, suffix string) string {
	t.Helper()
	ctx := context.Background()
	name := ephemeralRepoPrefix + suffix

	t.Logf("[setup] Creating GitLab project %s/%s", group, name)
	_, err := client.CreateRepo(ctx, group, name, "repos e2e test (auto-deleted)", false)
	require.NoError(t, err, "creating GitLab project %s/%s", group, name)

	t.Cleanup(func() {
		t.Logf("[cleanup] Deleting GitLab project %s/%s", group, name)
		if delErr := client.DeleteRepo(context.Background(), group, name); delErr != nil {
			t.Logf("[cleanup] failed to delete GitLab project %s/%s: %v", group, name, delErr)
		}
	})

	return name
}

func writeTestManifest(t *testing.T, path string, m *repos.Manifest) {
	t.Helper()
	data, err := repos.MarshalWithHeader(m)
	require.NoError(t, err, "marshalling manifest")
	require.NoError(t, os.WriteFile(path, data, 0o644), "writing manifest")
}

func readTestManifest(t *testing.T, path string) *repos.Manifest {
	t.Helper()
	ctx := context.Background()
	m, err := repos.LoadManifest(ctx, path)
	require.NoError(t, err, "reading manifest")
	return m
}

type statusJSON struct {
	Repos []struct {
		Owner      string `json:"owner"`
		Repo       string `json:"repo"`
		Installed  bool   `json:"installed"`
		CurrentRef string `json:"current_ref"`
		Error      string `json:"error"`
	} `json:"repos"`
	Summary struct {
		Total        int `json:"total"`
		Installed    int `json:"installed"`
		NotInstalled int `json:"not_installed"`
		Drifted      int `json:"drifted"`
		Errored      int `json:"errored"`
	} `json:"summary"`
}

func parseStatusJSON(t *testing.T, output string) *statusJSON {
	t.Helper()
	jsonStr := extractJSON(output)
	var s statusJSON
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &s), "parsing status JSON")
	return &s
}

type diffJSON struct {
	Changes []struct {
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
		Field string `json:"field"`
	} `json:"changes"`
	Warnings []string `json:"warnings"`
}

func parseDiffJSON(t *testing.T, output string) *diffJSON {
	t.Helper()
	jsonStr := extractJSON(output)
	var d diffJSON
	require.NoError(t, json.Unmarshal([]byte(jsonStr), &d), "parsing diff JSON")
	return &d
}

func extractJSON(output string) string {
	start := strings.Index(output, "{")
	if start < 0 {
		return output
	}
	dec := json.NewDecoder(strings.NewReader(output[start:]))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return output[start:]
	}
	return string(raw)
}

const (
	staleRepoThreshold      = 1 * time.Hour
	repoVisibilityTimeout   = 30 * time.Second
	repoVisibilityPollDelay = 2 * time.Second
)

func waitForRepoVisible(t *testing.T, client forge.Client, owner, repo string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), repoVisibilityTimeout)
	defer cancel()
	for {
		_, err := client.GetRepo(ctx, owner, repo)
		if err == nil {
			return
		}
		t.Logf("[setup] Waiting for %s/%s to become visible...", owner, repo)
		select {
		case <-time.After(repoVisibilityPollDelay):
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s/%s to become visible", owner, repo)
		}
	}
}

func cleanupStaleEphemeralRepos(t *testing.T, ghClient *gh.LiveClient, glClient *gl.LiveClient, ghToken, glToken, ghOrg, glGroup string) {
	t.Helper()
	ctx := context.Background()

	t.Log("[cleanup] Scanning for stale ephemeral repos...")

	ghRepos, err := ghClient.ListOrgRepos(ctx, ghOrg, true)
	if err != nil {
		t.Logf("[cleanup] Warning: could not list GitHub repos in %s: %v", ghOrg, err)
	} else {
		for _, r := range ghRepos {
			if !strings.HasPrefix(r.Name, ephemeralRepoPrefix) {
				continue
			}
			createdAt, ageErr := e2etest.GetRepoCreatedAt(ctx, ghToken, ghOrg, r.Name)
			if ageErr != nil {
				t.Logf("[cleanup] Warning: could not check age of %s/%s: %v", ghOrg, r.Name, ageErr)
				continue
			}
			if time.Since(createdAt) > staleRepoThreshold {
				t.Logf("[cleanup] Deleting stale GitHub repo %s/%s (age: %s)", ghOrg, r.Name, time.Since(createdAt).Truncate(time.Second))
				if delErr := ghClient.DeleteRepo(ctx, ghOrg, r.Name); delErr != nil {
					t.Logf("[cleanup] Warning: failed to delete %s/%s: %v", ghOrg, r.Name, delErr)
				}
			}
		}
	}

	glRepos, err := glClient.ListOrgRepos(ctx, glGroup, true)
	if err != nil {
		t.Logf("[cleanup] Warning: could not list GitLab projects in %s: %v", glGroup, err)
	} else {
		for _, r := range glRepos {
			if !strings.HasPrefix(r.Name, ephemeralRepoPrefix) {
				continue
			}
			createdAt, ageErr := getGitLabProjectCreatedAt(ctx, glToken, glGroup, r.Name)
			if ageErr != nil {
				t.Logf("[cleanup] Warning: could not check age of %s/%s: %v", glGroup, r.Name, ageErr)
				continue
			}
			if time.Since(createdAt) > staleRepoThreshold {
				t.Logf("[cleanup] Deleting stale GitLab project %s/%s (age: %s)", glGroup, r.Name, time.Since(createdAt).Truncate(time.Second))
				if delErr := glClient.DeleteRepo(ctx, glGroup, r.Name); delErr != nil {
					t.Logf("[cleanup] Warning: failed to delete %s/%s: %v", glGroup, r.Name, delErr)
				}
			}
		}
	}

	t.Log("[cleanup] Stale ephemeral repo scan complete")
}

func getGitLabProjectCreatedAt(ctx context.Context, token, group, repo string) (time.Time, error) {
	projectPath := url.PathEscape(group + "/" + repo)
	apiURL := fmt.Sprintf("https://gitlab.com/api/v4/projects/%s", projectPath)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return time.Time{}, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("fetching project: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var result struct {
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return time.Time{}, fmt.Errorf("decoding response: %w", err)
	}

	return result.CreatedAt, nil
}

func buildCombinedManifest(ghOrg, glGroup string, ghRepos, glRepos []string) *repos.Manifest {
	m := &repos.Manifest{
		Version: 1,
		Mint: repos.MintConfig{
			URL:     "https://mint.example.com",
			Project: "dummy-project",
			Region:  "us-central1",
		},
		Defaults: repos.DefaultsConfig{
			Forge:            repos.ForgeGitHub,
			InferenceProject: "dummy-project",
			InferenceRegion:  "us-central1",
			FullsendRef:      "v0.1.0-e2e-test",
		},
	}

	for _, name := range ghRepos {
		m.Repos = append(m.Repos, repos.RepoEntry{
			Repo: fmt.Sprintf("%s/%s", ghOrg, name),
		})
	}
	for _, name := range glRepos {
		m.Repos = append(m.Repos, repos.RepoEntry{
			Repo:  fmt.Sprintf("%s/%s", glGroup, name),
			Forge: repos.NullableString{Set: true, Value: repos.ForgeGitLab},
		})
	}

	return m
}
