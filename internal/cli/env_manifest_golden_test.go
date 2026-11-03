package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// update rewrites the committed sandbox env manifest from collector output,
// following the repo's golden-test flag convention (internal/agentnew,
// internal/runtime).
var update = flag.Bool("update", false, "rewrite the committed sandbox env manifest at docs/normative/sandbox-env/v1/sandbox-env.json")

// envManifestGoldenPath locates the committed sandbox env contract manifest,
// repo-relative from internal/cli's test working directory — the same ../..
// relative-read pattern this package's other repo-root reads use.
var envManifestGoldenPath = filepath.Join("..", "..", "docs", "normative", "sandbox-env", "v1", "sandbox-env.json")

// TestEnvManifestGolden byte-compares the committed sandbox env contract at
// docs/normative/sandbox-env/v1/sandbox-env.json against the manifest the
// collector derives by executing the production emitters, so the published
// contract cannot drift from the code that builds the sandbox environment.
//
// Regenerate with: go test ./internal/cli -run TestEnvManifestGolden -update
func TestEnvManifestGolden(t *testing.T) {
	rendered, err := collectSandboxEnvManifest(t).Render()
	require.NoError(t, err, "rendering the collected manifest")

	if *update {
		// MkdirAll covers first-time generation: a change that has never
		// committed a manifest (or a fresh checkout mid-change) creates the
		// normative directory here rather than failing on a missing parent.
		require.NoError(t, os.MkdirAll(filepath.Dir(envManifestGoldenPath), 0o755))
		require.NoError(t, os.WriteFile(envManifestGoldenPath, rendered, 0o644))
		// The update run must land exactly what the collector produced: a
		// partial or failed write would otherwise pass here and fail the
		// next plain run, so verify the file now holds the rendered bytes.
		written, err := os.ReadFile(envManifestGoldenPath)
		require.NoError(t, err, "reading back the rewritten manifest")
		assert.Equal(t, rendered, written, "the rewritten manifest must equal collector output byte for byte")
		return
	}

	committed, err := os.ReadFile(envManifestGoldenPath)
	if os.IsNotExist(err) {
		t.Fatalf("committed sandbox env manifest %s does not exist (not generated yet); run with -update to create it: go test ./internal/cli -run TestEnvManifestGolden -update",
			envManifestGoldenPath)
	}
	require.NoError(t, err, "reading the committed sandbox env manifest")

	if !bytes.Equal(committed, rendered) {
		t.Fatalf("committed sandbox env manifest %s differs from collector output:\n%s\nif the change is intended, regenerate and review the diff: go test ./internal/cli -run TestEnvManifestGolden -update",
			envManifestGoldenPath, envManifestDiffSummary(committed, rendered))
	}
}

// envManifestDiffSummary locates a committed/rendered divergence: the first
// few differing line numbers with the first difference's lines, or the
// line-count gap when one side is a prefix of the other. Deliberately short —
// the line numbers point review at the change, and the full diff is one
// -update away.
func envManifestDiffSummary(committed, rendered []byte) string {
	committedLines := strings.Split(string(committed), "\n")
	renderedLines := strings.Split(string(rendered), "\n")
	var differing []int
	for i := 0; i < len(committedLines) && i < len(renderedLines) && len(differing) < 3; i++ {
		if committedLines[i] != renderedLines[i] {
			differing = append(differing, i+1)
		}
	}
	if len(differing) == 0 {
		return "one side is a byte-prefix of the other: committed " +
			strconv.Itoa(len(committedLines)) + " lines, rendered " +
			strconv.Itoa(len(renderedLines)) + " lines"
	}
	numbers := make([]string, len(differing))
	for i, n := range differing {
		numbers[i] = strconv.Itoa(n)
	}
	return "first differing lines: " + strings.Join(numbers, ", ") +
		" (committed " + strconv.Itoa(len(committedLines)) + " lines, rendered " +
		strconv.Itoa(len(renderedLines)) + " lines)\n" +
		"  line " + strconv.Itoa(differing[0]) + " committed: " + committedLines[differing[0]-1] + "\n" +
		"  line " + strconv.Itoa(differing[0]) + " rendered:  " + renderedLines[differing[0]-1]
}
