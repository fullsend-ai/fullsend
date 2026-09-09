package runtime

import (
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
