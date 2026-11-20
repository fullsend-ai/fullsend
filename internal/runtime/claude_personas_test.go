package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const claudeTestAgent = "---\nname: review\nmodel: opus\n---\nReview the change.\n"

// claudePersonaSkill is a skill with the fixture the issue proposes:
// correctness names a model, style names none.
func claudePersonaSkill(t *testing.T) string {
	t.Helper()
	skill := t.TempDir()
	writePersonaFile(t, skill, "correctness", "---\nname: correctness\ndescription: Checks logic.\nmodel: opus\nrole: dimension\n---\nReview for correctness.\n")
	writePersonaFile(t, skill, "style", "---\nname: style\ndescription: Checks style.\n---\nReview for style.\n")
	return skill
}

func claudePersonaInput(skill string, subagents map[string]*string) bootstrapInput {
	return bootstrapInput{
		sandboxName:    "test-sandbox",
		agentName:      "review",
		skillDirs:      []string{skill},
		agentSubagents: subagents,
	}
}

func claudePersonaByName(t *testing.T, files []claudePersonaFile, name string) (claudePersonaFile, *piAgentDef) {
	t.Helper()
	for _, f := range files {
		if f.Name == name {
			def, err := parsePiAgent(f.Data)
			require.NoError(t, err, "a rendered persona must parse: %s", f.Data)
			return f, def
		}
	}
	t.Fatalf("persona %q not registered", name)
	return claudePersonaFile{}, nil
}

func TestClaudePersonas_NoSubagentsBlockLeavesAgentUnchanged(t *testing.T) {
	skill := claudePersonaSkill(t)
	for name, cfg := range map[string]map[string]*string{
		"absent":          nil,
		"only tombstones": {"correctness": nil, "default": nil},
	} {
		t.Run(name, func(t *testing.T) {
			files, data, err := claudePersonas(claudePersonaInput(skill, cfg), []byte(claudeTestAgent), false)
			require.NoError(t, err)
			assert.Empty(t, files)
			assert.Equal(t, claudeTestAgent, string(data))
		})
	}
}

func TestClaudePersonas_ConfigOverrideWins(t *testing.T) {
	skill := claudePersonaSkill(t)
	files, data, err := claudePersonas(claudePersonaInput(skill, map[string]*string{"correctness": strp("haiku")}), []byte(claudeTestAgent), false)
	require.NoError(t, err)
	require.Len(t, files, 2)

	f, def := claudePersonaByName(t, files, "correctness")
	assert.Equal(t, "claude-haiku-5-5", def.Model)
	assert.Equal(t, "subagents.correctness", f.Source)
	assert.Equal(t, "Checks logic.", def.Description)
	assert.Equal(t, "Review for correctness.", def.Body)
	assert.Contains(t, string(def.Frontmatter), "role: dimension", "other keys are kept")

	_, def = claudePersonaByName(t, files, "style")
	assert.Equal(t, "inherit", def.Model, "a persona nothing configures inherits the parent's model")

	assert.True(t, strings.HasPrefix(string(data), strings.TrimRight(claudeTestAgent, "\n")))
	assert.Contains(t, string(data), "## Runtime note")
	assert.Contains(t, string(data), "Registered sub-agent personas: correctness, style.")
	assert.Contains(t, string(data), "omit `model`")
}

func TestClaudePersonas_DefaultIsAFloorNotAnOverride(t *testing.T) {
	skill := claudePersonaSkill(t)
	files, _, err := claudePersonas(claudePersonaInput(skill, map[string]*string{"default": strp("haiku")}), []byte(claudeTestAgent), false)
	require.NoError(t, err)

	f, def := claudePersonaByName(t, files, "correctness")
	assert.Equal(t, "claude-opus-5-5", def.Model, "frontmatter model wins over default")
	assert.Equal(t, "frontmatter", f.Source)

	f, def = claudePersonaByName(t, files, "style")
	assert.Equal(t, "claude-haiku-5-5", def.Model)
	assert.Equal(t, "subagents.default", f.Source)
}

func TestClaudePersonas_AliasRetargetAndInherit(t *testing.T) {
	skill := claudePersonaSkill(t)
	in := claudePersonaInput(skill, map[string]*string{"correctness": strp("sonnet"), "style": strp("inherit")})
	in.modelAliases = map[string]string{"sonnet": "claude-sonnet-5"}
	files, _, err := claudePersonas(in, []byte(claudeTestAgent), false)
	require.NoError(t, err)

	_, def := claudePersonaByName(t, files, "correctness")
	assert.Equal(t, "claude-sonnet-5", def.Model, "models.aliases retargets a persona's alias")
	_, def = claudePersonaByName(t, files, "style")
	assert.Equal(t, "inherit", def.Model)
}

func TestClaudePersonas_ParentModelIsTrusted(t *testing.T) {
	skill := claudePersonaSkill(t)
	in := claudePersonaInput(skill, map[string]*string{"correctness": strp("claude-opus-4-1@20250805")})
	in.parentModel = "claude-opus-4-1@20250805"
	files, _, err := claudePersonas(in, []byte(claudeTestAgent), false)
	require.NoError(t, err)
	_, def := claudePersonaByName(t, files, "correctness")
	assert.Equal(t, "claude-opus-4-1@20250805", def.Model)

	// A built-in fallback id is trusted too.
	in = claudePersonaInput(skill, map[string]*string{"correctness": strp("claude-sonnet-4-6")})
	files, _, err = claudePersonas(in, []byte(claudeTestAgent), false)
	require.NoError(t, err)
	_, def = claudePersonaByName(t, files, "correctness")
	assert.Equal(t, "claude-sonnet-4-6", def.Model)
}

func TestClaudePersonas_Errors(t *testing.T) {
	skill := claudePersonaSkill(t)
	writePersonaFile(t, skill, "broken", "---\nname: other\n---\n")
	tests := map[string]struct {
		cfg  map[string]*string
		want string
	}{
		"unknown key":           {map[string]*string{"nope": strp("haiku")}, `subagents.nope: no persona "nope" was discovered; discovered personas: correctness, style`},
		"skipped key":           {map[string]*string{"broken": strp("haiku")}, "subagents.broken: persona"},
		"skipped under default": {map[string]*string{"default": strp("haiku")}, "subagents.default would have applied to it"},
		"untrusted persona":     {map[string]*string{"correctness": strp("gpt-5.5")}, `persona "correctness": resolved model "gpt-5.5" is not available in this run; accepted:`},
		"untrusted default":     {map[string]*string{"default": strp("claude-unknown-1")}, `subagents.default: resolved model "claude-unknown-1" is not available`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := claudePersonas(claudePersonaInput(skill, tc.cfg), []byte(claudeTestAgent), false)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestClaudePersonas_UnreferencedDefectsAreSkipped(t *testing.T) {
	skill := t.TempDir()
	writePersonaFile(t, skill, "correctness", "---\nname: correctness\ndescription: x\nmodel: opus\n---\nbody\n")
	writePersonaFile(t, skill, "pinned", "---\nname: pinned\ndescription: x\nmodel: claude-unknown-1\n---\nbody\n")
	writePersonaFile(t, skill, "empty-tools", "---\nname: empty-tools\ndescription: x\nmodel: opus\ntools: []\n---\nbody\n")
	writePersonaFile(t, skill, "bash-list", "---\nname: bash-list\ndescription: x\nmodel: opus\ntools: Bash(gh,jq)\n---\nbody\n")
	writePersonaFile(t, skill, "mismatch", "---\nname: wrong\nmodel: opus\n---\nbody\n")

	files, data, err := claudePersonas(claudePersonaInput(skill, map[string]*string{"correctness": strp("haiku")}), []byte(claudeTestAgent), false)
	require.NoError(t, err, "a defect in a persona the config does not name is a warning")
	require.Len(t, files, 1)
	assert.Equal(t, "correctness", files[0].Name)
	assert.Contains(t, string(data), "Registered sub-agent personas: correctness.")

	// The same defects are fatal once the config names the persona. (A
	// subagents.pinned entry would replace the untrusted frontmatter model.)
	for name, want := range map[string]string{
		"empty-tools": "empty tool set",
		"bash-list":   "Bash(...) allowlist",
	} {
		_, _, err := claudePersonas(claudePersonaInput(skill, map[string]*string{name: strp("opus")}), []byte(claudeTestAgent), false)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), want, name)
	}
}

func TestClaudePersonas_AgentWithoutAgentToolDispatchesNothing(t *testing.T) {
	skill := claudePersonaSkill(t)
	agent := "---\nname: review\ntools: Read, Grep\n---\nbody\n"
	files, data, err := claudePersonas(claudePersonaInput(skill, map[string]*string{"correctness": strp("haiku")}), []byte(agent), false)
	require.NoError(t, err)
	assert.Empty(t, files)
	assert.Equal(t, agent, string(data))
}

func TestClaudePersonas_RestrictedAgentSpecLimitsPersonas(t *testing.T) {
	skill := claudePersonaSkill(t)
	cfg := map[string]*string{"correctness": strp("haiku")}

	// Agent(Explore) leaves out both personas: Claude Code would refuse to
	// dispatch them. correctness is named by the config, so that is fatal.
	restricted := "---\nname: review\ntools: Agent(Explore), Read\n---\nbody\n"
	_, _, err := claudePersonas(claudePersonaInput(skill, cfg), []byte(restricted), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `persona "correctness"`)
	assert.Contains(t, err.Error(), "restrict Agent(...)")

	// Unreferenced personas outside the list are left out with a warning;
	// the ones the list names are registered and advertised.
	partial := "---\nname: review\ntools: Agent(Explore, correctness), Read\n---\nbody\n"
	files, data, err := claudePersonas(claudePersonaInput(skill, cfg), []byte(partial), false)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "correctness", files[0].Name)
	assert.Contains(t, string(data), "Registered sub-agent personas: correctness.")

	// The legacy Task name is restricted the same way.
	legacy := "---\nname: review\ntools: Task(style), Read\n---\nbody\n"
	_, _, err = claudePersonas(claudePersonaInput(skill, cfg), []byte(legacy), false)
	require.Error(t, err)

	// A bare Agent or Task alongside a restricted spec leaves dispatch open.
	open := "---\nname: review\ntools: Agent(Explore), Task, Read\n---\nbody\n"
	files, _, err = claudePersonas(claudePersonaInput(skill, cfg), []byte(open), false)
	require.NoError(t, err)
	assert.Len(t, files, 2)
}

func TestClaudePersonas_DefaultWithNoPersonaFails(t *testing.T) {
	// subagents.default reaches only registered personas, so with none it
	// would be accepted and do nothing; Bootstrap says so instead.
	_, _, err := claudePersonas(claudePersonaInput(t.TempDir(), map[string]*string{"default": strp("haiku")}), []byte(claudeTestAgent), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subagents.default")
	assert.Contains(t, err.Error(), "no persona was registered")
}

func TestClaudePersonas_NonDelegatingAgentIgnoresWholeBlock(t *testing.T) {
	// An agent whose tools: leave out Agent dispatches nothing, so its
	// subagents block is ignored as a whole, as on pi: an unknown persona
	// key, an unavailable model and an inapplicable default are not errors.
	agent := "---\nname: review\ntools: Read, Grep\n---\nbody\n"
	for name, cfg := range map[string]map[string]*string{
		"unknown persona": {"nosuch": strp("haiku")},
		"unlisted model":  {"correctness": strp("gpt-9")},
		"default only":    {"default": strp("haiku")},
	} {
		t.Run(name, func(t *testing.T) {
			files, data, err := claudePersonas(claudePersonaInput(claudePersonaSkill(t), cfg), []byte(agent), false)
			require.NoError(t, err)
			assert.Empty(t, files)
			assert.Equal(t, agent, string(data))
		})
	}
}

func TestClaudePersonas_EmptyAgentSpecRestrictsToNothing(t *testing.T) {
	skill := claudePersonaSkill(t)
	for _, tools := range []string{"Agent()", "Task( )"} {
		agent := "---\nname: review\ntools: " + tools + ", Read\n---\nbody\n"

		// Every persona is left out, and one the config names is fatal.
		_, _, err := claudePersonas(claudePersonaInput(skill, map[string]*string{"correctness": strp("haiku")}), []byte(agent), false)
		require.Error(t, err, tools)
		assert.Contains(t, err.Error(), "restrict Agent(...)", tools)

		// Nothing names a persona: none is registered, rather than all.
		allowed, restricted := claudeAgentDispatchAllowlist([]byte("tools: " + tools + ", Read\n"))
		assert.True(t, restricted, tools)
		assert.Empty(t, allowed, tools)
	}

	// A bare Agent alongside the empty spec still leaves dispatch open.
	allowed, restricted := claudeAgentDispatchAllowlist([]byte("tools: Agent(), Agent, Read\n"))
	assert.False(t, restricted)
	assert.Nil(t, allowed)
}

func TestClaudePersonas_ToolsNeverWiderThanTheAgent(t *testing.T) {
	skill := t.TempDir()
	writePersonaFile(t, skill, "inherits", "---\nname: inherits\ndescription: x\n---\nbody\n")
	writePersonaFile(t, skill, "narrow", "---\nname: narrow\ndescription: x\ntools: Read, Bash, WebFetch\n---\nbody\n")
	writePersonaFile(t, skill, "outside", "---\nname: outside\ndescription: x\ntools: WebFetch\n---\nbody\n")
	agent := "---\nname: review\ntools: Agent, Read, Grep, Bash(gh,jq)\n---\nbody\n"

	files, _, err := claudePersonas(claudePersonaInput(skill, map[string]*string{"inherits": strp("haiku")}), []byte(agent), false)
	require.NoError(t, err)
	require.Len(t, files, 2, "a persona left with no tool the agent has is skipped")

	_, def := claudePersonaByName(t, files, "inherits")
	assert.Equal(t, []string{"Read", "Grep", "Bash"}, def.Tools, "the agent's tools, less Agent")
	assert.Equal(t, []string{"gh", "jq"}, def.BashAllowlist, "the agent's Bash restriction carries over")

	_, def = claudePersonaByName(t, files, "narrow")
	assert.Equal(t, []string{"Read", "Bash"}, def.Tools, "WebFetch is dropped")
	assert.Equal(t, []string{"gh", "jq"}, def.BashAllowlist, "plain Bash takes the agent's restriction")

	_, _, err = claudePersonas(claudePersonaInput(skill, map[string]*string{"outside": strp("haiku")}), []byte(agent), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tool the agent also has")
}

// The agent's disallowedTools: reach every persona, whether the agent
// declares tools: or not, and merge with the persona's own denials.
func TestClaudePersonas_AgentDenialsCarryOver(t *testing.T) {
	skill := t.TempDir()
	writePersonaFile(t, skill, "plain", "---\nname: plain\ndescription: x\ntools: Read, Bash\n---\nbody\n")
	writePersonaFile(t, skill, "own", "---\nname: own\ndescription: x\ndisallowedTools: WebFetch, Bash(git push *)\n---\nbody\n")
	cfg := map[string]*string{"plain": strp("haiku"), "own": strp("haiku")}

	denied := func(t *testing.T, files []claudePersonaFile, name string) []string {
		t.Helper()
		_, def := claudePersonaByName(t, files, name)
		got, err := claudeDisallowedToolSpecs(def.Frontmatter)
		require.NoError(t, err)
		return got
	}

	for name, agent := range map[string]string{
		"explicit tools": "---\nname: review\ntools: Agent, Read, Bash\ndisallowedTools: >-\n  Bash(git push *), Bash(rm *)\n---\nbody\n",
		"omitted tools":  "---\nname: review\ndisallowedTools:\n  - Bash(git push *)\n  - Bash(rm *)\n---\nbody\n",
	} {
		t.Run(name, func(t *testing.T) {
			files, _, err := claudePersonas(claudePersonaInput(skill, cfg), []byte(agent), false)
			require.NoError(t, err)
			assert.Equal(t, []string{"Bash(git push *)", "Bash(rm *)"}, denied(t, files, "plain"))
			assert.Equal(t, []string{"Bash(git push *)", "Bash(rm *)", "WebFetch"}, denied(t, files, "own"),
				"the agent's denials come first; the persona's own are kept, without duplicates")
		})
	}

	// No agent denials: a persona's own disallowedTools stays as written.
	files, _, err := claudePersonas(claudePersonaInput(skill, cfg), []byte(claudeTestAgent), false)
	require.NoError(t, err)
	assert.Empty(t, denied(t, files, "plain"))
	assert.Equal(t, []string{"WebFetch", "Bash(git push *)"}, denied(t, files, "own"))
}

func TestClaudePersonas_MalformedAgentDenialsFail(t *testing.T) {
	agent := "---\nname: review\ndisallowedTools: 3\n---\nbody\n"
	_, _, err := claudePersonas(claudePersonaInput(claudePersonaSkill(t), map[string]*string{"style": strp("haiku")}), []byte(agent), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disallowedTools")

	// A persona whose own disallowedTools is malformed is left out, or
	// fails when the config names it.
	skill := t.TempDir()
	writePersonaFile(t, skill, "bad", "---\nname: bad\ndescription: x\ndisallowedTools: 3\n---\nbody\n")
	agent = "---\nname: review\ndisallowedTools: Bash(rm *)\n---\nbody\n"
	_, _, err = claudePersonas(claudePersonaInput(skill, map[string]*string{"bad": strp("haiku")}), []byte(agent), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disallowedTools")
}

func TestClaudePersonas_UnrestrictedAgentKeepsPersonaTools(t *testing.T) {
	skill := t.TempDir()
	writePersonaFile(t, skill, "reader", "---\nname: reader\ndescription: x\ntools:\n  - Read\n  - Grep\n---\nbody\n")
	files, _, err := claudePersonas(claudePersonaInput(skill, map[string]*string{"reader": strp("haiku")}), []byte(claudeTestAgent), false)
	require.NoError(t, err)
	_, def := claudePersonaByName(t, files, "reader")
	assert.Equal(t, []string{"Read", "Grep"}, def.Tools)
}

func TestClaudePersonas_DescriptionSynthesized(t *testing.T) {
	skill := t.TempDir()
	writePersonaFile(t, skill, "terse", "---\nname: terse\n---\nbody\n")
	files, _, err := claudePersonas(claudePersonaInput(skill, map[string]*string{"terse": strp("haiku")}), []byte(claudeTestAgent), false)
	require.NoError(t, err)
	_, def := claudePersonaByName(t, files, "terse")
	assert.Equal(t, "Sub-agent persona terse", def.Description)
}

func TestClaudePersonas_GatewayRoute(t *testing.T) {
	// style inherits; correctness is pinned to the parent's gateway id.
	skill := claudePersonaSkill(t)
	in := claudePersonaInput(skill, map[string]*string{"correctness": strp("gateway/team-model")})
	in.parentModel = "gateway/team-model"
	files, _, err := claudePersonas(in, []byte(claudeTestAgent), true)
	require.NoError(t, err)
	_, def := claudePersonaByName(t, files, "correctness")
	assert.Equal(t, "team-model", def.Model)
	_, def = claudePersonaByName(t, files, "style")
	assert.Equal(t, "inherit", def.Model)

	// An Anthropic alias is not something the gateway is known to serve.
	in = claudePersonaInput(skill, map[string]*string{"correctness": strp("opus")})
	in.parentModel = "gateway/team-model"
	_, _, err = claudePersonas(in, []byte(claudeTestAgent), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gateway route")
}

func TestClaudePersonas_MalformedAgentDefinition(t *testing.T) {
	_, _, err := claudePersonas(claudePersonaInput(t.TempDir(), map[string]*string{"default": strp("haiku")}), []byte("---\nname: review\n"), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subagents: reading the agent definition")
}

func TestClaudePersonaAgentFile_RejectsNonMapping(t *testing.T) {
	_, err := claudePersonaAgentFile(piPersona{Name: "x", Frontmatter: []byte("- a\n")}, "", nil, nil)
	require.Error(t, err)
	_, err = claudePersonaAgentFile(piPersona{Name: "x", Frontmatter: []byte("a: [\n")}, "", nil, nil)
	require.Error(t, err)
}

// Bootstrap uploads each persona next to the agent definition, and fails
// before any sandbox call on a config error.
func TestClaudeRuntime_Bootstrap_RegistersPersonas(t *testing.T) {
	skill := claudePersonaSkill(t)
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "openshell.log")
	fakeOpenshellBootstrap(t, logPath, filepath.Join(binDir, "unused.tar.gz"))

	agentFile := filepath.Join(t.TempDir(), "review.md")
	require.NoError(t, os.WriteFile(agentFile, []byte(claudeTestAgent), 0o644))
	in := claudePersonaInput(skill, map[string]*string{"correctness": strp("haiku")})
	in.agentPath = agentFile
	require.NoError(t, ClaudeRuntime{}.Bootstrap(in))

	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(log), "/sandbox/claude-config/agents/correctness.md")
	assert.Contains(t, string(log), "/sandbox/claude-config/agents/style.md")
}

func TestClaudeRuntime_Bootstrap_PersonaConfigErrorFailsFirst(t *testing.T) {
	t.Setenv("PATH", "")
	agentFile := filepath.Join(t.TempDir(), "review.md")
	require.NoError(t, os.WriteFile(agentFile, []byte(claudeTestAgent), 0o644))
	in := claudePersonaInput(claudePersonaSkill(t), map[string]*string{"nope": strp("haiku")})
	in.agentPath = agentFile
	err := ClaudeRuntime{}.Bootstrap(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subagents.nope")
}
