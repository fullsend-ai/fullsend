package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEntrypointCmd_Structure(t *testing.T) {
	root := newRootCmd()

	// entrypoint command exists under root.
	ep, _, err := root.Find([]string{"entrypoint"})
	require.NoError(t, err)
	assert.Equal(t, "entrypoint", ep.Name())

	// code subcommand exists.
	code, _, err := root.Find([]string{"entrypoint", "code"})
	require.NoError(t, err)
	assert.Equal(t, "code", code.Name())

	// implementation alias resolves to code.
	impl, _, err := root.Find([]string{"entrypoint", "implementation"})
	require.NoError(t, err)
	assert.Equal(t, "code", impl.Name())
}

func TestEntrypointCmd_SCMFlag(t *testing.T) {
	root := newRootCmd()

	ep, _, err := root.Find([]string{"entrypoint"})
	require.NoError(t, err)

	scmFlag := ep.PersistentFlags().Lookup("scm")
	require.NotNil(t, scmFlag)
	assert.Equal(t, "github", scmFlag.DefValue)
}

func TestEntrypointCmd_UnsupportedSCM(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"entrypoint", "code", "--scm", "gitlab"})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported SCM backend: gitlab")
}
