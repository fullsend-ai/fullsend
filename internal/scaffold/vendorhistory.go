package scaffold

import (
	"strings"
	"sync"
)

// historicalVendoredInfraPaths are .defaults/-relative paths from the root
// action.yml, .github/actions/ and .github/scripts/ trees that earlier
// vendored installs shipped but the current allowlists no longer do. Before
// the allowlists (#6512) those directories were vendored whole, so installs
// from that era hold these files. Identified from the repository history
// between the introduction of --vendor and the allowlist change.
var historicalVendoredInfraPaths = []string{
	".github/actions/check-e2e-authorization/action.yml",
	".github/scripts/check-agents-gate-pin-test.sh",
	".github/scripts/check-agents-gate-pin.sh",
	".github/scripts/check-fix-eligibility-test.sh",
	".github/scripts/redact-behaviour-artifacts-test.sh",
	".github/scripts/redact-behaviour-artifacts.sh",
}

// historicalVendoredLayeredPaths are paths, relative to
// .defaults/internal/scaffold/fullsend-repo/, that earlier vendored installs
// shipped from the layered content but the embedded scaffold no longer
// carries (agents, harnesses, skills, scripts and similar that moved out of
// the scaffold). Identified from the repository history of the scaffold
// since --vendor was introduced, limited to the layered directories vendoring
// copied and excluding *-test.sh and *-test.py self-tests, which vendoring
// never shipped.
var historicalVendoredLayeredPaths = []string{
	"agents/code.md",
	"agents/fix.md",
	"agents/prioritize.md",
	"agents/retro.md",
	"agents/review.md",
	"agents/triage.md",
	"env/code-agent.env",
	"env/fix-agent.env",
	"env/gcp-vertex.env",
	"env/prioritize.env",
	"env/retro.env",
	"env/review.env",
	"env/triage.env",
	"harness/code.yaml",
	"harness/fix.yaml",
	"harness/prioritize.yaml",
	"harness/retro.yaml",
	"harness/review.yaml",
	"harness/triage.yaml",
	"plugins/gopls-lsp/.lsp.json",
	"plugins/gopls-lsp/plugin.json",
	"policies/base.yaml",
	"policies/code.yaml",
	"policies/fix.yaml",
	"policies/prioritize.yaml",
	"policies/retro.yaml",
	"policies/review.yaml",
	"policies/triage.yaml",
	"profiles/fullsend-jira.yaml",
	"providers/jira.yaml",
	"schemas/code-result.schema.json",
	"schemas/fix-result.schema.json",
	"schemas/prioritize-result.schema.json",
	"schemas/retro-result.schema.json",
	"schemas/review-result.schema.json",
	"schemas/triage-result.schema.json",
	"scripts/extract-transcript-error.sh",
	"scripts/lib/github-api-csma.sh",
	"scripts/post-code.sh",
	"scripts/post-fix.sh",
	"scripts/post-prioritize.sh",
	"scripts/post-retro.sh",
	"scripts/post-review.sh",
	"scripts/post-triage.sh",
	"scripts/pre-code.sh",
	"scripts/pre-fix.sh",
	"scripts/pre-prioritize.sh",
	"scripts/pre-retro.sh",
	"scripts/pre-review.sh",
	"scripts/pre-triage.sh",
	"scripts/process-fix-result.py",
	"scripts/reconcile-repos.sh",
	"scripts/scan-secrets",
	"scripts/validate-output-schema.sh",
	"scripts/validate-source-repo.sh",
	"skills/agent-scaffolding/SKILL.md",
	"skills/autonomy-readiness/SKILL.md",
	"skills/code-implementation/SKILL.md",
	"skills/code-review/SKILL.md",
	"skills/docs-review/SKILL.md",
	"skills/finding-agent-runs/SKILL.md",
	"skills/fix-review/SKILL.md",
	"skills/issue-labels/SKILL.md",
	"skills/pr-review/SKILL.md",
	"skills/pr-review/meta-prompt.md",
	"skills/pr-review/sub-agents/challenger.md",
	"skills/pr-review/sub-agents/correctness.md",
	"skills/pr-review/sub-agents/cross-repo-contracts.md",
	"skills/pr-review/sub-agents/docs-currency.md",
	"skills/pr-review/sub-agents/intent-coherence.md",
	"skills/pr-review/sub-agents/security.md",
	"skills/pr-review/sub-agents/style-conventions.md",
	"skills/retro-analysis/SKILL.md",
}

// shippedMirrorPaths returns the set of paths, relative to a .defaults/
// mirror root, that a vendored install ships now or shipped in the past: the
// current embedded layout plus the explicit historical lists above. A path
// outside this set is never treated as Fullsend-owned, even when the vendor
// manifest lists it, because a manifest is repository content a user can edit.
var shippedMirrorPaths = sync.OnceValues(func() (map[string]bool, error) {
	current, err := enumerateVendoredPaths()
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(current)+len(historicalVendoredInfraPaths)+len(historicalVendoredLayeredPaths))
	for _, p := range current {
		if rest, ok := strings.CutPrefix(p, defaultsVendoredPrefix); ok {
			set[rest] = true
		}
	}
	for _, p := range historicalVendoredInfraPaths {
		set[p] = true
	}
	for _, p := range historicalVendoredLayeredPaths {
		set["internal/scaffold/fullsend-repo/"+p] = true
	}
	return set, nil
})
