package repos

import (
	"regexp"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

var (
	ghWorkflowRefPattern = regexp.MustCompile(
		`uses:\s+fullsend-ai/fullsend/\.github/workflows/[^@]+@(\S+)`,
	)
	ghShimRefPattern = regexp.MustCompile(
		`(uses:\s+` + shimOwner + `/` + shimRepo + `/[^@]+@)\S+([ \t]*#.*)?`,
	)
	glWorkflowRefPattern = regexp.MustCompile(
		`(?m)\bref:\s+['"]?(\S+?)['"]?[ \t]*$`,
	)
	glShimRefPattern = regexp.MustCompile(
		`(?m)(\bref:\s+)['"]?\S+?['"]?[ \t]*$`,
	)
)

// ForgeConfig holds forge-specific CI paths and regex patterns used by
// status and upgrade operations. Each forge has different workflow file
// conventions and ref syntax. When populated by a ForgeClientFactory,
// the Client field carries a live API client for the forge.
type ForgeConfig struct {
	// Client is the API client for this forge. Set by ForgeClientFactory;
	// nil when ForgeConfig is constructed by ForgeConfigFor (pattern-only).
	Client forge.Client

	// WorkflowPaths lists the shim workflow file paths to try, in order.
	WorkflowPaths []string

	// WorkflowRefPattern extracts the @ref from a fullsend shim workflow.
	WorkflowRefPattern *regexp.Regexp

	// ShimRefPattern matches all @ref occurrences in fullsend uses: lines
	// within a workflow file, used by upgrade to rewrite refs.
	ShimRefPattern *regexp.Regexp
}

// ForgeClientFactory creates ForgeConfig instances with a live Client.
// The CLI layer implements this with lazy client creation and caching.
type ForgeClientFactory interface {
	// ConfigFor returns a ForgeConfig with a live Client for the named forge.
	// The factory lazily creates and caches clients, so a GitLab token is
	// only required if the manifest actually contains GitLab entries.
	ConfigFor(forgeName string) (ForgeConfig, error)
}

// GitHubForgeConfig returns the ForgeConfig for GitHub repositories.
// GitHub Actions workflow files live under .github/workflows/ and use
// the "uses: owner/repo/.github/workflows/file@ref" syntax.
func GitHubForgeConfig() ForgeConfig {
	return ForgeConfig{
		WorkflowPaths: []string{
			".github/workflows/fullsend.yml",
			".github/workflows/fullsend.yaml",
		},
		WorkflowRefPattern: ghWorkflowRefPattern,
		ShimRefPattern:     ghShimRefPattern,
	}
}

// GitLabForgeConfig returns the ForgeConfig for GitLab repositories.
// GitLab CI files live under .gitlab/ci/ and use include: directives
// with ref: fields. Patterns here are placeholders for the GitLab CI
// dispatch template structure (see ADR 0067). The ShimRefPattern
// matches any ref: line in the file — the dispatch file must contain
// only the fullsend include (single-include constraint).
func GitLabForgeConfig() ForgeConfig {
	return ForgeConfig{
		WorkflowPaths: []string{
			".gitlab/ci/fullsend-dispatch.yml",
		},
		WorkflowRefPattern: glWorkflowRefPattern,
		ShimRefPattern:     glShimRefPattern,
	}
}

// defaultForgeConfig is the GitHub forge config, used as a fallback
// when no forge is specified. This preserves backward compatibility
// with code paths that predate the multi-forge manifest support.
var defaultForgeConfig = GitHubForgeConfig()

// ForgeConfigFor returns the ForgeConfig for the given forge name.
// Unknown values (including empty string) default to GitHub. This is
// intentional: callers in legacy code paths may pass an empty forge,
// and Validate() catches invalid forge names before they reach here.
func ForgeConfigFor(forgeName string) ForgeConfig {
	switch forgeName {
	case ForgeGitLab:
		return GitLabForgeConfig()
	default:
		return GitHubForgeConfig()
	}
}
