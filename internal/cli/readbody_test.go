package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadBodyFrom_Stdin(t *testing.T) {
	stdin := strings.NewReader("hello from stdin")
	body, err := readBodyFrom("-", stdin)
	require.NoError(t, err)
	assert.Equal(t, "hello from stdin", body)
}

func TestReadBodyFrom_StdinOversize(t *testing.T) {
	big := strings.NewReader(strings.Repeat("x", maxBodyBytes+1))
	_, err := readBodyFrom("-", big)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum size")
}

func TestReadBodyFrom_StdinExactLimit(t *testing.T) {
	exact := strings.NewReader(strings.Repeat("x", maxBodyBytes))
	body, err := readBodyFrom("-", exact)
	require.NoError(t, err)
	assert.Len(t, body, maxBodyBytes)
}

func TestReadBodyFrom_File(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "body.txt")
	require.NoError(t, os.WriteFile(tmp, []byte("file content"), 0o644))

	body, err := readBodyFrom(tmp, nil)
	require.NoError(t, err)
	assert.Equal(t, "file content", body)
}

func TestReadBodyFrom_FileOversize(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "big.txt")
	require.NoError(t, os.WriteFile(tmp, make([]byte, maxBodyBytes+1), 0o644))

	_, err := readBodyFrom(tmp, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum size")
}

func TestReadBodyFrom_FileNotFound(t *testing.T) {
	_, err := readBodyFrom("/nonexistent/path", nil)
	require.Error(t, err)
}
