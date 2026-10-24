package harness

import (
	"cmp"
	"fmt"
	"log"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/google/cel-go/common/types"
	"gopkg.in/yaml.v3"
)

// ForgeConfig holds platform-specific harness configuration.
// This is purely declarative YAML config — it selects which
// scripts, skills, host files, and env vars to use per platform. It is
// distinct from the forge.Client interface (internal/forge/),
// which is the runtime abstraction for forge API operations.
type ForgeConfig struct {
	PreScript      string            `yaml:"pre_script,omitempty"`
	PostScript     string            `yaml:"post_script,omitempty"`
	Policy         string            `yaml:"policy,omitempty"`
	Skills         []SkillEntry      `yaml:"skills,omitempty"` // SkillEntry (not string) to support file-level overrides
	Providers      []string          `yaml:"providers,omitempty"`
	OpenShell      *OpenShellConfig  `yaml:"openshell,omitempty"`
	HostFiles      []HostFile        `yaml:"host_files,omitempty"`
	ValidationLoop *ValidationLoop   `yaml:"validation_loop,omitempty"`
	RunnerEnv      map[string]string `yaml:"runner_env,omitempty"`
	Env            *EnvConfig        `yaml:"env,omitempty"`
}

// OverlayFields holds the harness fields an overlay may set beyond ForgeConfig
// (ADR 0112 Decision 1). OverlayFields, ForgeConfig and excludedOverlayKeys
// together cover every Harness YAML key; TestOverlayFields_EveryHarnessFieldHasOneHome
// fails when a new Harness field is placed in none of them.
type OverlayFields struct {
	Agent                  string            `yaml:"agent,omitempty"`
	Doc                    string            `yaml:"doc,omitempty"`
	Description            string            `yaml:"description,omitempty"`
	Image                  string            `yaml:"image,omitempty"`
	Plugins                []PluginSpec      `yaml:"plugins,omitempty"`
	APIServers             []APIServer       `yaml:"api_servers,omitempty"`
	Model                  string            `yaml:"model,omitempty"`
	Effort                 string            `yaml:"effort,omitempty"`
	PrivilegeLevels        map[string]string `yaml:"privilege_levels,omitempty"`
	AgentInput             string            `yaml:"agent_input,omitempty"`
	TimeoutMinutes         int               `yaml:"timeout_minutes,omitempty"`
	SandboxTimeoutSeconds  int               `yaml:"sandbox_timeout_seconds,omitempty"`
	Security               *SecurityConfig   `yaml:"security,omitempty"`
	ReadonlyRepo           bool              `yaml:"readonly_repo,omitempty"`
	AllowRuntimeFetch      bool              `yaml:"allow_runtime_fetch,omitempty"`
	MaxRuntimeFetches      *int              `yaml:"max_runtime_fetches,omitempty"`
	AllowedRemoteResources []string          `yaml:"allowed_remote_resources,omitempty"`
}

// OverlayEntry is a CEL-guarded conditional config block (ADR 0088, 0112).
// Each entry carries a CEL expression in When and override fields from both
// ForgeConfig and OverlayFields. All entries whose When evaluates to true
// are merged into the harness in order (later matches override earlier
// matches). The When expression is evaluated against the overlay CEL
// environment: event (normevent map), runtime.forge (platform string),
// and config (per-repo config map).
type OverlayEntry struct {
	When          string `yaml:"when"`
	ForgeConfig   `yaml:",inline"`
	OverlayFields `yaml:",inline"`
	excludedKeys  []string `yaml:"-"`
}

// excludedOverlayKeys are harness fields that may not appear in an overlay
// because they govern how a harness is found, composed, or dispatched.
var excludedOverlayKeys = map[string]bool{
	"base": true, "trigger": true, "overlays": true,
	"slug": true, "role": true, "forge": true,
}

// unguardedOverlayKeys are overlay-settable fields that do not require a
// trusted when: expression (ADR 0112 Decision 2).
var unguardedOverlayKeys = map[string]bool{
	"model": true, "effort": true, "description": true, "doc": true,
}

// UnmarshalYAML decodes an overlay entry and detects excluded keys that
// arrive through YAML merge keys, aliases, or tagged keys.
func (oe *OverlayEntry) UnmarshalYAML(node *yaml.Node) error {
	type plain OverlayEntry
	if err := node.Decode((*plain)(oe)); err != nil {
		return err
	}
	var raw map[string]yaml.Node
	if err := node.Decode(&raw); err != nil {
		return err
	}
	for key := range raw {
		if excludedOverlayKeys[key] {
			oe.excludedKeys = append(oe.excludedKeys, key)
		}
	}
	// Map order is random; the error message must not depend on it.
	sort.Strings(oe.excludedKeys)
	return nil
}

// guardedFields returns the names of the guarded fields this overlay entry
// sets: every ForgeConfig and OverlayFields field with a non-zero value
// except the unguarded ones (ADR 0112 Decision 2). A field added to either
// struct is guarded unless it is listed in unguardedOverlayKeys.
func (oe *OverlayEntry) guardedFields() []string {
	var out []string
	for _, rv := range []reflect.Value{reflect.ValueOf(oe.ForgeConfig), reflect.ValueOf(oe.OverlayFields)} {
		rt := rv.Type()
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name := strings.Split(f.Tag.Get("yaml"), ",")[0]
			if name == "-" || unguardedOverlayKeys[name] {
				continue
			}
			if !rv.Field(i).IsZero() {
				out = append(out, name)
			}
		}
	}
	return out
}

var validForgeKeys = map[string]bool{
	"github": true,
	"gitlab": true,
	// "jira" is a temporary addition to unblock the triage-agent PoC for
	// JIRA issues. The code agent remains blocked on a longer-term forge
	// abstraction design (see https://github.com/fullsend-ai/fullsend/pull/6237).
	"jira": true,
}

// ValidForgePlatform reports whether platform is a recognized forge key.
func ValidForgePlatform(platform string) bool {
	return validForgeKeys[platform]
}

// ForgeKeyList returns a comma-separated list of valid forge platform keys.
func ForgeKeyList() string {
	keys := make([]string, 0, len(validForgeKeys))
	for k := range validForgeKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// validateForge checks that the forge section contains only recognized keys
// and that each ForgeConfig uses valid field values.
func (h *Harness) validateForge() error {
	for key, fc := range h.Forge {
		if !validForgeKeys[key] {
			return fmt.Errorf("forge: unrecognized key %q (valid: %s)", key, ForgeKeyList())
		}
		if fc == nil {
			continue
		}
		if fc.Policy != "" && IsURL(fc.Policy) {
			if _, _, hasHash := ParseIntegrityHash(fc.Policy); !hasHash {
				return fmt.Errorf("forge.%s.policy URL must include #sha256=... integrity hash", key)
			}
		}
		if fc.PreScript != "" && IsURL(fc.PreScript) {
			return fmt.Errorf("forge.%s.pre_script must be a local path, not a URL", key)
		}
		if fc.PostScript != "" && IsURL(fc.PostScript) {
			return fmt.Errorf("forge.%s.post_script must be a local path, not a URL", key)
		}
		for i, s := range fc.Skills {
			if IsURL(s.Source) {
				if _, _, hasHash := ParseIntegrityHash(s.Source); !hasHash {
					return fmt.Errorf("forge.%s.skills[%d] URL must include #sha256=... integrity hash", key, i)
				}
			}
		}
		if err := ValidateSkillOverrides(fc.Skills); err != nil {
			return fmt.Errorf("forge.%s: %w", key, err)
		}
		for i, p := range fc.Providers {
			if IsURL(p) {
				if _, _, hasHash := ParseIntegrityHash(p); !hasHash {
					return fmt.Errorf("forge.%s.providers[%d] URL must include #sha256=... integrity hash", key, i)
				}
			}
		}
		if fc.OpenShell != nil {
			for i, p := range fc.OpenShell.Profiles {
				if IsURL(p) {
					if _, _, hasHash := ParseIntegrityHash(p); !hasHash {
						return fmt.Errorf("forge.%s.openshell.profiles[%d] URL must include #sha256=... integrity hash", key, i)
					}
				}
			}
		}
		for i, hf := range fc.HostFiles {
			if hf.Src == "" {
				return fmt.Errorf("forge.%s.host_files[%d]: src is required", key, i)
			}
			if hf.Dest == "" {
				return fmt.Errorf("forge.%s.host_files[%d]: dest is required", key, i)
			}
			if err := ValidateHostFileDest(hf.Dest); err != nil {
				return fmt.Errorf("forge.%s.host_files[%d]: %w", key, i, err)
			}
			if IsURL(hf.Src) {
				return fmt.Errorf("forge.%s.host_files[%d].src must be a local path, not a URL", key, i)
			}
		}
		// Field-level merge inherits script from the top-level loop.
		topHasScript := h.ValidationLoop != nil && h.ValidationLoop.Script != ""
		if err := validateValidationLoop(fmt.Sprintf("forge.%s", key), fc.ValidationLoop, topHasScript); err != nil {
			return err
		}
	}
	return nil
}

// validateOverlayForgeConfig validates a ForgeConfig embedded in an overlay
// entry, applying the same checks as validateForge per entry. OverlayEntry
// embeds ForgeConfig via yaml:",inline" (see OverlayEntry), so overlay
// entries carry the same override fields as forge platform blocks.
//
// Naming: The function references "ForgeConfig" rather than "OverlayEntry"
// because it validates the ForgeConfig fields embedded in each overlay entry.
// The ForgeConfig type name is a legacy artifact from the original forge
// feature (ADR 0088 deprecated forge in favor of overlays), but the name
// remains accurate: this function validates ForgeConfig fields.
func validateOverlayForgeConfig(idx int, fc *ForgeConfig) error {
	return validateOverlayForgeConfigInherit(idx, fc, false)
}

// validateOverlayForgeConfigInherit validates a ForgeConfig embedded in an
// overlay entry, applying the same checks as validateForge per entry. When
// inheritScript is true, an empty validation_loop.script is allowed because
// field-level merge will inherit it from the top-level validation_loop (see
// validateValidationLoop).
func validateOverlayForgeConfigInherit(idx int, fc *ForgeConfig, inheritScript bool) error {
	prefix := fmt.Sprintf("overlays[%d]", idx)
	if fc.Policy != "" && IsURL(fc.Policy) {
		if _, _, hasHash := ParseIntegrityHash(fc.Policy); !hasHash {
			return fmt.Errorf("%s.policy URL must include #sha256=... integrity hash", prefix)
		}
	}
	if fc.PreScript != "" && IsURL(fc.PreScript) {
		return fmt.Errorf("%s.pre_script must be a local path, not a URL", prefix)
	}
	if fc.PostScript != "" && IsURL(fc.PostScript) {
		return fmt.Errorf("%s.post_script must be a local path, not a URL", prefix)
	}
	for i, s := range fc.Skills {
		if IsURL(s.Source) {
			if _, _, hasHash := ParseIntegrityHash(s.Source); !hasHash {
				return fmt.Errorf("%s.skills[%d] URL must include #sha256=... integrity hash", prefix, i)
			}
		}
	}
	if err := ValidateSkillOverrides(fc.Skills); err != nil {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	for i, p := range fc.Providers {
		if IsURL(p) {
			if _, _, hasHash := ParseIntegrityHash(p); !hasHash {
				return fmt.Errorf("%s.providers[%d] URL must include #sha256=... integrity hash", prefix, i)
			}
		}
	}
	if fc.OpenShell != nil {
		for i, p := range fc.OpenShell.Profiles {
			if IsURL(p) {
				if _, _, hasHash := ParseIntegrityHash(p); !hasHash {
					return fmt.Errorf("%s.openshell.profiles[%d] URL must include #sha256=... integrity hash", prefix, i)
				}
			}
		}
	}
	for i, hf := range fc.HostFiles {
		if hf.Src == "" {
			return fmt.Errorf("%s.host_files[%d]: src is required", prefix, i)
		}
		if hf.Dest == "" {
			return fmt.Errorf("%s.host_files[%d]: dest is required", prefix, i)
		}
		if err := ValidateHostFileDest(hf.Dest); err != nil {
			return fmt.Errorf("%s.host_files[%d]: %w", prefix, i, err)
		}
		if IsURL(hf.Src) {
			return fmt.Errorf("%s.host_files[%d].src must be a local path, not a URL", prefix, i)
		}
	}
	return validateValidationLoop(prefix, fc.ValidationLoop, inheritScript)
}

// validateValidationLoop applies validation_loop field checks shared by both
// forge platform blocks (validateForge) and overlay entries
// (validateOverlayForgeConfigInherit): script is required unless
// inheritScript allows an empty script to inherit from the top-level
// validation_loop, script must not be a URL, and schema must not be a URL.
// A nil vl is valid (no validation_loop set) and returns nil.
func validateValidationLoop(prefix string, vl *ValidationLoop, inheritScript bool) error {
	if vl == nil {
		return nil
	}
	if vl.Script == "" {
		if !inheritScript {
			return fmt.Errorf("%s.validation_loop.script is required when validation_loop is set", prefix)
		}
	} else if IsURL(vl.Script) {
		return fmt.Errorf("%s.validation_loop.script must be a local path, not a URL", prefix)
	}
	if vl.Schema != "" && IsURL(vl.Schema) {
		return fmt.Errorf("%s.validation_loop.schema must be a local path, not a URL", prefix)
	}
	return nil
}

// validateOverlays checks that the overlays section is well-formed:
// mutual exclusion with forge, excluded-key detection, trusted-field
// guard for guarded fields, CEL expression compilation, and ForgeConfig
// field validation per entry. It knows no org allowlist, so any overlay
// allowed_remote_resources entry is refused; Validate and LoadWithOpts call
// it, and the LoadWithBase sites call validateOverlaysWith with the
// config-level list.
func (h *Harness) validateOverlays() error {
	return h.validateOverlaysWith(nil)
}

// validateOverlaysWith is validateOverlays given the config-level
// allowed_remote_resources, which bounds every prefix an overlay adds to
// allowed_remote_resources: the fetch service authorizes runtime fetches
// against the merged list, so an overlay may widen it only within what the
// config allows. Within that bound an overlay's list never authorizes a URL
// the config-level list does not.
func (h *Harness) validateOverlaysWith(orgAllowlist []string) error {
	if len(h.Overlays) == 0 {
		return nil
	}
	if h.Forge != nil {
		// This error includes remediation advice ("migrate forge entries to
		// overlays") unlike other validation errors because it's a migration
		// error guiding users from the deprecated forge feature to overlays
		// (ADR 0088). The remediation is brief, actionable, and appropriate
		// for a one-time migration scenario.
		return fmt.Errorf("forge and overlays cannot coexist in the same harness; migrate forge entries to overlays")
	}
	// A probe without any overlay tells whether the top-level values already
	// fail the overlay-settable checks; then the per-entry probe below would
	// only misattribute that to an overlay, so no entry is probed. Validate
	// reports the top-level value, unless a matching overlay replaces it.
	// Known limitation: in that case a non-matching entry's values go
	// unchecked on this load; they are checked on any load where the top
	// level is valid.
	topLevelOK := h.overlayProbe(nil).validateOverlaySettable() == nil
	for i, entry := range h.Overlays {
		if len(entry.excludedKeys) > 0 {
			return fmt.Errorf("overlays[%d]: %s cannot be set in an overlay (base, trigger, overlays, slug, role and forge decide how a harness is found, composed or dispatched)", i, quotedKeys(entry.excludedKeys))
		}

		when := strings.TrimSpace(entry.When)
		if when == "" {
			return fmt.Errorf("overlays[%d].when is required", i)
		}
		env, err := NewOverlayEnv()
		if err != nil {
			return fmt.Errorf("overlays[%d]: creating CEL env: %w", i, err)
		}
		ast, issues := env.Compile(when)
		if issues != nil && issues.Err() != nil {
			return fmt.Errorf("overlays[%d].when: %w", i, issues.Err())
		}
		if !ast.OutputType().IsExactType(types.BoolType) {
			return fmt.Errorf("overlays[%d].when must evaluate to bool, got %v", i, ast.OutputType())
		}

		if guarded := entry.guardedFields(); len(guarded) > 0 {
			if ref, ok := guardedOverlayWhenTrusted(ast); !ok {
				return fmt.Errorf("overlays[%d].when must read only trusted facts (runtime.forge, config and the trusted event fields) because the entry sets guarded %s; %q is not trusted", i, quotedKeys(guarded), ref)
			}
		}

		if len(entry.AllowedRemoteResources) > 0 {
			grant := &Harness{AllowedRemoteResources: entry.AllowedRemoteResources}
			if err := grant.ValidateAllowedRemoteResources(orgAllowlist); err != nil {
				return fmt.Errorf("overlays[%d]: %w", i, err)
			}
		}
		if topLevelOK {
			if err := h.overlayProbe(&entry.OverlayFields).validateOverlaySettable(); err != nil {
				return fmt.Errorf("overlays[%d]: merged with the top-level values: %w", i, err)
			}
		}

		fc := entry.ForgeConfig
		inheritScript := h.ValidationLoop != nil && h.ValidationLoop.Script != ""
		if err := validateOverlayForgeConfigInherit(i, &fc, inheritScript); err != nil {
			return err
		}
	}
	return nil
}

// overlayProbe returns a copy of h with, when of is non-nil, that overlay's
// OverlayFields merged in, so the merged values can be validated without
// touching h. The checks it feeds read only top-level fields.
func (h *Harness) overlayProbe(of *OverlayFields) *Harness {
	probe := *h
	if of != nil {
		// mergeOverlayFields writes into the map; the lists it merges are new.
		probe.PrivilegeLevels = maps.Clone(h.PrivilegeLevels)
		mergeOverlayFields(&probe, of)
	}
	return &probe
}

// ResolveOverlays evaluates each overlay's When expression against the
// overlay CEL environment (event, runtime.forge, config) and merges all
// matching entries into the harness in order (later matches take
// precedence over earlier matches). After resolution, h.Overlays is set
// to nil (consumed). When h.Overlays is empty, this is a no-op. When
// event is nil (CLI run/lock flows and dispatch enumeration without an
// event context), CEL sees `event` as null: overlays conditioned only on
// runtime.forge or config still match, `event != null` does not, has()
// on an event field is false, an overlay that selects an event field or
// uses event as a map does not match, and a comparison with a map changes
// result (`event != {}` is true).
//
// An evaluation error is classified by context (ADR 0112 Decision 2):
// with no event every error is a non-match; with an event a missing key
// ("no such key") is a non-match; any other error fails the run when the
// entry sets a guarded field and is logged as a non-match otherwise. The
// log-and-continue branches match the MatchHarnesses pattern in
// harnessdispatch/enumerate.go, so a more-specific overlay that cannot
// evaluate does not stop a broader fallback overlay from matching.
func (h *Harness) ResolveOverlays(event map[string]any, forgePlatform string, config map[string]any) error {
	if len(h.Overlays) == 0 {
		h.Overlays = nil
		return nil
	}
	hasEvent := event != nil
	for i, entry := range h.Overlays {
		matched, err := EvaluateOverlay(entry.When, event, forgePlatform, config)
		if err != nil {
			if !hasEvent {
				log.Printf("harness: overlay[%d].when eval failed (no event, non-match): %v", i, err)
				continue
			}
			if overlayMissingKey(err) {
				log.Printf("harness: overlay[%d].when eval failed (missing key, non-match): %v", i, err)
				continue
			}
			guarded := entry.guardedFields()
			if len(guarded) > 0 {
				return fmt.Errorf("overlays[%d].when: %w", i, err)
			}
			log.Printf("harness: overlay[%d].when eval failed (unguarded, non-match): %v", i, err)
			continue
		}
		if matched {
			fc := entry.ForgeConfig
			mergeForgeConfig(h, &fc)
			of := entry.OverlayFields
			mergeOverlayFields(h, &of)
		}
	}
	h.Overlays = nil
	return nil
}

// mergeOverlayFields applies a matching overlay's OverlayFields onto the
// harness with mergeBaseIntoChild's rules, the overlay standing in the child
// position: a non-zero scalar replaces, lists concatenate, maps merge with
// the overlay's keys winning, a non-nil security block replaces the whole
// top-level block, and nothing is ever cleared. Unlike base composition it
// does apply allowed_remote_resources, allow_runtime_fetch and
// max_runtime_fetches: a child must not inherit a base's fetch grants, but an
// overlay is the same harness author's and, as guarded fields, these follow
// trusted facts only. It also applies readonly_repo, which base composition
// drops (#7360). The guard itself is ADR 0112 Decision 2.
func mergeOverlayFields(h *Harness, of *OverlayFields) {
	h.Agent = cmp.Or(of.Agent, h.Agent)
	h.Doc = cmp.Or(of.Doc, h.Doc)
	h.Description = cmp.Or(of.Description, h.Description)
	h.Image = cmp.Or(of.Image, h.Image)
	h.Model = cmp.Or(of.Model, h.Model)
	h.Effort = cmp.Or(of.Effort, h.Effort)
	h.AgentInput = cmp.Or(of.AgentInput, h.AgentInput)
	h.TimeoutMinutes = cmp.Or(of.TimeoutMinutes, h.TimeoutMinutes)
	h.SandboxTimeoutSeconds = cmp.Or(of.SandboxTimeoutSeconds, h.SandboxTimeoutSeconds)
	h.ReadonlyRepo = h.ReadonlyRepo || of.ReadonlyRepo
	h.AllowRuntimeFetch = h.AllowRuntimeFetch || of.AllowRuntimeFetch
	if of.MaxRuntimeFetches != nil {
		h.MaxRuntimeFetches = of.MaxRuntimeFetches
	}
	if of.Plugins != nil {
		h.Plugins = slices.Concat(h.Plugins, of.Plugins)
	}
	if of.APIServers != nil {
		h.APIServers = slices.Concat(h.APIServers, of.APIServers)
	}
	if of.AllowedRemoteResources != nil {
		h.AllowedRemoteResources = slices.Concat(h.AllowedRemoteResources, of.AllowedRemoteResources)
	}
	if of.PrivilegeLevels != nil {
		if h.PrivilegeLevels == nil {
			h.PrivilegeLevels = make(map[string]string, len(of.PrivilegeLevels))
		}
		maps.Copy(h.PrivilegeLevels, of.PrivilegeLevels)
	}
	if of.Security != nil {
		h.Security = of.Security
	}
}

// quotedKeys renders sorted keys as `key "a"` or `keys "a", "b"`.
func quotedKeys(keys []string) string {
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = fmt.Sprintf("%q", k)
	}
	if len(keys) == 1 {
		return "key " + quoted[0]
	}
	return "keys " + strings.Join(quoted, ", ")
}

// ResolveForge merges forge-specific overrides into the harness in place.
// After merging, h.Forge is set to nil (consumed). If platform is empty or
// h.Forge is nil, this is a no-op. If platform is not present in h.Forge,
// an error is returned.
//
// Pipeline ordering: LoadWithOpts calls validateForge → ResolveForge →
// Validate. validateForge must run first because ResolveForge consumes
// h.Forge (sets it to nil). After ResolveForge, Validate's validateForge
// call sees nil and is a no-op, which is correct because the forge map
// was already validated before merging.
func (h *Harness) ResolveForge(platform string) error {
	if platform == "" || h.Forge == nil {
		return nil
	}
	if !validForgeKeys[platform] {
		return fmt.Errorf("forge platform %q is not valid (valid: %s)", platform, ForgeKeyList())
	}
	fc, ok := h.Forge[platform]
	if !ok {
		return fmt.Errorf("forge platform %q not configured (available: %s)", platform, forgeKeyList(h.Forge))
	}
	if fc != nil {
		mergeForgeConfig(h, fc)
	}
	h.Forge = nil
	return nil
}

// mergeForgeConfig applies forge-specific overrides to the harness.
//
// Merge rules per ADR-0045:
//   - Scalars: forge overrides if non-empty
//   - Skills: top-level + forge (merged by skill identity; forge overrides matching top-level)
//   - Providers: top-level + forge (concatenated)
//   - OpenShell.Profiles: top-level + forge (concatenated)
//   - HostFiles: top-level + forge (concatenated with last-writer-wins dedup by Dest)
//   - RunnerEnv: top-level merged with forge; forge keys win
//   - ValidationLoop: field-level merge; forge non-zero fields win
func mergeForgeConfig(h *Harness, fc *ForgeConfig) {
	if fc.PreScript != "" {
		h.PreScript = fc.PreScript
	}
	if fc.PostScript != "" {
		h.PostScript = fc.PostScript
	}
	if fc.Policy != "" {
		h.Policy = fc.Policy
	}

	if fc.Skills != nil {
		h.Skills = mergeSkills(h.Skills, fc.Skills)
	}

	if fc.Providers != nil {
		merged := make([]string, 0, len(h.Providers)+len(fc.Providers))
		merged = append(merged, h.Providers...)
		merged = append(merged, fc.Providers...)
		h.Providers = merged
	}

	if fc.OpenShell != nil && len(fc.OpenShell.Profiles) > 0 {
		if h.OpenShell == nil {
			h.OpenShell = &OpenShellConfig{}
		}
		merged := make([]string, 0, len(h.OpenShell.Profiles)+len(fc.OpenShell.Profiles))
		merged = append(merged, h.OpenShell.Profiles...)
		merged = append(merged, fc.OpenShell.Profiles...)
		h.OpenShell.Profiles = merged
	}

	if fc.HostFiles != nil {
		h.HostFiles = mergeHostFiles(h.HostFiles, fc.HostFiles)
	}

	if fc.RunnerEnv != nil {
		if h.RunnerEnv == nil {
			h.RunnerEnv = make(map[string]string, len(fc.RunnerEnv))
		}
		for k, v := range fc.RunnerEnv {
			h.RunnerEnv[k] = v
		}
	}

	if fc.ValidationLoop != nil {
		h.ValidationLoop = mergeValidationLoop(h.ValidationLoop, fc.ValidationLoop)
	}

	// Env: merge sub-maps independently; forge keys win (ADR 0055)
	if fc.Env != nil {
		if h.Env == nil {
			h.Env = &EnvConfig{}
		}
		h.Env.mergeEnvFrom(fc.Env, true)
	}
}

func forgeKeyList(m map[string]*ForgeConfig) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}

// BuildConfigMap extracts user-facing per-repo config fields for overlay
// CEL evaluation (ADR 0088). The returned map is exposed to overlay when
// expressions as the "config" variable. Returns nil when cfg is nil or
// does not implement PerRepoConfigReader.
//
// All safe per-repo config fields are exposed. Sensitive fields (mint_url,
// inference provider details) are excluded. Per PR #6285 review feedback,
// the 4-key whitelist was expanded to expose the full non-sensitive config.
//
// Both the CLI (run, lock) and harnessdispatch (enumerate) call sites use
// this single implementation to ensure overlay CEL resolution sees the
// same config shape regardless of the call path.
func BuildConfigMap(cfg config.ConfigReader) map[string]any {
	if cfg == nil {
		return nil
	}
	pr, ok := cfg.(config.PerRepoConfigReader)
	if !ok {
		return nil
	}
	m := map[string]any{}

	// Core platform fields
	if v := pr.ConfigForge(); v != "" {
		m["forge"] = v
	}
	if v := pr.ConfigTracker(); v != "" {
		m["tracker"] = v
	}
	if v := pr.ConfigRuntime(); v != "" {
		m["runtime"] = v
	}
	if roles := pr.ConfigRoles(); len(roles) > 0 {
		// Convert to []any for CEL evaluation compatibility.
		anyRoles := make([]any, len(roles))
		for i, r := range roles {
			anyRoles[i] = r
		}
		m["roles"] = anyRoles
	}

	// Operational fields
	if v := pr.ConfigVersion(); v != "" {
		m["version"] = v
	}
	if pr.IsKillSwitchActive() {
		m["kill_switch"] = true
	}

	// Agent entries (convert to CEL-compatible map slice)
	if agents := pr.AgentEntries(); len(agents) > 0 {
		anyAgents := make([]any, len(agents))
		for i, a := range agents {
			agentMap := map[string]any{}
			if a.Source != "" {
				agentMap["source"] = a.Source
			}
			if a.Name != "" {
				agentMap["name"] = a.Name
			}
			if a.Enabled != nil {
				agentMap["enabled"] = *a.Enabled
			}
			if a.Runtime != "" {
				agentMap["runtime"] = a.Runtime
			}
			if a.Model != "" {
				agentMap["model"] = a.Model
			}
			if a.Effort != "" {
				agentMap["effort"] = a.Effort
			}
			anyAgents[i] = agentMap
		}
		m["agents"] = anyAgents
	}

	// Security policies
	if arr := pr.AllowedResources(); len(arr) > 0 {
		anyArr := make([]any, len(arr))
		for i, r := range arr {
			anyArr[i] = r
		}
		m["allowed_remote_resources"] = anyArr
	}

	// Issue creation config
	if ci := pr.IssueCreationConfig(); ci != nil {
		ciMap := map[string]any{}
		if len(ci.AllowTargets.Orgs) > 0 {
			anyOrgs := make([]any, len(ci.AllowTargets.Orgs))
			for i, o := range ci.AllowTargets.Orgs {
				anyOrgs[i] = o
			}
			ciMap["allow_orgs"] = anyOrgs
		}
		if len(ci.AllowTargets.Repos) > 0 {
			anyRepos := make([]any, len(ci.AllowTargets.Repos))
			for i, r := range ci.AllowTargets.Repos {
				anyRepos[i] = r
			}
			ciMap["allow_repos"] = anyRepos
		}
		if len(ciMap) > 0 {
			m["create_issues"] = ciMap
		}
	}

	// Status notifications config
	if sn := pr.StatusNotifications(); sn != nil {
		snMap := map[string]any{}
		if sn.Comment.Start != "" {
			snMap["start"] = sn.Comment.Start
		}
		if sn.Comment.Completion != "" {
			snMap["completion"] = sn.Comment.Completion
		}
		if len(snMap) > 0 {
			m["status_notifications"] = snMap
		}
	}

	// Note: mint_url and inference.* fields are intentionally excluded
	// (contain credential URLs and GCP project identifiers that should not
	// be exposed to harness CEL expressions).

	return m
}
