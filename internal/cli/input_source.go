package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	// inputSourceEnvVar lets a workflow pass the input source of a run whose
	// harness does not declare input_source (#9509).
	inputSourceEnvVar = "FULLSEND_INPUT_SOURCE"
	// dryRunEnvVar asks host-side scripts to print instead of writing. An
	// external-input run sets it to 1 when the runner has not set it.
	dryRunEnvVar = "FULLSEND_DRY_RUN"
)

// Provider types that reach the forge. The first two can write; the
// read-only one is listed in the run output but needs no opt-in.
const (
	providerTypeGitHub       = "fullsend-github"
	providerTypeGitHubRO     = "fullsend-github-ro"
	providerTypeGitLabForge  = "fullsend-gitlab-forge"
	forgeWriteProviderAdvice = "switch to github-ro (type " + providerTypeGitHubRO + "), or set allow_forge_write: true in the harness"
)

// inputSource is the effective input source of a run and where it came
// from: "harness", "env" or "default".
type inputSource struct {
	value  string
	origin string
}

func (s inputSource) external() bool { return s.value == harness.InputSourceExternal }

// resolveInputSource returns the run's input source: the harness field when
// set, else FULLSEND_INPUT_SOURCE, else maintainer. An empty variable counts
// as unset. An invalid variable fails even when the harness sets the field,
// so a broken workflow input is not hidden by the harness.
func resolveInputSource(h *harness.Harness, lookup func(string) (string, bool)) (inputSource, error) {
	envValue, envSet := lookup(inputSourceEnvVar)
	envSet = envSet && envValue != ""
	if envSet && !harness.ValidInputSource(envValue) {
		return inputSource{}, fmt.Errorf("%s=%q is not valid (allowed: %s, %s)",
			inputSourceEnvVar, envValue, harness.InputSourceExternal, harness.InputSourceMaintainer)
	}
	if h.InputSource != "" {
		if !harness.ValidInputSource(h.InputSource) {
			return inputSource{}, fmt.Errorf("input_source %q is not valid (allowed: %s, %s)",
				h.InputSource, harness.InputSourceExternal, harness.InputSourceMaintainer)
		}
		return inputSource{value: h.InputSource, origin: "harness"}, nil
	}
	if envSet {
		return inputSource{value: envValue, origin: "env"}, nil
	}
	return inputSource{value: harness.InputSourceMaintainer, origin: "default"}, nil
}

// applyDryRunDefault sets FULLSEND_DRY_RUN=1 in the runner environment of an
// external-input run when neither the process environment nor env.runner
// sets it, so host-side scripts that read it do not write. A value that is
// already set, such as an explicit 0, is left as is. It returns the
// dry-run segment of the run output line.
func applyDryRunDefault(h *harness.Harness, src inputSource, lookup func(string) (string, bool)) string {
	if !src.external() {
		return "off"
	}
	if _, ok := h.RunnerEnv[dryRunEnvVar]; ok {
		return "off (" + dryRunEnvVar + " is set)"
	}
	if _, ok := lookup(dryRunEnvVar); ok {
		return "off (" + dryRunEnvVar + " is set)"
	}
	if h.RunnerEnv == nil {
		h.RunnerEnv = map[string]string{}
	}
	h.RunnerEnv[dryRunEnvVar] = "1"
	return "on"
}

// runProviderDefs returns the definitions of the providers the run will
// attach, resolved the way the provider step resolves them later: declared
// local definitions first, then the definitions shipped with fullsend, then
// URL- and path-resolved ones.
func runProviderDefs(absFullsendDir string, declared []string, resolved []resolve.ResolvedProvider) ([]harness.ProviderDef, error) {
	if len(declared) == 0 && len(resolved) == 0 {
		// The provider step skips providers/ too, so an unrelated file
		// there cannot fail a run that attaches no provider.
		return nil, nil
	}
	only := make(map[string]struct{}, len(declared))
	for _, p := range declared {
		only[p] = struct{}{}
	}
	localDefs, err := harness.LoadProviderDefs(filepath.Join(absFullsendDir, "providers"), only)
	if err != nil {
		return nil, fmt.Errorf("loading provider definitions: %w", err)
	}
	resolved = dedupResolvedProviders(resolved)
	// The provider step prints the embedded-definition notice; stay quiet here.
	defs := appendEmbeddedProviderDefs(localDefs, resolved, declared, ui.New(io.Discard))
	allDefs, _, _ := mergeProviderDefs(defs, resolved)
	return allDefs, nil
}

// forgeProviders returns the providers in defs that reach the forge, and
// those among them that can write to it.
func forgeProviders(defs []harness.ProviderDef) (forge, write []harness.ProviderDef) {
	for _, d := range defs {
		switch d.Type {
		case providerTypeGitHub, providerTypeGitLabForge:
			forge = append(forge, d)
			write = append(write, d)
		case providerTypeGitHubRO:
			forge = append(forge, d)
		}
	}
	return forge, write
}

// checkForgeWrite fails an external-input run that attaches a forge-write
// provider unless the harness sets allow_forge_write: true.
func checkForgeWrite(h *harness.Harness, src inputSource, write []harness.ProviderDef) error {
	if !src.external() || h.AllowForgeWrite || len(write) == 0 {
		return nil
	}
	names := make([]string, len(write))
	for i, d := range write {
		names[i] = fmt.Sprintf("provider %q (type %s)", d.Name, d.Type)
	}
	return fmt.Errorf("input_source is external (%s), but %s can write to the forge; %s",
		src.origin, strings.Join(names, ", "), forgeWriteProviderAdvice)
}

// durableHostFileDests returns the dest of each host_files entry whose
// source checkHostFileCredentials classifies as a durable credential.
func durableHostFileDests(h *harness.Harness) []string {
	var dests []string
	for _, hf := range h.HostFiles {
		path, copied := hostFileSource(hf)
		if !copied {
			continue
		}
		if shape, err := classifyHostFile(path, hf.Expand); err == nil && shape.durable() {
			dests = append(dests, hf.Dest)
		}
	}
	return dests
}

// inputSourceLine is the run output line that records the input source and
// the defaults it set.
func inputSourceLine(src inputSource, dryRun string, forge []harness.ProviderDef, durableDests []string) string {
	forgeNames := make([]string, len(forge))
	for i, d := range forge {
		forgeNames[i] = d.Name
	}
	return fmt.Sprintf("input_source: %s (%s); dry run default: %s; forge write: %s; durable host files: %s",
		src.value, src.origin, dryRun, listOrNone(forgeNames), listOrNone(durableDests))
}

func listOrNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}

// applyInputSource resolves the run's input source, applies its defaults
// and returns the run output line (#9509). It runs on the host before the
// pre-script and before any gateway or sandbox change.
func applyInputSource(h *harness.Harness, absFullsendDir string, resolved []resolve.ResolvedProvider, lookup func(string) (string, bool)) (string, error) {
	src, err := resolveInputSource(h, lookup)
	if err != nil {
		return "", err
	}
	defs, err := runProviderDefs(absFullsendDir, h.Providers, resolved)
	if err != nil {
		return "", err
	}
	forge, write := forgeProviders(defs)
	if err := checkForgeWrite(h, src, write); err != nil {
		return "", err
	}
	dryRun := applyDryRunDefault(h, src, lookup)
	return inputSourceLine(src, dryRun, forge, durableHostFileDests(h)), nil
}
