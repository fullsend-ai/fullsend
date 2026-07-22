package mintcore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// WorkerConfig holds the explicit configuration for constructing a Handler
// without relying on os.Getenv. Used by the Cloudflare Worker WASM host
// and any other deployment where environment variables are unavailable.
type WorkerConfig struct {
	// RoleAppIDs is the JSON-encoded mapping of role names to GitHub App IDs.
	// Same format as the ROLE_APP_IDS environment variable.
	RoleAppIDs string

	// AllowedRoles is a comma-separated list of allowed roles.
	// Same format as the ALLOWED_ROLES environment variable.
	// If empty, all roles from RoleAppIDs are allowed.
	AllowedRoles string

	// AllowedOrgs is a comma-separated list of allowed GitHub orgs.
	AllowedOrgs string

	// OIDCAudience is the expected OIDC audience claim.
	OIDCAudience string

	// AllowedWorkflowFiles is a comma-separated list of allowed workflow filenames.
	AllowedWorkflowFiles string

	// PerRepoWIFRepos is a comma-separated list of repos with per-repo WIF.
	PerRepoWIFRepos string

	// CustomRolePermissions is a JSON-encoded map of custom role permissions.
	// Same format as the CUSTOM_ROLE_PERMISSIONS environment variable.
	CustomRolePermissions string
}

// ParseWorkerConfig parses a WorkerConfig and returns a Handler.
// This is the primary constructor for Worker deployments where config
// comes from Worker bindings rather than process environment variables.
func ParseWorkerConfig(cfg WorkerConfig, pemAccessor PEMAccessor, oidcVerifier OIDCVerifier, httpClient HTTPDoer) (*Handler, error) {
	if cfg.RoleAppIDs == "" {
		return nil, fmt.Errorf("RoleAppIDs is required")
	}
	if cfg.OIDCAudience == "" {
		return nil, fmt.Errorf("OIDCAudience is required")
	}
	if cfg.AllowedOrgs == "" {
		return nil, fmt.Errorf("AllowedOrgs is required")
	}

	if cfg.CustomRolePermissions != "" {
		var perms map[string]map[string]string
		if err := json.Unmarshal([]byte(cfg.CustomRolePermissions), &perms); err != nil {
			return nil, fmt.Errorf("failed to parse CustomRolePermissions: %w", err)
		}
		if err := RegisterCustomRolePermissions(perms); err != nil {
			return nil, fmt.Errorf("registering custom role permissions: %w", err)
		}
	}

	return NewHandlerFromConfig(cfg.RoleAppIDs, cfg.AllowedRoles, pemAccessor, oidcVerifier, httpClient)
}

// SplitCSV splits a comma-separated string into trimmed, non-empty entries.
// Shared by cmd/mint and cmd/mint-wasm for parsing config fields like
// AllowedOrgs and AllowedWorkflowFiles.
func SplitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	for _, entry := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// NewHandlerFromConfig creates a Handler from explicit configuration values
// instead of reading from environment variables. The roleAppIDsJSON parameter
// is the JSON-encoded ROLE_APP_IDS mapping; allowedRolesCSV is the
// comma-separated ALLOWED_ROLES list (empty means all roles from roleAppIDs).
//
// The caller is responsible for configuring the OIDCVerifier with the
// appropriate AllowedOrgs, AllowedWorkflowFiles, and PerRepoWIFRepos
// before passing it here. ParseWorkerConfig handles this automatically;
// direct callers must do it themselves.
func NewHandlerFromConfig(roleAppIDsJSON, allowedRolesCSV string, pemAccessor PEMAccessor, oidcVerifier OIDCVerifier, httpClient HTTPDoer) (*Handler, error) {
	h := &Handler{
		httpClient:      httpClient,
		pemAccessor:     pemAccessor,
		oidcVerifier:    oidcVerifier,
		githubBaseURL:   "https://api.github.com",
		foreignCache:    make(map[string]foreignCacheEntry),
		foreignInflight: make(map[string]*foreignInflight),
		foreignCacheTTL: defaultForeignCacheTTL,
	}

	if roleAppIDsJSON != "" {
		var ids map[string]string
		if err := json.Unmarshal([]byte(roleAppIDsJSON), &ids); err != nil {
			return nil, fmt.Errorf("failed to parse RoleAppIDs: %w", err)
		}
		h.roleAppIDs = RoleOnlyAppIDs(ids)
		h.legacyAppIDsOnly = legacyAppIDsOnly(ids)
	}

	roleSet := make(map[string]bool, len(h.roleAppIDs))
	for role := range h.roleAppIDs {
		roleSet[role] = true
	}

	if allowedRolesCSV != "" {
		for _, entry := range strings.Split(allowedRolesCSV, ",") {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				if !RolePattern.MatchString(trimmed) {
					return nil, fmt.Errorf("AllowedRoles contains invalid entry %q: must match %s", trimmed, RolePattern.String())
				}
				h.allowedRoles = append(h.allowedRoles, trimmed)
			}
		}
	} else {
		for role := range roleSet {
			h.allowedRoles = append(h.allowedRoles, role)
		}
		sort.Strings(h.allowedRoles)
	}

	for _, role := range h.allowedRoles {
		if !HasRole(role) {
			return nil, fmt.Errorf("AllowedRoles contains %q but RolePermissions has no entry for it", role)
		}
		if !roleSet[role] {
			return nil, fmt.Errorf("AllowedRoles contains %q but RoleAppIDs has no entry for it", role)
		}
	}

	return h, nil
}
