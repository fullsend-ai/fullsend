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
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// githubUserAgent returns the User-Agent header value for outbound GitHub API
// requests. Cloudflare Workers only forward explicitly set headers — without
// this, HostFetchDoer requests reach GitHub with no User-Agent and receive 403.
func githubUserAgent() string {
	if Version != "" {
		return "fullsend-mint/" + Version
	}
	return "fullsend-mint"
}

// installationResponse is the response from GET /repos/{owner}/{repo}/installation.
type installationResponse struct {
	ID          int64             `json:"id"`
	Permissions map[string]string `json:"permissions"`
	Account     struct {
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
	"scribe":     {"contents": "read", "issues": "write", "metadata": "read"},
	"coder":      {"contents": "write", "packages": "read", "pull_requests": "write", "issues": "write", "checks": "read", "metadata": "read"},
	"review":     {"contents": "read", "pull_requests": "write", "issues": "write", "checks": "read", "metadata": "read"},
	"fix":        {"contents": "write", "packages": "read", "pull_requests": "write", "issues": "write", "metadata": "read"},
	"retro":      {"actions": "read", "contents": "read", "pull_requests": "write", "issues": "write", "metadata": "read"},
	"prioritize": {"contents": "read", "issues": "write", "organization_projects": "write", "metadata": "read"},
	"fullsend":   {"actions": "write", "actions_variables": "read", "contents": "write", "pull_requests": "write", "workflows": "write", "metadata": "read"},
	"e2e": {
		"actions": "write", "actions_variables": "write", "administration": "write",
		"contents": "write", "issues": "write", "members": "write", "metadata": "read",
		"organization_actions_variables": "write", "organization_administration": "write",
		"pull_requests": "write", "secrets": "write", "workflows": "write",
	},
}

// customRoles stores user-defined role permissions. Written once at startup
// via RegisterCustomRolePermissions, read concurrently by request handlers.
// Lives in mintcore (not cmd/mint) so that RolePermissionsFor, HasRole, and
// RolePermissions return a unified view — callers like CreateInstallationToken
// need not distinguish built-in from custom roles.
var customRoles atomic.Value // holds map[string]map[string]string

func loadCustomRoles() map[string]map[string]string {
	v := customRoles.Load()
	if v == nil {
		return nil
	}
	return v.(map[string]map[string]string)
}

// RegisterCustomRolePermissions adds user-defined role permissions that are
// checked alongside the canonical built-in permissions. Pass nil to clear.
// Returns an error if any custom role name collides with a built-in role.
// Used by cmd/mint (standalone mint) only; the GCF mint uses canonical roles.
func RegisterCustomRolePermissions(perms map[string]map[string]string) error {
	if perms == nil {
		customRoles.Store(map[string]map[string]string(nil))
		return nil
	}
	safe := make(map[string]map[string]string, len(perms))
	for role, p := range perms {
		if err := ValidateRoleName(role); err != nil {
			return fmt.Errorf("custom role name invalid: %w", err)
		}
		if _, ok := canonicalRolePermissions[role]; ok {
			return fmt.Errorf("custom role %q collides with built-in role", role)
		}
		if len(p) == 0 {
			return fmt.Errorf("custom role %q: no permissions defined", role)
		}
		cp := make(map[string]string, len(p))
		for k, v := range p {
			if v != "read" && v != "write" && v != "admin" {
				return fmt.Errorf("custom role %q: permission %q has invalid level %q (must be read, write, or admin)", role, k, v)
			}
			cp[k] = v
		}
		safe[role] = cp
	}
	customRoles.Store(safe)
	return nil
}

// RolePermissions returns a deep copy of the combined canonical and custom
// role-to-permissions map. Custom roles are included alongside canonical ones.
func RolePermissions() map[string]map[string]string {
	out := make(map[string]map[string]string, len(canonicalRolePermissions))
	for role, perms := range canonicalRolePermissions {
		cp := make(map[string]string, len(perms))
		for k, v := range perms {
			cp[k] = v
		}
		out[role] = cp
	}
	if custom := loadCustomRoles(); len(custom) > 0 {
		for role, perms := range custom {
			cp := make(map[string]string, len(perms))
			for k, v := range perms {
				cp[k] = v
			}
			out[role] = cp
		}
	}
	return out
}

// RolePermissionsFor returns the permissions for a specific role, or nil if
// the role is not defined. Canonical roles are checked first (avoids atomic
// load on the hot path), then custom roles. Name collisions are rejected at
// registration time so lookups are unambiguous. The returned map is a copy.
func RolePermissionsFor(role string) map[string]string {
	if perms, ok := canonicalRolePermissions[role]; ok {
		cp := make(map[string]string, len(perms))
		for k, v := range perms {
			cp[k] = v
		}
		return cp
	}
	if custom := loadCustomRoles(); custom != nil {
		if perms, ok := custom[role]; ok {
			cp := make(map[string]string, len(perms))
			for k, v := range perms {
				cp[k] = v
			}
			return cp
		}
	}
	return nil
}

// HasRole reports whether the given role has a permissions entry,
// checking canonical roles first (avoids atomic load on the hot path),
// then custom roles.
func HasRole(role string) bool {
	if _, ok := canonicalRolePermissions[role]; ok {
		return true
	}
	if custom := loadCustomRoles(); custom != nil {
		if _, ok := custom[role]; ok {
			return true
		}
	}
	return false
}

// BuiltInRoles returns the sorted names of canonical mint roles
// (excludes standalone-mint custom roles registered at runtime).
func BuiltInRoles() []string {
	roles := make([]string, 0, len(canonicalRolePermissions))
	for role := range canonicalRolePermissions {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

// zeroSlice overwrites every byte in b with zero.
func zeroSlice(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// zeroBigInt overwrites the internal word buffer of n with zeros.
// Bits() shares the underlying array with the big.Int, so writing
// through the returned slice zeros the actual backing memory.
// This is best-effort: the Go runtime may retain prior copies of the
// backing array if the big.Int was resized during arithmetic.
func zeroBigInt(n *big.Int) {
	if n == nil {
		return
	}
	words := n.Bits()
	for i := range words {
		words[i] = 0
	}
	n.SetInt64(0)
}

// zeroRSAKey zeros the private components of an RSA key.
// Covers D, Primes, and Precomputed values. The Go runtime and GC may
// retain copies of big.Int backing arrays, so full scrubbing is not
// possible — this is defense-in-depth to reduce the window for memory
// disclosure.
func zeroRSAKey(key *rsa.PrivateKey) {
	if key == nil {
		return
	}
	zeroBigInt(key.D)
	for _, p := range key.Primes {
		zeroBigInt(p)
	}
	zeroBigInt(key.Precomputed.Dp)
	zeroBigInt(key.Precomputed.Dq)
	zeroBigInt(key.Precomputed.Qinv)
	for i := range key.Precomputed.CRTValues {
		zeroBigInt(key.Precomputed.CRTValues[i].Exp)
		zeroBigInt(key.Precomputed.CRTValues[i].Coeff)
		zeroBigInt(key.Precomputed.CRTValues[i].R)
	}
}

// GenerateAppJWT creates a signed RS256 JWT for GitHub App authentication.
func GenerateAppJWT(appID string, pemData []byte) (string, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM block")
	}
	// Zero the DER bytes produced by pem.Decode once signing is done.
	// block.Bytes may be a sub-slice of pemData (already zeroed by the
	// caller's defer) or an independent copy — zero it either way so
	// raw key material does not linger in heap memory.
	defer zeroSlice(block.Bytes)

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
	// Best-effort zeroing of RSA private key internals. Go's GC may
	// have already copied big.Int backing arrays during parsing or
	// signing, so this cannot guarantee full scrubbing — but it
	// reduces the window for memory disclosure.
	defer zeroRSAKey(key)

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

// ErrInstallationNotFound is returned by FindInstallation when the GitHub
// API responds with 404 — meaning the repo is not covered by the GitHub
// App's installation.
var ErrInstallationNotFound = errors.New("installation not found")

// FindInstallation looks up a GitHub App's installation ID for a repo.
// The returned installation's account is verified against the expected org to
// prevent cross-org token leakage.
func FindInstallation(ctx context.Context, githubBaseURL, jwt, org, repo string) (int64, error) {
	inst, err := findInstallationDetails(ctx, githubBaseURL, jwt, org, repo)
	if err != nil {
		return 0, err
	}
	return inst.ID, nil
}

func findInstallationDetails(ctx context.Context, githubBaseURL, jwt, org, repo string) (installationResponse, error) {
	reqURL := fmt.Sprintf("%s/repos/%s/%s/installation", githubBaseURL, org, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return installationResponse{}, fmt.Errorf("creating installation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := mintHTTP(req)
	if err != nil {
		return installationResponse{}, fmt.Errorf("getting installation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusNotFound {
			return installationResponse{}, fmt.Errorf("getting installation for %s/%s: %w", org, repo, ErrInstallationNotFound)
		}
		return installationResponse{}, fmt.Errorf("getting installation for %s/%s returned status %d", org, repo, resp.StatusCode)
	}

	var inst installationResponse
	if err := json.NewDecoder(resp.Body).Decode(&inst); err != nil {
		return installationResponse{}, fmt.Errorf("decoding installation: %w", err)
	}

	if inst.ID == 0 {
		return installationResponse{}, fmt.Errorf("no installation found for %s/%s", org, repo)
	}

	if !strings.EqualFold(inst.Account.Login, org) {
		log.Printf("cross-org installation mismatch: %s/%s belongs to %s, not %s",
			org, repo, inst.Account.Login, org)
		return installationResponse{}, fmt.Errorf("installation for %s/%s belongs to %s, not %s",
			org, repo, inst.Account.Login, org)
	}

	return inst, nil
}

// FindOrgInstallation looks up a GitHub App's installation ID for an organization.
func FindOrgInstallation(ctx context.Context, githubBaseURL, jwt, org string) (int64, error) {
	inst, err := findOrgInstallationDetails(ctx, githubBaseURL, jwt, org)
	if err != nil {
		return 0, err
	}
	return inst.ID, nil
}

func findOrgInstallationDetails(ctx context.Context, githubBaseURL, jwt, org string) (installationResponse, error) {
	reqURL := fmt.Sprintf("%s/orgs/%s/installation", githubBaseURL, org)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return installationResponse{}, fmt.Errorf("creating org installation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := mintHTTP(req)
	if err != nil {
		return installationResponse{}, fmt.Errorf("getting org installation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return installationResponse{}, fmt.Errorf("getting org installation for %s returned status %d", org, resp.StatusCode)
	}

	var inst installationResponse
	if err := json.NewDecoder(resp.Body).Decode(&inst); err != nil {
		return installationResponse{}, fmt.Errorf("decoding org installation: %w", err)
	}

	if inst.ID == 0 {
		return installationResponse{}, fmt.Errorf("no installation found for org %s", org)
	}

	if !strings.EqualFold(inst.Account.Login, org) {
		return installationResponse{}, fmt.Errorf("installation for org %s belongs to %s, not %s",
			org, inst.Account.Login, org)
	}

	return inst, nil
}

// variableResponse is the JSON shape for GET /orgs/{org}/actions/variables/{name}
// and GET /repos/{owner}/{repo}/actions/variables/{name} (identical schema).
type variableResponse struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// GetOrgVariable reads an org-level Actions variable using an installation token.
func GetOrgVariable(ctx context.Context, githubBaseURL, installationToken, org, name string) (value string, exists bool, err error) {
	reqURL := fmt.Sprintf("%s/orgs/%s/actions/variables/%s", githubBaseURL, org, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", false, fmt.Errorf("creating org variable request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+installationToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := mintHTTP(req)
	if err != nil {
		return "", false, fmt.Errorf("getting org variable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", false, fmt.Errorf("getting org variable %s returned status %d", name, resp.StatusCode)
	}

	var varResp variableResponse
	if err := json.NewDecoder(resp.Body).Decode(&varResp); err != nil {
		return "", false, fmt.Errorf("decoding org variable: %w", err)
	}
	return varResp.Value, true, nil
}

// GetRepoVariable reads a repo-level Actions variable using an installation token.
func GetRepoVariable(ctx context.Context, githubBaseURL, installationToken, owner, repo, name string) (value string, exists bool, err error) {
	reqURL := fmt.Sprintf("%s/repos/%s/%s/actions/variables/%s", githubBaseURL, owner, repo, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", false, fmt.Errorf("creating repo variable request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+installationToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := mintHTTP(req)
	if err != nil {
		return "", false, fmt.Errorf("getting repo variable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", false, fmt.Errorf("getting repo variable %s on %s/%s returned status %d", name, owner, repo, resp.StatusCode)
	}

	var varResp variableResponse
	if err := json.NewDecoder(resp.Body).Decode(&varResp); err != nil {
		return "", false, fmt.Errorf("decoding repo variable: %w", err)
	}
	return varResp.Value, true, nil
}

// foreignPolicyPermissions are requested when reading FULLSEND_FOREIGN_* org variables.
var foreignPolicyPermissions = map[string]string{
	"organization_actions_variables": "read",
}

// repoForeignPolicyPermissions are requested when reading FULLSEND_FOREIGN_* repo variables.
var repoForeignPolicyPermissions = map[string]string{
	"actions_variables": "read",
}

// createInstallationTokenWithPermissions creates an installation access token with explicit permissions.
func createInstallationTokenWithPermissions(ctx context.Context, githubBaseURL, jwt string, installationID int64, perms map[string]string, repos []string) (string, error) {
	tokenReqBody := map[string]interface{}{
		"permissions": perms,
	}
	if len(repos) > 0 {
		tokenReqBody["repositories"] = repos
	}

	tokenReqBytes, err := json.Marshal(tokenReqBody)
	if err != nil {
		return "", fmt.Errorf("marshaling token request: %w", err)
	}

	reqURL := fmt.Sprintf("%s/app/installations/%d/access_tokens", githubBaseURL, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(tokenReqBytes))
	if err != nil {
		return "", fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := mintHTTP(req)
	if err != nil {
		return "", fmt.Errorf("creating installation token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("creating installation token returned status %d", resp.StatusCode)
	}

	var tokenResp installationTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}
	if tokenResp.Token == "" {
		return "", fmt.Errorf("empty installation token returned")
	}
	return tokenResp.Token, nil
}

// ReadForeignAllowlist reads FULLSEND_FOREIGN_<role>_REPOS from the target org.
func ReadForeignAllowlist(ctx context.Context, githubBaseURL, jwt string, installationID int64, targetOrg, role string) ([]string, error) {
	policyToken, err := createInstallationTokenWithPermissions(ctx, githubBaseURL, jwt, installationID,
		foreignPolicyPermissions, nil)
	if err != nil {
		return nil, fmt.Errorf("creating policy check token: %w", err)
	}

	value, exists, err := GetOrgVariable(ctx, githubBaseURL, policyToken, targetOrg, ForeignVariableName(role))
	if err != nil {
		return nil, err
	}
	if !exists || strings.TrimSpace(value) == "" {
		return nil, nil
	}
	return ParseForeignAllowlist(value), nil
}

// ReadForeignAllowlistFromRepo reads FULLSEND_FOREIGN_<role>_REPOS from a
// specific target repository. This is the repo-level counterpart of
// ReadForeignAllowlist, enabling per-repo foreign authorization grants.
func ReadForeignAllowlistFromRepo(ctx context.Context, githubBaseURL, jwt string, installationID int64, targetOrg, targetRepo, role string) ([]string, error) {
	policyToken, err := createInstallationTokenWithPermissions(ctx, githubBaseURL, jwt, installationID,
		repoForeignPolicyPermissions, []string{targetRepo})
	if err != nil {
		return nil, fmt.Errorf("creating repo policy check token: %w", err)
	}

	value, exists, err := GetRepoVariable(ctx, githubBaseURL, policyToken, targetOrg, targetRepo, ForeignVariableName(role))
	if err != nil {
		return nil, err
	}
	if !exists || strings.TrimSpace(value) == "" {
		return nil, nil
	}
	return ParseForeignAllowlist(value), nil
}

// isPublicGitHubAPI reports whether githubBaseURL refers to github.com's API
// (or the empty default). Used only to choose Accept-URL wording.
func isPublicGitHubAPI(githubBaseURL string) bool {
	u := strings.TrimSpace(githubBaseURL)
	if u == "" {
		return true
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "api.github.com" || host == "github.com"
}

// InstallationAcceptHint returns a human-readable message directing an admin to
// Accept the pending App permission update for the given installation.
func InstallationAcceptHint(githubBaseURL, org string, installationID int64) string {
	if isPublicGitHubAPI(githubBaseURL) {
		return fmt.Sprintf("if the App already requests these permissions, Accept the pending update at https://github.com/organizations/%s/settings/installations/%d; otherwise the App owner must add them first", org, installationID)
	}
	return fmt.Sprintf("if the App already requests these permissions, an admin should Accept the pending update for org=%q installation_id=%d on this GitHub host; otherwise the App owner must add them first", org, installationID)
}

// optionalRolePermissions lists permissions that may be omitted during a
// rollout while installations catch up with an App permission update. Only
// permissions in this map are dropped when ungranted; everything else in the
// role's set is required and fails hard if missing — preserving the pre-PR
// behavior where GitHub's 422 surfaced immediately.
var optionalRolePermissions = map[string]map[string]bool{
	"coder": {"packages": true},
	"fix":   {"packages": true},
}

// IsOptionalRolePermission reports whether the given permission may be omitted
// from the role's requested set — and therefore only warned about — while
// installations catch up with an App permission update. Every other permission
// the role requests is required.
func IsOptionalRolePermission(role, permission string) bool {
	return optionalRolePermissions[role][permission]
}

// ErrRequiredPermissionsMissing is returned by effectiveInstallationPermissions
// when the installation lacks a non-optional permission for the role.
var ErrRequiredPermissionsMissing = errors.New("required permissions missing")

// GitHub's app-permissions schema currently defines read, write, and admin
// levels (see https://github.com/github/rest-api-description/blob/main/descriptions/api.github.com/api.github.com.yaml).
// Unknown or empty levels intentionally fail closed as not granted.
var permRank = map[string]int{"read": 1, "write": 2, "admin": 3}

// PermissionLevelAtLeast reports whether a granted permission level satisfies
// the requested level.
func PermissionLevelAtLeast(granted, requested string) bool {
	g, gok := permRank[granted]
	r, rok := permRank[requested]
	return gok && rok && g >= r
}

// GrantedPermissionLevel resolves the level an installation actually grants for
// a permission. GitHub implicitly grants metadata:read to every App
// installation, so an absent "metadata" entry is treated as "read".
func GrantedPermissionLevel(granted map[string]string, permission string) string {
	level := granted[permission]
	if permission == "metadata" && level == "" {
		// GitHub implicitly grants metadata:read to every App installation.
		return "read"
	}
	return level
}

// effectiveInstallationPermissions builds the permission map for a token POST.
// Only permissions listed in optionalRolePermissions for the role may be
// omitted when the installation has not yet granted them. All other permissions
// are required: if any required permission is ungranted, the function returns
// ErrRequiredPermissionsMissing. A nil or empty granted map means the lookup
// response did not provide permission information, so the full requested set is
// preserved: GitHub always grants at least metadata:read to an installation, so
// an empty map cannot be a real grant set.
func effectiveInstallationPermissions(role string, requested, granted map[string]string) (map[string]string, []string, error) {
	if len(granted) == 0 {
		effective := copyPermissions(requested)
		if len(effective) == 0 {
			return nil, nil, fmt.Errorf("%w for role %q: no permissions remain", ErrRequiredPermissionsMissing, role)
		}
		return effective, nil, nil
	}

	optional := optionalRolePermissions[role]
	effective := make(map[string]string, len(requested))
	var dropped []string
	var missingRequired []string
	for perm, level := range requested {
		if PermissionLevelAtLeast(GrantedPermissionLevel(granted, perm), level) {
			effective[perm] = level
			continue
		}
		if optional[perm] {
			dropped = append(dropped, fmt.Sprintf("%s:%s", perm, level))
			continue
		}
		missingRequired = append(missingRequired, fmt.Sprintf("%s:%s", perm, level))
	}

	if len(missingRequired) > 0 {
		sort.Strings(missingRequired)
		return nil, nil, fmt.Errorf("%w for role %q: %s", ErrRequiredPermissionsMissing, role, strings.Join(missingRequired, ", "))
	}
	if len(effective) == 0 {
		return nil, nil, fmt.Errorf("%w for role %q: no permissions remain", ErrRequiredPermissionsMissing, role)
	}

	sort.Strings(dropped)
	return effective, dropped, nil
}

func copyPermissions(perms map[string]string) map[string]string {
	out := make(map[string]string, len(perms))
	for k, v := range perms {
		out[k] = v
	}
	return out
}

// CreateInstallationToken exchanges a JWT for an installation access token,
// scoped to the given repos and role-specific permissions.
func CreateInstallationToken(ctx context.Context, githubBaseURL, jwt string, installationID int64, org, role string, repos []string) (string, string, *GrantedScope, error) {
	return createInstallationToken(ctx, githubBaseURL, jwt, installationID, org, role, repos, nil)
}

// CreateInstallationTokenWithGrantedPermissions mints a token after
// downscoping requested role permissions to those currently granted to the
// installation. This avoids a failed token POST while an App update is pending.
func CreateInstallationTokenWithGrantedPermissions(ctx context.Context, githubBaseURL, jwt string, installationID int64, org, role string, repos []string, granted map[string]string) (string, string, *GrantedScope, error) {
	return createInstallationToken(ctx, githubBaseURL, jwt, installationID, org, role, repos, granted)
}

func createInstallationToken(ctx context.Context, githubBaseURL, jwt string, installationID int64, org, role string, repos []string, granted map[string]string) (string, string, *GrantedScope, error) {
	requested := RolePermissionsFor(role)
	if requested == nil {
		return "", "", nil, fmt.Errorf("no permissions defined for role %q", role)
	}

	perms, dropped, err := effectiveInstallationPermissions(role, requested, granted)
	if err != nil {
		return "", "", nil, fmt.Errorf("%w; %s", err, InstallationAcceptHint(githubBaseURL, org, installationID))
	}
	if len(dropped) > 0 {
		log.Printf("installation permissions not granted: org=%q installation_id=%d role=%q dropped=%s; %s",
			org, installationID, role, strings.Join(dropped, ", "), InstallationAcceptHint(githubBaseURL, org, installationID))
	}

	token, expiresAt, tokenGranted, status, body, err := postInstallationAccessToken(ctx, githubBaseURL, jwt, installationID, perms, repos)
	if err != nil {
		return "", "", nil, err
	}
	if status != http.StatusCreated {
		return "", "", nil, fmt.Errorf("creating installation token returned status %d: %s", status, truncateForLog(body, 256))
	}
	return token, expiresAt, tokenGranted, nil
}

// postInstallationAccessToken POSTs /app/installations/{id}/access_tokens and
// returns the parsed success payload, or the raw status and body for the caller
// to surface in an error. Network and marshal errors are returned via err.
func postInstallationAccessToken(ctx context.Context, githubBaseURL, jwt string, installationID int64, perms map[string]string, repos []string) (token, expiresAt string, granted *GrantedScope, status int, body string, err error) {
	tokenReqBody := map[string]interface{}{
		"permissions": perms,
	}
	if len(repos) > 0 {
		tokenReqBody["repositories"] = repos
	}

	tokenReqBytes, err := json.Marshal(tokenReqBody)
	if err != nil {
		return "", "", nil, 0, "", fmt.Errorf("marshaling token request: %w", err)
	}

	reqURL := fmt.Sprintf("%s/app/installations/%d/access_tokens", githubBaseURL, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(tokenReqBytes))
	if err != nil {
		return "", "", nil, 0, "", fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := mintHTTP(req)
	if err != nil {
		return "", "", nil, 0, "", fmt.Errorf("creating installation token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		// Cap error bodies only — success payloads can include full repository
		// objects and must not be truncated before JSON decode.
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if readErr != nil {
			return "", "", nil, resp.StatusCode, "", fmt.Errorf("reading installation token response: %w", readErr)
		}
		return "", "", nil, resp.StatusCode, string(raw), nil
	}

	var tokenResp installationTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", "", nil, resp.StatusCode, "", fmt.Errorf("decoding token response: %w", err)
	}
	if tokenResp.Token == "" {
		return "", "", nil, resp.StatusCode, "", fmt.Errorf("empty installation token returned")
	}

	granted = &GrantedScope{
		Permissions:   tokenResp.Permissions,
		RepoSelection: tokenResp.RepositorySelection,
	}
	for _, r := range tokenResp.Repositories {
		granted.Repos = append(granted.Repos, r.FullName)
	}
	return tokenResp.Token, tokenResp.ExpiresAt, granted, resp.StatusCode, "", nil
}

// truncateForLog trims whitespace and truncates s to max runes, appending "…"
// if truncation occurs.
func truncateForLog(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
