package harness

import (
	"reflect"
)

// DiffResult holds the minimal child harness and any warnings produced
// during diffing.
type DiffResult struct {
	// Child is the minimal harness containing only fields that differ from
	// the base. Nil if base and child are identical and no customized files
	// require field overrides.
	Child *Harness

	// Warnings lists non-fatal issues (e.g., slice items removed from base
	// that cannot be expressed with base: composition).
	Warnings []string
}

// DiffHarness computes the minimal child harness that, when composed with
// the given base via mergeBaseIntoChild, reproduces the full child.
//
// customizedFiles is a set of relative paths (e.g., "agents/triage.md")
// that exist in the customized/ directory. When a file-referencing field
// in the child matches the base value but the referenced file has been
// customized, the field is kept in the diff so the local file overrides
// the base's URL-resolved version.
//
// Returns nil DiffResult.Child when base and child are identical and no
// file overrides are needed.
func DiffHarness(base, child *Harness, customizedFiles map[string]bool) *DiffResult {
	result := &DiffResult{Child: &Harness{}}
	hasAny := false

	// Scalar strings — keep if child differs from base, or if the
	// referenced file is customized.
	if diffScalarFile(&result.Child.Agent, base.Agent, child.Agent, customizedFiles) {
		hasAny = true
	}
	if diffScalarFile(&result.Child.Doc, base.Doc, child.Doc, customizedFiles) {
		hasAny = true
	}
	if child.Description != base.Description {
		result.Child.Description = child.Description
		hasAny = true
	}
	if child.Role != base.Role {
		result.Child.Role = child.Role
		hasAny = true
	}
	if child.Slug != base.Slug {
		result.Child.Slug = child.Slug
		hasAny = true
	}
	if child.Image != base.Image {
		result.Child.Image = child.Image
		hasAny = true
	}
	if diffScalarFile(&result.Child.Policy, base.Policy, child.Policy, customizedFiles) {
		hasAny = true
	}
	if child.Model != base.Model {
		result.Child.Model = child.Model
		hasAny = true
	}
	if diffScalarFile(&result.Child.PreScript, base.PreScript, child.PreScript, customizedFiles) {
		hasAny = true
	}
	if diffScalarFile(&result.Child.PostScript, base.PostScript, child.PostScript, customizedFiles) {
		hasAny = true
	}
	if diffScalarFile(&result.Child.AgentInput, base.AgentInput, child.AgentInput, customizedFiles) {
		hasAny = true
	}

	// Scalar ints
	if child.TimeoutMinutes != base.TimeoutMinutes {
		result.Child.TimeoutMinutes = child.TimeoutMinutes
		hasAny = true
	}
	if child.SandboxTimeoutSeconds != base.SandboxTimeoutSeconds {
		result.Child.SandboxTimeoutSeconds = child.SandboxTimeoutSeconds
		hasAny = true
	}

	// Security/fetch fields — mergeBaseIntoChild does NOT merge these from
	// base to child (prevents privilege escalation). The diff must always
	// include non-zero child values so they survive composition.
	if child.AllowRuntimeFetch {
		result.Child.AllowRuntimeFetch = true
		hasAny = true
	}
	if child.MaxRuntimeFetches != nil {
		result.Child.MaxRuntimeFetches = child.MaxRuntimeFetches
		hasAny = true
	}
	if len(child.AllowedRemoteResources) > 0 {
		result.Child.AllowedRemoteResources = child.AllowedRemoteResources
		hasAny = true
	}

	// String slices (concatenated by mergeBaseIntoChild) — keep only extras.
	// Pass nil for customizedFiles: file-path override semantics only apply
	// to scalar fields, not concatenated slices (would cause duplication).
	if extras, removed := diffStringSlice(base.Skills, child.Skills, nil); len(extras) > 0 || removed {
		if removed {
			result.Warnings = append(result.Warnings, "skills: child removes items from base; cannot express with base: composition")
			result.Child = nil
			return result
		}
		result.Child.Skills = extras
		hasAny = true
	}
	if extras, removed := diffStringSlice(base.Plugins, child.Plugins, nil); len(extras) > 0 || removed {
		if removed {
			result.Warnings = append(result.Warnings, "plugins: child removes items from base; cannot express with base: composition")
			result.Child = nil
			return result
		}
		result.Child.Plugins = extras
		hasAny = true
	}
	if extras, removed := diffStringSlice(base.Providers, child.Providers, nil); len(extras) > 0 || removed {
		if removed {
			result.Warnings = append(result.Warnings, "providers: child removes items from base; cannot express with base: composition")
			result.Child = nil
			return result
		}
		result.Child.Providers = extras
		hasAny = true
	}

	// HostFiles — keep entries in child not in base (by dest)
	if extras, hfRemoved := diffHostFiles(base.HostFiles, child.HostFiles); len(extras) > 0 || hfRemoved {
		if hfRemoved {
			result.Warnings = append(result.Warnings, "host_files: child removes items from base; cannot express with base: composition")
			result.Child = nil
			return result
		}
		result.Child.HostFiles = extras
		hasAny = true
	}

	// APIServers — keep entries in child not in base (by name)
	if extras, asRemoved := diffAPIServers(base.APIServers, child.APIServers); len(extras) > 0 || asRemoved {
		if asRemoved {
			result.Warnings = append(result.Warnings, "api_servers: child removes items from base; cannot express with base: composition")
			result.Child = nil
			return result
		}
		result.Child.APIServers = extras
		hasAny = true
	}

	// Maps — keep keys where child value differs from base
	if diff, mapRemoved := diffStringMap(base.RunnerEnv, child.RunnerEnv); len(diff) > 0 || mapRemoved {
		if mapRemoved {
			result.Warnings = append(result.Warnings, "runner_env: child removes keys from base; cannot express with base: composition")
			result.Child = nil
			return result
		}
		result.Child.RunnerEnv = diff
		hasAny = true
	}

	// Env — diff sub-maps independently
	if envDiff, envRemoved := diffEnvConfig(base.Env, child.Env); envDiff != nil || envRemoved {
		if envRemoved {
			result.Warnings = append(result.Warnings, "env: child removes keys from base; cannot express with base: composition")
			result.Child = nil
			return result
		}
		result.Child.Env = envDiff
		hasAny = true
	}

	// Pointer structs — keep if non-nil and different; abort if child removes.
	if child.ValidationLoop != nil && !reflect.DeepEqual(child.ValidationLoop, base.ValidationLoop) {
		result.Child.ValidationLoop = child.ValidationLoop
		hasAny = true
	} else if child.ValidationLoop == nil && base.ValidationLoop != nil {
		result.Warnings = append(result.Warnings, "validation_loop: child removes block from base; cannot express with base: composition")
		result.Child = nil
		return result
	}
	if child.Security != nil && !reflect.DeepEqual(child.Security, base.Security) {
		result.Child.Security = child.Security
		hasAny = true
	} else if child.Security == nil && base.Security != nil {
		result.Warnings = append(result.Warnings, "security: child removes block from base; cannot express with base: composition")
		result.Child = nil
		return result
	}

	// Forge — diff per platform
	if forgeDiff, forgeWarnings := diffForge(base.Forge, child.Forge); len(forgeDiff) > 0 || len(forgeWarnings) > 0 {
		if len(forgeWarnings) > 0 {
			result.Warnings = append(result.Warnings, forgeWarnings...)
			result.Child = nil
			return result
		}
		result.Child.Forge = forgeDiff
		hasAny = true
	}

	if !hasAny {
		result.Child = nil
	}
	return result
}

// diffScalarFile sets *dst to childVal if it differs from baseVal, or if
// childVal is a path that appears in customizedFiles. Returns true if a
// difference was recorded.
func diffScalarFile(dst *string, baseVal, childVal string, customizedFiles map[string]bool) bool {
	if childVal != baseVal {
		*dst = childVal
		return true
	}
	if childVal != "" && customizedFiles[childVal] {
		*dst = childVal
		return true
	}
	return false
}

// diffStringSlice returns items in child that are not in base (extras),
// and whether any base items are missing from child (removed).
// Items whose path appears in customizedFiles are always kept as extras.
func diffStringSlice(base, child []string, customizedFiles map[string]bool) (extras []string, removed bool) {
	baseSet := make(map[string]bool, len(base))
	for _, s := range base {
		baseSet[s] = true
	}

	childSet := make(map[string]bool, len(child))
	for _, s := range child {
		childSet[s] = true
	}

	for _, s := range base {
		if !childSet[s] {
			removed = true
			break
		}
	}

	for _, s := range child {
		if !baseSet[s] || customizedFiles[s] {
			extras = append(extras, s)
		}
	}
	return extras, removed
}

// diffHostFiles returns HostFile entries in child whose Dest is not in base,
// or whose fields differ from the base entry with the same Dest. It also
// reports whether any base entries were removed from child.
func diffHostFiles(base, child []HostFile) (extras []HostFile, removed bool) {
	baseByDest := make(map[string]HostFile, len(base))
	for _, hf := range base {
		baseByDest[hf.Dest] = hf
	}

	childByDest := make(map[string]bool, len(child))
	for _, hf := range child {
		childByDest[hf.Dest] = true
	}

	for _, hf := range base {
		if !childByDest[hf.Dest] {
			removed = true
			break
		}
	}

	for _, hf := range child {
		if bHF, exists := baseByDest[hf.Dest]; !exists || !reflect.DeepEqual(hf, bHF) {
			extras = append(extras, hf)
		}
	}
	return extras, removed
}

// diffAPIServers returns APIServer entries in child whose Name is not in
// base (truly new entries). It also reports whether any base entries were
// removed or modified in child. mergeBaseIntoChild concatenates APIServers
// without dedup, so returning modified-by-Name entries would produce
// duplicates after composition.
func diffAPIServers(base, child []APIServer) (extras []APIServer, removed bool) {
	baseByName := make(map[string]APIServer, len(base))
	for _, as := range base {
		baseByName[as.Name] = as
	}

	childByName := make(map[string]bool, len(child))
	for _, as := range child {
		childByName[as.Name] = true
	}

	for _, as := range base {
		if !childByName[as.Name] {
			removed = true
			break
		}
	}

	for _, as := range child {
		bAS, exists := baseByName[as.Name]
		if !exists {
			extras = append(extras, as)
		} else if !reflect.DeepEqual(as, bAS) {
			removed = true
		}
	}
	return extras, removed
}

// diffStringMap returns keys where child value differs from base value,
// or keys present in child but not in base. It also reports whether any
// base keys are missing from child (removed).
func diffStringMap(base, child map[string]string) (diff map[string]string, removed bool) {
	for k := range base {
		if _, ok := child[k]; !ok {
			removed = true
			break
		}
	}
	if len(child) == 0 {
		return nil, removed
	}
	diff = make(map[string]string)
	for k, cv := range child {
		if bv, ok := base[k]; !ok || cv != bv {
			diff[k] = cv
		}
	}
	if len(diff) == 0 {
		return nil, removed
	}
	return diff, removed
}

// diffEnvConfig returns an EnvConfig with only the runner/sandbox keys
// that differ between base and child, and whether any base keys were removed.
func diffEnvConfig(base, child *EnvConfig) (*EnvConfig, bool) {
	if child == nil {
		if base != nil && (len(base.Runner) > 0 || len(base.Sandbox) > 0) {
			return nil, true
		}
		return nil, false
	}
	var baseRunner, baseSandbox map[string]string
	if base != nil {
		baseRunner = base.Runner
		baseSandbox = base.Sandbox
	}

	runnerDiff, runnerRemoved := diffStringMap(baseRunner, child.Runner)
	sandboxDiff, sandboxRemoved := diffStringMap(baseSandbox, child.Sandbox)
	removed := runnerRemoved || sandboxRemoved

	if runnerDiff == nil && sandboxDiff == nil {
		return nil, removed
	}
	return &EnvConfig{Runner: runnerDiff, Sandbox: sandboxDiff}, removed
}

// diffForge returns forge platforms/fields that differ between base and child,
// along with any warnings about unrepresentable changes.
func diffForge(base, child map[string]*ForgeConfig) (map[string]*ForgeConfig, []string) {
	if len(child) == 0 && len(base) == 0 {
		return nil, nil
	}

	diff := make(map[string]*ForgeConfig)
	var warnings []string

	for platform := range base {
		childFC, ok := child[platform]
		if !ok || childFC == nil {
			warnings = append(warnings, "forge["+platform+"]: child removes platform from base; cannot express with base: composition")
		}
	}

	for platform, childFC := range child {
		if childFC == nil {
			continue
		}
		baseFC := base[platform]
		if baseFC == nil {
			diff[platform] = childFC
			continue
		}
		fc, w := diffForgeConfig(baseFC, childFC, platform)
		warnings = append(warnings, w...)
		if fc != nil {
			diff[platform] = fc
		}
	}
	if len(diff) == 0 {
		return nil, warnings
	}
	return diff, warnings
}

// diffForgeConfig returns a ForgeConfig with only fields that differ,
// along with any warnings about unrepresentable changes.
func diffForgeConfig(base, child *ForgeConfig, platform string) (*ForgeConfig, []string) {
	if child == nil {
		return nil, nil
	}
	fc := &ForgeConfig{}
	hasAny := false
	var warnings []string

	if child.PreScript != base.PreScript {
		fc.PreScript = child.PreScript
		hasAny = true
	}
	if child.PostScript != base.PostScript {
		fc.PostScript = child.PostScript
		hasAny = true
	}
	if extras, removed := diffStringSlice(base.Skills, child.Skills, nil); len(extras) > 0 || removed {
		if removed {
			return nil, []string{"forge[" + platform + "].skills: child removes items from base; cannot express with base: composition"}
		}
		fc.Skills = extras
		hasAny = true
	}
	if child.ValidationLoop != nil && !reflect.DeepEqual(child.ValidationLoop, base.ValidationLoop) {
		fc.ValidationLoop = child.ValidationLoop
		hasAny = true
	}
	if d, mapRemoved := diffStringMap(base.RunnerEnv, child.RunnerEnv); len(d) > 0 || mapRemoved {
		if mapRemoved {
			return nil, []string{"forge[" + platform + "].runner_env: child removes keys from base; cannot express with base: composition"}
		}
		fc.RunnerEnv = d
		hasAny = true
	}
	if d, envRemoved := diffEnvConfig(base.Env, child.Env); d != nil || envRemoved {
		if envRemoved {
			return nil, []string{"forge[" + platform + "].env: child removes keys from base; cannot express with base: composition"}
		}
		fc.Env = d
		hasAny = true
	}

	if !hasAny {
		return nil, warnings
	}
	return fc, warnings
}
