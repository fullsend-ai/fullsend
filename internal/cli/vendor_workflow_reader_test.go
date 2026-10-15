package cli

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestNewVendoredWorkflowReader(t *testing.T) {
	t.Run("reads the workflow a vendored install copies", func(t *testing.T) {
		read, cleanup := newVendoredWorkflowReader("../..")
		defer cleanup()

		got, err := read(".github/workflows/reusable-prioritize.yml")
		require.NoError(t, err)
		want, err := os.ReadFile("../../.github/workflows/reusable-prioritize.yml")
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})
	t.Run("a workflow the source does not vendor is not found", func(t *testing.T) {
		read, cleanup := newVendoredWorkflowReader("../..")
		defer cleanup()

		_, err := read(".github/workflows/reusable-does-not-exist.yml")
		require.Error(t, err)
		assert.True(t, forge.IsNotFound(err))
	})
	t.Run("an invalid source fails the read", func(t *testing.T) {
		read, cleanup := newVendoredWorkflowReader(t.TempDir())
		defer cleanup()

		_, err := read(".github/workflows/reusable-prioritize.yml")
		require.Error(t, err)
		assert.False(t, forge.IsNotFound(err))
		assert.False(t, errors.Is(err, forge.ErrNotFound))
	})
	t.Run("cleanup without a read is harmless", func(t *testing.T) {
		_, cleanup := newVendoredWorkflowReader("")
		cleanup()
	})
}
