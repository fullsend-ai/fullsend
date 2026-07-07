package binary

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSourceRoot_RejectsMissingModule(t *testing.T) {
	dir := t.TempDir()
	err := ValidateSourceRoot(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "go.mod")
}

func TestValidateSourceRoot_AcceptsCheckout(t *testing.T) {
	root, err := ModuleRoot()
	if err != nil {
		t.Skip("not in fullsend checkout")
	}
	require.NoError(t, ValidateSourceRoot(root))
}

func TestResolveVendorRoot_ExplicitSource(t *testing.T) {
	root, err := ModuleRoot()
	if err != nil {
		t.Skip("not in fullsend checkout")
	}

	got, err := ResolveVendorRoot(root, "dev")
	require.NoError(t, err)
	assert.Equal(t, root, got.Path)
	assert.Nil(t, got.Cleanup)
}

func TestResolveVendorRoot_FromModuleRoot(t *testing.T) {
	if _, err := ModuleRoot(); err != nil {
		t.Skip("not in fullsend checkout")
	}

	got, err := ResolveVendorRoot("", "dev")
	require.NoError(t, err)
	assert.DirExists(t, got.Path)
	assert.Contains(t, filepath.Join(got.Path, "go.mod"), "go.mod")
}

func TestResolveVendorRoot_DevBuildOutsideCheckout(t *testing.T) {
	dir := t.TempDir()
	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(prev) })

	_, err = ResolveVendorRoot("", "dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dev build")
}
