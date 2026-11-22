package scaffold

import (
	"fmt"
	"strings"
)

// LintCallerPath is the path of the Fullsend-managed GitHub lint caller
// (#9787).
const LintCallerPath = ".github/workflows/fullsend-lint.yml"

// lintCallerTemplate is the embedded template rendered to LintCallerPath.
const lintCallerTemplate = "templates/lint-per-repo.yaml"

// LintCallerMarker marks a workflow at LintCallerPath as Fullsend-managed.
// `repos install` never rewrites or deletes a file there without it.
const LintCallerMarker = "# fullsend-component: lint"

// RenderLintCaller renders the managed lint caller, pinned to the ref in
// opts. It always references the upstream repository, also for a vendored
// install.
func RenderLintCaller(opts RenderOptions) ([]byte, error) {
	raw, err := FullsendRepoFile(lintCallerTemplate)
	if err != nil {
		return nil, fmt.Errorf("loading lint caller template: %w", err)
	}
	out := strings.ReplaceAll(string(raw), "__REUSABLE_WORKFLOW__", upstreamReusableWorkflowUses("reusable-fullsend-lint.yml", opts))
	return PrependManagedHeader(lintCallerTemplate, []byte(out)), nil
}

// IsManagedLintCaller reports whether content is a Fullsend-managed lint
// caller: one of its lines is LintCallerMarker.
func IsManagedLintCaller(content []byte) bool {
	return hasMarkerLine(content, LintCallerMarker)
}
