//go:build e2e

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const defaultMintAudience = "fullsend-mint"

type mintTokenRequest struct {
	Role      string   `json:"role"`
	TargetOrg string   `json:"target_org"`
	Repos     []string `json:"repos"`
}

type mintTokenResponse struct {
	Token string `json:"token"`
	Error string `json:"error"`
}

// resolveLocalToken returns a user token from env or gh auth.
func resolveLocalToken() (string, error) {
	if token := os.Getenv("GH_TOKEN"); token != "" {
		return token, nil
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token, nil
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err == nil {
		token := strings.TrimSpace(string(out))
		if token != "" {
			return token, nil
		}
	}
	return "", fmt.Errorf("no GitHub token found: set GH_TOKEN, GITHUB_TOKEN, or run 'gh auth login'")
}

// runningInGitHubActions reports whether the test process runs inside GHA.
func runningInGitHubActions() bool {
	return os.Getenv("GITHUB_ACTIONS") == "true"
}

// requestGitHubOIDCToken obtains a GHA OIDC token for the mint audience.
func requestGitHubOIDCToken(ctx context.Context) (string, error) {
	reqURL := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL")
	reqToken := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if reqURL == "" || reqToken == "" {
		return "", fmt.Errorf("ACTIONS_ID_TOKEN_REQUEST_URL/TOKEN not set")
	}

	fullURL := reqURL
	if !strings.Contains(fullURL, "audience=") {
		sep := "?"
		if strings.Contains(fullURL, "?") {
			sep = "&"
		}
		fullURL = fullURL + sep + "audience=" + defaultMintAudience
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating OIDC request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+reqToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting OIDC token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("OIDC request returned %d: %s", resp.StatusCode, body)
	}

	var payload struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decoding OIDC response: %w", err)
	}
	if payload.Value == "" {
		return "", fmt.Errorf("empty OIDC token")
	}
	return payload.Value, nil
}

// resolveE2EToken mints a cross-org e2e installation token for targetOrg.
func resolveE2EToken(ctx context.Context, mintURL, targetOrg string, repos []string) (string, error) {
	if mintURL == "" {
		return "", fmt.Errorf("E2E_MINT_URL not set")
	}
	oidcToken, err := requestGitHubOIDCToken(ctx)
	if err != nil {
		return "", err
	}

	body, err := json.Marshal(mintTokenRequest{
		Role:      "e2e",
		TargetOrg: targetOrg,
		Repos:     repos,
	})
	if err != nil {
		return "", fmt.Errorf("marshaling mint request: %w", err)
	}

	mintEndpoint := strings.TrimRight(mintURL, "/") + "/v1/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mintEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating mint request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+oidcToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling mint: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("reading mint response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mint returned %d: %s", resp.StatusCode, respBody)
	}

	var mintResp mintTokenResponse
	if err := json.Unmarshal(respBody, &mintResp); err != nil {
		return "", fmt.Errorf("decoding mint response: %w", err)
	}
	if mintResp.Token == "" {
		return "", fmt.Errorf("mint returned empty token")
	}
	return mintResp.Token, nil
}

// tokenForOrg returns an API token for operating on a pool org.
func tokenForOrg(ctx context.Context, cfg envConfig, org string) (string, error) {
	if cfg.useMint {
		return resolveE2EToken(ctx, cfg.mintURL, org, []string{lockRepo, testRepo})
	}
	return resolveLocalToken()
}
