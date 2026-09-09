package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writePersonaFile(t *testing.T, skillDir, name, frontmatter string) {
	t.Helper()
	dir := filepath.Join(skillDir, "sub-agents")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".md"), []byte(frontmatter), 0o644))
}

func TestDiscoverPersonas_Basic(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "correctness", "---\nname: correctness\nmodel: opus\n---\nReview for correctness.\n")
	writePersonaFile(t, skill, "style", "---\nname: style\n---\nReview for style.\n")

	personas, _, err := discoverPersonas([]string{skill}, "code")
	require.NoError(t, err)
	require.Len(t, personas, 2)
	// Sorted by name.
	assert.Equal(t, "correctness", personas[0].Name)
	assert.Equal(t, "opus", personas[0].Model)
	assert.Equal(t, "style", personas[1].Name)
	assert.Empty(t, personas[1].Model)
}

func TestDiscoverPersonas_NoSubagentsDir(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	personas, _, err := discoverPersonas([]string{skill}, "code")
	require.NoError(t, err)
	assert.Empty(t, personas)
}

func TestDiscoverPersonas_ReservedName(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "default", "---\nname: default\n---\n")

	personas, skipped, err := discoverPersonas([]string{skill}, "code")
	require.NoError(t, err, "an invalid persona is skipped, never fatal on its own")
	assert.Empty(t, personas)
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0].Reason, "reserved")
}

func TestDiscoverPersonas_AgentNameCollision(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "code", "---\nname: code\n---\n")

	personas, skipped, err := discoverPersonas([]string{skill}, "code")
	require.NoError(t, err, "an invalid persona is skipped, never fatal on its own")
	assert.Empty(t, personas)
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0].Reason, "collides with the agent's own name")
}

func TestDiscoverPersonas_BuiltinAgentNameCollision(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "triage", "---\nname: triage\n---\n")

	personas, skipped, err := discoverPersonas([]string{skill}, "review")
	require.NoError(t, err, "an invalid persona is skipped, never fatal on its own")
	assert.Empty(t, personas)
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0].Reason, "built-in agent name")
}

func TestDiscoverPersonas_NameMismatch(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "style", "---\nname: other-name\n---\n")

	personas, skipped, err := discoverPersonas([]string{skill}, "code")
	require.NoError(t, err, "an invalid persona is skipped, never fatal on its own")
	assert.Empty(t, personas)
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0].Reason, "must equal file basename")
}

func TestDiscoverPersonas_MissingName(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "style", "---\ndescription: no name\n---\n")

	personas, skipped, err := discoverPersonas([]string{skill}, "code")
	require.NoError(t, err, "an invalid persona is skipped, never fatal on its own")
	assert.Empty(t, personas)
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0].Reason, "name: is required")
}

func TestDiscoverPersonas_DuplicateAcrossSkills(t *testing.T) {
	t.Parallel()
	skill1 := t.TempDir()
	skill2 := t.TempDir()
	writePersonaFile(t, skill1, "correctness", "---\nname: correctness\n---\n")
	writePersonaFile(t, skill2, "correctness", "---\nname: correctness\n---\n")

	personas, skipped, err := discoverPersonas([]string{skill1, skill2}, "code")
	require.NoError(t, err)
	require.Len(t, personas, 1, "the first declaration wins")
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0].Reason, "already declared")
}

func TestDiscoverPersonas_InvalidNameShape(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "Bad", "---\nname: Bad\n---\n")

	personas, skipped, err := discoverPersonas([]string{skill}, "code")
	require.NoError(t, err, "an invalid persona is skipped, never fatal on its own")
	assert.Empty(t, personas)
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0].Reason, "lowercase alphanumeric")
}

func TestResolvePersonaModels_Basic(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{
		{Name: "correctness", Model: "opus"},
		{Name: "style"},
	}
	trustedSpecs := map[string]string{
		"anthropic-vertex/claude-opus-4-6":   "anthropic-vertex/claude-opus-4-6",
		"anthropic-vertex/claude-sonnet-4-6": "anthropic-vertex/claude-sonnet-4-6",
	}

	result, defaultSpec, _, err := resolvePersonaModels(personas, nil, nil, testModels, trustedSpecs)
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", result["correctness"].Model)
	assert.Empty(t, result["style"].Model,
		"nothing configures this persona: the manifest carries no model so the extension inherits the parent's live one")
	assert.Empty(t, defaultSpec)
}

func TestResolvePersonaModels_ConfigOverride(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{
		{Name: "correctness", Model: "opus"},
	}
	cfg := map[string]*string{
		"correctness": strp("haiku"),
	}
	trustedSpecs := map[string]string{
		"anthropic-vertex/claude-haiku-4-5":  "anthropic-vertex/claude-haiku-4-5",
		"anthropic-vertex/claude-opus-4-6":   "anthropic-vertex/claude-opus-4-6",
		"anthropic-vertex/claude-sonnet-4-6": "anthropic-vertex/claude-sonnet-4-6",
	}

	result, _, _, err := resolvePersonaModels(personas, nil, cfg, testModels, trustedSpecs)
	require.NoError(t, err)
	assert.Equal(t, "anthropic-vertex/claude-haiku-4-5", result["correctness"].Model, "config overrides frontmatter")
}

func TestResolvePersonaModels_DefaultFallback(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{
		{Name: "style"},
	}
	cfg := map[string]*string{
		"default": strp("haiku"),
	}
	trustedSpecs := map[string]string{
		"anthropic-vertex/claude-haiku-4-5":  "anthropic-vertex/claude-haiku-4-5",
		"anthropic-vertex/claude-sonnet-4-6": "anthropic-vertex/claude-sonnet-4-6",
	}

	result, defaultSpec, _, err := resolvePersonaModels(personas, nil, cfg, testModels, trustedSpecs)
	require.NoError(t, err)
	assert.Equal(t, "anthropic-vertex/claude-haiku-4-5", result["style"].Model, "default fills in")
	assert.Equal(t, "anthropic-vertex/claude-haiku-4-5", defaultSpec,
		"the default is also returned for anonymous children")
}

// A tombstone means "no explicit entry for this persona": the persona
// resolves as if the key were absent, so its frontmatter model comes
// first and subagents.default only if it has none.
func TestResolvePersonaModels_Tombstone(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	trustedSpecs := map[string]string{
		"anthropic-vertex/claude-haiku-4-5": "anthropic-vertex/claude-haiku-4-5",
		"anthropic-vertex/claude-opus-4-6":  "anthropic-vertex/claude-opus-4-6",
	}
	cfg := map[string]*string{"default": strp("haiku"), "correctness": nil}

	withFrontmatter, _, _, err := resolvePersonaModels([]piPersona{{Name: "correctness", Model: "opus"}}, nil,
		cfg, testModels, trustedSpecs)
	require.NoError(t, err)
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", withFrontmatter["correctness"].Model,
		"tombstoned persona with a frontmatter model keeps it even when default is set")

	noFrontmatter, _, _, err := resolvePersonaModels([]piPersona{{Name: "correctness"}}, nil,
		cfg, testModels, trustedSpecs)
	require.NoError(t, err)
	assert.Equal(t, "anthropic-vertex/claude-haiku-4-5", noFrontmatter["correctness"].Model,
		"tombstoned persona with no frontmatter model falls to default")
}

// subagents.default is a floor, not an override: a persona whose
// frontmatter names a model keeps it.
func TestResolvePersonaModels_FrontmatterBeatsDefault(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{{Name: "correctness", Model: "opus"}, {Name: "style"}}
	trustedSpecs := map[string]string{
		"anthropic-vertex/claude-haiku-4-5": "anthropic-vertex/claude-haiku-4-5",
		"anthropic-vertex/claude-opus-4-6":  "anthropic-vertex/claude-opus-4-6",
	}
	result, defaultSpec, _, err := resolvePersonaModels(personas, nil, map[string]*string{"default": strp("haiku")}, testModels, trustedSpecs)
	require.NoError(t, err)
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", result["correctness"].Model, "frontmatter wins over default")
	assert.Equal(t, "anthropic-vertex/claude-haiku-4-5", result["style"].Model, "no frontmatter: default applies")
	assert.Equal(t, "anthropic-vertex/claude-haiku-4-5", defaultSpec, "and anonymous children get it too")
}

// subagents.default must resolve even when the harness ships no personas —
// that is retro's case, where every child is anonymous.
func TestResolvePersonaModels_DefaultWithoutPersonas(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	trustedSpecs := map[string]string{
		"anthropic-vertex/claude-sonnet-4-6": "anthropic-vertex/claude-sonnet-4-6",
	}

	result, defaultSpec, _, err := resolvePersonaModels(nil, nil,
		map[string]*string{"default": strp("sonnet")}, testModels, trustedSpecs)
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Equal(t, "anthropic-vertex/claude-sonnet-4-6", defaultSpec)
}

// An unmatched key is caught even when discovery found no personas at all,
// so a typo cannot be silently accepted.
func TestResolvePersonaModels_UnknownKeyWithoutPersonas(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	_, _, _, err := resolvePersonaModels(nil, nil, map[string]*string{"correctnes": strp("opus")}, testModels,
		map[string]string{"anthropic-vertex/claude-opus-4-6": "anthropic-vertex/claude-opus-4-6"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "correctnes")
}

// A persona naming a tool pi cannot serve must fail Bootstrap rather than
// have it dropped: an empty set used to fall back to the parent's full one.
func TestResolvePersonaModels_UnservableTools(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{{Name: "webby", Model: "opus", Tools: []string{"WebFetch"}}}
	trusted := map[string]string{"anthropic-vertex/claude-opus-4-6": "anthropic-vertex/claude-opus-4-6"}

	// Unreferenced: warned and left out of the manifest, the run proceeds.
	result, _, _, err := resolvePersonaModels(personas, nil, nil, testModels, trusted)
	require.NoError(t, err)
	assert.NotContains(t, result, "webby")

	// Referenced by the repo's config: the repo asked for it, so it is fatal.
	_, _, _, err = resolvePersonaModels(personas, nil, map[string]*string{"webby": strp("opus")}, testModels, trusted)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WebFetch")
	assert.Contains(t, err.Error(), "cannot be served")

	// Referenced through default: it would have received the model.
	_, _, _, err = resolvePersonaModels([]piPersona{{Name: "webby", Tools: []string{"WebFetch"}}}, nil,
		map[string]*string{"default": strp("opus")}, testModels, trusted)
	require.Error(t, err)
}

func TestResolvePersonaModels_EmptyToolsRefused(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{{Name: "locked", Model: "opus", Tools: []string{}}}
	trusted := map[string]string{"anthropic-vertex/claude-opus-4-6": "anthropic-vertex/claude-opus-4-6"}
	result, _, _, err := resolvePersonaModels(personas, nil, nil, testModels, trusted)
	require.NoError(t, err)
	assert.NotContains(t, result, "locked", "skipped, never registered with the parent's tools")
	_, _, _, err = resolvePersonaModels(personas, nil, map[string]*string{"locked": strp("opus")}, testModels, trusted)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty tool set")
}

// A Bash(...) allowlist would be recorded but not enforced on the child, so
// it is refused rather than silently ignored.
func TestResolvePersonaModels_BashAllowlistRefused(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{{Name: "risk", Model: "opus", Tools: []string{"Bash"}, BashAllowlist: []string{"git"}}}
	trusted := map[string]string{"anthropic-vertex/claude-opus-4-6": "anthropic-vertex/claude-opus-4-6"}
	result, _, _, err := resolvePersonaModels(personas, nil, nil, testModels, trusted)
	require.NoError(t, err)
	assert.NotContains(t, result, "risk")
	_, _, _, err = resolvePersonaModels(personas, nil, map[string]*string{"risk": strp("opus")}, testModels, trusted)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported yet")
}

// A persona-style "@suffix" resolves instead of failing the closed-set
// check naming the wrong cause.
func TestResolvePersonaModels_AtSuffixStripped(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{{Name: "correctness", Model: "claude-opus-4-6@default"}}
	result, _, _, err := resolvePersonaModels(personas, nil, nil, testModels,
		map[string]string{"anthropic-vertex/claude-opus-4-6": "anthropic-vertex/claude-opus-4-6"})
	require.NoError(t, err)
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", result["correctness"].Model)
}

func TestResolvePersonaModels_UnknownConfigKey(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{
		{Name: "style"},
	}
	cfg := map[string]*string{
		"nonexistent": strp("haiku"),
	}
	trustedSpecs := map[string]string{
		"anthropic-vertex/claude-sonnet-4-6": "anthropic-vertex/claude-sonnet-4-6",
	}

	_, _, _, err := resolvePersonaModels(personas, nil, cfg, testModels, trustedSpecs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
	assert.Contains(t, err.Error(), "no persona")
}

func TestResolvePersonaModels_UntrustedModel(t *testing.T) {
	t.Setenv(piProviderEnv, "")

	personas := []piPersona{
		{Name: "style", Model: "unknown-model-999"},
	}
	trustedSpecs := map[string]string{
		"anthropic-vertex/claude-sonnet-4-6": "anthropic-vertex/claude-sonnet-4-6",
	}

	result, _, _, err := resolvePersonaModels(personas, nil, nil, testModels, trustedSpecs)
	require.NoError(t, err, "a bad frontmatter model on an unreferenced persona is skipped, not fatal")
	assert.NotContains(t, result, "style")

	// The repo names it with a servable model: the config value replaces
	// the frontmatter and the persona registers.
	result, _, _, err = resolvePersonaModels(personas, nil, map[string]*string{"style": strp("sonnet")}, testModels, trustedSpecs)
	require.NoError(t, err)
	assert.Equal(t, "anthropic-vertex/claude-sonnet-4-6", result["style"].Model)

	// The repo names it with an unservable value: still fatal.
	_, _, _, err = resolvePersonaModels(personas, nil, map[string]*string{"style": strp("gemini-9")}, testModels, trustedSpecs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not available")
}

// testModels mirrors the manifest model table Bootstrap builds: the alias
// keys plus "default", already resolved to full specs.
var testModels = map[string]string{
	"default": "anthropic-vertex/claude-sonnet-4-6",
	"opus":    "anthropic-vertex/claude-opus-4-6",
	"sonnet":  "anthropic-vertex/claude-sonnet-4-6",
	"haiku":   "anthropic-vertex/claude-haiku-4-5",
}

func strp(s string) *string { return &s }

// A bare persona value must resolve through the manifest's model table,
// not through the FULLSEND_PI_PROVIDER prefix: a repo running pi on Grok
// would otherwise have every `subagents: {x: opus}` fail Bootstrap naming
// a spec nobody wrote.
func TestResolvePersonaModels_BareValueIgnoresProviderEnv(t *testing.T) {
	t.Setenv(piProviderEnv, "xai-vertex")

	personas := []piPersona{{Name: "correctness"}}
	trusted := map[string]string{
		"anthropic-vertex/claude-opus-4-6":   "anthropic-vertex/claude-opus-4-6",
		"anthropic-vertex/claude-sonnet-4-6": "anthropic-vertex/claude-sonnet-4-6",
	}
	for _, spec := range []string{"opus", "claude-opus-4-6", "anthropic-vertex/claude-opus-4-6"} {
		result, _, _, err := resolvePersonaModels(personas, nil, map[string]*string{"correctness": strp(spec)}, testModels, trusted)
		require.NoError(t, err, "spec %q", spec)
		assert.Equal(t, "anthropic-vertex/claude-opus-4-6", result["correctness"].Model, "spec %q", spec)
	}
}

func TestDiscoverPersonas_AgentNameReserved(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "agent", "---\nname: agent\n---\n")

	personas, skipped, err := discoverPersonas([]string{skill}, "review")
	require.NoError(t, err, "an invalid persona is skipped, never fatal on its own")
	assert.Empty(t, personas)
	require.Len(t, skipped, 1)
	assert.Contains(t, skipped[0].Reason, "reserved for anonymous sub-agent sessions")
}

// A file discovery skipped is fatal only when the repo's config names it.
func TestResolvePersonaModels_SkippedPersonaReferencedFails(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	trusted := map[string]string{"anthropic-vertex/claude-opus-4-6": "anthropic-vertex/claude-opus-4-6"}
	skipped := []piSkippedPersona{{Name: "correctnes", Path: "sub-agents/correctnes.md", Model: "opus",
		Reason: `frontmatter name "correctness" must equal file basename "correctnes"`}}

	result, _, _, err := resolvePersonaModels(nil, skipped, nil, testModels, trusted)
	require.NoError(t, err, "unreferenced: the run proceeds")
	assert.Empty(t, result)

	_, _, _, err = resolvePersonaModels(nil, skipped, map[string]*string{"correctnes": strp("opus")}, testModels, trusted)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "was not registered")
	assert.Contains(t, err.Error(), "must equal file basename")

	// default would have applied to a skipped persona with no model.
	_, _, _, err = resolvePersonaModels(nil, []piSkippedPersona{{Name: "x", Path: "sub-agents/x.md", Reason: "name is reserved"}},
		map[string]*string{"default": strp("opus")}, testModels, trusted)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subagents.default would have applied")
}

// The nine pr-review personas as fullsend-ai/agents main ships them today,
// five of them still pinned `claude-sonnet-4-6@default`: all nine must
// register with no config at all, on their frontmatter tiers.
func TestDiscoverPersonas_FleetMainRegistersNine(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	skill := t.TempDir()
	fleet := map[string]string{
		"challenger": "opus", "correctness": "opus", "security": "opus",
		"security-triage":      "haiku",
		"cross-repo-contracts": "claude-sonnet-4-6@default", "docs-currency": "claude-sonnet-4-6@default",
		"intent-coherence": "claude-sonnet-4-6@default", "risk-assessment": "claude-sonnet-4-6@default",
		"style-conventions": "claude-sonnet-4-6@default",
	}
	for name, model := range fleet {
		tools := "Read, Grep, Glob"
		if name == "risk-assessment" {
			tools = "Read, Bash, Grep, Glob"
		}
		writePersonaFile(t, skill, name, "---\nname: "+name+"\nmodel: "+model+"\ntools: "+tools+"\n---\nbody\n")
	}
	personas, skipped, err := discoverPersonas([]string{skill}, "review")
	require.NoError(t, err)
	assert.Empty(t, skipped)
	require.Len(t, personas, 9)

	trusted := map[string]string{
		"anthropic-vertex/claude-opus-4-6":   "anthropic-vertex/claude-opus-4-6",
		"anthropic-vertex/claude-sonnet-4-6": "anthropic-vertex/claude-sonnet-4-6",
		"anthropic-vertex/claude-haiku-4-5":  "anthropic-vertex/claude-haiku-4-5",
	}
	result, defaultSpec, _, err := resolvePersonaModels(personas, skipped, nil, testModels, trusted)
	require.NoError(t, err)
	assert.Empty(t, defaultSpec)
	require.Len(t, result, 9)
	assert.Equal(t, "anthropic-vertex/claude-opus-4-6", result["correctness"].Model)
	assert.Equal(t, "anthropic-vertex/claude-sonnet-4-6", result["risk-assessment"].Model, "@suffix stripped")
	assert.Equal(t, "anthropic-vertex/claude-haiku-4-5", result["security-triage"].Model)
	assert.Equal(t, []string{"read", "bash", "grep", "find"}, result["risk-assessment"].Tools)
}

// A deliberately bad persona beside good ones: the run proceeds, the good
// ones register, the bad one is skipped with its rule.
func TestDiscoverPersonas_BadPersonaSkippedBesideGoodOnes(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	skill := t.TempDir()
	writePersonaFile(t, skill, "correctness", "---\nname: correctness\nmodel: opus\n---\n")
	writePersonaFile(t, skill, "broken", "---\nname: not-broken\nmodel: opus\n---\n")
	personas, skipped, err := discoverPersonas([]string{skill}, "review")
	require.NoError(t, err)
	require.Len(t, personas, 1)
	require.Len(t, skipped, 1)
	assert.Equal(t, "broken", skipped[0].Name)
	assert.Contains(t, skipped[0].Reason, "must equal file basename")
}

// A frontmatter that does not parse hides its model, so default's
// would-have-applied check must not turn the skip into a failure.
func TestResolvePersonaModels_UnparsedSkipIsNotFatalViaDefault(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	trusted := map[string]string{"anthropic-vertex/claude-opus-4-6": "anthropic-vertex/claude-opus-4-6"}
	sk := []piSkippedPersona{{Name: "odd", Path: "sub-agents/odd.md", Unparsed: true, Reason: "frontmatter does not parse"}}
	result, _, skippedOut, err := resolvePersonaModels(nil, sk, map[string]*string{"default": strp("opus")}, testModels, trusted)
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Contains(t, skippedOut, "odd")
}

// A file named sub-agents, or an unreadable one, is not this run's concern.
func TestDiscoverPersonas_NotADirectoryIsSkipped(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(skill, "sub-agents"), []byte("not a dir"), 0o644))
	personas, skipped, err := discoverPersonas([]string{skill}, "review")
	require.NoError(t, err)
	assert.Empty(t, personas)
	assert.Empty(t, skipped)
}

// Two alias entries sharing a trailing id resolve the same way every run,
// and the agent's own "default" spec never shadows an alias.
func TestResolvePersonaModels_BareIDTableIsDeterministic(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	models := map[string]string{
		"default": "xai-vertex/xai/claude-opus-4-6", // a Grok run's own model, bare id collides
		"opus":    "anthropic-vertex/claude-opus-4-6",
		"sonnet":  "anthropic-vertex/claude-sonnet-4-6",
	}
	trusted := map[string]string{
		"anthropic-vertex/claude-opus-4-6": "anthropic-vertex/claude-opus-4-6",
		"xai-vertex/xai/claude-opus-4-6":   "xai-vertex/xai/claude-opus-4-6",
	}
	for i := 0; i < 20; i++ {
		result, _, _, err := resolvePersonaModels([]piPersona{{Name: "correctness", Model: "claude-opus-4-6"}}, nil, nil, models, trusted)
		require.NoError(t, err)
		assert.Equal(t, "anthropic-vertex/claude-opus-4-6", result["correctness"].Model)
	}
}

// A persona pinned to the parent's own effective model is accepted even
// when that model is outside the alias table -- the anonymous path would
// inherit it, so the named path must not be the one that fails.
func TestPiTrustedSpecs_IncludesEffectiveParent(t *testing.T) {
	t.Setenv(piProviderEnv, "")
	trusted := piTrustedSpecs(testModels, map[string][]string{"google-vertex": {"gemini-3.8-flash"}}, "claude-sonnet-5", nil)
	assert.Contains(t, trusted, "anthropic-vertex/claude-sonnet-5", "the --model override, canonicalised")
	assert.Contains(t, trusted, "google-vertex/gemini-3.8-flash")
	assert.Contains(t, trusted, "anthropic-vertex/claude-opus-4-6")

	result, _, _, err := resolvePersonaModels([]piPersona{{Name: "correctness"}}, nil,
		map[string]*string{"correctness": strp("anthropic-vertex/claude-sonnet-5")}, testModels, trusted)
	require.NoError(t, err)
	assert.Equal(t, "anthropic-vertex/claude-sonnet-5", result["correctness"].Model)

	// Without the parent in the set the same pin is refused.
	_, _, _, err = resolvePersonaModels([]piPersona{{Name: "correctness"}}, nil,
		map[string]*string{"correctness": strp("anthropic-vertex/claude-sonnet-5")}, testModels,
		piTrustedSpecs(testModels, nil, "", nil))
	require.Error(t, err)
}
