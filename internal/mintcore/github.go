// Package mintcore provides shared code for the fullsend token mint
// implementations (GCP Cloud Function and local dev mint).
package mintcore

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// installationResponse is the response from GET /repos/{owner}/{repo}/installation.
type installationResponse struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
}

// installationTokenResponse is the response from POST /app/installations/{id}/access_tokens.
type installationTokenResponse struct {
	Token               string                        `json:"token"`
	ExpiresAt           string                        `json:"expires_at"`
	Permissions         map[string]string             `json:"permissions,omitempty"`
	Repositories        []installationTokenRepository `json:"repositories,omitempty"`
	RepositorySelection string                        `json:"repository_selection,omitempty"`
}

// installationTokenRepository is a repo entry in the installation token response.
type installationTokenRepository struct {
	FullName string `json:"full_name"`
}

// GrantedScope holds the actual scope GitHub granted for the installation token.
type GrantedScope struct {
	Repos          []string
	Permissions    map[string]string
	RepoSelection  string
	AppID          string
	InstallationID int64
}

// canonicalRolePermissions defines the minimum GitHub App permissions per agent role.
// Tokens are always downscoped to these permissions regardless of what the
// App itself has configured. Unexported to prevent mutation; use
// RolePermissions() to get a copy.
var canonicalRolePermissions = map[string]map[string]string{
	"triage":     {"contents": "read", "issues": "write", "metadata": "read"},
	"coder":      {"contents": "write", "pull_requests": "write", "issues": "write", "checks": "read", "metadata": "read"},
	"review":     {"contents": "read", "pull_requests": "write", "issues": "write", "checks": "read", "metadata": "read"},
	"fix":        {"contents": "write", "pull_requests": "write", "issues": "write", "metadata": "read"},
	"retro":      {"actions": "read", "contents": "read", "pull_requests": "write", "issues": "write", "metadata": "read"},
	"prioritize": {"contents": "read", "issues": "write", "organization_projects": "write", "metadata": "read"},
	"fullsend":   {"actions": "write", "actions_variables": "read", "contents": "write", "pull_requests": "write", "workflows": "write", "metadata": "read"},
	"e2e": {
		"actions": "write", "actions_variables": "read", "administration": "write",
		"contents": "write", "issues": "write", "members": "write", "metadata": "read",
		"organization_administration": "write", "pull_requests": "write",
		"secrets": "write", "workflows": "write",
	},
}

// RolePermissions returns a deep copy of the role-to-permissions map,
// preventing callers from mutating the canonical permission definitions.
func RolePermissions() map[string]map[string]string {
	out := make(map[string]map[string]string, len(canonicalRolePermissions))
	for role, perms := range canonicalRolePermissions {
		cp := make(map[string]string, len(perms))
		for k, v := range perms {
			cp[k] = v
		}
		out[role] = cp
	}
	return out
}

// RolePermissionsFor returns the permissions for a specific role, or nil if
// the role is not defined. The returned map is a copy.
func RolePermissionsFor(role string) map[string]string {
	perms, ok := canonicalRolePermissions[role]
	if !ok {
		return nil
	}
	cp := make(map[string]string, len(perms))
	for k, v := range perms {
		cp[k] = v
	}
	return cp
}

// HasRole reports whether the given role has a permissions entry.
func HasRole(role string) bool {
	_, ok := canonicalRolePermissions[role]
	return ok
}

// GenerateAppJWT creates a signed RS256 JWT for GitHub App authentication.
func GenerateAppJWT(appID string, pemData []byte) (string, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM block")
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		pkcs8Key, pkcs8Err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if pkcs8Err != nil {
			return "", fmt.Errorf("failed to parse private key (PKCS1: %v, PKCS8: %v)", err, pkcs8Err)
		}
		var ok bool
		key, ok = pkcs8Key.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("PKCS8 key is not RSA")
		}
	}

	now := time.Now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]interface{}{
		"iss": appID,
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshaling JWT header: %w", err)
	}

	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshaling JWT claims: %w", err)
	}

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signingInput := headerB64 + "." + claimsB64

	hashed := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("signing JWT: %w", err)
	}

	signatureB64 := base64.RawURLEncoding.EncodeToString(signature)

	return signingInput + "." + signatureB64, nil
}

// FindInstallation looks up a GitHub App's installation ID for a repo.
// The returned installation's account is verified against the expected org to
// prevent cross-org token leakage.
func FindInstallation(ctx context.Context, httpClient HTTPDoer, githubBaseURL, jwt, org, repo string) (int64, error) {
	reqURL := fmt.Sprintf("%s/repos/%s/%s/installation", githubBaseURL, org, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, fmt.Errorf("creating installation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("getting installation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("getting installation for %s/%s returned status %d", org, repo, resp.StatusCode)
	}

	var inst installationResponse
	if err := json.NewDecoder(resp.Body).Decode(&inst); err != nil {
		return 0, fmt.Errorf("decoding installation: %w", err)
	}

	if inst.ID == 0 {
		return 0, fmt.Errorf("no installation found for %s/%s", org, repo)
	}

	if !strings.EqualFold(inst.Account.Login, org) {
		log.Printf("cross-org installation mismatch: %s/%s belongs to %s, not %s",
			org, repo, inst.Account.Login, org)
		return 0, fmt.Errorf("installation for %s/%s belongs to %s, not %s",
			org, repo, inst.Account.Login, org)
	}

	return inst.ID, nil
}

// CreateInstallationToken exchanges a JWT for an installation access token,
// scoped to the given repos and role-specific permissions.
func CreateInstallationToken(ctx context.Context, httpClient HTTPDoer, githubBaseURL, jwt string, installationID int64, role string, repos []string) (string, string, *GrantedScope, error) {
	perms := RolePermissionsFor(role)
	if perms == nil {
		return "", "", nil, fmt.Errorf("no permissions defined for role %q", role)
	}
	tokenReqBody := map[string]interface{}{
		"repositories": repos,
		"permissions":  perms,
	}

	tokenReqBytes, err := json.Marshal(tokenReqBody)
	if err != nil {
		return "", "", nil, fmt.Errorf("marshaling token request: %w", err)
	}

	reqURL := fmt.Sprintf("%s/app/installations/%d/access_tokens", githubBaseURL, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(tokenReqBytes))
	if err != nil {
		return "", "", nil, fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", nil, fmt.Errorf("creating installation token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", "", nil, fmt.Errorf("creating installation token returned status %d", resp.StatusCode)
	}

	var tokenResp installationTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", "", nil, fmt.Errorf("decoding token response: %w", err)
	}

	if tokenResp.Token == "" {
		return "", "", nil, fmt.Errorf("empty installation token returned")
	}

	granted := &GrantedScope{
		Permissions:   tokenResp.Permissions,
		RepoSelection: tokenResp.RepositorySelection,
	}
	for _, r := range tokenResp.Repositories {
		granted.Repos = append(granted.Repos, r.FullName)
	}

	return tokenResp.Token, tokenResp.ExpiresAt, granted, nil
}
