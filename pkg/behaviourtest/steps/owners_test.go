package steps

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
)

func TestAuthorizationOwnersFileRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("enable then marshal", func(t *testing.T) {
		t.Parallel()
		input := []byte("version: \"1\"\nruntime: claude\n")
		cfg, err := config.ParsePerRepoConfigWriter(input)
		require.NoError(t, err)
		cfg.SetAuthorizationOwnersFile(true)
		out, err := cfg.Marshal()
		require.NoError(t, err)
		s := string(out)
		assert.Contains(t, s, "authorization:")
		assert.Contains(t, s, "owners_file: true")
		assert.Contains(t, s, "runtime: claude")
	})

	t.Run("enable is idempotent", func(t *testing.T) {
		t.Parallel()
		input := []byte("version: \"1\"\n")
		cfg, err := config.ParsePerRepoConfigWriter(input)
		require.NoError(t, err)
		cfg.SetAuthorizationOwnersFile(true)
		cfg.SetAuthorizationOwnersFile(true)
		out, err := cfg.Marshal()
		require.NoError(t, err)
		assert.Contains(t, string(out), "owners_file: true")
	})

	t.Run("disable removes authorization block", func(t *testing.T) {
		t.Parallel()
		input := []byte("version: \"1\"\n")
		cfg, err := config.ParsePerRepoConfigWriter(input)
		require.NoError(t, err)
		cfg.SetAuthorizationOwnersFile(true)
		cfg.SetAuthorizationOwnersFile(false)
		out, err := cfg.Marshal()
		require.NoError(t, err)
		assert.NotContains(t, string(out), "authorization")
	})

	t.Run("parse existing authorization from YAML", func(t *testing.T) {
		t.Parallel()
		input := []byte("version: \"1\"\nauthorization:\n  owners_file: true\n")
		cfg, err := config.ParsePerRepoConfigWriter(input)
		require.NoError(t, err)
		out, err := cfg.Marshal()
		require.NoError(t, err)
		assert.Contains(t, string(out), "owners_file: true")
	})

	t.Run("disable when never enabled is no-op", func(t *testing.T) {
		t.Parallel()
		input := []byte("version: \"1\"\nruntime: claude\n")
		cfg, err := config.ParsePerRepoConfigWriter(input)
		require.NoError(t, err)
		cfg.SetAuthorizationOwnersFile(false)
		out, err := cfg.Marshal()
		require.NoError(t, err)
		s := string(out)
		assert.NotContains(t, s, "authorization")
		assert.Contains(t, s, "runtime: claude")
	})

	t.Run("round-trip preserves other fields", func(t *testing.T) {
		t.Parallel()
		input := []byte("version: \"1\"\nruntime: claude\nkill_switch: false\nroles:\n  - coder\n  - reviewer\n")
		cfg, err := config.ParsePerRepoConfigWriter(input)
		require.NoError(t, err)
		cfg.SetAuthorizationOwnersFile(true)
		out, err := cfg.Marshal()
		require.NoError(t, err)
		s := string(out)
		assert.Contains(t, s, "runtime: claude")
		assert.Contains(t, s, "kill_switch: false")
		assert.Contains(t, s, "- coder")
		assert.Contains(t, s, "owners_file: true")
	})
}
