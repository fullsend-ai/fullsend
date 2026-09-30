package forge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTreeFileBytes_Content(t *testing.T) {
	f := TreeFile{Path: "a.txt", Content: []byte("hello")}
	got, err := f.Bytes()
	require.NoError(t, err)
	assert.Equal(t, []byte("hello"), got)
}

func TestTreeFileBytes_LocalPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin")
	require.NoError(t, os.WriteFile(path, []byte{0x7f, 0x45, 0x4c, 0x46}, 0o755))

	f := TreeFile{Path: "bin/fullsend", LocalPath: path}
	got, err := f.Bytes()
	require.NoError(t, err)
	assert.Equal(t, []byte{0x7f, 0x45, 0x4c, 0x46}, got)
}

func TestTreeFileBytes_LocalPathOverridesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin")
	require.NoError(t, os.WriteFile(path, []byte("from-disk"), 0o644))

	f := TreeFile{Path: "f", Content: []byte("ignored"), LocalPath: path}
	got, err := f.Bytes()
	require.NoError(t, err)
	assert.Equal(t, []byte("from-disk"), got)
}

func TestTreeFileBytes_Delete(t *testing.T) {
	f := TreeFile{Path: "gone", Delete: true, Content: []byte("x")}
	got, err := f.Bytes()
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestTreeFileBytes_MissingLocalPath(t *testing.T) {
	f := TreeFile{Path: "bin/fullsend", LocalPath: filepath.Join(t.TempDir(), "missing")}
	_, err := f.Bytes()
	require.Error(t, err)
}
