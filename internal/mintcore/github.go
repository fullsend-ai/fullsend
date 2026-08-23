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

// Level constants for the two mandatory privilege level names.
// Every role (built-in and custom) must define at least these two
// levels. Custom roles may define additional named levels.
const (
	LevelRead  = "read"
	LevelWrite = "write"
)

// canonicalRolePermissions defines the minimum GitHub App permissions per
// agent role, organized by privilege level. Tokens are always downscoped
// to these permissions regardless of what the App itself has configured.
// Unexported to prevent mutation; use RolePermissionsForLevel() to get
// a copy.
//
// Every built-in role statically defines both "read" and "write" levels.
// The "write" level is the canonical ceiling; the "read" level has the
// same keys with every "write" GitHub permission value rewritten to "read".
// Both are baked into the table as data — no derivation at mint time.
var canonicalRolePermissions = map[string]map[string]map[string]string{
	"triage": {
		LevelWrite: {"contents": "read", "issues": "write", "metadata": "read"},
		LevelRead:  {"contents": "read", "issues": "read", "metadata": "read"},
	},
	"scribe": {
		LevelWrite: {"contents": "read", "issues": "write", "metadata": "read"},
		LevelRead:  {"contents": "read", "issues": "read", "metadata": "read"},
	},
	"coder": {
		LevelWrite: {"contents": "write", "pull_requests": "write", "issues": "write", "checks": "read", "metadata": "read"},
		LevelRead:  {"contents": "read", "pull_requests": "read", "issues": "read", "checks": "read", "metadata": "read"},
	},
	"review": {
		LevelWrite: {"contents": "read", "pull_requests": "write", "issues": "write", "checks": "read", "metadata": "read"},
		LevelRead:  {"contents": "read", "pull_requests": "read", "issues": "read", "checks": "read", "metadata": "read"},
	},
	"fix": {
		LevelWrite: {"contents": "write", "pull_requests": "write", "issues": "write", "metadata": "read"},
		LevelRead:  {"contents": "read", "pull_requests": "read", "issues": "read", "metadata": "read"},
	},
	"retro": {
		LevelWrite: {"actions": "read", "contents": "read", "pull_requests": "write", "issues": "write", "metadata": "read"},
		LevelRead:  {"actions": "read", "contents": "read", "pull_requests": "read", "issues": "read", "metadata": "read"},
	},
	"prioritize": {
		LevelWrite: {"contents": "read", "issues": "write", "organization_projects": "write", "metadata": "read"},
		LevelRead:  {"contents": "read", "issues": "read", "organization_projects": "read", "metadata": "read"},
	},
	"fullsend": {
		LevelWrite: {"actions": "write", "actions_variables": "read", "contents": "write", "pull_requests": "write", "workflows": "write", "metadata": "read"},
		LevelRead:  {"actions": "read", "actions_variables": "read", "contents": "read", "pull_requests": "read", "workflows": "read", "metadata": "read"},
	},
	"e2e": {
		LevelWrite: {
			"actions": "write", "actions_variables": "write", "administration": "write",
			"contents": "write", "issues": "write", "members": "write", "metadata": "read",
			"organization_actions_variables": "write", "organization_administration": "write",
			"pull_requests": "write", "secrets": "write", "workflows": "write",
		},
		LevelRead: {
			"actions": "read", "actions_variables": "read", "administration": "read",
			"contents": "read", "issues": "read", "members": "read", "metadata": "read",
			"organization_actions_variables": "read", "organization_administration": "read",
			"pull_requests": "read", "secrets": "read", "workflows": "read",
		},
	},
}

// customRoleLevels stores user-defined role permissions organized by level.
// Written once at startup via RegisterCustomRolePermissions or
// RegisterCustomRoleLevels, read concurrently by request handlers.
// Lives in mintcore (not cmd/mint) so that HasRole and
// RolePermissionsForLevel return a unified view — callers like
// CreateInstallationToken need not distinguish built-in from custom roles.
//
// Structure: role → level → permission → access.
// Flat-format custom roles are stored with both "read" and "write" levels
// containing the same permission map. Multi-level roles have explicit
// entries for each level; extra named levels beyond read/write are allowed.
var customRoleLevels atomic.Value // holds map[string]map[string]map[string]string

func loadCustomRoleLevels() map[string]map[string]map[string]string {
	v := customRoleLevels.Load()
	if v == nil {
		return nil
	}
	return v.(map[string]map[string]map[string]string)
}

// validateCustomRoleLevels validates the structure of custom role levels.
// Every role must define at least the mandatory "read" and "write" levels.
// Extra named levels are allowed (non-empty, except the reserved JSON
// discriminator key "levels"). No write-superset-of-read enforcement —
// level contents are the operator's responsibility.
func validateCustomRoleLevels(levels map[string]map[string]map[string]string) error {
	for role, roleLevels := range levels {
		if err := ValidateRoleName(role); err != nil {
			return fmt.Errorf("custom role name invalid: %w", err)
		}
		if _, ok := canonicalRolePermissions[role]; ok {
			return fmt.Errorf("custom role %q collides with built-in role", role)
		}
		// Mandatory levels.
		if _, ok := roleLevels[LevelRead]; !ok {
			return fmt.Errorf("custom role %q: missing mandatory level %q", role, LevelRead)
		}
		if _, ok := roleLevels[LevelWrite]; !ok {
			return fmt.Errorf("custom role %q: missing mandatory level %q", role, LevelWrite)
		}
		for level, perms := range roleLevels {
			if level == "" {
				return fmt.Errorf("custom role %q: level name must not be empty", role)
			}
			if level == "levels" {
				return fmt.Errorf("custom role %q: %q is a reserved key and cannot be used as a level name", role, level)
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
// The given flat permission map is stored as both the "read" and "write"
// levels for the role. Both levels return the same GitHub permission values
// — the level name selects the tier, not the individual access values.
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
		cpRead := make(map[string]string, len(p))
		cpWrite := make(map[string]string, len(p))
		for k, v := range p {
			if v != "read" && v != "write" {
				return fmt.Errorf("custom role %q: permission %q has invalid value %q (must be read or write)", role, k, v)
			}
			cpRead[k] = v
			cpWrite[k] = v
		}
		safe[role] = map[string]map[string]string{LevelRead: cpRead, LevelWrite: cpWrite}
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
// level structure.
//
// Flat format (role → permissions): the given map is stored as both the
// "read" and "write" levels for the role. Both levels return the same
// GitHub permission values.
//
// Multi-level format (role → {"levels": {level → permissions}}): the
// levels object is used as-is. Extra named levels beyond read/write are
// allowed; the mandatory read and write levels are enforced at
// registration time by validateCustomRoleLevels.
//
// Mixed format within a single value is allowed.
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
			// Validate permission values early (defense-in-depth:
			// RegisterCustomRoleLevels also validates, but catching
			// errors here gives a clearer parse-time error message).
			for level, perms := range multiLevel.Levels {
				for k, v := range perms {
					if v != "read" && v != "write" {
						return nil, fmt.Errorf("custom role %q level %q: permission %q has invalid value %q (must be read or write)", role, level, k, v)
					}
				}
			}
			result[role] = multiLevel.Levels
			continue
		}

		// Fall back to flat format: {"permission": "value"}.
		// Store the same map under both mandatory levels so that
		// read and write are ordinary lookups — no derivation needed.
		var flat map[string]string
		if err := json.Unmarshal(rawVal, &flat); err != nil {
			return nil, fmt.Errorf("custom role %q: invalid format: %w", role, err)
		}
		for k, v := range flat {
			if v != "read" && v != "write" {
				return nil, fmt.Errorf("custom role %q: permission %q has invalid value %q (must be read or write)", role, k, v)
			}
		}
		// Deep-copy for the second level to avoid aliasing.
		flatCopy := make(map[string]string, len(flat))
		for k, v := range flat {
			flatCopy[k] = v
		}
		result[role] = map[string]map[string]string{LevelRead: flat, LevelWrite: flatCopy}
	}

	return result, nil
}

// RolePermissionsForLevel returns the permissions for a role at the given
// privilege level. Levels are keys on the role — the lookup is a simple
// table index with no derivation or fallback.
//
// When level is empty it defaults to LevelRead (the API default for
// omitted levels). The function checks canonical (built-in) roles first
// (avoids atomic load on the hot path), then custom roles. Returns a
// copy of the stored permission map.
//
// Errors:
//   - unknown role → "no permissions defined for role %q"
//   - known role, missing level → "role %q has no level %q"
func RolePermissionsForLevel(role, level string) (map[string]string, error) {
	if level == "" {
		level = LevelRead
	}

	// Check built-in roles first.
	if roleLevels, ok := canonicalRolePermissions[role]; ok {
		if perms, ok := roleLevels[level]; ok {
			cp := make(map[string]string, len(perms))
			for k, v := range perms {
				cp[k] = v
			}
			return cp, nil
		}
		return nil, fmt.Errorf("role %q has no level %q", role, level)
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
			return nil, fmt.Errorf("role %q has no level %q", role, level)
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
// privilege level. When level is empty, RolePermissionsForLevel defaults to
// LevelRead. The level selects a key on the role's permission table — there
// is no derivation or fallback.
func CreateInstallationToken(ctx context.Context, githubBaseURL, jwt string, installationID int64, role, level string, repos []string) (string, string, *GrantedScope, error) {
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
