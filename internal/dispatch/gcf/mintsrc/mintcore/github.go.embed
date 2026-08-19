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
	"net/http"
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

// Level constants for privilege levels within a role.
// Each role has ordered levels: read ⊆ write. The read level is derived
// by downgrading all write permissions to read; the write level is the
// full canonical permission set. See ADR 0073.
const (
	LevelRead  = "read"
	LevelWrite = "write"
)

// ValidLevel reports whether level is a recognized privilege level name.
func ValidLevel(level string) bool {
	return level == LevelRead || level == LevelWrite
}

// deriveReadPermissions returns a copy of perms with all "write" values
// downgraded to "read". Permissions already at "read" are unchanged.
func deriveReadPermissions(perms map[string]string) map[string]string {
	out := make(map[string]string, len(perms))
	for k, v := range perms {
		if v == "write" {
			out[k] = "read"
		} else {
			out[k] = v
		}
	}
	return out
}

// canonicalRolePermissions defines the minimum GitHub App permissions per agent role.
// Tokens are always downscoped to these permissions regardless of what the
// App itself has configured. Unexported to prevent mutation; use
// RolePermissions() to get a copy.
//
// These are the "write" level permissions for each built-in role. The "read"
// level is derived automatically by deriveReadPermissions.
var canonicalRolePermissions = map[string]map[string]string{
	"triage":     {"contents": "read", "issues": "write", "metadata": "read"},
	"scribe":     {"contents": "read", "issues": "write", "metadata": "read"},
	"coder":      {"contents": "write", "pull_requests": "write", "issues": "write", "checks": "read", "metadata": "read"},
	"review":     {"contents": "read", "pull_requests": "write", "issues": "write", "checks": "read", "metadata": "read"},
	"fix":        {"contents": "write", "pull_requests": "write", "issues": "write", "metadata": "read"},
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

// customRoleLevels stores user-defined role permissions organized by level.
// Written once at startup via RegisterCustomRolePermissions or
// RegisterCustomRoleLevels, read concurrently by request handlers.
// Lives in mintcore (not cmd/mint) so that RolePermissionsFor, HasRole, and
// RolePermissions return a unified view — callers like CreateInstallationToken
// need not distinguish built-in from custom roles.
//
// Structure: role → level → permission → access.
// Flat-format custom roles are stored with a single "read" level;
// multi-level roles have explicit "read" and/or "write" entries.
var customRoleLevels atomic.Value // holds map[string]map[string]map[string]string

func loadCustomRoleLevels() map[string]map[string]map[string]string {
	v := customRoleLevels.Load()
	if v == nil {
		return nil
	}
	return v.(map[string]map[string]map[string]string)
}

// loadCustomRoles returns a flat role→permissions map for backward
// compatibility. For each role, it returns the write level if defined,
// falling back to read. This preserves pre-levels behavior where
// RolePermissionsFor returned the full (write-equivalent) permission set.
func loadCustomRoles() map[string]map[string]string {
	levels := loadCustomRoleLevels()
	if levels == nil {
		return nil
	}
	out := make(map[string]map[string]string, len(levels))
	for role, roleLevels := range levels {
		// Prefer write, fall back to read.
		if perms, ok := roleLevels[LevelWrite]; ok {
			cp := make(map[string]string, len(perms))
			for k, v := range perms {
				cp[k] = v
			}
			out[role] = cp
		} else if perms, ok := roleLevels[LevelRead]; ok {
			cp := make(map[string]string, len(perms))
			for k, v := range perms {
				cp[k] = v
			}
			out[role] = cp
		}
	}
	return out
}

// validateCustomRoleLevels validates the structure of custom role levels.
func validateCustomRoleLevels(levels map[string]map[string]map[string]string) error {
	for role, roleLevels := range levels {
		if err := ValidateRoleName(role); err != nil {
			return fmt.Errorf("custom role name invalid: %w", err)
		}
		if _, ok := canonicalRolePermissions[role]; ok {
			return fmt.Errorf("custom role %q collides with built-in role", role)
		}
		for level, perms := range roleLevels {
			if !ValidLevel(level) {
				return fmt.Errorf("custom role %q: unknown level %q", role, level)
			}
			for k, v := range perms {
				if v != "read" && v != "write" {
					return fmt.Errorf("custom role %q level %q: permission %q has invalid value %q (must be read or write)", role, level, k, v)
				}
			}
		}
	}
	return nil
}

// RegisterCustomRolePermissions adds user-defined role permissions that are
// checked alongside the canonical built-in permissions. Pass nil to clear.
// Returns an error if any custom role name collides with a built-in role.
// Used by cmd/mint (standalone mint) only; the GCF mint uses canonical roles.
//
// Permissions are registered as the "read" level. For roles where write is
// not explicitly defined, requesting write falls back to read automatically
// (per ADR 0073).
func RegisterCustomRolePermissions(perms map[string]map[string]string) error {
	if perms == nil {
		customRoleLevels.Store(map[string]map[string]map[string]string(nil))
		return nil
	}
	safe := make(map[string]map[string]map[string]string, len(perms))
	for role, p := range perms {
		if err := ValidateRoleName(role); err != nil {
			return fmt.Errorf("custom role name invalid: %w", err)
		}
		if _, ok := canonicalRolePermissions[role]; ok {
			return fmt.Errorf("custom role %q collides with built-in role", role)
		}
		cp := make(map[string]string, len(p))
		for k, v := range p {
			if v != "read" && v != "write" {
				return fmt.Errorf("custom role %q: permission %q has invalid value %q (must be read or write)", role, k, v)
			}
			cp[k] = v
		}
		safe[role] = map[string]map[string]string{LevelRead: cp}
	}
	customRoleLevels.Store(safe)
	return nil
}

// RegisterCustomRoleLevels adds user-defined role permissions with explicit
// level support. Each role maps level names to permission sets. Pass nil to
// clear. Returns an error if any custom role collides with a built-in role.
func RegisterCustomRoleLevels(levels map[string]map[string]map[string]string) error {
	if levels == nil {
		customRoleLevels.Store(map[string]map[string]map[string]string(nil))
		return nil
	}
	if err := validateCustomRoleLevels(levels); err != nil {
		return err
	}
	// Deep copy to prevent caller mutation.
	safe := make(map[string]map[string]map[string]string, len(levels))
	for role, roleLevels := range levels {
		safeLevels := make(map[string]map[string]string, len(roleLevels))
		for level, perms := range roleLevels {
			cp := make(map[string]string, len(perms))
			for k, v := range perms {
				cp[k] = v
			}
			safeLevels[level] = cp
		}
		safe[role] = safeLevels
	}
	customRoleLevels.Store(safe)
	return nil
}

// ParseCustomRolePermissions parses a CUSTOM_ROLE_PERMISSIONS JSON value,
// auto-detecting flat vs multi-level format per role. Returns the unified
// level structure. The flat format (role → permissions) is stored as read-only;
// the multi-level format (role → {"levels": {level → permissions}}) is stored
// as-is. Mixed format within a single value is allowed.
func ParseCustomRolePermissions(raw string) (map[string]map[string]map[string]string, error) {
	var roles map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &roles); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	result := make(map[string]map[string]map[string]string, len(roles))
	for role, rawVal := range roles {
		// Try multi-level format: {"levels": {"read": {...}, "write": {...}}}
		var multiLevel struct {
			Levels map[string]map[string]string `json:"levels"`
		}
		if err := json.Unmarshal(rawVal, &multiLevel); err == nil && multiLevel.Levels != nil {
			result[role] = multiLevel.Levels
			continue
		}

		// Fall back to flat format: {"permission": "level"}
		var flat map[string]string
		if err := json.Unmarshal(rawVal, &flat); err != nil {
			return nil, fmt.Errorf("role %q: invalid format: %w", role, err)
		}
		result[role] = map[string]map[string]string{LevelRead: flat}
	}

	return result, nil
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

// RolePermissionsForLevel returns the permissions for a role at the given
// privilege level. For built-in roles, the read level is derived by
// downgrading all write permissions to read; the write level is the full
// canonical set. For custom roles with flat format, both levels return the
// same permissions. For multi-level custom roles, the requested level is
// returned directly. If write is not explicitly defined for a custom role,
// it falls back to read (per ADR 0073). Returns an error if the role does
// not exist or the level is unknown.
func RolePermissionsForLevel(role, level string) (map[string]string, error) {
	if !ValidLevel(level) {
		return nil, fmt.Errorf("unknown level %q", level)
	}

	// Check built-in roles first.
	if perms, ok := canonicalRolePermissions[role]; ok {
		if level == LevelRead {
			return deriveReadPermissions(perms), nil
		}
		cp := make(map[string]string, len(perms))
		for k, v := range perms {
			cp[k] = v
		}
		return cp, nil
	}

	// Check custom roles.
	if custom := loadCustomRoleLevels(); custom != nil {
		if roleLevels, ok := custom[role]; ok {
			if perms, ok := roleLevels[level]; ok {
				cp := make(map[string]string, len(perms))
				for k, v := range perms {
					cp[k] = v
				}
				return cp, nil
			}
			// Write not defined → fall back to read (ADR 0073).
			if level == LevelWrite {
				if perms, ok := roleLevels[LevelRead]; ok {
					cp := make(map[string]string, len(perms))
					for k, v := range perms {
						cp[k] = v
					}
					return cp, nil
				}
			}
			// Read not defined → derive from write by downgrading.
			if level == LevelRead {
				if perms, ok := roleLevels[LevelWrite]; ok {
					return deriveReadPermissions(perms), nil
				}
			}
		}
	}

	return nil, fmt.Errorf("no permissions defined for role %q", role)
}

// HasRole reports whether the given role has a permissions entry,
// checking canonical roles first (avoids atomic load on the hot path),
// then custom roles.
func HasRole(role string) bool {
	if _, ok := canonicalRolePermissions[role]; ok {
		return true
	}
	if custom := loadCustomRoleLevels(); custom != nil {
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

// ErrInstallationNotFound is returned by FindInstallation when the GitHub
// API responds with 404 — meaning the repo is not covered by the GitHub
// App's installation.
var ErrInstallationNotFound = errors.New("installation not found")

// FindInstallation looks up a GitHub App's installation ID for a repo.
// The returned installation's account is verified against the expected org to
// prevent cross-org token leakage.
func FindInstallation(ctx context.Context, githubBaseURL, jwt, org, repo string) (int64, error) {
	reqURL := fmt.Sprintf("%s/repos/%s/%s/installation", githubBaseURL, org, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, fmt.Errorf("creating installation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := mintHTTP(req)
	if err != nil {
		return 0, fmt.Errorf("getting installation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusNotFound {
			return 0, fmt.Errorf("getting installation for %s/%s: %w", org, repo, ErrInstallationNotFound)
		}
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

// FindOrgInstallation looks up a GitHub App's installation ID for an organization.
func FindOrgInstallation(ctx context.Context, githubBaseURL, jwt, org string) (int64, error) {
	reqURL := fmt.Sprintf("%s/orgs/%s/installation", githubBaseURL, org)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return 0, fmt.Errorf("creating org installation request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := mintHTTP(req)
	if err != nil {
		return 0, fmt.Errorf("getting org installation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("getting org installation for %s returned status %d", org, resp.StatusCode)
	}

	var inst installationResponse
	if err := json.NewDecoder(resp.Body).Decode(&inst); err != nil {
		return 0, fmt.Errorf("decoding org installation: %w", err)
	}

	if inst.ID == 0 {
		return 0, fmt.Errorf("no installation found for org %s", org)
	}

	if !strings.EqualFold(inst.Account.Login, org) {
		return 0, fmt.Errorf("installation for org %s belongs to %s, not %s",
			org, inst.Account.Login, org)
	}

	return inst.ID, nil
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

// CreateInstallationToken exchanges a JWT for an installation access token,
// scoped to the given repos and role-specific permissions at the requested
// privilege level. When level is empty, it defaults to LevelRead (per ADR
// 0073). The level determines which permission set is used: read is derived
// by downgrading write→read for built-in roles; write uses the full set.
func CreateInstallationToken(ctx context.Context, githubBaseURL, jwt string, installationID int64, role, level string, repos []string) (string, string, *GrantedScope, error) {
	if level == "" {
		level = LevelRead
	}
	perms, err := RolePermissionsForLevel(role, level)
	if err != nil {
		return "", "", nil, err
	}
	tokenReqBody := map[string]interface{}{
		"permissions": perms,
	}
	if len(repos) > 0 {
		tokenReqBody["repositories"] = repos
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
	req.Header.Set("User-Agent", githubUserAgent())

	resp, err := mintHTTP(req)
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
