package steps

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func TestAuthorizationOwnersFileRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("enable then marshal", func(t *testing.T) {
		t.Parallel()
		input := []byte("version: \"1\"\nruntime: claude\n")
		cfg, err := config.ParsePerRepoConfigWriter(input)
		require.NoError(t, err)
		cfg.SetOwnersFileAuthEnabled(true)
		out, err := cfg.Marshal()
		require.NoError(t, err)
		s := string(out)
		assert.Contains(t, s, "authorization:")
		assert.Contains(t, s, "provider: owners_file")
		assert.Contains(t, s, "runtime: claude")
	})

	t.Run("enable is idempotent", func(t *testing.T) {
		t.Parallel()
		input := []byte("version: \"1\"\n")
		cfg, err := config.ParsePerRepoConfigWriter(input)
		require.NoError(t, err)
		cfg.SetOwnersFileAuthEnabled(true)
		cfg.SetOwnersFileAuthEnabled(true)
		out, err := cfg.Marshal()
		require.NoError(t, err)
		assert.Contains(t, string(out), "provider: owners_file")
	})

	t.Run("disable removes authorization block", func(t *testing.T) {
		t.Parallel()
		input := []byte("version: \"1\"\n")
		cfg, err := config.ParsePerRepoConfigWriter(input)
		require.NoError(t, err)
		cfg.SetOwnersFileAuthEnabled(true)
		cfg.SetOwnersFileAuthEnabled(false)
		out, err := cfg.Marshal()
		require.NoError(t, err)
		assert.NotContains(t, string(out), "authorization")
	})

	t.Run("parse existing authorization from YAML", func(t *testing.T) {
		t.Parallel()
		input := []byte("version: \"1\"\nauthorization:\n  - provider: owners_file\n")
		cfg, err := config.ParsePerRepoConfigWriter(input)
		require.NoError(t, err)
		assert.True(t, cfg.IsOwnersFileAuthEnabled())
		out, err := cfg.Marshal()
		require.NoError(t, err)
		assert.Contains(t, string(out), "provider: owners_file")
	})

	t.Run("disable when never enabled is no-op", func(t *testing.T) {
		t.Parallel()
		input := []byte("version: \"1\"\nruntime: claude\n")
		cfg, err := config.ParsePerRepoConfigWriter(input)
		require.NoError(t, err)
		cfg.SetOwnersFileAuthEnabled(false)
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
		cfg.SetOwnersFileAuthEnabled(true)
		out, err := cfg.Marshal()
		require.NoError(t, err)
		s := string(out)
		assert.Contains(t, s, "runtime: claude")
		assert.Contains(t, s, "kill_switch: false")
		assert.Contains(t, s, "- coder")
		assert.Contains(t, s, "provider: owners_file")
	})
}

// fakeArtifactCI answers DownloadNamedArtifactFromRun only.
type fakeArtifactCI struct {
	ci.Driver
	err error
}

func (f *fakeArtifactCI) DownloadNamedArtifactFromRun(context.Context, string, string, int, string, string) error {
	return f.err
}

func TestThenTriageAgentDidNotRun(t *testing.T) {
	t.Parallel()

	run := func(ciErr error) error {
		return thenTriageAgentDidNotRun(&world.World{
			RepoOwner:   "org",
			RepoName:    "repo",
			WorkflowRun: &forge.WorkflowRun{ID: 7},
			CI:          &fakeArtifactCI{err: ciErr},
		})
	}

	require.NoError(t, run(errors.New(`artifact "fullsend-triage" not found on workflow run 7`)))
	require.ErrorContains(t, run(nil), "the agent ran")
	require.ErrorContains(t, run(errors.New("HTTP 502")), "checking triage run 7 artifacts")
}
