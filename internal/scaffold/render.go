package scaffold

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// RenderOptions controls install-time substitution for shim and thin-caller templates.
type RenderOptions struct {
	Vendored bool
	PerRepo  bool
}

// RenderOptionsForInstall builds render options from the --vendor flag.
func RenderOptionsForInstall(vendored, perRepo bool) RenderOptions {
	return RenderOptions{Vendored: vendored, PerRepo: perRepo}
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
	}

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

func reusableWorkflowUses(stage string, opts RenderOptions) string {
	if opts.Vendored {
		if opts.PerRepo {
			return "./.fullsend/.github/workflows/reusable-" + stage + ".yml"
		}
		return "./.github/workflows/reusable-" + stage + ".yml"
	}
	return config.DefaultUpstreamRepo + "/.github/workflows/reusable-" + stage + ".yml@" + config.DefaultUpstreamRef
}

func reusableDispatchUses(opts RenderOptions) string {
	if opts.Vendored {
		return "./.fullsend/.github/workflows/reusable-dispatch.yml"
	}
	return config.DefaultUpstreamRepo + "/.github/workflows/reusable-dispatch.yml@" + config.DefaultUpstreamRef
}

// RenderDispatchPerRepoStagePaths rewrites stage workflow paths for vendored
// per-repo installs where reusable-dispatch.yml lives under .fullsend/.
func RenderDispatchPerRepoStagePaths(content []byte) []byte {
	return dispatchStageUses.ReplaceAll(content, []byte(`uses: ./.fullsend/.github/workflows/reusable-$1.yml`))
}

var dispatchStageUses = regexp.MustCompile(`uses: fullsend-ai/fullsend/\.github/workflows/reusable-([a-z-]+)\.yml@[^\s]+`)
