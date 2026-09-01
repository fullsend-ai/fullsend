package pluginformat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDetect_ClaudeMarkerWins covers the precedence that makes the two
// families disjoint: plugin.json at the root settles it, and pi's rule is
// never consulted. A Claude plugin that bundles a Node MCP server ships a
// package.json whose "main" resolves, which satisfies pi's rule 3 as well —
// without the precedence such a directory would have no single kind.
func TestDetect_ClaudeMarkerWins(t *testing.T) {
	t.Parallel()

	for name, files := range map[string]map[string]string{
		"plugin.json only":     {"plugin.json": `{"name":"x"}`},
		"claude code manifest": {".claude-plugin/plugin.json": `{"name":"x"}`, ".lsp.json": `{}`},
		"claude manifest beside a node server": {
			".claude-plugin/plugin.json": `{"name":"x"}`,
			"package.json":               `{"main":"server/index.js"}`,
			"server/index.js":            "//",
		},
		"plugin.json beside index": {"plugin.json": `{"name":"x"}`, "index.js": "//"},
		"bundled node mcp server": {
			"plugin.json":       `{"name":"x"}`,
			"package.json":      `{"name":"x","main":"server/index.js"}`,
			"server/index.js":   "//",
			".mcp.json":         `{"mcpServers":{}}`,
			"commands/go.md":    "# go",
			"skills/s/SKILL.md": "#",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeExtDir(t, files)
			kind, problem, err := Detect(dir)
			require.NoError(t, err)
			assert.Empty(t, problem)
			assert.Equal(t, KindClaude, kind)
		})
	}

	// A directory whose plugin.json is itself a directory is not a Claude
	// plugin, so the pi rule decides — and refuses it.
	dir := writeExtDir(t, map[string]string{"plugin.json/inner.txt": "x"})
	assert.Contains(t, detectProblem(t, dir), "no index.js")
}

// TestDetectTree is the fetched-tree twin of Detect: same precedence, same
// verdicts, on the map a forge fetch returns.
func TestDetectTree(t *testing.T) {
	t.Parallel()

	claude, problem := DetectTree(map[string][]byte{
		"plugin.json":     []byte(`{"name":"x"}`),
		"package.json":    []byte(`{"main":"server/index.js"}`),
		"server/index.js": []byte("//"),
	})
	assert.Equal(t, KindClaude, claude)
	assert.Empty(t, problem)

	claude2, problem := DetectTree(map[string][]byte{
		".claude-plugin/plugin.json": []byte(`{"name":"x"}`),
		"index.js":                   []byte("//"),
	})
	assert.Equal(t, KindClaude, claude2, "Claude Code's own manifest path is a marker too")
	assert.Empty(t, problem)

	pi, problem := DetectTree(map[string][]byte{"index.js": []byte("//")})
	assert.Equal(t, KindPi, pi)
	assert.Empty(t, problem)

	none, problem := DetectTree(map[string][]byte{"README.md": []byte("#")})
	assert.Empty(t, string(none))
	assert.Equal(t,
		`not a Claude plugin (no plugin.json or .claude-plugin/plugin.json) and not a pi extension `+
			`(no index.js/index.ts/index.mjs/index.cjs, package.json "pi.extensions" entry or "main" file — pi would fail to load it)`,
		problem)

	// A tree the pi rule refuses for package layout reports that reason,
	// not the index one.
	none, problem = DetectTree(map[string][]byte{"index.js": []byte("//"), "skills/s/SKILL.md": []byte("#")})
	assert.Empty(t, string(none))
	assert.Contains(t, problem, `a "skills" entry makes pi read it as a package`)
}

// TestDetect_EmptyDirs covers the two degenerate inputs Detect must not
// panic on: an empty directory and an empty tree.
func TestDetect_EmptyDirs(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	assert.Contains(t, detectProblem(t, dir), "no index.js")

	kind, problem := DetectTree(nil)
	assert.Empty(t, string(kind))
	assert.Contains(t, problem, "no index.js")
}
