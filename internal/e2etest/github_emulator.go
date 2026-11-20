package e2etest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// GitHubEmulatorEnvironment is the ENVIRONMENT value that points the behaviour
// suite at a GitHub emulator instead of github.com. In that mode the suite
// takes the forge root from BEHAVIOUR_GITHUB_URL, uses the token in
// GH_TOKEN for every org (no mint e2e role, even inside GitHub Actions)
// and creates its pool org on the emulator when it is missing.
const GitHubEmulatorEnvironment = "github-emulator"

// GitHubEmulatorURLEnv names the variable that holds the emulator's root URL,
// e.g. http://127.0.0.1:18302. The REST API is served under /api/v3.
const GitHubEmulatorURLEnv = "BEHAVIOUR_GITHUB_URL"

// GitHubEmulatorMode reports whether ENVIRONMENT selects the GitHub emulator.
func GitHubEmulatorMode() bool {
	return strings.TrimSpace(os.Getenv("ENVIRONMENT")) == GitHubEmulatorEnvironment
}

// ConfigureGitHubEmulatorForge points this process, and every fullsend CLI it
// starts, at the emulator named by BEHAVIOUR_GITHUB_URL: it sets
// GITHUB_SERVER_URL to the root and GITHUB_API_URL to <root>/api/v3.
//
// The harness sets them itself instead of expecting them in the
// environment because an Actions runner already exports both for
// github.com, and a workflow cannot override GITHUB_* variables.
func ConfigureGitHubEmulatorForge() (string, error) {
	root, err := githubEmulatorRoot(os.Getenv(GitHubEmulatorURLEnv))
	if err != nil {
		return "", err
	}
	for k, v := range map[string]string{
		"GITHUB_SERVER_URL": root,
		"GITHUB_API_URL":    root + "/api/v3",
	} {
		if err := os.Setenv(k, v); err != nil {
			return "", fmt.Errorf("setting %s: %w", k, err)
		}
	}
	return root, nil
}

// setUpGitHubEmulatorMode applies ENVIRONMENT=github-emulator to this
// process and reports whether it is selected. In that mode it points the
// process at the emulator (ConfigureGitHubEmulatorForge) and requires the
// emulator token in GH_TOKEN. The GITHUB_TOKEN and `gh auth token`
// fallbacks hold github.com credentials, which must never reach the
// emulator; with GH_TOKEN set, resolveLocalToken never reaches them.
func setUpGitHubEmulatorMode() (bool, error) {
	if !GitHubEmulatorMode() {
		return false, nil
	}
	if _, err := ConfigureGitHubEmulatorForge(); err != nil {
		return true, fmt.Errorf("emulator forge: %w", err)
	}
	if strings.TrimSpace(os.Getenv("GH_TOKEN")) == "" {
		return true, fmt.Errorf("ENVIRONMENT=%s needs the emulator token in GH_TOKEN", GitHubEmulatorEnvironment)
	}
	return true, nil
}

func githubEmulatorRoot(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", fmt.Errorf("ENVIRONMENT=%s needs %s (the emulator root, e.g. http://127.0.0.1:18302)", GitHubEmulatorEnvironment, GitHubEmulatorURLEnv)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%s=%q is not an http(s) URL", GitHubEmulatorURLEnv, raw)
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%s=%q must be the emulator root, with no path", GitHubEmulatorURLEnv, raw)
	}
	return raw, nil
}

// EnsureGitHubEmulatorOrg creates org on the emulator through the GitHub
// Enterprise site-admin route (POST /admin/organizations) when it does
// not exist yet, owned by the login adminLogin returns. It is a no-op for
// an org that already exists, and then never calls adminLogin: an App
// installation token gets 403 on GET /user, and a seed that creates the
// org first lets such a token drive the suite.
func EnsureGitHubEmulatorOrg(ctx context.Context, token, org string, adminLogin func(context.Context) (string, error)) error {
	base := githubAPIBaseURL()
	status, body, err := githubEmulatorRequest(ctx, http.MethodGet, base+"/orgs/"+url.PathEscape(org), token, nil)
	if err != nil {
		return fmt.Errorf("looking up org %s: %w", org, err)
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
	default:
		return fmt.Errorf("looking up org %s: unexpected status %d: %s", org, status, body)
	}
	owner, err := adminLogin(ctx)
	if err != nil {
		return fmt.Errorf("creating org %s: %w", org, err)
	}
	payload, _ := json.Marshal(map[string]string{"login": org, "admin": owner})
	status, body, err = githubEmulatorRequest(ctx, http.MethodPost, base+"/admin/organizations", token, payload)
	if err != nil {
		return fmt.Errorf("creating org %s: %w", org, err)
	}
	if status != http.StatusCreated {
		return fmt.Errorf("creating org %s: unexpected status %d: %s", org, status, body)
	}
	return nil
}

// GitHubEmulatorViewerLogin returns the login of the token's user, used as the
// owner of orgs the harness creates.
func GitHubEmulatorViewerLogin(ctx context.Context, token string) (string, error) {
	status, body, err := githubEmulatorRequest(ctx, http.MethodGet, githubAPIBaseURL()+"/user", token, nil)
	if err != nil {
		return "", fmt.Errorf("reading the token's user: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("reading the token's user: unexpected status %d: %s", status, body)
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &user); err != nil || user.Login == "" {
		return "", fmt.Errorf("reading the token's user: no login in %s", body)
	}
	return user.Login, nil
}

func githubEmulatorRequest(ctx context.Context, method, target, token string, payload []byte) (int, []byte, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := githubEmulatorHTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, body, nil
}

// githubEmulatorHTTPClient talks to the emulator root the harness was given; it is
// a first-party endpoint, so a timeout and a bounded body are enough.
var githubEmulatorHTTPClient = &http.Client{Timeout: 30 * time.Second}
