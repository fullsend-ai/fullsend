package cli

import (
	"bytes"
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// appendStaleVendoredDeletes must turn manifest-recorded paths that left the
// vendored set into Delete tree entries, and pass through cleanly when no
// manifest exists (first vendor install) or the manifest is unreadable.
func TestAppendStaleVendoredDeletes(t *testing.T) {
	ctx := context.Background()
	printer := ui.New(&bytes.Buffer{})

	newFiles := []forge.TreeFile{
		{Path: ".defaults/action.yml", Content: []byte("a"), Mode: "100644"},
		{Path: ".defaults/.github/scripts/check-fix-eligibility.sh", Content: []byte("b"), Mode: "100755"},
	}

	t.Run("prunes de-listed manifest paths", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.FileContents["o/r/.fullsend/vendor-manifest.yaml"] = []byte(
			"version: \"1\"\n" +
				"binary_path: .defaults/bin/fullsend\n" +
				"cli_version: v0.36.0\n" +
				"paths:\n" +
				"  - .defaults/action.yml\n" +
				"  - .defaults/.github/scripts/check-fix-eligibility.sh\n" +
				"  - .defaults/.github/scripts/redact-behaviour-artifacts.sh\n" +
				"  - .defaults/.github/scripts/redact-behaviour-artifacts-test.sh\n")

		out, err := appendStaleVendoredDeletes(ctx, client, printer, "o", "r", newFiles)
		assert.NoError(t, err)

		var deletes []string
		for _, f := range out {
			if f.Delete {
				deletes = append(deletes, f.Path)
			}
		}
		assert.Equal(t, []string{
			".defaults/.github/scripts/redact-behaviour-artifacts-test.sh",
			".defaults/.github/scripts/redact-behaviour-artifacts.sh",
		}, deletes)
		assert.Len(t, out, len(newFiles)+2)
	})

	t.Run("no manifest is a clean pass-through", func(t *testing.T) {
		client := forge.NewFakeClient()
		out, err := appendStaleVendoredDeletes(ctx, client, printer, "o", "r", newFiles)
		assert.NoError(t, err)
		assert.Equal(t, newFiles, out)
	})

	t.Run("present-but-invalid manifest fails the vendor instead of orphaning", func(t *testing.T) {
		client := forge.NewFakeClient()
		client.FileContents["o/r/.fullsend/vendor-manifest.yaml"] = []byte("{not yaml")
		_, err := appendStaleVendoredDeletes(ctx, client, printer, "o", "r", newFiles)
		assert.Error(t, err)
	})
}

// Pruning must fire on every vendor commit path, not just acquireAndVendor —
// prepareVendorFiles is the chokepoint, exercised here through
// appendVendorTreeFiles and the combined-commit collect func.
func TestVendorCommitPathsPruneStaleFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux ELF binary")
	}
	exe, err := os.Executable()
	require.NoError(t, err)
	ctx := context.Background()

	seed := func() *forge.FakeClient {
		client := forge.NewFakeClient()
		client.FileContents["org/my-repo/.fullsend/vendor-manifest.yaml"] = []byte(
			"version: \"1\"\n" +
				"binary_path: .fullsend/.defaults/bin/fullsend\n" +
				"paths:\n" +
				"  - .defaults/.github/scripts/redact-behaviour-artifacts.sh\n")
		return client
	}
	countDeletes := func(files []forge.TreeFile) int {
		n := 0
		for _, f := range files {
			if f.Delete {
				n++
			}
		}
		return n
	}

	out, _, err := appendVendorTreeFiles(ctx, seed(), ui.New(&strings.Builder{}), "org", "my-repo", nil, true, exe, "")
	require.NoError(t, err)
	assert.Equal(t, 1, countDeletes(out), "appendVendorTreeFiles must prune")

	fn := makeVendorCollectFunc(exe, "")
	out, _, err = fn(ctx, seed(), ui.New(&strings.Builder{}), "org", "my-repo")
	require.NoError(t, err)
	assert.Equal(t, 1, countDeletes(out), "combined collect func must prune")
}
