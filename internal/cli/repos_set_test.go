package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runReposSet(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newReposCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"set"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestReposSetCmd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.yaml")

	out, err := runReposSet(t, "-f", path, "github.mint_url", "https://mint.example.com")
	require.NoError(t, err)
	assert.Contains(t, out, "Set github.mint_url in "+path)

	out, err = runReposSet(t, "-f", path, "github.mint_url", "https://mint.example.com")
	require.NoError(t, err)
	assert.Contains(t, out, "nothing changed")

	out, err = runReposSet(t, "--manifest", path, `github.repos["acme/api"]`, `{"app_set": "acme"}`)
	require.NoError(t, err)
	assert.Contains(t, out, "Set github.repos")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "version: 1\ngithub:\n  mint_url: https://mint.example.com\n  repos:\n    - name: acme/api\n      app_set: acme\n", string(data))

	out, err = runReposSet(t, "-f", path, "--unset", "github.mint_url")
	require.NoError(t, err)
	assert.Contains(t, out, "Removed github.mint_url from "+path)

	out, err = runReposSet(t, "-f", path, "--unset", "github.mint_url")
	require.NoError(t, err)
	assert.Contains(t, out, "is not set")

	_, err = runReposSet(t, "-f", path, "--unset", "github.url", "extra")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--unset takes exactly one argument")

	_, err = runReposSet(t, "-f", path, "github.url")
	require.Error(t, err)

	_, err = runReposSet(t, "-f", path, "github.nope", "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field "nope" at github`)

	_, err = runReposSet(t, "-f", path, "--unset", "github.nope")
	require.Error(t, err)
}

func TestReposSetDefaultRemoved(t *testing.T) {
	cmd := newReposCmd()
	cmd.SetArgs([]string{"set-default", "github.url", "https://ghes.example.com"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	require.Error(t, cmd.Execute())
}
