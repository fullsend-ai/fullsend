package harness

import (
	"fmt"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/maputil"
)

// Config and manifest validation run the same content check composition
// does, through config.AgentHarness.Validate.
func init() {
	config.SetAgentHarnessCheck(checkAgentBlock)
}

// reservedSandboxKey reports whether fullsend reserves an env name in the
// sandbox (ADR 0146). The runner registers its check through
// SetReservedSandboxKeyCheck; until then no name is reserved here.
var reservedSandboxKey = func(string) bool { return false }

// SetReservedSandboxKeyCheck registers the runner's check of reserved
// sandbox names. A harness: block may not set one in either env section
// (ADR 0162 Decision 5).
func SetReservedSandboxKeyCheck(f func(string) bool) {
	reservedSandboxKey = f
}

// declaredEnvNames are the env names a harness declares per section, in any
// layer, forge: block or overlay. They are recorded before forge: blocks and
// overlays are resolved, so a name only one forge or overlay declares counts
// for every command.
type declaredEnvNames struct {
	runner  map[string]bool
	sandbox map[string]bool
}

// recordDeclaredEnv adds the env names h declares at top level, in its
// forge: blocks and in its overlays. Only env.runner and env.sandbox
// declare a name; runner_env does not.
func (h *Harness) recordDeclaredEnv() {
	h.addDeclaredEnv(h.Env)
	for _, fc := range h.Forge {
		if fc != nil {
			h.addDeclaredEnv(fc.Env)
		}
	}
	for _, o := range h.Overlays {
		h.addDeclaredEnv(o.Env)
	}
}

func (h *Harness) addDeclaredEnv(env *EnvConfig) {
	if h.declaredEnv.runner == nil {
		h.declaredEnv.runner = make(map[string]bool)
		h.declaredEnv.sandbox = make(map[string]bool)
	}
	if env == nil {
		return
	}
	for k := range env.Runner {
		h.declaredEnv.runner[k] = true
	}
	for k := range env.Sandbox {
		h.declaredEnv.sandbox[k] = true
	}
}

// mergeDeclaredEnv adds the names base declares to h's.
func (h *Harness) mergeDeclaredEnv(base *Harness) {
	h.addDeclaredEnv(nil)
	for k := range base.declaredEnv.runner {
		h.declaredEnv.runner[k] = true
	}
	for k := range base.declaredEnv.sandbox {
		h.declaredEnv.sandbox[k] = true
	}
}

// composeAgentBlocks composes opts.Blocks on h, the agent's composed
// harness, whose forge: blocks and overlays are not resolved yet. As in a
// base: chain, h is the base of the first block and each block the base of
// the next. It returns the last block, merged, with its own overlays left
// for the caller to resolve.
func composeAgentBlocks(h *Harness, opts ComposeOpts) (*Harness, error) {
	if len(opts.Blocks) == 0 {
		return h, nil
	}
	// The runtime fetch fields a layer below the last block sets.
	fetchBelow := h.runtimeFetchFields()
	// With no forge selected nothing resolves h's forge: sections. A layer
	// that blocks merge onto cannot keep them, so they are set aside here.
	var unresolved map[string]*ForgeConfig
	if opts.ForgePlatform == "" {
		unresolved = h.Forge
	}
	if err := resolveLayerForgeAndOverlays(h, opts, false); err != nil {
		return nil, err
	}
	declared := h.declaredEnv
	base := h
	triggerIgnored := false
	for i, b := range opts.Blocks {
		block, err := parseAgentBlock(opts.Agent, b)
		if err != nil {
			return nil, err
		}
		// allow_runtime_fetch: false turns runtime fetch off on purpose, so
		// nothing below that block counts as dropped.
		if b.Sets("allow_runtime_fetch") && !block.AllowRuntimeFetch {
			fetchBelow = nil
		}
		// A harness.trigger on a built-in agent is not evaluated until
		// built-in agents are routed by CEL alone (ADR 0162 Decision 6,
		// #2902). Dropping it here gives every command the same harness;
		// Lint warns about it.
		if block.Trigger != "" && config.IsBuiltinAgentName(opts.Agent) {
			block.Trigger = ""
			triggerIgnored = true
		}
		if opts.WorkspaceRoot != "" {
			if err := block.resolveSkillSourcesWithin(opts.WorkspaceRoot); err != nil {
				return nil, fmt.Errorf("agents entry %q: harness: %w", opts.Agent, err)
			}
		}
		if baseVer, blockVer := base.EffectiveSchemaVersion(), block.EffectiveSchemaVersion(); !schemaVersionsCompose(baseVer, blockVer) {
			return nil, fmt.Errorf("agents entry %q: harness: block declares schema_version %d, which is incompatible with schema_version %d of the harness it builds on (ADR 0127); set the block's schema_version to match", opts.Agent, blockVer, baseVer)
		}
		if err := block.checkBlockEnv(opts.Agent, declared); err != nil {
			return nil, err
		}
		block.captureDeprecatedIssueURL()
		own := len(block.issueURLDiags)
		if err := mergeBaseIntoChild(base, block); err != nil {
			return nil, fmt.Errorf("agents entry %q: harness: %w", opts.Agent, err)
		}
		// mergeBaseIntoChild labels the base's GITHUB_ISSUE_URL diagnostics
		// "base."; the agent's harness is not a base: harness to its user,
		// so Lint labels its fields as it does without a block.
		for j := own; j < len(block.issueURLDiags); j++ {
			block.issueURLDiags[j].Field = strings.TrimPrefix(block.issueURLDiags[j].Field, "base.")
		}
		if i < len(opts.Blocks)-1 {
			if err := resolveLayerForgeAndOverlays(block, opts, false); err != nil {
				return nil, fmt.Errorf("agents entry %q: harness: %w", opts.Agent, err)
			}
			fetchBelow = append(fetchBelow, block.runtimeFetchFields()...)
		}
		base = block
	}
	// base: composition does not carry the runtime fetch fields to a child,
	// so a base cannot widen the child's fetch grants (ADR 0158). The block
	// follows the same rule; Lint names each field a layer below set and the
	// last block leaves out.
	last := opts.Blocks[len(opts.Blocks)-1]
	for _, f := range fetchBelow {
		if !last.Sets(f) {
			base.blockDroppedFetch = append(base.blockDroppedFetch, f)
		}
	}
	slices.Sort(base.blockDroppedFetch)
	base.blockDroppedFetch = slices.Compact(base.blockDroppedFetch)
	if triggerIgnored {
		base.blockTriggerIgnored = opts.Agent
	}
	base.unresolvedForge = unresolved
	return base, nil
}

// restoreUnresolvedForge puts the forge: sections composeAgentBlocks set
// aside back on the loaded harness, where a load without a block leaves them
// when no forge is selected. It runs after the block's own overlays are
// resolved, since forge and overlays cannot coexist in one harness.
func (h *Harness) restoreUnresolvedForge() {
	if h.unresolvedForge != nil {
		h.Forge, h.unresolvedForge = h.unresolvedForge, nil
	}
}

// RuntimeFetchLayer returns, without composing the harness, the layer whose
// runtime fetch fields the composed harness keeps: the last of blocks, or
// the harness file at path when the agent's entry has none. Composition
// carries those fields from no layer below (composeAgentBlocks).
func RuntimeFetchLayer(path, agent string, blocks []*config.AgentHarness) (*Harness, error) {
	if len(blocks) == 0 {
		return LoadRaw(path)
	}
	return parseAgentBlock(agent, blocks[len(blocks)-1])
}

// runtimeFetchFields names the runtime fetch fields h sets.
func (h *Harness) runtimeFetchFields() []string {
	var set []string
	if len(h.AllowedRemoteResources) > 0 {
		set = append(set, "allowed_remote_resources")
	}
	if h.AllowRuntimeFetch {
		set = append(set, "allow_runtime_fetch")
	}
	if h.MaxRuntimeFetches != nil {
		set = append(set, "max_runtime_fetches")
	}
	return set
}

// parseAgentBlock decodes an agents: entry's harness: block, failing on
// the fields a block may not set and on content decodeAgentBlock rejects
// (ADR 0162 Decision 2).
func parseAgentBlock(agent string, b *config.AgentHarness) (*Harness, error) {
	if err := b.CheckKeys(); err != nil {
		return nil, fmt.Errorf("agents entry %q: %w", agent, err)
	}
	h, err := decodeAgentBlock(b)
	if err != nil {
		return nil, fmt.Errorf("agents entry %q: %w", agent, err)
	}
	return h, nil
}

// checkAgentBlock is the content check config.AgentHarness.Validate runs.
func checkAgentBlock(b *config.AgentHarness) error {
	_, err := decodeAgentBlock(b)
	return err
}

// decodeAgentBlock decodes a block and checks what needs no harness to
// check: unknown keys, value types, schema_version, env names fullsend
// reserves in the sandbox (ADR 0146, either env section) and field values
// no harness accepts, such as an unknown effort. The trigger is left to
// composition, which ignores it on a built-in agent.
func decodeAgentBlock(b *config.AgentHarness) (*Harness, error) {
	var h Harness
	if err := b.DecodeStrict(&h); err != nil {
		return nil, fmt.Errorf("harness: %w", err)
	}
	if err := validateSchemaVersion(h.SchemaVersion); err != nil {
		return nil, fmt.Errorf("harness: %w", err)
	}
	err := h.blockEnvNames(func(field, section, name string) error {
		if reservedSandboxKey(name) {
			return fmt.Errorf("%s.%s.%s: fullsend reserves this name in the sandbox (ADR 0146)", field, section, name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := h.validate(false); err != nil {
		return nil, fmt.Errorf("harness: %w", err)
	}
	return &h, nil
}

// blockEnvNames calls f for each env name a block sets, at top level and
// in its overlays, in a stable order.
func (h *Harness) blockEnvNames(f func(field, section, name string) error) error {
	envs := []struct {
		field string
		env   *EnvConfig
	}{{"harness.env", h.Env}}
	for i, o := range h.Overlays {
		envs = append(envs, struct {
			field string
			env   *EnvConfig
		}{fmt.Sprintf("harness.overlays[%d].env", i), o.Env})
	}
	for _, e := range envs {
		if e.env == nil {
			continue
		}
		for _, s := range []struct {
			name   string
			values map[string]string
		}{{"runner", e.env.Runner}, {"sandbox", e.env.Sandbox}} {
			for _, k := range maputil.SortedKeys(s.values) {
				if err := f(e.field, s.name, k); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkBlockEnv checks the env names a block sets (ADR 0162 Decision 5):
// each must be declared in that section by some layer of the agent's
// harness, since the block changes harness variables and adding one still
// takes a base: harness.
func (h *Harness) checkBlockEnv(agent string, declared declaredEnvNames) error {
	return h.blockEnvNames(func(field, section, name string) error {
		names := declared.runner
		if section == "sandbox" {
			names = declared.sandbox
		}
		if !names[name] {
			return fmt.Errorf("agents entry %q: %s.%s.%s: the %q harness does not declare this variable in env.%s; a harness: block only changes declared variables (ADR 0162), so add it with a base: harness", agent, field, section, name, agent, section)
		}
		return nil
	})
}
