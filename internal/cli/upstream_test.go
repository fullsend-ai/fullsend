package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestPinUpstreamRef(t *testing.T) {
	ctx := context.Background()

	t.Run("rewrites all v0 patterns", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents["org/repo/.github/workflows/triage.yml"] =
			[]byte("    uses: fullsend-ai/fullsend/.github/workflows/reusable-triage.yml@v0\n" +
				"      fullsend_ai_ref: v0\n")
		fc.FileContents["org/repo/.github/workflows/repo-maintenance.yml"] =
			[]byte("          ref: v0\n" +
				"        uses: fullsend-ai/fullsend/.github/actions/mint-token@v0\n")

		var buf bytes.Buffer
		printer := ui.New(&buf)
		err := pinUpstreamRef(ctx, fc, printer, "org", "repo", "abc123def456",
			[]string{".github/workflows/triage.yml", ".github/workflows/repo-maintenance.yml"})
		require.NoError(t, err)

		triage, err := fc.GetFileContent(ctx, "org", "repo", ".github/workflows/triage.yml")
		require.NoError(t, err)
		assert.Contains(t, string(triage), "@abc123def456")
		assert.Contains(t, string(triage), "fullsend_ai_ref: abc123def456")
		assert.NotContains(t, string(triage), "@v0")

		maint, err := fc.GetFileContent(ctx, "org", "repo", ".github/workflows/repo-maintenance.yml")
		require.NoError(t, err)
		assert.Contains(t, string(maint), "ref: abc123def456")
		assert.Contains(t, string(maint), "@abc123def456")
		assert.NotContains(t, string(maint), "@v0")
	})

	t.Run("skips missing files", func(t *testing.T) {
		fc := forge.NewFakeClient()
		var buf bytes.Buffer
		printer := ui.New(&buf)
		err := pinUpstreamRef(ctx, fc, printer, "org", "repo", "abc123def456",
			[]string{".github/workflows/nonexistent.yml"})
		require.NoError(t, err)
	})

	t.Run("no-op when files have no v0 refs", func(t *testing.T) {
		fc := forge.NewFakeClient()
		fc.FileContents["org/repo/.github/workflows/custom.yml"] =
			[]byte("uses: some-other/action@v1\n")
		var buf bytes.Buffer
		printer := ui.New(&buf)
		err := pinUpstreamRef(ctx, fc, printer, "org", "repo", "abc123def456",
			[]string{".github/workflows/custom.yml"})
		require.NoError(t, err)
		// File should be unchanged
		content, _ := fc.GetFileContent(ctx, "org", "repo", ".github/workflows/custom.yml")
		assert.Equal(t, "uses: some-other/action@v1\n", string(content))
	})
}
