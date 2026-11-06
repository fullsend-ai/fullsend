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

	"github.com/fullsend-ai/fullsend/internal/envmanifest"
)

// update rewrites the committed sandbox env schemas from collector output,
// following the repo's golden-test flag convention (internal/agentnew,
// internal/runtime).
var update = flag.Bool("update", false, "rewrite the committed sandbox env schemas under docs/normative/sandbox-env/v1/")

// envManifestSchemaDir locates the committed sandbox env contract schemas,
// repo-relative from internal/cli's test working directory — the same ../..
// relative-read pattern this package's other repo-root reads use.
var envManifestSchemaDir = filepath.Join("..", "..", "docs", "normative", "sandbox-env", "v1")

// TestEnvManifestGolden byte-compares each committed sandbox env contract
// schema under docs/normative/sandbox-env/v1/ against the schema rendered
// from the manifest the collector derives by executing the production
// emitters, so the published contract cannot drift from the code that
// builds the sandbox environment.
//
// Regenerate with: go test ./internal/cli -run TestEnvManifestGolden -update
func TestEnvManifestGolden(t *testing.T) {
	schemas, err := collectSandboxEnvManifest(t).RenderSchemas()
	require.NoError(t, err, "rendering the collected manifest's schemas")
	require.ElementsMatch(t, []string{envmanifest.HarnessEnvSchemaFile, envmanifest.EnvReferencesSchemaFile}, keysOf(schemas))

	for file, rendered := range schemas {
		path := filepath.Join(envManifestSchemaDir, file)
		t.Run(file, func(t *testing.T) {
			if *update {
				require.NoError(t, os.MkdirAll(envManifestSchemaDir, 0o755))
				require.NoError(t, os.WriteFile(path, rendered, 0o644))
				// The update run must land exactly what the collector
				// produced: a partial or failed write would otherwise pass
				// here and fail the next plain run.
				written, err := os.ReadFile(path)
				require.NoError(t, err, "reading back the rewritten schema")
				assert.Equal(t, rendered, written, "the rewritten schema must equal collector output byte for byte")
				return
			}

			committed, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				t.Fatalf("committed sandbox env schema %s does not exist (not generated yet); run with -update to create it: go test ./internal/cli -run TestEnvManifestGolden -update", path)
			}
			require.NoError(t, err, "reading the committed sandbox env schema")

			if !bytes.Equal(committed, rendered) {
				t.Fatalf("committed sandbox env schema %s differs from collector output:\n%s\nif the change is intended, regenerate and review the diff: go test ./internal/cli -run TestEnvManifestGolden -update",
					path, envManifestDiffSummary(committed, rendered))
			}
		})
	}
}

func keysOf(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
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
