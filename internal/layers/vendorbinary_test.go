package layers

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newVendorBinaryLayer(t *testing.T, client *forge.FakeClient, enabled bool, vendorFn VendorFunc) (*VendorBinaryLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewVendorBinaryLayer("test-org", client, printer, enabled, vendorFn)
	return layer, &buf
}

func TestVendorBinaryLayer_Name(t *testing.T) {
	layer, _ := newVendorBinaryLayer(t, &forge.FakeClient{}, false, nil)
	assert.Equal(t, "vendor-binary", layer.Name())
}

func TestVendorBinaryLayer_RequiredScopes(t *testing.T) {
	layer, _ := newVendorBinaryLayer(t, &forge.FakeClient{}, false, nil)

	assert.Equal(t, []string{"repo"}, layer.RequiredScopes(OpInstall))
	assert.Nil(t, layer.RequiredScopes(OpUninstall))
	assert.Nil(t, layer.RequiredScopes(OpAnalyze))
}

func TestVendorBinaryLayer_EnabledCallsVendorFn(t *testing.T) {
	client := &forge.FakeClient{}
	called := false
	vendorFn := func(ctx context.Context, c forge.Client, p *ui.Printer, org string) error {
		called = true
		assert.Equal(t, "test-org", org)
		return nil
	}

	layer, _ := newVendorBinaryLayer(t, client, true, vendorFn)

	err := layer.Install(context.Background())
	require.NoError(t, err)
	assert.True(t, called, "vendor function should have been called")
}

func TestVendorBinaryLayer_EnabledVendorFnError(t *testing.T) {
	client := &forge.FakeClient{}
	vendorFn := func(_ context.Context, _ forge.Client, _ *ui.Printer, _ string) error {
		return errors.New("cross-compile failed")
	}

	layer, _ := newVendorBinaryLayer(t, client, true, vendorFn)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cross-compile failed")
}

func TestVendorBinaryLayer_EnabledErrorsWithoutVendorFn(t *testing.T) {
	client := &forge.FakeClient{}
	layer, _ := newVendorBinaryLayer(t, client, true, nil)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vendor function not configured")
}

func TestVendorBinaryLayer_DisabledDeletesBinary(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/.fullsend/bin/fullsend": []byte("binary-data"),
		},
	}
	layer, _ := newVendorBinaryLayer(t, client, false, nil)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	// Binary should have been deleted
	require.Len(t, client.DeletedFiles, 1)
	assert.Equal(t, "test-org", client.DeletedFiles[0].Owner)
	assert.Equal(t, ".fullsend", client.DeletedFiles[0].Repo)
	assert.Equal(t, "bin/fullsend", client.DeletedFiles[0].Path)

	// File should no longer be in FileContents
	_, ok := client.FileContents["test-org/.fullsend/bin/fullsend"]
	assert.False(t, ok, "binary should have been removed from FileContents")
}

func TestVendorBinaryLayer_DisabledNoopWhenAbsent(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{},
	}
	layer, _ := newVendorBinaryLayer(t, client, false, nil)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	assert.Empty(t, client.DeletedFiles, "no files should have been deleted")
}

func TestVendorBinaryLayer_DisabledDeleteError(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/.fullsend/bin/fullsend": []byte("binary-data"),
		},
		Errors: map[string]error{
			"DeleteFile": errors.New("permission denied"),
		},
	}
	layer, _ := newVendorBinaryLayer(t, client, false, nil)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deleting vendored binary")
}

func TestVendorBinaryLayer_Uninstall(t *testing.T) {
	layer, _ := newVendorBinaryLayer(t, &forge.FakeClient{}, false, nil)
	err := layer.Uninstall(context.Background())
	require.NoError(t, err)
}

// Analyze tests — 4 combinations of enabled/disabled x present/absent.

func TestVendorBinaryLayer_Analyze_EnabledPresent(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/.fullsend/bin/fullsend": []byte("binary-data"),
		},
	}
	layer, _ := newVendorBinaryLayer(t, client, true, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "vendor-binary", report.Name)
	assert.Equal(t, StatusInstalled, report.Status)
	assert.Contains(t, report.Details, "vendored binary present")
}

func TestVendorBinaryLayer_Analyze_EnabledAbsent(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{},
	}
	layer, _ := newVendorBinaryLayer(t, client, true, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.Contains(t, report.WouldInstall, "upload vendored binary")
}

func TestVendorBinaryLayer_Analyze_DisabledPresent(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/.fullsend/bin/fullsend": []byte("binary-data"),
		},
	}
	layer, _ := newVendorBinaryLayer(t, client, false, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StatusDegraded, report.Status)
	assert.Contains(t, report.Details, "stale vendored binary present")
	assert.Contains(t, report.WouldFix, "delete vendored binary")
}

func TestVendorBinaryLayer_Analyze_DisabledAbsent(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{},
	}
	layer, _ := newVendorBinaryLayer(t, client, false, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StatusInstalled, report.Status)
	assert.Contains(t, report.Details, "no vendored binary present")
}

func TestVendorBinaryLayer_Analyze_Error(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{
			"GetFileContent": errors.New("network error"),
		},
	}
	layer, _ := newVendorBinaryLayer(t, client, false, nil)

	_, err := layer.Analyze(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking for vendored binary")
}
