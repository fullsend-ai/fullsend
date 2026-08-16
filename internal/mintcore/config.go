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

	// WorkflowHostRepos is a comma-separated list of repos whose workflows
	// are trusted to call the mint in per-repo mode. Defaults to
	// fullsend-ai/fullsend when empty.
	WorkflowHostRepos string

	// CustomRolePermissions is a JSON-encoded map of custom role permissions.
	// Same format as the CUSTOM_ROLE_PERMISSIONS environment variable.
	CustomRolePermissions string

	// Version is the fullsend semver stamped on the deployed Worker.
	// For WASM deployments this is injected via the config JSON since
	// the binary is precompiled and cannot embed version at compile time.
	Version string

	// Commit is the git SHA stamped on the deployed Worker.
	Commit string
}

// ParseWorkerConfig parses a WorkerConfig and returns a Handler.
// This is the primary constructor for Worker deployments where config
// comes from Worker bindings rather than process environment variables.
func ParseWorkerConfig(cfg WorkerConfig, pemAccessor PEMAccessor, oidcVerifier OIDCVerifier, doer Doer) (*Handler, error) {
	if cfg.RoleAppIDs == "" {
		return nil, fmt.Errorf("RoleAppIDs is required")
	}
	if cfg.OIDCAudience == "" {
		return nil, fmt.Errorf("OIDCAudience is required")
	}
	// Stamp version metadata from the config so that /health and /status
	// report the deployed version. For GCF deploys this is compiled into
	// the source (version.go); for WASM deploys it arrives at runtime via
	// the config JSON because the binary is precompiled.
	if cfg.Version != "" {
		Version = cfg.Version
	}
	if cfg.Commit != "" {
		Commit = cfg.Commit
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

	h, err := NewHandlerFromConfig(cfg.RoleAppIDs, cfg.AllowedRoles, cfg.PerRepoWIFRepos, cfg.AllowedOrgs, cfg.AllowedWorkflowFiles, cfg.WorkflowHostRepos, pemAccessor, oidcVerifier, doer)
	if err != nil {
		return nil, err
	}
	return h, nil
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
// comma-separated ALLOWED_ROLES list (empty means all roles from roleAppIDs);
// perRepoWIFReposCSV is the comma-separated PER_REPO_WIF_REPOS list;
// allowedOrgsCSV is the comma-separated ALLOWED_ORGS list;
// allowedWorkflowFilesCSV is the comma-separated ALLOWED_WORKFLOW_FILES list;
// workflowHostReposCSV is the comma-separated WORKFLOW_HOST_REPOS list
// (defaults to fullsend-ai/fullsend when empty).
//
// The handler performs authorization (org-allowed, workflow-ref) after the
// OIDCVerifier authenticates the token.
func NewHandlerFromConfig(roleAppIDsJSON, allowedRolesCSV, perRepoWIFReposCSV, allowedOrgsCSV, allowedWorkflowFilesCSV, workflowHostReposCSV string, pemAccessor PEMAccessor, oidcVerifier OIDCVerifier, doer Doer) (*Handler, error) {
	perRepoWIFRepos := make(map[string]bool)
	for _, entry := range SplitCSV(perRepoWIFReposCSV) {
		perRepoWIFRepos[strings.ToLower(entry)] = true
	}

	workflowHostRepos := make(map[string]bool)
	for _, entry := range SplitCSV(workflowHostReposCSV) {
		workflowHostRepos[strings.ToLower(entry)] = true
	}
	if len(workflowHostRepos) == 0 {
		workflowHostRepos["fullsend-ai/fullsend"] = true
	}

	h := &Handler{
		doer:                 doer,
		pemAccessor:          pemAccessor,
		oidcVerifier:         oidcVerifier,
		githubBaseURL:        "https://api.github.com",
		foreignCache:         make(map[string]foreignCacheEntry),
		foreignInflight:      make(map[string]*foreignInflight),
		foreignCacheTTL:      defaultForeignCacheTTL,
		perRepoWIFRepos:      perRepoWIFRepos,
		allowedOrgs:          SplitCSV(allowedOrgsCSV),
		allowedWorkflowFiles: SplitCSV(allowedWorkflowFilesCSV),
		workflowHostRepos:    workflowHostRepos,
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
