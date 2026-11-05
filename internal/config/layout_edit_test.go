package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// layoutFixture is a hand-maintained config: comments everywhere, 4-space
// nesting, quoted scalars, and top-level keys out of Marshal's order.
const layoutFixture = `# Per-repo fullsend config for example/repo.
# Hand-maintained: keep the rationale comments.
mint_url: https://mint.example.com
version: "1"

# Agents. The pollers dispatch review and triage.
agents:
    # review runs on opus: sonnet missed cross-file bugs.
    - name: review
      model: opus   # named directly, not via an alias
      subagents:
          correctness: opus    # catches the most
          style-conventions: haiku
    - "https://raw.githubusercontent.com/org/agents/0123456789abcdef0123456789abcdef01234567/harness/triage.yaml"

allowed_remote_resources:
    - "https://raw.githubusercontent.com/org/agents/"

# Authorization comes before runtime on purpose.
authorization:
    - provider: jira
      write:
          accounts:
              - "12345:abcdef01-2345-6789-abcd-ef0123456789"
runtime: claude
`

const (
	triageSource = "https://raw.githubusercontent.com/org/agents/0123456789abcdef0123456789abcdef01234567/harness/triage.yaml"
	orgPrefix    = "https://raw.githubusercontent.com/org/agents/"
)

func parseLayoutFixture(t *testing.T, data string) *perRepoConfig {
	t.Helper()
	w, err := ParsePerRepoConfigWriter([]byte(data))
	require.NoError(t, err)
	return w.(*perRepoConfig)
}

// replaceOnce returns s with old replaced by new, failing if old is absent.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	require.Contains(t, s, old)
	return strings.Replace(s, old, new, 1)
}

func marshalLayout(t *testing.T, orig string, w ConfigWriter) string {
	t.Helper()
	out, err := MarshalPreservingLayout([]byte(orig), w)
	require.NoError(t, err)
	return string(out)
}

func TestMarshalPreservingLayout_AddAgentAndPrefix(t *testing.T) {
	w := parseLayoutFixture(t, layoutFixture)
	agents := w.LocalAgentEntries()
	agents = append(agents, AgentEntry{
		Name:   "fix",
		Source: "https://raw.githubusercontent.com/other/agents/89abcdef0123456789abcdef0123456789abcdef/harness/fix.yaml#sha256=abc",
		Ref:    "main",
	})
	w.SetAgents(agents)
	w.SetAllowedRemoteResources(append(w.AllowedRemoteResources, "https://raw.githubusercontent.com/other/agents/"))

	got := marshalLayout(t, layoutFixture, w)

	want := replaceOnce(t, layoutFixture,
		`    - "https://raw.githubusercontent.com/org/agents/0123456789abcdef0123456789abcdef01234567/harness/triage.yaml"
`,
		`    - "https://raw.githubusercontent.com/org/agents/0123456789abcdef0123456789abcdef01234567/harness/triage.yaml"
    - name: fix
      source: https://raw.githubusercontent.com/other/agents/89abcdef0123456789abcdef0123456789abcdef/harness/fix.yaml#sha256=abc
      ref: main
`)
	want = replaceOnce(t, want,
		`    - "https://raw.githubusercontent.com/org/agents/"
`,
		`    - "https://raw.githubusercontent.com/org/agents/"
    - "https://raw.githubusercontent.com/other/agents/"
`)
	assert.Equal(t, want, got)
}

func TestMarshalPreservingLayout_RemoveAgentAndPrefix(t *testing.T) {
	w := parseLayoutFixture(t, layoutFixture)
	w.SetAgents(w.LocalAgentEntries()[1:])
	w.SetAllowedRemoteResources([]string{})

	got := marshalLayout(t, layoutFixture, w)

	want := replaceOnce(t, layoutFixture,
		`    # review runs on opus: sonnet missed cross-file bugs.
    - name: review
      model: opus   # named directly, not via an alias
      subagents:
          correctness: opus    # catches the most
          style-conventions: haiku
`, "")
	want = replaceOnce(t, want,
		`allowed_remote_resources:
    - "https://raw.githubusercontent.com/org/agents/"
`, "allowed_remote_resources: []\n")
	assert.Equal(t, want, got)
}

func TestMarshalPreservingLayout_RemoveAllAgentsDropsKey(t *testing.T) {
	w := parseLayoutFixture(t, layoutFixture)
	w.SetAgents(nil)
	w.SetAllowedRemoteResources(nil)

	got := marshalLayout(t, layoutFixture, w)

	// The key goes with the comment directly above it.
	want := replaceOnce(t, layoutFixture,
		`# Agents. The pollers dispatch review and triage.
agents:
    # review runs on opus: sonnet missed cross-file bugs.
    - name: review
      model: opus   # named directly, not via an alias
      subagents:
          correctness: opus    # catches the most
          style-conventions: haiku
    - "https://raw.githubusercontent.com/org/agents/0123456789abcdef0123456789abcdef01234567/harness/triage.yaml"
`, "")
	want = replaceOnce(t, want,
		`allowed_remote_resources:
    - "https://raw.githubusercontent.com/org/agents/"
`, "")
	assert.Equal(t, want, got)
}

func TestMarshalPreservingLayout_SetFields(t *testing.T) {
	w := parseLayoutFixture(t, layoutFixture)
	agents := UpsertAgentSettings(w.LocalAgentEntries(), "review", "pi", "sonnet", "", map[string]*string{
		"correctness":       strPtr("opus"),
		"style-conventions": nil,
		"default":           strPtr("haiku"),
	})
	w.SetAgents(agents)

	got := marshalLayout(t, layoutFixture, w)

	want := replaceOnce(t, layoutFixture,
		`      model: opus   # named directly, not via an alias
      subagents:
          correctness: opus    # catches the most
          style-conventions: haiku
`,
		`      model: sonnet # named directly, not via an alias
      subagents:
          correctness: opus    # catches the most
          style-conventions: null
          default: haiku
      runtime: pi
`)
	assert.Equal(t, want, got)
}

func TestMarshalPreservingLayout_SetOnNewBuiltinAgent(t *testing.T) {
	w := parseLayoutFixture(t, layoutFixture)
	w.SetAgents(UpsertAgentSettings(w.LocalAgentEntries(), "code", "", "", "high", nil))

	got := marshalLayout(t, layoutFixture, w)

	want := replaceOnce(t, layoutFixture,
		`    - "https://raw.githubusercontent.com/org/agents/0123456789abcdef0123456789abcdef01234567/harness/triage.yaml"
`,
		`    - "https://raw.githubusercontent.com/org/agents/0123456789abcdef0123456789abcdef01234567/harness/triage.yaml"
    - name: code
      effort: high
`)
	assert.Equal(t, want, got)
}

func TestMarshalPreservingLayout_ClearSubagentsAndSettings(t *testing.T) {
	w := parseLayoutFixture(t, layoutFixture)
	w.SetAgents(UpsertAgentSettings(w.LocalAgentEntries(), "review", "", "", "", nil))

	got := marshalLayout(t, layoutFixture, w)

	want := replaceOnce(t, layoutFixture,
		`    - name: review
      model: opus   # named directly, not via an alias
      subagents:
          correctness: opus    # catches the most
          style-conventions: haiku
`,
		`    - name: review
`)
	assert.Equal(t, want, got)
}

func TestMarshalPreservingLayout_UpdateScalarSourceKeepsForm(t *testing.T) {
	w := parseLayoutFixture(t, layoutFixture)
	agents := w.LocalAgentEntries()
	newSource := strings.Replace(triageSource, "0123456789abcdef0123456789abcdef01234567", "fedcba9876543210fedcba9876543210fedcba98", 1) + "#sha256=def"
	agents[1].Source = newSource
	w.SetAgents(agents)

	got := marshalLayout(t, layoutFixture, w)

	want := replaceOnce(t, layoutFixture, `    - "`+triageSource+`"`, `    - "`+newSource+`"`)
	assert.Equal(t, want, got)
}

func TestMarshalPreservingLayout_ScalarEntryGainingSettings(t *testing.T) {
	w := parseLayoutFixture(t, layoutFixture)
	w.SetAgents(UpsertAgentSettings(w.LocalAgentEntries(), "triage", "", "haiku", "", nil))

	got := marshalLayout(t, layoutFixture, w)

	want := replaceOnce(t, layoutFixture, `    - "`+triageSource+`"`,
		`    - source: `+triageSource+`
      model: haiku`)
	assert.Equal(t, want, got)
}

func TestMarshalPreservingLayout_InsertBeforeExistingEntry(t *testing.T) {
	w := parseLayoutFixture(t, layoutFixture)
	agents := append([]AgentEntry{{Source: "harness/lint.yaml"}}, w.LocalAgentEntries()...)
	w.SetAgents(agents)

	got := marshalLayout(t, layoutFixture, w)

	want := replaceOnce(t, layoutFixture,
		`    # review runs on opus`,
		`    - source: harness/lint.yaml
    # review runs on opus`)
	assert.Equal(t, want, got)
}

func TestMarshalPreservingLayout_AppendsMissingKeys(t *testing.T) {
	orig := `# Minimal config.
version: "1"
roles:
- triage   # no indent under roles
runtime: claude
`
	w := parseLayoutFixture(t, orig)
	w.SetAgents([]AgentEntry{{Source: "harness/lint.yaml", Subagents: map[string]*string{"default": strPtr("haiku")}}})
	w.SetAllowedRemoteResources([]string{orgPrefix})

	got := marshalLayout(t, orig, w)

	assert.Equal(t, `# Minimal config.
version: "1"
roles:
- triage   # no indent under roles
agents:
- source: harness/lint.yaml
  subagents:
    default: haiku
allowed_remote_resources:
- `+orgPrefix+`
runtime: claude
`, got)
}

func TestMarshalPreservingLayout_AppendsAtEndWithoutNeighbours(t *testing.T) {
	orig := "version: \"1\"\nruntime: claude   # last\n"
	w := parseLayoutFixture(t, orig)
	w.SetAgents([]AgentEntry{{Source: "harness/lint.yaml"}})

	got := marshalLayout(t, orig, w)

	assert.Equal(t, orig+"agents:\n  - source: harness/lint.yaml\n", got)
}

func TestMarshalPreservingLayout_FlowListReplaced(t *testing.T) {
	orig := `version: "1"
agents: [harness/lint.yaml]   # one agent
allowed_remote_resources: []
`
	w := parseLayoutFixture(t, orig)
	w.SetAgents(append(w.LocalAgentEntries(), AgentEntry{Source: "harness/review.yaml"}))

	got := marshalLayout(t, orig, w)

	assert.Equal(t, `version: "1"
agents:
  - source: harness/lint.yaml
  - source: harness/review.yaml
allowed_remote_resources: []
`, got)
}

func TestMarshalPreservingLayout_UnchangedFlowListKept(t *testing.T) {
	orig := `version: "1"
agents: [harness/lint.yaml]   # one agent
allowed_remote_resources:
  - ` + orgPrefix + `
`
	w := parseLayoutFixture(t, orig)
	w.SetAllowedRemoteResources(append(w.LocalAllowedResources(), "https://example.com/x/"))

	got := marshalLayout(t, orig, w)

	assert.Equal(t, orig+"  - https://example.com/x/\n", got)
}

func TestMarshalPreservingLayout_DashOnOwnLineFallsBack(t *testing.T) {
	orig := `version: "1"
agents:
  -
    name: review
    model: opus
`
	w := parseLayoutFixture(t, orig)
	w.SetAgents(UpsertAgentSettings(w.LocalAgentEntries(), "review", "", "sonnet", "", nil))

	got := marshalLayout(t, orig, w)

	full, err := w.Marshal()
	require.NoError(t, err)
	assert.Equal(t, string(full), got, "an item without content on its dash line is not spliced")
}

func TestPerRepoConfig_LocalAllowedResources(t *testing.T) {
	assert.Nil(t, parseLayoutFixture(t, "version: \"1\"\n").LocalAllowedResources())
	assert.Equal(t, []string{}, parseLayoutFixture(t, "allowed_remote_resources: []\n").LocalAllowedResources())
	assert.Equal(t, []string{orgPrefix}, parseLayoutFixture(t, layoutFixture).LocalAllowedResources())
}

func TestMarshalPreservingLayout_UnchangedIsIdentical(t *testing.T) {
	w := parseLayoutFixture(t, layoutFixture)
	assert.Equal(t, layoutFixture, marshalLayout(t, layoutFixture, w))
}

func TestMarshalPreservingLayout_CRLF(t *testing.T) {
	orig := strings.ReplaceAll(layoutFixture, "\n", "\r\n")
	w := parseLayoutFixture(t, orig)
	w.SetAgents(append(w.LocalAgentEntries(), AgentEntry{Source: "harness/lint.yaml"}))

	got := marshalLayout(t, orig, w)

	want := replaceOnce(t, orig, `triage.yaml"`+"\r\n", `triage.yaml"`+"\r\n"+`    - source: harness/lint.yaml`+"\r\n")
	assert.Equal(t, want, got)
}

func TestMarshalPreservingLayout_FallsBackToMarshal(t *testing.T) {
	t.Run("other field changed", func(t *testing.T) {
		w := parseLayoutFixture(t, layoutFixture)
		w.SetRuntime("pi")
		full, err := w.Marshal()
		require.NoError(t, err)
		assert.Equal(t, string(full), marshalLayout(t, layoutFixture, w))
	})
	t.Run("empty original", func(t *testing.T) {
		w := NewPerRepoConfig(nil, "")
		full, err := w.Marshal()
		require.NoError(t, err)
		assert.Equal(t, string(full), marshalLayout(t, "", w))
	})
	t.Run("original not a mapping", func(t *testing.T) {
		w := NewPerRepoConfig(nil, "")
		full, err := w.Marshal()
		require.NoError(t, err)
		assert.Equal(t, string(full), marshalLayout(t, "- a\n", w))
	})
	t.Run("original malformed", func(t *testing.T) {
		w := NewPerRepoConfig(nil, "")
		full, err := w.Marshal()
		require.NoError(t, err)
		assert.Equal(t, string(full), marshalLayout(t, "agents: [\n", w))
	})
	t.Run("merge key in entry", func(t *testing.T) {
		orig := `version: "1"
x-defaults: &d
  model: opus
agents:
  - <<: *d
    name: review
`
		w := parseLayoutFixture(t, orig)
		w.SetAgents(UpsertAgentSettings(w.LocalAgentEntries(), "review", "", "sonnet", "", nil))
		got := marshalLayout(t, orig, w)
		cfg := parseLayoutFixture(t, got)
		review, ok := AgentSettingsFor(cfg.LocalAgentEntries(), "review")
		require.True(t, ok)
		assert.Equal(t, "sonnet", review.Model)
	})
}

func TestSpliceAgentLists_Rejects(t *testing.T) {
	for name, orig := range map[string]string{
		"multiple documents": "version: \"1\"\n---\nversion: \"1\"\n",
		"flow root":          "{version: \"1\"}\n",
		"empty mapping":      "{}\n",
		"merge at root":      "x: &a {version: \"1\"}\n<<: *a\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := spliceAgentLists([]byte(orig), []AgentEntry{{Source: "a.yaml"}}, nil)
			assert.Error(t, err)
		})
	}
}

func TestLayoutEditor_ApplyRejectsOverlap(t *testing.T) {
	e := &layoutEditor{lines: []string{"a", "b", "c"}}
	e.replace(1, 2, []string{"x"})
	e.replace(2, 3, []string{"y"})
	_, err := e.apply()
	assert.ErrorIs(t, err, errNoSplice)
}

func TestWithScalarStyle(t *testing.T) {
	str := func(v string, s yaml.Style) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: s}
	}
	assert.Equal(t, yaml.DoubleQuotedStyle, withScalarStyle(str("a", 0), str("b", yaml.DoubleQuotedStyle)).Style)
	assert.Equal(t, yaml.SingleQuotedStyle, withScalarStyle(str("a", 0), str("b", yaml.SingleQuotedStyle)).Style)
	assert.Equal(t, yaml.Style(0), withScalarStyle(str("a", 0), str("b", 0)).Style, "plain reference stays plain")
	assert.Equal(t, yaml.Style(0), withScalarStyle(str("a\nb", 0), str("b", yaml.SingleQuotedStyle)).Style, "multi-line value is not single-quoted")
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	assert.Same(t, m, withScalarStyle(m, str("b", yaml.DoubleQuotedStyle)))
}
