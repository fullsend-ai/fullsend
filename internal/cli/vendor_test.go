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
	out, count, err := appendVendorTreeFiles(ui.New(nil), "org", "my-repo", files, false, "", "")
	require.NoError(t, err)
	assert.Equal(t, files, out)
	assert.Equal(t, 0, count)
}

func TestAppendVendorTreeFiles_Enabled(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux ELF binary")
	}
	exe, err := os.Executable()
	require.NoError(t, err)

	files := []forge.TreeFile{{Path: "shim.yaml", Content: []byte("x")}}
	var buf strings.Builder
	out, count, err := appendVendorTreeFiles(ui.New(&buf), "org", "my-repo", files, true, exe, "")
	require.NoError(t, err)
	assert.Greater(t, len(out), len(files))
	assert.Greater(t, count, 0)
}

func TestMakeVendorCollectFunc(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux ELF binary")
	}
	exe, err := os.Executable()
	require.NoError(t, err)

	var buf strings.Builder
	fn := makeVendorCollectFunc(exe, "")
	require.NotNil(t, fn)
	files, count, err := fn(context.Background(), ui.New(&buf), "org", "my-repo")
	require.NoError(t, err)
	assert.NotEmpty(t, files)
	assert.Greater(t, count, 0)
}

func TestMakeVendorCollectFunc_InvalidBinary(t *testing.T) {
	fn := makeVendorCollectFunc("/nonexistent/fullsend", "")
	_, _, err := fn(context.Background(), ui.New(&strings.Builder{}), "org", "my-repo")
	require.Error(t, err)
}

func TestAcquireAndVendor_ExplicitPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux ELF binary")
	}
	exe, err := os.Executable()
	require.NoError(t, err)

	client := &forge.FakeClient{}
	var buf strings.Builder
	printer := ui.New(&buf)

	err = acquireAndVendor(context.Background(), client, printer, "org", "my-repo", exe, "")
	require.NoError(t, err)

	key := "org/my-repo/" + layers.VendoredBinaryPathPerRepo
	require.Contains(t, client.FileContents, key)
	require.Len(t, client.CommittedFiles, 1)
	commit := client.CommittedFiles[0]
	assert.Contains(t, commit.Message, "\n\n")
	assert.Contains(t, commit.Message, "Source: --vendor install")
	var paths []string
	for _, f := range commit.Files {
		paths = append(paths, f.Path)
	}
	assert.Contains(t, paths, layers.VendoredBinaryPathPerRepo)
}

func TestAcquireAndVendor_CheckoutBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cross-compile in short mode")
	}

	client := &forge.FakeClient{}
	var buf strings.Builder
	printer := ui.New(&buf)

	err := acquireAndVendor(context.Background(), client, printer, "org", forge.ConfigRepoName, "", "")
	require.NoError(t, err)

	key := "org/" + forge.ConfigRepoName + "/" + layers.VendoredBinaryPath
	require.Contains(t, client.FileContents, key)
	require.Len(t, client.CommittedFiles, 1)
	assert.Contains(t, client.CommittedFiles[0].Message, "\n\n")
	assert.Contains(t, client.CommittedFiles[0].Message, "Source: --vendor install")
}

func TestVendorStackArgs(t *testing.T) {
	vendorFn, collectFn := vendorStackArgs(false, "", "")
	assert.Nil(t, vendorFn)
	assert.Nil(t, collectFn)

	vendorFn, collectFn = vendorStackArgs(true, "", "")
	assert.NotNil(t, vendorFn)
	assert.NotNil(t, collectFn)
}

func TestVendorPathPrefix(t *testing.T) {
	assert.Equal(t, "", vendorPathPrefix("org", forge.ConfigRepoName))
	assert.Equal(t, ".fullsend/", vendorPathPrefix("org", "my-repo"))
}

func TestMakeVendorFunc(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux ELF binary")
	}
	exe, err := os.Executable()
	require.NoError(t, err)

	fn := makeVendorFunc(exe, "")
	require.NotNil(t, fn)
	err = fn(context.Background(), &forge.FakeClient{}, ui.New(&strings.Builder{}), "org", "my-repo")
	require.NoError(t, err)
}

func TestApplyDeprecatedVendorBinaryFlag(t *testing.T) {
	cmd := newInstallCmd()
	require.NoError(t, cmd.ParseFlags([]string{"--vendor-fullsend-binary"}))

	var vendor bool
	applyDeprecatedVendorBinaryFlag(cmd, &vendor)
	assert.True(t, vendor)
}

func TestPrepareVendorFiles_ExplicitBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux ELF binary")
	}
	exe, err := os.Executable()
	require.NoError(t, err)

	bundle, cleanup, err := prepareVendorFiles(ui.New(&strings.Builder{}), "org", "my-repo", exe, "")
	require.NoError(t, err)
	t.Cleanup(cleanup)
	assert.Greater(t, bundle.assetCount, 0)
	assert.NotEmpty(t, bundle.files)
}

func TestPrepareVendorFiles_InvalidExplicitBinary(t *testing.T) {
	_, cleanup, err := prepareVendorFiles(ui.New(&strings.Builder{}), "org", "my-repo", "/nonexistent/fullsend", "")
	require.Error(t, err)
	cleanup()
	assert.Contains(t, err.Error(), "validating --fullsend-binary")
}
