package security

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pythonNameSet extracts the quoted names of a `NAME = frozenset({...})`
// literal from the embedded hook script, so the Go and Python copies of the
// vocabulary can be compared without executing Python.
func pythonNameSet(t *testing.T, script []byte, name string) []string {
	t.Helper()
	re := regexp.MustCompile(`(?s)\n` + name + ` = frozenset\(\s*\{(.*?)\}\s*\)`)
	m := re.FindSubmatch(script)
	require.NotNil(t, m, "%s frozenset literal not found in tool_allowlist_pretool.py", name)
	// A commented-out entry is absent from the runtime set; do not count it.
	// Only whole-line comments are stripped — keep the literals one name per
	// line with no trailing comments.
	body := regexp.MustCompile(`(?m)^\s*#.*$`).ReplaceAll(m[1], nil)
	var names []string
	for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(body, -1) {
		names = append(names, string(q[1]))
	}
	sort.Strings(names)
	return names
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestToolAllowlistHook_VocabularyMatchesGo(t *testing.T) {
	t.Parallel()
	assert.Equal(t, sortedKeys(CanonicalClaudeTools),
		pythonNameSet(t, ToolAllowlistPreToolHook, "CANONICAL_TOOLS"),
		"CANONICAL_TOOLS in tool_allowlist_pretool.py must mirror security.CanonicalClaudeTools")
	assert.Equal(t, sortedKeys(LegacyClaudeTools),
		pythonNameSet(t, ToolAllowlistPreToolHook, "LEGACY_TOOLS"),
		"LEGACY_TOOLS in tool_allowlist_pretool.py must mirror security.LegacyClaudeTools")
}

func TestPythonNameSet_IgnoresCommentedEntries(t *testing.T) {
	t.Parallel()
	script := []byte("x = 1\nNAMES = frozenset(\n    {\n        \"Bash\",\n        # \"LS\",  # removed\n        \"Read\",\n    }\n)\n")
	assert.Equal(t, []string{"Bash", "Read"}, pythonNameSet(t, script, "NAMES"))
}

func TestCanonicalClaudeTools_WellFormed(t *testing.T) {
	t.Parallel()
	for name := range CanonicalClaudeTools {
		assert.Equal(t, strings.TrimSpace(name), name)
		assert.NotEmpty(t, name)
		// A `Bash(gh,jq)` permission spec or a list must never be mistaken
		// for a tool name.
		assert.NotContains(t, name, "(")
		assert.NotContains(t, name, ",")
		assert.False(t, strings.HasPrefix(name, "mcp__"), "MCP names are not canonical: %s", name)
		_, legacy := LegacyClaudeTools[name]
		assert.False(t, legacy, "%s cannot be both canonical and legacy", name)
	}
	for name, replacement := range LegacyClaudeTools {
		assert.NotEmpty(t, name)
		if replacement != "" {
			assert.True(t, CanonicalClaudeTools[replacement],
				"legacy %s points at %s, which is not canonical", name, replacement)
		}
		assert.True(t, KnownClaudeTool(name))
	}
	assert.True(t, KnownClaudeTool("Bash"))
	assert.False(t, KnownClaudeTool("bash"))
	assert.False(t, KnownClaudeTool("mcp__github__issue_read"))
}

// Every tool HookPlan binds a script to must be canonical vocabulary — the
// plan is what every runtime adapter matches against, so a non-canonical name
// here would silently never fire on any runtime.
func TestHookPlan_ToolsAreCanonical(t *testing.T) {
	t.Parallel()
	on := true
	configs := map[string]*harness.Harness{
		"defaults": nil,
		"all-enabled": {Security: &harness.SecurityConfig{SandboxHooks: &harness.SandboxHooks{
			Tirith:                  &harness.TirithConfig{Enabled: &on},
			SSRFPreTool:             &on,
			SecretRedactPostTool:    &on,
			UnicodePostTool:         &on,
			ContextSuppressPostTool: &on,
			CanaryPreTool:           &on,
			CanaryPostTool:          &on,
			ToolAllowlistPreTool:    &harness.ToolAllowlistConfig{Enabled: &on},
		}}},
	}
	for label, h := range configs {
		for _, g := range HookPlan(SandboxHookConfigFromHarness(h)) {
			for _, tool := range g.Tools {
				if tool == AllTools {
					continue
				}
				assert.True(t, CanonicalClaudeTools[tool],
					"%s: HookPlan %s group binds non-canonical tool %q", label, g.Phase, tool)
			}
		}
	}
}
