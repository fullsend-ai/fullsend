package gcf

import (
	"context"
	"fmt"
	"log"
	"maps"
	"net/url"
	"regexp"
	"strings"
)

// GitLabIDTokenAudience is the audience GitLab agent jobs request for the
// id_tokens OIDC token exchanged through a GitLab inference WIF provider.
// It matches scaffold.DefaultGitLabIDTokenAudience.
const GitLabIDTokenAudience = "fullsend"

// DefaultGitLabURL is the GitLab instance used as the OIDC issuer when none
// is given.
const DefaultGitLabURL = "https://gitlab.com"

// gitlabProjectPathPattern matches a GitLab project path: a group (with
// optional subgroups) and a project, each segment starting with an
// alphanumeric or underscore and otherwise containing only alphanumerics,
// underscores, dots, and hyphens. The character set excludes quotes, so a
// valid path is safe to embed in a CEL string literal.
var gitlabProjectPathPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)+$`)

// gitlabProjectIDPattern matches a positive decimal GitLab project ID.
var gitlabProjectIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

// GitLabProject identifies a GitLab project for inference WIF provisioning.
type GitLabProject struct {
	// Path is the full project path (group[/subgroup...]/project) exactly
	// as it appears in the project_path claim. CEL comparison is
	// case-sensitive, so the canonical case must be used.
	Path string
	// ID is the numeric GitLab project ID (Settings → General).
	ID string
	// IssuerURL is the GitLab instance URL, which is the OIDC issuer of
	// its id_tokens. Empty selects DefaultGitLabURL.
	IssuerURL string
}

// ValidateGitLabProjectPath checks that path is a group/project path.
func ValidateGitLabProjectPath(path string) error {
	if !gitlabProjectPathPattern.MatchString(path) {
		return fmt.Errorf("invalid GitLab project path %q: expected group[/subgroup...]/project using only alphanumerics, underscores, dots, and hyphens", path)
	}
	for seg := range strings.SplitSeq(path, "/") {
		if strings.HasSuffix(seg, ".git") || strings.HasSuffix(seg, ".atom") || strings.Contains(seg, "..") {
			return fmt.Errorf("invalid GitLab project path %q: segment %q is not a valid GitLab path segment", path, seg)
		}
	}
	return nil
}

// ValidateGitLabProjectID checks that id is a positive decimal project ID.
func ValidateGitLabProjectID(id string) error {
	if !gitlabProjectIDPattern.MatchString(id) {
		return fmt.Errorf("invalid GitLab project ID %q: must be the numeric project ID (Settings → General → Project ID)", id)
	}
	return nil
}

// NormalizeGitLabIssuerURL validates a GitLab instance URL for use as a WIF
// OIDC issuer and returns it without a trailing slash. An empty value
// returns DefaultGitLabURL. The URL must be https with a host and no
// credentials, query, or fragment; a path is allowed for instances served
// under a relative URL root.
func NormalizeGitLabIssuerURL(raw string) (string, error) {
	if raw == "" {
		return DefaultGitLabURL, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid GitLab URL %q: %w", raw, err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("invalid GitLab URL %q: must be an https:// URL with a host", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", fmt.Errorf("invalid GitLab URL %q: must not contain credentials, a query, or a fragment", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

// BuildGitLabProviderID derives the per-project WIF provider ID for a GitLab
// project path, mirroring the GitHub per-repo convention: "gl-" plus the
// lowercased path with every character outside [a-z0-9-] (including "/")
// replaced by "-", truncated to the 32-character provider ID limit, with
// trailing hyphens trimmed.
func BuildGitLabProviderID(projectPath string) string {
	raw := strings.ToLower("gl-" + projectPath)
	raw = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, raw)
	if len(raw) > 32 {
		raw = raw[:32]
	}
	return strings.TrimRight(raw, "-")
}

// gitlabAttributeMapping maps GitLab id_tokens claims. google.subject uses
// the numeric project_id because GitLab's structured sub claim can exceed
// the 127-byte google.subject limit.
func gitlabAttributeMapping() map[string]string {
	return map[string]string{
		"google.subject":           "assertion.project_id",
		"attribute.namespace_path": "assertion.namespace_path",
		"attribute.project_path":   "assertion.project_path",
	}
}

// gitlabAttributeCondition binds both the immutable numeric project ID and
// the readable project path, so a deleted path reused by a different
// project cannot satisfy the condition.
func gitlabAttributeCondition(projectID, projectPath string) string {
	return fmt.Sprintf("assertion.project_id == '%s' && %s", projectID, gitlabProjectPathClause(projectPath))
}

// gitlabProjectPathClause is the part of the attribute condition that
// identifies the project path; provider-collision checks look for it.
func gitlabProjectPathClause(projectPath string) string {
	return fmt.Sprintf("assertion.project_path == '%s'", projectPath)
}

// GitLabProviderPlan describes the WIF provider and IAM binding that
// ProvisionGitLabWIF creates for a GitLab project. It needs no GCP calls,
// so dry runs can print it.
type GitLabProviderPlan struct {
	ProviderID string
	Config     OIDCProviderConfig
	// PrincipalSetSuffix is the pool-relative principal set granted
	// roles/aiplatform.user ("attribute.project_path/<path>").
	PrincipalSetSuffix string
}

// PlanGitLabWIF validates gl and returns the provider and IAM binding that
// would be provisioned for it.
func PlanGitLabWIF(gl GitLabProject) (*GitLabProviderPlan, error) {
	if err := ValidateGitLabProjectPath(gl.Path); err != nil {
		return nil, err
	}
	if err := ValidateGitLabProjectID(gl.ID); err != nil {
		return nil, err
	}
	issuer, err := NormalizeGitLabIssuerURL(gl.IssuerURL)
	if err != nil {
		return nil, err
	}
	return &GitLabProviderPlan{
		ProviderID: BuildGitLabProviderID(gl.Path),
		Config: OIDCProviderConfig{
			IssuerURI:          issuer,
			AttributeCondition: gitlabAttributeCondition(gl.ID, gl.Path),
			AllowedAudiences:   []string{GitLabIDTokenAudience},
			AttributeMapping:   gitlabAttributeMapping(),

			RequiredConditionClause: gitlabProjectPathClause(gl.Path),
		},
		PrincipalSetSuffix: "attribute.project_path/" + gl.Path,
	}, nil
}

// ProvisionGitLabWIF creates the WIF pool, a dedicated per-project OIDC
// provider trusting the GitLab instance, and the roles/aiplatform.user
// binding on the project's attribute.project_path principal set. All
// operations are idempotent; a provider that already matches is not
// rewritten. Returns the full WIF provider resource path.
//
// An existing provider with the same ID whose condition is bound to a
// different project path is never overwritten: provider IDs are truncated
// to 32 characters, so two long paths can derive the same ID.
func (p *Provisioner) ProvisionGitLabWIF(ctx context.Context, gl GitLabProject) (string, error) {
	if p.cfg.ProjectID == "" {
		return "", fmt.Errorf("GCP project ID is required")
	}
	if !gcpProjectIDPattern.MatchString(p.cfg.ProjectID) {
		return "", fmt.Errorf("invalid GCP project ID: %q", p.cfg.ProjectID)
	}
	plan, err := PlanGitLabWIF(gl)
	if err != nil {
		return "", err
	}

	projectNumber, err := p.gcpAPI.GetProjectNumber(ctx, p.cfg.ProjectID)
	if err != nil {
		return "", fmt.Errorf("getting project number: %w", err)
	}
	wifProvider := fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s/providers/%s",
		projectNumber, p.cfg.WIFPoolName, plan.ProviderID)

	existing, err := p.gcpAPI.GetWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, plan.ProviderID)
	if err != nil {
		return "", fmt.Errorf("reading WIF provider %s: %w", plan.ProviderID, err)
	}
	if existing != nil && strings.TrimSpace(existing.AttributeCondition) != "" &&
		!strings.Contains(existing.AttributeCondition, plan.Config.RequiredConditionClause) {
		return "", fmt.Errorf("WIF provider %s already exists with a condition for a different project (%q); refusing to overwrite it", plan.ProviderID, existing.AttributeCondition)
	}

	if !gitlabProviderMatches(existing, plan.Config) {
		if err := p.gcpAPI.CreateWIFPool(ctx, projectNumber, p.cfg.WIFPoolName, "Fullsend GitHub OIDC Pool"); err != nil {
			return "", fmt.Errorf("creating WIF pool: %w", err)
		}
		if existing != nil && existing.Usable() {
			// Active provider with drifted config: patch it in place.
			if err := p.gcpAPI.UpdateWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, plan.ProviderID, plan.Config); err != nil {
				return "", fmt.Errorf("updating WIF provider: %w", err)
			}
		} else if err := p.gcpAPI.CreateWIFProvider(ctx, projectNumber, p.cfg.WIFPoolName, plan.ProviderID, plan.Config); err != nil {
			// Missing, disabled, or soft-deleted: CreateWIFProvider's
			// conflict path undeletes, updates, and re-enables.
			return "", fmt.Errorf("creating WIF provider: %w", err)
		}
	}

	principal := fmt.Sprintf("principalSet://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/%s",
		projectNumber, p.cfg.WIFPoolName, plan.PrincipalSetSuffix)
	if err := p.gcpAPI.SetProjectIAMBinding(ctx, p.cfg.ProjectID, principal, "roles/aiplatform.user"); err != nil {
		return "", fmt.Errorf("granting Agent Platform access for GitLab project %s: %w", gl.Path, err)
	}
	log.Printf("granted roles/aiplatform.user to %s (propagation may take several minutes)", gl.Path)

	return wifProvider, nil
}

// gitlabProviderMatches reports whether an existing provider already has the
// desired issuer, condition, audiences, and attribute mapping and can
// exchange tokens.
func gitlabProviderMatches(existing *WIFProviderInfo, desired OIDCProviderConfig) bool {
	if !repoProviderMatches(existing, desired) {
		return false
	}
	return maps.Equal(existing.AttributeMapping, desired.AttributeMapping)
}
