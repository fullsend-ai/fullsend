package scaffold

import (
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// RenderOptions controls install-time substitution for shim and thin-caller templates.
type RenderOptions struct {
	Vendored       bool
	PerRepo        bool
	UpstreamRef    string // commit SHA to pin workflow refs to; empty = use DefaultUpstreamRef
	UpstreamTag    string // version tag for traceability comment (e.g. "v0.19.0")
	RunnerImage    string // GitHub Actions runner image; empty = use DefaultGHRunner
	CredentialMode string // credential mode (wif/oidc); controls WIF secret inclusion in per-repo shim
}

// RenderOptionsForInstall builds render options from the --vendor flag.
func RenderOptionsForInstall(vendored, perRepo bool, upstreamRef, upstreamTag string) RenderOptions {
	return RenderOptions{Vendored: vendored, PerRepo: perRepo, UpstreamRef: upstreamRef, UpstreamTag: upstreamTag}
}

// thinStageWorkflows lists thin caller paths and their stage markers. Keep in sync
// with the # fullsend-stage comments embedded in each workflow template.
var thinStageWorkflows = []struct {
	stage string
	path  string
}{
	{"triage", ".github/workflows/triage.yml"},
	{"code", ".github/workflows/code.yml"},
	{"review", ".github/workflows/review.yml"},
	{"fix", ".github/workflows/fix.yml"},
	{"retro", ".github/workflows/retro.yml"},
	{"prioritize", ".github/workflows/prioritize.yml"},
}

// RenderTemplate applies vendoring-aware substitutions to scaffold templates.
// Substitutions are fixed string replacements (not text/template), so only
// compile-time constants are injected into workflow YAML.
func RenderTemplate(path string, content []byte, opts RenderOptions) ([]byte, error) {
	out := string(content)

	switch {
	case isThinStageCaller(path):
		stage, err := thinStageName(out)
		if err != nil {
			return nil, err
		}
		out = strings.ReplaceAll(out, "__REUSABLE_WORKFLOW__", reusableWorkflowUses(stage, opts))
	case path == "templates/shim-per-repo.yaml":
		out = strings.ReplaceAll(out, "__REUSABLE_DISPATCH__", reusableDispatchUses(opts))
		out = stripWIFSecrets(out, opts)
	}

	out = strings.ReplaceAll(out, "__FULLSEND_AI_REF__", resolvedRefWithComment(opts))
	out = strings.ReplaceAll(out, "__GH_RUNNER__", resolvedRunner(opts))

	return []byte(out), nil
}

func isThinStageCaller(path string) bool {
	for _, w := range thinStageWorkflows {
		if path == w.path {
			return true
		}
	}
	return false
}

func thinStageName(content string) (string, error) {
	for _, w := range thinStageWorkflows {
		if strings.Contains(content, "# fullsend-stage: "+w.stage) {
			return w.stage, nil
		}
	}
	return "", fmt.Errorf("could not determine thin caller stage")
}

func resolvedRunner(opts RenderOptions) string {
	if opts.RunnerImage != "" {
		return opts.RunnerImage
	}
	return config.DefaultGHRunner
}

func resolvedRef(opts RenderOptions) string {
	if opts.UpstreamRef != "" {
		return opts.UpstreamRef
	}
	return config.DefaultUpstreamRef
}

func resolvedRefWithComment(opts RenderOptions) string {
	ref := resolvedRef(opts)
	if opts.UpstreamTag != "" && opts.UpstreamTag != ref {
		return ref + " # " + opts.UpstreamTag
	}
	return ref
}

func reusableWorkflowUses(stage string, opts RenderOptions) string {
	if opts.Vendored {
		return "./.github/workflows/reusable-" + stage + ".yml"
	}
	ref := resolvedRef(opts)
	uses := config.DefaultUpstreamRepo + "/.github/workflows/reusable-" + stage + ".yml@" + ref
	if opts.UpstreamTag != "" && opts.UpstreamTag != ref {
		uses += " # " + opts.UpstreamTag
	}
	return uses
}

// stripWIFSecrets removes WIF secret references from the per-repo shim
// when credential mode is "oidc". OIDC repos authenticate directly to
// a public mint without GCP WIF, so these secrets are unnecessary and
// their presence in the generated workflow is confusing.
func stripWIFSecrets(content string, opts RenderOptions) string {
	if opts.CredentialMode != "oidc" {
		return content
	}
	content = strings.ReplaceAll(content,
		"      FULLSEND_GCP_WIF_PROVIDER: ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}\n", "")
	content = strings.ReplaceAll(content,
		"      FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}\n", "")
	return content
}

func reusableDispatchUses(opts RenderOptions) string {
	if opts.Vendored {
		return "./.github/workflows/reusable-dispatch.yml"
	}
	ref := resolvedRef(opts)
	uses := config.DefaultUpstreamRepo + "/.github/workflows/reusable-dispatch.yml@" + ref
	if opts.UpstreamTag != "" && opts.UpstreamTag != ref {
		uses += " # " + opts.UpstreamTag
	}
	return uses
}
