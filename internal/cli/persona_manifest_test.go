package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// collectWarn returns a warn func and the slice it appends to.
func collectWarn() (func(string), *[]string) {
	var w []string
	return func(s string) { w = append(w, s) }, &w
}

func TestBuildPersonaManifest_OriginsRemovedAndSorting(t *testing.T) {
	src := upstreamSkill(t)
	other := filepath.Join(realTempDir(t), "zeta")
	writeTree(t, other, map[string]string{
		"SKILL.md":             "---\nname: zeta\n---\n",
		"sub-agents/alpha.md":  "no frontmatter at all\n",
		"sub-agents/notes.txt": "ignored\n",
		"sub-agents/deep/x.md": "---\nname: x\n---\n",
		"sub-agents/beta.md":   "---\nname: beta\n---\n",
		"other/ignored.md":     "---\nname: ignored\n---\n",
	})
	override := filepath.Join(realTempDir(t), "security.md")
	const overrideBody = "---\nname: security\n---\nlocal\n"
	require.NoError(t, os.WriteFile(override, []byte(overrideBody), 0o644))
	added := filepath.Join(realTempDir(t), "added.md")
	require.NoError(t, os.WriteFile(added, []byte("---\nname: added\n---\n"), 0o644))

	staged, err := stageSkills([]harness.SkillEntry{
		{Source: other},
		{Source: src, Overrides: map[string]*string{
			"sub-agents/security.md":        strPtr(override),
			"sub-agents/added.md":           strPtr(added),
			"sub-agents/user-experience.md": nil,
			"sub-agents/deep/none.md":       nil,
			"scripts/run.sh":                nil,
		}},
	}, realTempDir(t), func(string) {})
	require.NoError(t, err)

	warn, warnings := collectWarn()
	m, err := buildPersonaManifest(staged, "review", warn)
	require.NoError(t, err)
	assert.Equal(t, 1, m.Version)

	type row struct{ skill, name, path, origin string }
	var rows []row
	for _, p := range m.Personas {
		rows = append(rows, row{p.Skill, p.Name, p.Path, p.Origin})
	}
	assert.Equal(t, []row{
		{"pr-review", "added", "sub-agents/added.md", originAdded},
		{"pr-review", "correctness", "sub-agents/correctness.md", originUpstream},
		{"pr-review", "security", "sub-agents/security.md", originOverride},
		{"zeta", "beta", "sub-agents/beta.md", originUpstream},
	}, rows)
	assert.Equal(t, sha256Hex(overrideBody), m.Personas[2].SHA256)
	assert.Equal(t, []removedPersona{{Skill: "pr-review", Path: "sub-agents/user-experience.md"}}, m.Removed,
		"only sub-agents/*.md removals are listed")
	assert.Equal(t, []string{
		"persona " + filepath.Join(other, "sub-agents", "alpha.md") + " skipped: frontmatter name: is required",
	}, *warnings)
}

func TestBuildPersonaManifest_FrontmatterFields(t *testing.T) {
	dir := filepath.Join(realTempDir(t), "pr-review")
	writeTree(t, dir, map[string]string{
		"sub-agents/full.md": `---
name: full
description: Finds bugs
role: dimension
when: always relevant
categories: [injection, authz]
always: false
failure_severity: high
model: opus
unknown: {nested: true}
---
body
`,
		"sub-agents/bare.md":  "---\nname: bare\n---\n",
		"sub-agents/empty.md": "---\nname: empty\ncategories: []\ndescription: \"\"\n---\n",
		"sub-agents/nulls.md": "---\nname: nulls\nrole: null\nwhen: ~\ncategories:\nalways: null\nfailure_severity: null\n---\n",
	})
	warn, warnings := collectWarn()
	m, err := buildPersonaManifest([]stagedSkill{{Dir: dir}}, "review", warn)
	require.NoError(t, err)
	assert.Empty(t, *warnings, "a YAML null is absent, not the wrong type")

	data, err := json.Marshal(m.Personas)
	require.NoError(t, err)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	require.Len(t, got, 4)

	bare, empty, full, nulls := got[0], got[1], got[2], got[3]
	base := []string{"name", "origin", "path", "sha256", "skill"}
	assert.Equal(t, base, sortedKeys(bare))
	assert.Equal(t, base, sortedKeys(nulls), "null fields are omitted")
	assert.Equal(t, []any{}, empty["categories"])
	assert.Equal(t, "", empty["description"])
	assert.Equal(t, "Finds bugs", full["description"])
	assert.Equal(t, "dimension", full["role"])
	assert.Equal(t, "always relevant", full["when"])
	assert.Equal(t, []any{"injection", "authz"}, full["categories"])
	assert.Equal(t, false, full["always"])
	assert.Equal(t, "high", full["failure_severity"])
	assert.NotContains(t, full, "model")
	assert.NotContains(t, full, "unknown")
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// A pass-through field of the wrong type is dropped from that entry with a
// warning; the persona itself stays registered.
func TestBuildPersonaManifest_WrongTypeFieldsOmitted(t *testing.T) {
	for name, tc := range map[string]struct {
		field, value, want string
	}{
		"role list":           {"role", "[a]", "a string"},
		"description number":  {"description", "5", "a string"},
		"when map":            {"when", "{a: b}", "a string"},
		"failure_sev bool":    {"failure_severity", "true", "a string"},
		"always string":       {"always", `"yes"`, "a bool"},
		"categories map":      {"categories", "{a: b}", "a list of strings"},
		"categories mixed":    {"categories", "[a, 2]", "a list of strings"},
		"categories a string": {"categories", "injection", "a list of strings"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(realTempDir(t), "pr-review")
			writeTree(t, dir, map[string]string{
				"sub-agents/x.md": "---\nname: x\n" + tc.field + ": " + tc.value + "\n---\n",
			})
			warn, warnings := collectWarn()
			m, err := buildPersonaManifest([]stagedSkill{{Dir: dir}}, "review", warn)
			require.NoError(t, err)
			require.Len(t, m.Personas, 1)
			data, err := json.Marshal(m.Personas[0])
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(data, &got))
			assert.NotContains(t, got, tc.field)
			assert.Equal(t, []string{
				`personas.json: pr-review/sub-agents/x.md: frontmatter field "` + tc.field + `" is not ` + tc.want + "; omitted",
			}, *warnings)
		})
	}
}

// Files pi does not register are left out of the manifest with pi's
// warning instead of failing the run.
func TestBuildPersonaManifest_SkippedFilesWarn(t *testing.T) {
	for name, tc := range map[string]struct {
		file, body, reason string
	}{
		"bad yaml":        {"x.md", "---\nname: [unclosed\n---\n", "frontmatter does not parse"},
		"unclosed":        {"x.md", "---\nname: x\n", "frontmatter does not parse"},
		"name not string": {"x.md", "---\nname: [x]\n---\n", "frontmatter does not parse"},
		"no name":         {"x.md", "---\nrole: dimension\n---\n", "frontmatter name: is required"},
		"name mismatch":   {"x.md", "---\nname: y\n---\n", `frontmatter name "y" must equal file basename "x"`},
		"reserved":        {"default.md", "---\nname: default\n---\n", "name is reserved"},
		"agent's own":     {"review.md", "---\nname: review\n---\n", "collides with the agent's own name"},
		"built-in":        {"general-purpose.md", "---\nname: general-purpose\n---\n", "Claude Code built-in agent type"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(realTempDir(t), "pr-review")
			writeTree(t, dir, map[string]string{"sub-agents/" + tc.file: tc.body})
			warn, warnings := collectWarn()
			m, err := buildPersonaManifest([]stagedSkill{{Dir: dir}}, "review", warn)
			require.NoError(t, err)
			assert.Empty(t, m.Personas)
			require.Len(t, *warnings, 1)
			assert.True(t, strings.HasPrefix((*warnings)[0], "persona "+filepath.Join(dir, "sub-agents", tc.file)+" skipped: "), (*warnings)[0])
			assert.Contains(t, (*warnings)[0], tc.reason)
		})
	}
}

// A name two skills declare: the first skill's file wins, the second is
// skipped with pi's warning, and the run goes on.
func TestBuildPersonaManifest_DuplicateNameAcrossSkillsFirstWins(t *testing.T) {
	a := filepath.Join(realTempDir(t), "alpha")
	b := filepath.Join(realTempDir(t), "beta")
	writeTree(t, a, map[string]string{"sub-agents/security.md": "---\nname: security\n---\nalpha\n"})
	writeTree(t, b, map[string]string{"sub-agents/security.md": "---\nname: security\n---\nbeta\n"})
	warn, warnings := collectWarn()
	m, err := buildPersonaManifest([]stagedSkill{{Dir: a}, {Dir: b}}, "review", warn)
	require.NoError(t, err)
	require.Len(t, m.Personas, 1)
	assert.Equal(t, "alpha", m.Personas[0].Skill)
	assert.Equal(t, sha256Hex("---\nname: security\n---\nalpha\n"), m.Personas[0].SHA256)
	assert.Equal(t, []string{
		"persona " + filepath.Join(b, "sub-agents", "security.md") + ` skipped: name already declared by skill "alpha"`,
	}, *warnings)
}

// An override whose frontmatter name differs from the file stem is not
// registered by pi, so the manifest leaves it out too.
func TestBuildPersonaManifest_MismatchedNameMatchesRuntimeRoster(t *testing.T) {
	src := upstreamSkill(t)
	staged, err := stageSkills([]harness.SkillEntry{
		{Source: src, Overrides: map[string]*string{
			"sub-agents/security.md": strPtr(filepath.Join(src, "sub-agents", "correctness.md")),
		}},
	}, realTempDir(t), func(string) {})
	require.NoError(t, err)
	warn, warnings := collectWarn()
	m, err := buildPersonaManifest(staged, "review", warn)
	require.NoError(t, err)

	var manifest []string
	for _, p := range m.Personas {
		manifest = append(manifest, p.Name+"="+p.Path)
	}
	assert.Equal(t, []string{"correctness=sub-agents/correctness.md", "user-experience=sub-agents/user-experience.md"}, manifest)

	roster, _ := agentruntime.DiscoverPersonas(stagedSkillDirs(staged), "review")
	var names []string
	for _, p := range roster {
		names = append(names, p.Name)
	}
	assert.Equal(t, []string{"correctness", "user-experience"}, names, "the manifest is the runtime's roster")
	require.Len(t, *warnings, 1)
	assert.Contains(t, (*warnings)[0], `frontmatter name "correctness" must equal file basename "security"`)
}

func TestBuildPersonaManifest_UnreadableSubAgents(t *testing.T) {
	dir := filepath.Join(realTempDir(t), "pr-review")
	writeTree(t, dir, map[string]string{"sub-agents": "a file, not a directory"})
	m, err := buildPersonaManifest([]stagedSkill{{Dir: dir}}, "review", func(string) {})
	require.NoError(t, err)
	assert.Empty(t, m.Personas)
}

func TestWritePersonaManifest_EmptyRoster(t *testing.T) {
	runDir := realTempDir(t)
	dir := filepath.Join(realTempDir(t), "plain")
	writeTree(t, dir, map[string]string{"SKILL.md": "---\nname: plain\n---\n"})

	path, err := writePersonaManifest([]stagedSkill{{Dir: dir}}, runDir, "review", func(string) {})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(runDir, "personas.json"), path)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "{\n  \"version\": 1,\n  \"personas\": [],\n  \"removed\": []\n}\n", string(data))

	path, err = writePersonaManifest(nil, runDir, "review", func(string) {})
	require.NoError(t, err)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"personas": []`)

	_, err = writePersonaManifest(nil, filepath.Join(runDir, "missing", "dir"), "review", func(string) {})
	require.Error(t, err)
}

func envValue(env []string, key string) (string, int) {
	var val string
	n := 0
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			val = v
			n++
		}
	}
	return val, n
}

func TestWithPersonasFile_RunnerEnvCannotOverride(t *testing.T) {
	runDir := realTempDir(t)
	want := filepath.Join(runDir, "personas.json")
	h := &harness.Harness{RunnerEnv: map[string]string{
		"FULLSEND_PERSONAS_FILE": "/tmp/spoofed.json",
		"OTHER":                  "kept",
	}}

	for name, env := range map[string][]string{
		"post-script": withPersonasFile(postScriptEnv(h, ""), runDir),
		"validation":  validationEnv(h, "/repo", runDir),
	} {
		t.Run(name, func(t *testing.T) {
			got, n := envValue(env, "FULLSEND_PERSONAS_FILE")
			assert.Equal(t, want, got)
			assert.Equal(t, 1, n, "the runner's value is the only one")
			assert.Equal(t, "FULLSEND_PERSONAS_FILE="+want, env[len(env)-1], "appended after runner_env")
			other, _ := envValue(env, "OTHER")
			assert.Equal(t, "kept", other)
		})
	}
}

// TestPersonaManifest_ReadyBeforePreScript stages the skills the way
// runAgent does before the pre-script, then runs a pre-script that
// requires the manifest to exist at FULLSEND_PERSONAS_FILE — even when
// runner_env tries to point the variable elsewhere.
func TestPersonaManifest_ReadyBeforePreScript(t *testing.T) {
	src := upstreamSkill(t)
	runDir := realTempDir(t)
	seen := filepath.Join(realTempDir(t), "seen.json")
	h := &harness.Harness{
		Skills: []harness.SkillEntry{
			{Source: src, Overrides: map[string]*string{"sub-agents/user-experience.md": nil}},
		},
		RunnerEnv: map[string]string{"FULLSEND_PERSONAS_FILE": "/tmp/spoofed.json"},
		PreScript: writePreScript(t, `test -f "${FULLSEND_PERSONAS_FILE}"
cp "${FULLSEND_PERSONAS_FILE}" `+seen+"\n"),
	}
	printer := ui.New(io.Discard)

	_, err := stageRunSkills(h, runDir, "", "review", printer)
	require.NoError(t, err)
	_, err = runPreScript(h, runDir, "", printer)
	require.NoError(t, err)

	data, err := os.ReadFile(seen)
	require.NoError(t, err)
	var m personaManifest
	require.NoError(t, json.Unmarshal(data, &m))
	assert.Equal(t, []removedPersona{{Skill: "pr-review", Path: "sub-agents/user-experience.md"}}, m.Removed)
}
