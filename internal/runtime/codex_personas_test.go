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
// developer_instructions), nothing else. No model line without a subagents
// entry (TestCodexRole_TOMLModelLine), and no dropped frontmatter turned into
// text.
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
	assert.NotContains(t, got, "model", "no model line without a subagents entry")
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
			roles, _ := codexRoles(dirs, "retro")
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

	roles, _ := codexRoles([]string{skill}, "review")
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
	var skipped map[string]string
	out := captureStderr(t, func() { roles, skipped = codexRoles([]string{skill}, "review") })

	assert.Equal(t, []string{"default", "explore"}, codexRoleNames(roles))
	assert.Equal(t, filepath.Join(skill, "sub-agents", "default.md")+": name is reserved", skipped["default"],
		"a discovery skip is recorded with its file, for the subagents key error")
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
			var skipped map[string]string
			out := captureStderr(t, func() { roles, skipped = codexRoles([]string{skill}, "review") })

			assert.Equal(t, []string{"default", "explore"}, codexRoleNames(roles))
			assert.Contains(t, out, tt.wantWarn)
			require.Contains(t, skipped, tt.file)
			assert.Contains(t, out, fmt.Sprintf("persona %q skipped: %s\n", tt.file, skipped[tt.file]), "the recorded reason is the warned one")
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
	out := captureStderr(t, func() { roles, _ = codexRoles([]string{first, second}, "review") })

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
	out := captureStderr(t, func() { roles, _ = codexRoles([]string{skill}, "review") })

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

	roles, _ := codexRoles([]string{skill}, "review")
	for _, role := range roles {
		doc := codexDecodeTOML(t, role.toml())
		assert.Len(t, doc, 3, "%s: name, description, developer_instructions and nothing else", role.Name)
		assert.Equal(t, role.Name, doc["name"])
		assert.Equal(t, role.Description, doc["description"])
		assert.Equal(t, role.Instructions, doc["developer_instructions"], "%s: the body round-trips", role.Name)
	}
}

// TestCodexCatalog reads the fixture reduced from the pinned CLI's dump and
// pins the questions Bootstrap asks it: is a model listed, does codex
// resolve it to collaboration V1, and which efforts a child on it may run
// at. On a CODEX_VERSION bump regenerate it, never edit it (the file name
// carries the pin):
//
//	codex debug models --bundled | python3 -c 'import json,sys; d=json.load(sys.stdin); json.dump({"models":[{"slug":m["slug"],"multi_agent_version":m.get("multi_agent_version"),"default_reasoning_level":m.get("default_reasoning_level"),"supported_reasoning_levels":[{"effort":l["effort"]} for l in m.get("supported_reasoning_levels") or []]} for m in d["models"]]}, sys.stdout, indent=2); print()' > testdata/codex/models-bundled-<pin>.json
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

	assert.True(t, catalog.has(codexDefaultSubagentModel), "the floor must be listed; a child takes its parent's collaboration version, so the floor's own does not matter")
	assert.True(t, catalog.resolvesV1("gpt-5.6-luna"), "a luna parent delegates on 0.159.3")
	assert.Equal(t, "medium", catalog["gpt-5.6-luna"].DefaultEffort, "children on the floor run at medium when the run sets no effort")
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, catalog["gpt-5.6-luna"].Efforts)
	assert.Equal(t, "low", catalog["gpt-5.6-sol"].DefaultEffort)
	assert.NotContains(t, catalog["gpt-5.5"].Efforts, "max", "the one fullsend effort a listed model lacks at the pin")
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
// unlisted parent keeps the multi-agent tools off and says why in one line,
// and so does a gateway/ parent whatever its id.
func TestCodexParentResolvesV1(t *testing.T) {
	t.Parallel()
	catalog := codexVersionsCatalog(map[string]string{"gpt-5.6-luna": "v1", "gpt-5.6-sol": "v2", "gpt-5.5": "", "gpt-x": "v3"})
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
		{"gateway route declines a listed V1 id", "", "gateway/gpt-5.6-luna", false, `parent model "gateway/gpt-5.6-luna" takes the inference gateway route`},
		{"gateway route declines before the catalog", "", "gateway/vendor/org/model", false, `parent model "gateway/vendor/org/model" takes the inference gateway route`},
		{"run-level OpenAI model beats a gateway definition", "openai/gpt-5.6-luna", "gateway/vendor/org/model", true, ""},
		{"run-level gateway route beats a V1 definition", "gateway/vendor/org/model", "openai/gpt-5.6-luna", false, `parent model "gateway/vendor/org/model" takes the inference gateway route`},
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

// TestCodexChildModel pins the check a configured child model gets: an
// OpenAI id, bare or prefixed; everything else, a gateway/ spec and
// `inherit` included, fails naming the key and the entry's own fix.
func TestCodexChildModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		in      string
		want    string
		wantErr []string
	}{
		{name: "bare id", key: "subagents.correctness", in: "gpt-5.6-sol", want: "gpt-5.6-sol"},
		{name: "openai prefix stripped", key: "subagents.correctness", in: "openai/gpt-5.6-sol", want: "gpt-5.6-sol"},
		{name: "whitespace trimmed", key: "subagents.default", in: "  openai/gpt-6-astra ", want: "gpt-6-astra"},
		{name: "another provider names the key and the tombstone", key: "subagents.correctness",
			in: "xai-vertex/xai/grok-4.6", wantErr: []string{"subagents.correctness", `"xai-vertex/xai/grok-4.6"`, "correctness: ~"}},
		{name: "google vertex on default", key: "subagents.default",
			in: "google-vertex/gemini-3.8-flash", wantErr: []string{"subagents.default", `"google-vertex/gemini-3.8-flash"`, "default: ~"}},
		{name: "claude alias", key: "subagents.security", in: "opus",
			wantErr: []string{"subagents.security", `"opus"`, "security: ~"}},
		{name: "gateway route names the key", key: "subagents.correctness", in: "gateway/vendor/org/model",
			wantErr: []string{"subagents.correctness", `"gateway/vendor/org/model"`, "correctness: ~"}},
		{name: "gateway route with an empty id", key: "subagents.correctness", in: "gateway/",
			wantErr: []string{"subagents.correctness", `"gateway/"`, "correctness: ~"}},
		{name: "inherit is refused", key: "subagents.correctness", in: "inherit",
			wantErr: []string{"subagents.correctness", `"inherit"`, "correctness: ~"}},
		{name: "inherit behind the prefix, any case", key: "subagents.correctness", in: "openai/Inherit",
			wantErr: []string{`"openai/Inherit"`}},
		{name: "empty value", key: "subagents.correctness", in: "", wantErr: []string{"subagents.correctness", `""`, "correctness: ~"}},
		{name: "the runner variable says unset, not tombstone", key: codexSubagentModelEnv, in: "haiku",
			wantErr: []string{"FULLSEND_CODEX_SUBAGENT_MODEL", `"haiku"`, "unset"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := codexChildModel(tt.key, tt.in)
			if len(tt.wantErr) > 0 {
				require.Error(t, err)
				for _, w := range tt.wantErr {
					assert.Contains(t, err.Error(), w)
				}
				assert.NotContains(t, err.Error(), "FULLSEND_CODEX_MODEL=", "the fix names the entry, not the run's own model")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Every env × subagents.default × persona-entry combination of the chain:
// only a persona with its own entry gets a role-file model; the second value
// is the run-level default every other child takes.
func TestResolveCodexChildModels_Precedence(t *testing.T) {
	t.Parallel()

	const (
		absent    = "absent"
		set       = "set"
		tombstone = "tombstone"
	)
	tests := []struct {
		name        string
		env         string
		defaultSet  bool
		persona     string
		wantModel   string // correctness's role-file model; "" = no model line
		wantDefault string
	}{
		{"nothing configured", "", false, absent, "", "gpt-5.6-luna"},
		{"persona entry only", "", false, set, "gpt-5.6-sol", "gpt-5.6-luna"},
		{"persona tombstone only", "", false, tombstone, "", "gpt-5.6-luna"},
		{"default only", "", true, absent, "", "gpt-6-astra"},
		{"default and persona entry", "", true, set, "gpt-5.6-sol", "gpt-6-astra"},
		{"default and persona tombstone", "", true, tombstone, "", "gpt-6-astra"},
		{"env only", "openai/gpt-5.6-terra", false, absent, "", "gpt-5.6-terra"},
		{"env and persona entry", "openai/gpt-5.6-terra", false, set, "gpt-5.6-sol", "gpt-5.6-terra"},
		{"env and persona tombstone", "openai/gpt-5.6-terra", false, tombstone, "", "gpt-5.6-terra"},
		{"env beats default", "openai/gpt-5.6-terra", true, absent, "", "gpt-5.6-terra"},
		{"env, default and persona entry", "openai/gpt-5.6-terra", true, set, "gpt-5.6-sol", "gpt-5.6-terra"},
		{"env, default and persona tombstone", "openai/gpt-5.6-terra", true, tombstone, "", "gpt-5.6-terra"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			roles := []codexRole{
				{Name: "correctness", Description: "d", Instructions: "i"},
				{Name: "default", Description: "d", Instructions: "i"},
				{Name: "explore", Description: codexExploreRoleDescription, Instructions: "i"},
			}
			cfg := map[string]*string{}
			if tt.defaultSet {
				cfg["default"] = strp("gpt-6-astra")
			}
			switch tt.persona {
			case set:
				cfg["correctness"] = strp("openai/gpt-5.6-sol")
			case tombstone:
				cfg["correctness"] = nil
			}

			got, gotDefault, _, err := resolveCodexChildModels(roles, nil, cfg, tt.env, "", codexTestCatalog())
			require.NoError(t, err)
			require.Len(t, got, 3)
			assert.Equal(t, tt.wantModel, got[0].Model, "correctness")
			assert.Empty(t, got[1].Model, "default never carries a model line: it takes agents.default_subagent_model")
			assert.Empty(t, got[2].Model, "explore never carries a model line")
			assert.Equal(t, tt.wantDefault, gotDefault)
			assert.Empty(t, roles[0].Model, "the input slice is not mutated")
			for i := range got {
				assert.Equal(t, roles[i].Name, got[i].Name, "order and names are kept")
			}
		})
	}
}

// A subagents key must name a discovered persona or `default`; anything else,
// `explore` included (a reserved key), fails Bootstrap and names the key, as
// pi does. A tombstoned unknown key is skipped, as on pi, because a tombstone
// only removes an inherited entry.
func TestResolveCodexChildModels_RejectsUnknownKeys(t *testing.T) {
	t.Parallel()

	roles := []codexRole{{Name: "correctness"}, {Name: "default"}, {Name: "explore"}}
	worker := map[string]string{"worker": "name is reserved on codex (enabled, explorer, worker)"}
	tests := []struct {
		name    string
		roles   []codexRole
		skipped map[string]string
		cfg     map[string]*string
		wantErr []string
		notErr  string
	}{
		{name: "explore is reserved", roles: roles, cfg: map[string]*string{"explore": strp("openai/gpt-5.6-sol")},
			wantErr: []string{"subagents.explore", "built-in role", "correctness"}},
		{name: "a typo", roles: roles, cfg: map[string]*string{"correctnes": strp("openai/gpt-5.6-sol")},
			wantErr: []string{"subagents.correctnes", `no persona "correctnes" is registered`, "correctness"}},
		{name: "no persona at all", roles: nil, cfg: map[string]*string{"correctness": strp("openai/gpt-5.6-sol")},
			wantErr: []string{"subagents.correctness", "none"}},
		{name: "a tombstoned unknown key is skipped", roles: roles, cfg: map[string]*string{"correctnes": nil}},
		{name: "default is not a persona key error", roles: nil, cfg: map[string]*string{"default": strp("openai/gpt-5.6-sol")}},
		{name: "a skipped persona's key carries the reason", roles: roles, skipped: worker,
			cfg:     map[string]*string{"worker": strp("openai/gpt-5.6-sol")},
			wantErr: []string{`subagents.worker: persona "worker" was not registered: name is reserved on codex (enabled, explorer, worker)`},
			notErr:  "registered personas"},
		{name: "a skipped persona is named only when the key matches it", roles: roles, skipped: worker,
			cfg:     map[string]*string{"correctnes": strp("openai/gpt-5.6-sol")},
			wantErr: []string{`no persona "correctnes" is registered`}, notErr: "worker"},
		{name: "explore stays a built-in role over a skipped explore.md", roles: roles,
			skipped: map[string]string{"explore": "/s/sub-agents/explore.md: name is reserved"},
			cfg:     map[string]*string{"explore": strp("openai/gpt-5.6-sol")},
			wantErr: []string{"subagents.explore", "built-in role"}, notErr: "was not registered"},
		{name: "a tombstoned skipped key is skipped", roles: roles, skipped: worker, cfg: map[string]*string{"worker": nil}},
		{name: "a duplicate skipped in a later skill does not hide the registered persona", roles: roles,
			skipped: map[string]string{"correctness": `/b/sub-agents/correctness.md: name already declared by skill "a"`},
			cfg:     map[string]*string{"correctness": strp("openai/gpt-5.6-sol")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, err := resolveCodexChildModels(tt.roles, tt.skipped, tt.cfg, "", "", codexTestCatalog())
			if len(tt.wantErr) == 0 {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, w := range tt.wantErr {
				assert.Contains(t, err.Error(), w)
			}
			if tt.notErr != "" {
				assert.NotContains(t, err.Error(), tt.notErr)
			}
		})
	}
}

// Every configured value is checked, whichever position of the chain wins:
// a non-OpenAI value fails Bootstrap and names the key (#6970).
func TestResolveCodexChildModels_NonOpenAIValueNamesTheKey(t *testing.T) {
	t.Parallel()

	roles := []codexRole{{Name: "correctness"}, {Name: "default"}, {Name: "explore"}}
	tests := []struct {
		name    string
		env     string
		cfg     map[string]*string
		wantErr []string
	}{
		{name: "persona from xai vertex", cfg: map[string]*string{"correctness": strp("xai-vertex/xai/grok-4.6")},
			wantErr: []string{"subagents.correctness", `"xai-vertex/xai/grok-4.6"`, "correctness: ~"}},
		{name: "default from google vertex", cfg: map[string]*string{"default": strp("google-vertex/gemini-3.8-flash")},
			wantErr: []string{"subagents.default", `"google-vertex/gemini-3.8-flash"`}},
		{name: "default is checked even when env wins", env: "openai/gpt-5.6-terra",
			cfg: map[string]*string{"default": strp("google-vertex/gemini-3.8-flash")}, wantErr: []string{"subagents.default"}},
		{name: "runner variable", env: "opus", wantErr: []string{"FULLSEND_CODEX_SUBAGENT_MODEL", `"opus"`, "unset"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, err := resolveCodexChildModels(roles, nil, tt.cfg, tt.env, "", codexTestCatalog())
			require.Error(t, err)
			for _, w := range tt.wantErr {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}

// codexTestCatalog lists every model the tables above use, with the versions,
// default efforts and supported efforts of
// testdata/codex/models-bundled-0.159.3.json; add any id a new row introduces.
func codexTestCatalog() codexCatalog {
	upToMax := []string{"low", "medium", "high", "xhigh", "max"}
	upToUltra := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	return codexCatalog{
		"gpt-5.6-luna":  {Version: "v1", DefaultEffort: "medium", Efforts: upToMax},
		"gpt-5.6-sol":   {Version: "v2", DefaultEffort: "low", Efforts: upToUltra},
		"gpt-5.6-terra": {Version: "v2", DefaultEffort: "medium", Efforts: upToUltra},
		"gpt-6-astra":   {Version: "v2", DefaultEffort: "low", Efforts: upToUltra},
		"gpt-5.5":       {Version: "", DefaultEffort: "medium", Efforts: []string{"low", "medium", "high", "xhigh"}},
	}
}

// codexVersionsCatalog is a catalog with versions only, for the gate's tests.
func codexVersionsCatalog(versions map[string]string) codexCatalog {
	c := make(codexCatalog, len(versions))
	for slug, v := range versions {
		c[slug] = codexCatalogModel{Version: v}
	}
	return c
}

// Every chain position is checked against the catalog, the floor included;
// the error names the position that supplied the id.
func TestResolveCodexChildModels_CatalogMembership(t *testing.T) {
	t.Parallel()
	catalog := codexVersionsCatalog(map[string]string{"gpt-5.6-luna": "v1", "gpt-5.6-sol": "v2", "gpt-6-astra": "v2"})
	sol, nova := "openai/gpt-5.6-sol", "openai/gpt-5.6-nova"
	roles := []codexRole{{Name: "correctness"}, {Name: "default"}, {Name: "explore"}}
	tests := []struct {
		name      string
		subagents map[string]*string
		env       string
		catalog   codexCatalog
		wantErr   string
	}{
		{"listed everywhere", map[string]*string{"correctness": &sol, "default": &sol}, "gpt-6-astra", catalog, ""},
		{"persona entry unlisted", map[string]*string{"correctness": &nova}, "", catalog, `subagents.correctness: model "gpt-5.6-nova" is not in the bundled catalog of the pinned codex`},
		{"default unlisted", map[string]*string{"default": &nova}, "", catalog, `subagents.default: model "gpt-5.6-nova" is not in the bundled catalog of the pinned codex`},
		{"runner variable unlisted", nil, "gpt-5.6-nova", catalog, `FULLSEND_CODEX_SUBAGENT_MODEL: model "gpt-5.6-nova" is not in the bundled catalog of the pinned codex`},
		{"floor unlisted after a CLI bump", nil, "", codexVersionsCatalog(map[string]string{"gpt-5.6-sol": "v2"}), `built-in default: model "gpt-5.6-luna" is not in the bundled catalog of the pinned codex`},
		{"floor unlisted but overridden", map[string]*string{"default": &sol}, "", codexVersionsCatalog(map[string]string{"gpt-5.6-sol": "v2"}), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, err := resolveCodexChildModels(roles, nil, tt.subagents, tt.env, "", tt.catalog)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestResolveCodexChildModels_Effort pins the child effort: the run's effort
// when set, else the run-level child model's catalog default (codex resets a
// child to it); either is checked against the run-level child model (the
// run's effort only) and every persona model, as codex checks them at
// spawn, and the error names the position that chose the model.
func TestResolveCodexChildModels_Effort(t *testing.T) {
	t.Parallel()
	gpt55, luna, sol := "openai/gpt-5.5", "openai/gpt-5.6-luna", "openai/gpt-5.6-sol"
	roles := []codexRole{{Name: "correctness"}, {Name: "default"}, {Name: "explore"}}
	ultraDefault := codexTestCatalog()
	ultraDefault["gpt-x-ultra"] = codexCatalogModel{Version: "v2", DefaultEffort: "ultra", Efforts: []string{"ultra"}}
	noLevels := codexTestCatalog()
	noLevels["gpt-5.6-luna"] = codexCatalogModel{Version: "v1", DefaultEffort: "medium"}
	tests := []struct {
		name       string
		subagents  map[string]*string
		env        string
		runEffort  string
		catalog    codexCatalog
		wantEffort string
		wantErr    string
	}{
		{name: "no run effort: no key", catalog: codexTestCatalog()},
		{name: "run effort high on luna", runEffort: "high", catalog: codexTestCatalog(), wantEffort: "high"},
		{name: "run effort trimmed and lower-cased", runEffort: " High ", catalog: codexTestCatalog(), wantEffort: "high"},
		{name: "max with subagents.default gpt-5.5", subagents: map[string]*string{"default": &gpt55}, runEffort: "max", catalog: codexTestCatalog(),
			wantErr: `subagents.default: model "gpt-5.5" does not support reasoning effort "max" (the run's effort)`},
		{name: "max with the runner variable gpt-5.5", env: gpt55, runEffort: "max", catalog: codexTestCatalog(),
			wantErr: `FULLSEND_CODEX_SUBAGENT_MODEL: model "gpt-5.5" does not support reasoning effort "max"`},
		{name: "max with a beaten subagents.default gpt-5.5", subagents: map[string]*string{"default": &gpt55}, env: luna, runEffort: "max",
			catalog: codexTestCatalog(), wantEffort: "max"},
		{name: "max with a persona on gpt-5.5", subagents: map[string]*string{"correctness": &gpt55}, runEffort: "max", catalog: codexTestCatalog(),
			wantErr: `subagents.correctness: model "gpt-5.5" does not support reasoning effort "max" (the run's effort)`},
		{name: "low with a persona on sol", subagents: map[string]*string{"correctness": &sol}, runEffort: "low", catalog: codexTestCatalog(), wantEffort: "low"},
		{name: "no run effort: a persona model lacks the default model's default", subagents: map[string]*string{"default": strp("openai/gpt-x-ultra"), "correctness": &luna},
			catalog: ultraDefault, wantErr: `subagents.correctness: model "gpt-5.6-luna" does not support reasoning effort "ultra" (the catalog default of gpt-x-ultra)`},
		{name: "an entry without supported levels", runEffort: "medium", catalog: noLevels,
			wantErr: `built-in default: model "gpt-5.6-luna" does not support reasoning effort "medium" (the run's effort), which codex would refuse at every spawn; it supports none`},
		{name: "a tombstoned subagents.default falls to the floor", subagents: map[string]*string{"default": nil}, runEffort: "high", catalog: codexTestCatalog(), wantEffort: "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, effort, err := resolveCodexChildModels(roles, nil, tt.subagents, tt.env, tt.runEffort, tt.catalog)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantEffort, effort)
		})
	}
}

// TestCodexRole_TOMLModelLine pins the role file with and without a model:
// keys name, description, [model], developer_instructions, nothing else. The
// bytes without a model are unchanged; the model line is the only difference.
func TestCodexRole_TOMLModelLine(t *testing.T) {
	t.Parallel()

	role := codexRole{
		Name:         "correctness",
		Description:  "Evaluates logic correctness, edge cases, nil handling, API contracts, test adequacy/integrity.",
		Instructions: "# Correctness\n\nYou are a senior software engineer reviewing for correctness.\n",
	}
	without := "name = \"correctness\"\n" +
		"description = \"Evaluates logic correctness, edge cases, nil handling, API contracts, test adequacy/integrity.\"\n" +
		"developer_instructions = \"\"\"\n# Correctness\n\nYou are a senior software engineer reviewing for correctness.\n\"\"\"\n"
	assert.Equal(t, without, string(role.toml()))

	role.Model = "gpt-5.6-sol"
	with := "name = \"correctness\"\n" +
		"description = \"Evaluates logic correctness, edge cases, nil handling, API contracts, test adequacy/integrity.\"\n" +
		"model = \"gpt-5.6-sol\"\n" +
		"developer_instructions = \"\"\"\n# Correctness\n\nYou are a senior software engineer reviewing for correctness.\n\"\"\"\n"
	assert.Equal(t, with, string(role.toml()), "the model line sits between description and developer_instructions")

	role.Model = "gpt-5.6-\"sol\""
	assert.Contains(t, string(role.toml()), "model = \"gpt-5.6-\\\"sol\\\"\"\n", "quoted like every other value")
}
