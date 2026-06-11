package cli

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestValidateVendorBinaryFlags(t *testing.T) {
	require.NoError(t, validateVendorBinaryFlags(false, ""))
	require.NoError(t, validateVendorBinaryFlags(true, ""))
	require.NoError(t, validateVendorBinaryFlags(true, "/tmp/fullsend"))

	err := validateVendorBinaryFlags(false, "/tmp/fullsend")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--fullsend-binary requires --vendor-fullsend-binary")
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
	msg := vendorDryRunMessage("/tmp/fullsend", layers.VendoredBinaryPathPerRepo)
	assert.Contains(t, msg, "/tmp/fullsend")
	assert.Contains(t, msg, layers.VendoredBinaryPathPerRepo)
}

func TestAcquireAndVendorFullsendBinary_ExplicitPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux ELF binary")
	}
	exe, err := os.Executable()
	require.NoError(t, err)

	client := &forge.FakeClient{}
	var buf strings.Builder
	printer := ui.New(&buf)

	err = acquireAndVendorFullsendBinary(context.Background(), client, printer, "org", "my-repo", exe)
	require.NoError(t, err)

	key := "org/my-repo/" + layers.VendoredBinaryPathPerRepo
	require.Contains(t, client.FileContents, key)
	require.NotEmpty(t, client.CreatedFiles)
	assert.Contains(t, client.CreatedFiles[0].Message, "\n\n")
	assert.Contains(t, client.CreatedFiles[0].Message, "Source: --fullsend-binary")
}

func TestAcquireAndVendorFullsendBinary_CheckoutBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cross-compile in short mode")
	}

	client := &forge.FakeClient{}
	var buf strings.Builder
	printer := ui.New(&buf)

	err := acquireAndVendorFullsendBinary(context.Background(), client, printer, "org", forge.ConfigRepoName, "")
	require.NoError(t, err)

	key := "org/" + forge.ConfigRepoName + "/" + layers.VendoredBinaryPath
	require.Contains(t, client.FileContents, key)
	require.NotEmpty(t, client.CreatedFiles)
	assert.Contains(t, client.CreatedFiles[0].Message, "cross-compiled from checkout")
}
