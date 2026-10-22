package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codexTestPersona is a correctness persona with the Claude-only keys a
// fleet persona carries, so the tests show them dropped rather than
// rendered.
const codexTestPersona = "---\n" +
	"name: correctness\n" +
	"description: Evaluates logic correctness, edge cases, nil handling, API contracts, test adequacy/integrity.\n" +
	"model: opus\n" +
	"tools: Read, Grep, Glob, Bash\n" +
	"---\n" +
	"# Correctness\n\nYou are a senior software engineer reviewing for correctness.\n"

// codexTestDelegatingAgentDef has no tools: line, so its tool set includes
// Agent, as review and retro do in fullsend-ai/agents.
const codexTestDelegatingAgentDef = `---
name: review
description: Review a pull request.
model: openai/gpt-5.6-luna
---
You are the review agent. Dispatch the roster.
`

func TestCodexDelegates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		agent    string
		security bool
		want     bool
	}{
		{name: "no tools line includes Agent", agent: codexTestDelegatingAgentDef, security: true, want: true},
		{name: "tools naming Agent", agent: "---\nname: review\ntools: Bash,Agent\n---\nbody\n", security: true, want: true},
		{name: "tools naming Task, the alias codex maps to spawn_agent", agent: "---\nname: review\ntools: Bash,Task\n---\nbody\n", security: true, want: true},
		{name: "tools without Agent", agent: codexTestAgentDef, security: true, want: false},
		{name: "security disabled", agent: codexTestDelegatingAgentDef, security: false, want: false},
		{name: "empty tools list", agent: "---\nname: review\ntools: []\n---\nbody\n", security: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			def, err := parsePiAgent([]byte(tt.agent))
			require.NoError(t, err)
			assert.Equal(t, tt.want, codexDelegates(def, tt.security))
		})
	}
}

// Codex needs the two values the pi manifest never carried. They are filled
// by the shared discovery so the two runtimes cannot drift (#6970).
func TestDiscoverPersonas_CarriesDescriptionAndBody(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "correctness", codexTestPersona)

	personas, skipped, err := discoverPersonas([]string{skill}, "review")
	require.NoError(t, err)
	require.Empty(t, skipped)
	require.Len(t, personas, 1)
	assert.Equal(t, "Evaluates logic correctness, edge cases, nil handling, API contracts, test adequacy/integrity.", personas[0].Description)
	assert.Equal(t, "# Correctness\n\nYou are a senior software engineer reviewing for correctness.", personas[0].Body,
		"the body is trimmed by parsePiAgent; the role renderer adds the final newline")
	assert.Equal(t, "name: correctness\n"+
		"description: Evaluates logic correctness, edge cases, nil handling, API contracts, test adequacy/integrity.\n"+
		"model: opus\ntools: Read, Grep, Glob, Bash\n", string(personas[0].Frontmatter),
		"the raw frontmatter, for the dropped-keys diagnostic")
	// The pi fields are untouched.
	assert.Equal(t, "opus", personas[0].Model)
	assert.Equal(t, []string{"Read", "Grep", "Glob", "Bash"}, personas[0].Tools)
}

// The role shape the pinned CLI reads: three keys, each a basic string
// (multi-line when the value holds a newline, as config.toml's
// developer_instructions), nothing else. No model line in this PR (ADR 0126's
// per-persona model arrives with delegation, #6970), and no dropped
// frontmatter turned into text.
func TestCodexRole_TOML(t *testing.T) {
	t.Parallel()

	role := codexRole{
		Name:         "correctness",
		Description:  "Evaluates logic correctness, edge cases, nil handling, API contracts, test adequacy/integrity.",
		Instructions: "# Correctness\n\nYou are a senior software engineer reviewing for correctness.\n",
	}
	want := "name = \"correctness\"\n" +
		"description = \"Evaluates logic correctness, edge cases, nil handling, API contracts, test adequacy/integrity.\"\n" +
		"developer_instructions = \"\"\"\n# Correctness\n\nYou are a senior software engineer reviewing for correctness.\n\"\"\"\n"
	assert.Equal(t, want, string(role.toml()))
}

func TestCodexRole_TOMLEscapesTheBody(t *testing.T) {
	t.Parallel()

	got := string(codexRole{Name: "x", Description: `say "hi"`, Instructions: "line\n\"\"\"\\end"}.toml())
	assert.Equal(t, "name = \"x\"\ndescription = \"say \\\"hi\\\"\"\ndeveloper_instructions = \"\"\"\nline\n\\\"\\\"\\\"\\\\end\"\"\"\n", got,
		"a persona body is arbitrary markdown; codexTOMLString keeps it inside one basic string, multi-line when it holds a newline")
	assert.NotContains(t, got, "model", "no model line in this PR: ADR 0126's per-persona model arrives with delegation (#6970)")
}

func codexRoleNames(roles []codexRole) []string {
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.Name)
	}
	return names
}

// A delegating agent always has default and explore registered (ADR 0126):
// retro dispatches unnamed children through default, and Explore maps to
// explore (#6970).
func TestCodexRoles_BuiltinsAlwaysPresent(t *testing.T) {
	t.Parallel()

	for name, dirs := range map[string][]string{
		"no skill dirs":              nil,
		"an empty entry":             {""},
		"a skill without sub-agents": {t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			roles := codexRoles(dirs, "retro")
			assert.Equal(t, []string{"default", "explore"}, codexRoleNames(roles))
			for _, r := range roles {
				assert.NotEmpty(t, r.Description, "%s: the parent sees the description in the spawn tool's role list", r.Name)
				assert.NotEmpty(t, r.Instructions, "%s", r.Name)
				assert.True(t, strings.HasSuffix(r.Instructions, "\n"), "%s: instructions end with one newline like a file", r.Name)
			}
		})
	}
}

func TestCodexRoles_SkillPersonasSorted(t *testing.T) {
	t.Parallel()
	skill := t.TempDir()
	writePersonaFile(t, skill, "style", "---\nname: style\ndescription: Style review.\n---\nReview for style.\n")
	writePersonaFile(t, skill, "correctness", codexTestPersona)

	roles := codexRoles([]string{skill}, "review")
	assert.Equal(t, []string{"correctness", "default", "explore", "style"}, codexRoleNames(roles),
		"ascending by name, built-ins included; the spawn digest env lists roles in this order")
	assert.Equal(t, codexRole{
		Name:         "correctness",
		Description:  "Evaluates logic correctness, edge cases, nil handling, API contracts, test adequacy/integrity.",
		Instructions: codexChildNote + "# Correctness\n\nYou are a senior software engineer reviewing for correctness.\n",
	}, roles[0])
	assert.Equal(t, codexChildNote+"Review for style.\n", roles[3].Instructions)
}

// A file discoverPersonas skips (reserved name, bad shape) is a warning here
// too: Bootstrap never fails over a stray sub-agents/*.md.
func TestCodexRoles_DiscoverySkipIsAWarning(t *testing.T) {
	skill := t.TempDir()
	writePersonaFile(t, skill, "default", "---\nname: default\ndescription: Not allowed.\n---\nbody\n")
	writePersonaFile(t, skill, "Bad", "---\nname: Bad\ndescription: Bad shape.\n---\nbody\n")

	var roles []codexRole
	out := captureStderr(t, func() { roles = codexRoles([]string{skill}, "review") })

	assert.Equal(t, []string{"default", "explore"}, codexRoleNames(roles))
	assert.Contains(t, out, fmt.Sprintf("Warning: persona %q skipped: name is reserved", filepath.Join(skill, "sub-agents", "default.md")))
	assert.Contains(t, out, fmt.Sprintf("Warning: persona %q skipped: name must be lowercase alphanumeric", filepath.Join(skill, "sub-agents", "Bad.md")))
}

// One row per rule a persona can break on Codex. Every row is a warning,
// never an error, and the built-ins are always left standing.
func TestCodexRoles_SkipsInvalidPersonas(t *testing.T) {
	tests := []struct {
		name      string
		file      string // persona basename
		content   string
		wantWarn  string
		wantRoles []string
	}{
		{name: "reserved: enabled is the [agents] table key", file: "enabled",
			content:  "---\nname: enabled\ndescription: d\n---\nbody\n",
			wantWarn: `persona "enabled" skipped: name is reserved on codex (enabled, explorer, worker)`},
		{name: "reserved: explorer is a codex built-in", file: "explorer",
			content:  "---\nname: explorer\ndescription: d\n---\nbody\n",
			wantWarn: `persona "explorer" skipped: name is reserved on codex`},
		{name: "reserved: worker is a codex built-in", file: "worker",
			content:  "---\nname: worker\ndescription: d\n---\nbody\n",
			wantWarn: `persona "worker" skipped: name is reserved on codex`},
		{name: "empty description", file: "quiet",
			content:  "---\nname: quiet\n---\nbody\n",
			wantWarn: `persona "quiet" skipped: frontmatter description: is required`},
		{name: "empty body", file: "hollow",
			content:  "---\nname: hollow\ndescription: d\n---\n",
			wantWarn: `persona "hollow" skipped: body is empty`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skill := t.TempDir()
			writePersonaFile(t, skill, tt.file, tt.content)

			var roles []codexRole
			out := captureStderr(t, func() { roles = codexRoles([]string{skill}, "review") })

			assert.Equal(t, []string{"default", "explore"}, codexRoleNames(roles))
			assert.Contains(t, out, tt.wantWarn)
		})
	}
}

// Two skills declaring one name: the first wins (discoverPersonas), the
// second is a warning, and exactly one role file is rendered (#6970).
func TestCodexRoles_DuplicateAcrossSkillsKeepsTheFirst(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writePersonaFile(t, first, "correctness", codexTestPersona)
	writePersonaFile(t, second, "correctness", "---\nname: correctness\ndescription: Other.\n---\nOther body.\n")

	var roles []codexRole
	out := captureStderr(t, func() { roles = codexRoles([]string{first, second}, "review") })

	assert.Equal(t, []string{"correctness", "default", "explore"}, codexRoleNames(roles))
	assert.Equal(t, codexChildNote+"# Correctness\n\nYou are a senior software engineer reviewing for correctness.\n", roles[0].Instructions)
	assert.Contains(t, out, "skipped: name already declared by skill")
}

// Claude-specific frontmatter is reported, never applied and never turned
// into prompt text (#6970): one line per persona, naming the keys
// present among the five, and no line for a persona that sets none.
func TestCodexRoles_ReportsDroppedFrontmatter(t *testing.T) {
	skill := t.TempDir()
	writePersonaFile(t, skill, "all-five", "---\nname: all-five\ndescription: d\nmodel: opus\ntools: Read, Grep\n"+
		"permissionMode: bypassPermissions\nbackground: true\nmaxTurns: 20\n---\nbody\n")
	writePersonaFile(t, skill, "two", "---\nname: two\ndescription: d\nmaxTurns: 3\nmodel: haiku\n---\nbody\n")
	writePersonaFile(t, skill, "clean", "---\nname: clean\ndescription: d\n---\nbody\n")

	var roles []codexRole
	out := captureStderr(t, func() { roles = codexRoles([]string{skill}, "review") })

	assert.Contains(t, out, `Codex persona "all-five": frontmatter model, tools, permissionMode, background, maxTurns dropped`)
	assert.Contains(t, out, `Codex persona "two": frontmatter model, maxTurns dropped`, "fixed order, not file order")
	assert.NotContains(t, out, `Codex persona "clean"`)
	assert.Equal(t, 2, strings.Count(out, "dropped"), "one line per persona that sets any of the five keys")
	for _, r := range roles {
		assert.NotContains(t, r.Instructions, "opus", "%s: a dropped value never becomes prompt text", r.Name)
		assert.NotContains(t, r.Instructions, "permissionMode", "%s", r.Name)
	}
}

// The keys reported are the registered file's: a same-named file an earlier
// skill holds that discovery skipped is not the one described.
func TestCodexRoles_DroppedFrontmatterIsTheRegisteredFile(t *testing.T) {
	withModel := "---\nname: %s\ndescription: d\nmodel: opus\n---\nbody\n"
	plain := "---\nname: %s\ndescription: d\n---\nbody\n"
	for name, tc := range map[string]struct{ skipped, registered, want string }{
		"the skipped file sets a key":    {fmt.Sprintf(withModel, "other"), fmt.Sprintf(plain, "correctness"), ""},
		"the registered file sets a key": {fmt.Sprintf(plain, "other"), fmt.Sprintf(withModel, "correctness"), `Codex persona "correctness": frontmatter model dropped`},
	} {
		t.Run(name, func(t *testing.T) {
			skipped := t.TempDir()
			writePersonaFile(t, skipped, "correctness", tc.skipped)
			registered := t.TempDir()
			writePersonaFile(t, registered, "correctness", tc.registered)

			out := captureStderr(t, func() { codexRoles([]string{skipped, registered}, "review") })

			assert.Contains(t, out, "skipped: frontmatter name")
			if tc.want == "" {
				assert.NotContains(t, out, `Codex persona "correctness"`)
			} else {
				assert.Contains(t, out, tc.want)
			}
		})
	}
}

func TestCodexDroppedFrontmatterKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
		want []string
	}{
		{name: "no frontmatter"},
		{name: "keys in fixed order", data: "maxTurns: 1\nmodel: opus\n", want: []string{"model", "maxTurns"}},
		{name: "a null value still counts as present", data: "model:\n", want: []string{"model"}},
		{name: "unrelated keys are ignored", data: "name: x\ndescription: y\n"},
		{name: "scalar yaml reports nothing", data: "scalar\n"},
		{name: "malformed yaml reports nothing", data: "model: [\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, codexDroppedFrontmatterKeys([]byte(tt.data)))
		})
	}
}

// codexDecodeTOML parses data with the stdlib tomllib, the parser codex's own
// loader has to agree with (as TestRenderCodexConfig_ParsesAsTOML does).
func codexDecodeTOML(t *testing.T, data []byte) map[string]any {
	t.Helper()
	cmd := exec.Command(pythonWithTomllib(t), "-c", "import json, sys, tomllib; print(json.dumps(tomllib.loads(sys.stdin.read())))")
	cmd.Stdin = strings.NewReader(string(data))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))
	return doc
}

func TestCodexRole_TOMLParsesWithTheThreeKeys(t *testing.T) {
	skill := t.TempDir()
	writePersonaFile(t, skill, "correctness", codexTestPersona)

	for _, role := range codexRoles([]string{skill}, "review") {
		doc := codexDecodeTOML(t, role.toml())
		assert.Len(t, doc, 3, "%s: name, description, developer_instructions and nothing else", role.Name)
		assert.Equal(t, role.Name, doc["name"])
		assert.Equal(t, role.Description, doc["description"])
		assert.Equal(t, role.Instructions, doc["developer_instructions"], "%s: the body round-trips", role.Name)
	}
}

// TestCodexCatalog reads the fixture reduced from the pinned CLI's dump and
// pins the two questions Bootstrap asks it: is a model listed, and does
// codex resolve it to collaboration V1. On a CODEX_VERSION bump regenerate
// it, never edit it (the file name carries the pin):
//
//	codex debug models --bundled | python3 -c 'import json,sys; d=json.load(sys.stdin); json.dump({"models":[{"slug":m["slug"],"multi_agent_version":m.get("multi_agent_version")} for m in d["models"]]}, sys.stdout, indent=2); print()' > testdata/codex/models-bundled-<pin>.json
func TestCodexCatalog(t *testing.T) {
	t.Parallel()
	containerfile, err := os.ReadFile(filepath.Join("..", "..", "images", "sandbox", "Containerfile"))
	require.NoError(t, err)
	pin := regexp.MustCompile(`(?m)^ARG CODEX_VERSION=(\S+)$`).FindStringSubmatch(string(containerfile))
	require.Len(t, pin, 2)
	data, err := os.ReadFile("testdata/codex/models-bundled-" + pin[1] + ".json")
	require.NoError(t, err, "the fixture is named after the pin: regenerate it for %s", pin[1])
	catalog, err := parseCodexCatalog(data)
	require.NoError(t, err)

	assert.True(t, catalog.has("gpt-5.6-luna"))
	assert.True(t, catalog.resolvesV1("gpt-5.6-luna"), "the floor model is the listed V1 model on 0.159.3")
	assert.True(t, catalog.has("gpt-5.6-sol"))
	assert.False(t, catalog.resolvesV1("gpt-5.6-sol"), "sol selects V2")
	assert.True(t, catalog.has("gpt-5.5"))
	assert.True(t, catalog.resolvesV1("gpt-5.5"), "an entry without a version takes V1 from the multi_agent feature")
	assert.False(t, catalog.has("gpt-5.6-nova"))
	assert.False(t, catalog.resolvesV1("gpt-5.6-nova"))
	// Hidden entries count: the fixture keeps no `visibility`, so the hidden
	// V1 entry resolves to V1 like a listed one and a hidden model is listed.
	assert.True(t, catalog.has("codex-auto-review"))
	assert.True(t, catalog.resolvesV1("codex-auto-review"), "a hidden entry that selects V1 resolves to V1")
	assert.True(t, catalog.has("gpt-daybreak-blue-latest"), "a hidden model passes the membership check")
	assert.False(t, catalog.resolvesV1("gpt-daybreak-blue-latest"), "hidden and V2")

	_, err = parseCodexCatalog([]byte("not json"))
	require.Error(t, err)
	_, err = parseCodexCatalog([]byte(`{"models": []}`))
	require.Error(t, err, "an empty catalog is a broken binary, not a catalog")
}

// TestCodexParentResolvesV1 is part two of the ADR 0126 gate: the parent model
// Run will use (the run-level model, else the definition's) must translate to
// an OpenAI id codex resolves to V1: its entry selects V1 or carries no
// version (codex's multi_agent feature, on by default, supplies V1). A V2 or
// unlisted parent keeps the multi-agent tools off and says why in one line.
func TestCodexParentResolvesV1(t *testing.T) {
	t.Parallel()
	catalog := codexCatalog{"gpt-5.6-luna": "v1", "gpt-5.6-sol": "v2", "gpt-5.5": "", "gpt-x": "v3"}
	tests := []struct {
		name        string
		parentModel string
		agentModel  string
		want        bool
		wantReason  string
	}{
		{"definition model, V1", "", "openai/gpt-5.6-luna", true, ""},
		{"run-level model wins", "openai/gpt-5.6-luna", "openai/gpt-5.6-sol", true, ""},
		{"run-level V2 beats a V1 definition", "openai/gpt-5.6-sol", "openai/gpt-5.6-luna", false, `parent model "gpt-5.6-sol" selects collaboration "v2"`},
		{"no multi-agent version (multi_agent supplies V1)", "", "gpt-5.5", true, ""},
		{"unknown collaboration version", "", "openai/gpt-x", false, `parent model "gpt-x" selects collaboration "v3"`},
		{"not in the catalog", "", "openai/gpt-5.6-nova", false, `parent model "gpt-5.6-nova" is not in the bundled catalog`},
		{"gateway route, the id is asked of the catalog", "", "gateway/vendor/org/model", false, `parent model "vendor/org/model" is not in the bundled catalog`},
		{"claude alias does not translate", "", "opus", false, "codex takes OpenAI model ids or gateway/<model> only"},
		{"no model at all", "", "", false, "no model was named"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, reason := codexParentResolvesV1(catalog, tt.parentModel, tt.agentModel)
			assert.Equal(t, tt.want, got)
			if tt.want {
				assert.Empty(t, reason)
			} else {
				assert.Contains(t, reason, tt.wantReason)
			}
		})
	}
}
