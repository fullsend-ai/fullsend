package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestValidateVendorFlags(t *testing.T) {
	require.NoError(t, validateVendorFlags(false, "", ""))
	require.NoError(t, validateVendorFlags(true, "", ""))
	require.NoError(t, validateVendorFlags(true, "/tmp/fullsend", ""))
	require.NoError(t, validateVendorFlags(true, "", "/tmp/src"))

	err := validateVendorFlags(false, "/tmp/fullsend", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--fullsend-binary requires --vendor")

	err = validateVendorFlags(false, "", "/tmp/src")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--fullsend-source requires --vendor")
}

func TestInstallCmd_HasFullsendBinaryFlag(t *testing.T) {
	cmd := newInstallCmd()
	flag := cmd.Flags().Lookup("fullsend-binary")
	require.NotNil(t, flag, "expected --fullsend-binary flag")
	assert.Equal(t, "", flag.DefValue)
}

func TestGitHubSetupCmd_HasFullsendBinaryFlag(t *testing.T) {
	cmd := newGitHubSetupCmd()
	flag := cmd.Flags().Lookup("fullsend-binary")
	require.NotNil(t, flag, "expected --fullsend-binary flag")
}

func TestVendorDryRunMessage(t *testing.T) {
	msg := vendorDryRunMessage("/tmp/fullsend", "", layers.VendoredBinaryPathPerRepo)
	assert.Contains(t, msg, "/tmp/fullsend")
	assert.Contains(t, msg, layers.VendoredBinaryPathPerRepo)

	msg = vendorDryRunMessage("/tmp/fullsend", "/tmp/src", layers.VendoredBinaryPathPerRepo)
	assert.Contains(t, msg, "content from /tmp/src")

	msg = vendorDryRunMessage("", "/tmp/src", layers.VendoredBinaryPath)
	assert.Contains(t, msg, "Would cross-compile from /tmp/src")

	msg = vendorDryRunMessage("", "", layers.VendoredBinaryPath)
	assert.True(t, strings.Contains(msg, "Would cross-compile and upload") ||
		strings.Contains(msg, "Would download release") ||
		strings.Contains(msg, "Would fail: dev CLI"))
}

func TestAppendVendorTreeFiles_Disabled(t *testing.T) {
	files := []forge.TreeFile{{Path: "shim.yaml", Content: []byte("x")}}
	out, count, err := appendVendorTreeFiles(context.Background(), forge.NewFakeClient(), ui.New(nil), "org", "my-repo", files, false, "", "")
	require.NoError(t, err)
	assert.Equal(t, files, out)
	assert.Equal(t, 0, count)
}

func TestAppendVendorTreeFiles_Enabled(t *testing.T) {
	exe := amd64VendorBinary(t)

	files := []forge.TreeFile{{Path: "shim.yaml", Content: []byte("x")}}
	var buf strings.Builder
	out, count, err := appendVendorTreeFiles(context.Background(), forge.NewFakeClient(), ui.New(&buf), "org", "my-repo", files, true, exe, "")
	require.NoError(t, err)
	assert.Greater(t, len(out), len(files))
	assert.Greater(t, count, 0)
}

func TestAppendVendorTreeFiles_InvalidBinary(t *testing.T) {
	_, _, err := appendVendorTreeFiles(context.Background(), forge.NewFakeClient(), ui.New(&strings.Builder{}), "org", "my-repo", nil, true, "/nonexistent/fullsend", "")
	require.Error(t, err)
}

func TestVendorPathPrefix(t *testing.T) {
	assert.Equal(t, "", vendorPathPrefix("org", forge.ConfigRepoName))
	assert.Equal(t, ".fullsend/", vendorPathPrefix("org", "my-repo"))
}

func TestApplyDeprecatedVendorBinaryFlag(t *testing.T) {
	cmd := newInstallCmd()
	require.NoError(t, cmd.ParseFlags([]string{"--vendor-fullsend-binary"}))

	var vendor bool
	applyDeprecatedVendorBinaryFlag(cmd, &vendor)
	assert.True(t, vendor)
}

func TestPrepareVendorFiles_ExplicitBinary(t *testing.T) {
	exe := amd64VendorBinary(t)

	bundle, cleanup, err := prepareVendorFiles(context.Background(), forge.NewFakeClient(), ui.New(&strings.Builder{}), "org", "my-repo", exe, "")
	require.NoError(t, err)
	t.Cleanup(cleanup)
	assert.Greater(t, bundle.assetCount, 0)
	assert.NotEmpty(t, bundle.files)
}

func TestPrepareVendorFiles_InvalidExplicitBinary(t *testing.T) {
	_, cleanup, err := prepareVendorFiles(context.Background(), forge.NewFakeClient(), ui.New(&strings.Builder{}), "org", "my-repo", "/nonexistent/fullsend", "")
	require.Error(t, err)
	cleanup()
	assert.Contains(t, err.Error(), "validating --fullsend-binary")
}
