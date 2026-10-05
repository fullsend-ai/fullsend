package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiRuntimeOpenAISeeder(t *testing.T) {
	t.Parallel()

	r := PiRuntime{}
	assert.Equal(t, sandbox.SandboxPiConfig+"/auth.json", r.OpenAIAuthFile(),
		"the file the runner greps after a re-seed is pi's auth.json")
	seed := r.OpenAIAuthSeed()
	assert.Equal(t, PiOpenAIAuthSeed(r.ConfigDir()), seed,
		"the interface method and the exported helper must stay one fragment")
	require.NotEmpty(t, seed)
	assert.Contains(t, seed, r.OpenAIAuthFile(),
		"the fragment writes the file the interface names")
}

// CodexRuntime's seeder is a stub until its Bootstrap writes the token file
// (#6920). Empty is the contract for "no re-seed": the runner then leaves
// both handle fields empty and a refresh only updates the provider.
func TestCodexRuntimeOpenAISeeder(t *testing.T) {
	t.Parallel()

	// PR D replaced the stub: codex reads its bearer token by running an auth
	// command that prints the placeholder from this runner-owned file, so the
	// runner has a real fragment to re-run after every credential refresh.
	// The behaviour of both is covered in codex_run_test.go, which executes
	// the fragment under /bin/sh.
	r := CodexRuntime{}
	assert.Equal(t, r.ConfigDir()+"/openai-token", r.OpenAIAuthFile())
	assert.Contains(t, r.OpenAIAuthSeed(), "OPENAI_API_KEY")
	assert.Contains(t, r.OpenAIAuthSeed(), r.OpenAIAuthFile())
}

// Not parallel: the pi provider default is an environment variable
// (FULLSEND_PI_PROVIDER), which t.Setenv refuses to set in a parallel test.
func TestNeedsOpenAIProvider(t *testing.T) {
	for _, tc := range []struct {
		name          string
		backend       string
		model         string
		agentModel    string // the agent definition's frontmatter model:
		provider      string // FULLSEND_PI_PROVIDER, when set
		configAliases map[string]string
		want          bool
	}{
		{name: "claude with an alias", backend: "claude", model: "opus"},
		{name: "claude with an openai spec", backend: "claude", model: "openai/gpt-5.6-luna",
			want: false}, // Claude Code cannot serve it; the run warns elsewhere
		{name: "pi on openai", backend: "pi", model: "openai/gpt-5.6-luna", want: true},
		{name: "pi on openai, mixed case", backend: "pi", model: "OpenAI/gpt-5.6-luna", want: true},
		{name: "pi on vertex", backend: "pi", model: "anthropic-vertex/claude-opus-4-6"},
		{name: "pi on an alias", backend: "pi", model: "opus"},
		{name: "pi with no model at all", backend: "pi", model: ""},
		{name: "pi on xai-vertex", backend: "pi", model: "xai/grok-4.6"},
		{name: "pi with a bare id under FULLSEND_PI_PROVIDER=openai", backend: "pi",
			model: "gpt-5.6-luna", provider: "openai", want: true},
		{name: "pi with a bare id under a mixed-case FULLSEND_PI_PROVIDER", backend: "pi",
			model: "gpt-5.6-luna", provider: "OpenAI", want: true},
		{name: "codex with any model", backend: "codex", model: "openai/gpt-5.6-luna", want: true},
		{name: "codex with no model", backend: "codex", model: "", want: true},
		// The agent definition's model: is pi's fallback when the runner
		// resolved none, so it decides here too. Reading only the runner's
		// value would strand this agent without a credential...
		{name: "pi with an openai model only in the agent frontmatter", backend: "pi",
			model: "", agentModel: "openai/gpt-5.6-luna", want: true},
		// ...and would attach a live OpenAI credential to this one, whose
		// frontmatter pins a Vertex model the provider default never reaches.
		{name: "pi with a vertex frontmatter model under FULLSEND_PI_PROVIDER=openai", backend: "pi",
			model: "", agentModel: "anthropic-vertex/claude-opus-4-6", provider: "openai"},
		{name: "an override still wins over the frontmatter", backend: "pi",
			model: "anthropic-vertex/claude-opus-4-6", agentModel: "openai/gpt-5.6-luna"},
		{name: "codex ignores both", backend: "codex", model: "", agentModel: "anthropic-vertex/claude-opus-4-6", want: true},
		{name: "pi on an alias the repo remapped to openai", backend: "pi", model: "sonnet",
			configAliases: map[string]string{"sonnet": "openai/gpt-5.6-luna"}, want: true},
		{name: "dummy", backend: "dummy", model: "openai/gpt-5.6-luna"},
		{name: "unknown backend", backend: "opencode", model: "openai/gpt-5.6-luna"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(piProviderEnv, tc.provider)
			assert.Equal(t, tc.want, NeedsOpenAIProvider(tc.backend, tc.model, tc.agentModel, tc.configAliases))
		})
	}
}

// OpenAIChildren is the runner's provider gate and pre-sandbox check for a
// configured pi child on the openai provider (#7981). It must resolve each
// model exactly as resolvePersonaModels does, and count only children the
// run could dispatch.
func TestOpenAIChildren(t *testing.T) {
	agentDir := t.TempDir()
	writeAgent := func(name, frontmatter string) string {
		p := filepath.Join(agentDir, name+".md")
		require.NoError(t, os.WriteFile(p, []byte(frontmatter+"You review.\n"), 0o644))
		return p
	}
	// No tools: line means pi's defaults, which include Agent.
	withAgent := writeAgent("with-agent", "---\nname: code\nmodel: opus\n---\n")
	noAgent := writeAgent("no-agent", "---\nname: code\nmodel: opus\ntools: Read, Grep\n---\n")

	noOpenAI := t.TempDir()
	writePersonaFile(t, noOpenAI, "correctness", "---\nname: correctness\nmodel: opus\n---\nReview for correctness.\n")
	writePersonaFile(t, noOpenAI, "style", "---\nname: style\n---\nReview for style.\n")
	writePersonaFile(t, noOpenAI, "writer", "---\nname: writer\nmodel: sonnet\n---\nWrite.\n")

	withOpenAI := t.TempDir()
	writePersonaFile(t, withOpenAI, "correctness", "---\nname: correctness\nmodel: opus\n---\nReview for correctness.\n")
	writePersonaFile(t, withOpenAI, "checker", "---\nname: checker\nmodel: openai/gpt-5.6-luna\n---\nCheck something.\n")

	// Personas resolvePersonaModels would skip: they never dispatch, so
	// their model must not create a provider.
	unregistrable := t.TempDir()
	writePersonaFile(t, unregistrable, "bashy", "---\nname: bashy\nmodel: openai/gpt-5.6-luna\ntools: Bash(git)\n---\nRun git.\n")
	writePersonaFile(t, unregistrable, "webby", "---\nname: webby\nmodel: openai/gpt-5.6-luna\ntools: WebSearch\n---\nSearch.\n")
	writePersonaFile(t, unregistrable, "empty", "---\nname: empty\nmodel: openai/gpt-5.6-luna\ntools: []\n---\nNothing.\n")
	writePersonaFile(t, unregistrable, "multiline", "---\nname: multiline\nmodel: |\n  openai/gpt-5\n  ::warning::injected\n---\nInject.\n")
	writePersonaFile(t, unregistrable, "dispatcher", "---\nname: dispatcher\nmodel: openai/gpt-5.6-luna\ntools: Agent, Task\n---\nDispatch.\n")

	withAlias := t.TempDir()
	writePersonaFile(t, withAlias, "checker", "---\nname: checker\nmodel: Luna@default\n---\nCheck something.\n")
	aliases := map[string]string{"luna": "openai/gpt-5.6-luna"}

	luna := "openai/gpt-5.6-luna"
	for _, tc := range []struct {
		name          string
		backend       string
		agentPath     string
		subagentsCfg  map[string]*string
		skillDirs     []string
		configAliases map[string]string
		providerEnv   string
		want          []PiChild
	}{
		{name: "not pi: always empty regardless of config", backend: "codex",
			subagentsCfg: map[string]*string{"default": strp(luna)}},
		{name: "an agent without the Agent tool dispatches no children", backend: "pi", agentPath: noAgent,
			skillDirs: []string{withOpenAI}, subagentsCfg: map[string]*string{"default": strp(luna)}},
		{name: "no config, no openai persona frontmatter", backend: "pi",
			skillDirs: []string{noOpenAI}, subagentsCfg: map[string]*string{"correctness": strp("opus")}},
		{name: "a persona's own frontmatter names openai", backend: "pi", skillDirs: []string{withOpenAI},
			want: []PiChild{{Source: `persona "checker" frontmatter model`, Spec: luna}}},
		{name: "a mixed-case frontmatter alias with @suffix resolves through models.aliases", backend: "pi",
			skillDirs: []string{withAlias}, configAliases: aliases,
			want: []PiChild{{Source: `persona "checker" frontmatter model`, Spec: luna}}},
		{name: "the same alias without models.aliases is not openai", backend: "pi", skillDirs: []string{withAlias}},
		{name: "a bare id that is an alias's target resolves to it", backend: "pi",
			subagentsCfg: map[string]*string{"default": strp("gpt-5.6-luna")}, configAliases: aliases,
			want: []PiChild{{Source: "subagents.default", Spec: luna, Configured: true}}},
		{name: "FULLSEND_PI_PROVIDER=openai does not move a Claude alias to openai", backend: "pi",
			skillDirs: []string{noOpenAI}, providerEnv: "openai"},
		{name: "personas Bootstrap would skip do not count", backend: "pi", skillDirs: []string{unregistrable}},
		{name: "subagents.default names openai", backend: "pi",
			subagentsCfg: map[string]*string{"default": strp(luna)},
			want:         []PiChild{{Source: "subagents.default", Spec: luna, Configured: true}}},
		{name: "subagents.<persona> override names openai", backend: "pi", skillDirs: []string{noOpenAI},
			subagentsCfg: map[string]*string{"style": strp(luna)},
			want:         []PiChild{{Source: "subagents.style", Spec: luna, Configured: true}}},
		{name: "a key naming no discovered persona is left to Bootstrap", backend: "pi", skillDirs: []string{noOpenAI},
			subagentsCfg: map[string]*string{"typo": strp(luna)}},
		{name: "a key naming a persona Bootstrap would skip is left to Bootstrap", backend: "pi",
			skillDirs: []string{unregistrable}, subagentsCfg: map[string]*string{"bashy": strp(luna)}},
		{name: "a config override away from openai beats an openai frontmatter", backend: "pi",
			skillDirs: []string{withOpenAI}, subagentsCfg: map[string]*string{"checker": strp("opus")}},
		{name: "a tombstoned entry (nil) is not a model reference", backend: "pi",
			subagentsCfg: map[string]*string{"default": nil}},
		{name: "several sources are listed sorted", backend: "pi", skillDirs: []string{withOpenAI},
			subagentsCfg: map[string]*string{"default": strp(luna)},
			want: []PiChild{
				{Source: `persona "checker" frontmatter model`, Spec: luna},
				{Source: "subagents.default", Spec: luna, Configured: true},
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(piProviderEnv, tc.providerEnv)
			agentPath := tc.agentPath
			if agentPath == "" {
				agentPath = withAgent
			}
			got := OpenAIChildren(tc.backend, agentPath, tc.subagentsCfg, tc.skillDirs, "code", tc.configAliases)
			assert.Equal(t, tc.want, got)
		})
	}
}

// piModelProvider is what both buildPiRunCommand's gates and
// NeedsOpenAIProvider branch on, so the prefix it returns must already be
// folded to lower case.
func TestPiModelProviderIsLowercase(t *testing.T) {
	t.Parallel()

	for _, model := range []string{"OpenAI/gpt-5.6-luna", "Anthropic-Vertex/claude-opus-4-6", "XAI/grok-4.6"} {
		got := piModelProvider(model, nil)
		assert.Equal(t, strings.ToLower(got), got, model)
	}
	assert.Equal(t, piOpenAIProvider, piModelProvider("OpenAI/gpt-5.6-luna", nil))
	assert.Equal(t, piDefaultProvider, piModelProvider("opus", nil))
	assert.Equal(t, piXaiVertexProvider, piModelProvider("xai/grok-4.6", nil))
}
