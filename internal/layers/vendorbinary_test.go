package layers

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/binary"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newVendorBinaryLayer(t *testing.T, client *forge.FakeClient, enabled bool, vendorFn VendorFunc) (*VendorBinaryLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewVendorBinaryLayer("test-org", ".fullsend", client, printer, enabled, vendorFn)
	return layer, &buf
}

func TestVendorBinaryLayer_Name(t *testing.T) {
	layer, _ := newVendorBinaryLayer(t, &forge.FakeClient{}, false, nil)
	assert.Equal(t, "vendor", layer.Name())
}

func TestVendorBinaryLayer_RequiredScopes(t *testing.T) {
	layer, _ := newVendorBinaryLayer(t, &forge.FakeClient{}, false, nil)

	assert.Equal(t, []string{"repo"}, layer.RequiredScopes(OpInstall))
	assert.Nil(t, layer.RequiredScopes(OpUninstall))
	assert.Nil(t, layer.RequiredScopes(OpAnalyze))
}

func TestVendorBinaryLayer_CombinedWithScaffold_SkipsVendorFn(t *testing.T) {
	client := &forge.FakeClient{}
	called := false
	vendorFn := func(ctx context.Context, c forge.Client, p *ui.Printer, owner, repo string) error {
		called = true
		return nil
	}

	layer, _ := newVendorBinaryLayer(t, client, true, vendorFn)
	layer.SetCombinedWithScaffold(true)

	err := layer.Install(context.Background())
	require.NoError(t, err)
	assert.False(t, called, "vendor function should be skipped when combined with scaffold")
}

func TestVendorBinaryLayer_EnabledCallsVendorFn(t *testing.T) {
	client := &forge.FakeClient{}
	called := false
	vendorFn := func(ctx context.Context, c forge.Client, p *ui.Printer, owner, repo string) error {
		called = true
		assert.Equal(t, "test-org", owner)
		assert.Equal(t, ".fullsend", repo)
		return nil
	}

	layer, _ := newVendorBinaryLayer(t, client, true, vendorFn)

	err := layer.Install(context.Background())
	require.NoError(t, err)
	assert.True(t, called, "vendor function should have been called")
}

func TestVendorBinaryLayer_EnabledVendorFnError(t *testing.T) {
	client := &forge.FakeClient{}
	vendorFn := func(_ context.Context, _ forge.Client, _ *ui.Printer, _, _ string) error {
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
	assert.Contains(t, client.DeletedFiles[0].Message, "remove stale vendored fullsend assets")
	assert.Contains(t, client.DeletedFiles[0].Message, "bin/fullsend")

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
			"DeleteFiles": errors.New("permission denied"),
		},
	}
	layer, _ := newVendorBinaryLayer(t, client, false, nil)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deleting vendored content")
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
	assert.Equal(t, "vendor", report.Name)
	assert.Equal(t, StatusDegraded, report.Status)
	assert.True(t, strings.Contains(strings.Join(report.Details, " "), "vendored binary present at"))
	assert.True(t, strings.Contains(strings.Join(report.Details, " "), "legacy vendored install"))
}

func TestVendorBinaryLayer_Analyze_EnabledAbsent(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{},
	}
	layer, _ := newVendorBinaryLayer(t, client, true, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.Contains(t, report.WouldInstall, "upload vendored binary and content")
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
	assert.True(t, strings.Contains(strings.Join(report.Details, " "), "vendored binary present at"))
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
	assert.Contains(t, report.Details, "vendored binary absent")
}

func TestVendorBinaryLayer_Analyze_ManifestAligned(t *testing.T) {
	manifest := scaffold.NewVendorManifest("0.4.0", "", "bin/fullsend", []string{
		".defaults/action.yml",
		".github/workflows/reusable-triage.yml",
	})
	manifestYAML, err := manifest.MarshalYAML()
	require.NoError(t, err)

	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/.fullsend/bin/fullsend":                          []byte("binary-data"),
			"test-org/.fullsend/.defaults/action.yml":                  []byte("marker"),
			"test-org/.fullsend/.github/workflows/reusable-triage.yml": []byte("workflow"),
			"test-org/.fullsend/vendor-manifest.yaml":                  manifestYAML,
		},
	}
	layer, _ := newVendorBinaryLayer(t, client, true, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StatusInstalled, report.Status)
	assert.Contains(t, strings.Join(report.Details, " "), "manifest alignment: ok")
}

func TestVendorBinaryLayer_Analyze_ManifestMissingPath(t *testing.T) {
	manifest := scaffold.NewVendorManifest("0.4.0", "", "bin/fullsend", []string{
		".defaults/action.yml",
		".github/workflows/reusable-triage.yml",
	})
	manifestYAML, err := manifest.MarshalYAML()
	require.NoError(t, err)

	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/.fullsend/bin/fullsend":         []byte("binary-data"),
			"test-org/.fullsend/.defaults/action.yml": []byte("marker"),
			"test-org/.fullsend/vendor-manifest.yaml": manifestYAML,
		},
	}
	layer, _ := newVendorBinaryLayer(t, client, true, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StatusDegraded, report.Status)
	assert.Contains(t, strings.Join(report.Details, " "), "manifest alignment: 1 missing path(s)")
}

func TestVendorBinaryLayer_Analyze_GetFileContentError(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{
			"GetFileContent": errors.New("network error"),
		},
	}
	layer, _ := newVendorBinaryLayer(t, client, false, nil)

	_, err := layer.Analyze(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checking vendored marker")
}

// binaryPath tests — per-org vs per-repo path selection.

func TestVendorBinaryLayer_BinaryPath_PerOrg(t *testing.T) {
	layer, _ := newVendorBinaryLayer(t, &forge.FakeClient{}, false, nil)
	assert.Equal(t, VendoredBinaryPath, layer.binaryPath())
}

func TestVendorBinaryLayer_BinaryPath_PerRepo(t *testing.T) {
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewVendorBinaryLayer("test-org", "my-repo", &forge.FakeClient{}, printer, false, nil)
	assert.Equal(t, VendoredBinaryPathPerRepo, layer.binaryPath())
}

// Per-repo mode tests — verify correct paths are used for cleanup and analyze.

func TestVendorBinaryLayer_PerRepo_DisabledDeletesBinary(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/my-repo/.fullsend/bin/fullsend": []byte("binary-data"),
		},
	}
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewVendorBinaryLayer("test-org", "my-repo", client, printer, false, nil)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	require.Len(t, client.DeletedFiles, 1)
	assert.Equal(t, "my-repo", client.DeletedFiles[0].Repo)
	assert.Equal(t, VendoredBinaryPathPerRepo, client.DeletedFiles[0].Path)
}

func TestVendorBinaryLayer_PerRepo_Analyze_EnabledPresent(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/my-repo/.fullsend/bin/fullsend": []byte("binary-data"),
		},
	}
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewVendorBinaryLayer("test-org", "my-repo", client, printer, true, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StatusDegraded, report.Status)
	assert.True(t, strings.Contains(strings.Join(report.Details, " "), "vendored binary present at"))
}

func TestVendorBinaryLayer_PerRepo_Analyze_DisabledPresent(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/my-repo/.fullsend/bin/fullsend": []byte("binary-data"),
		},
	}
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewVendorBinaryLayer("test-org", "my-repo", client, printer, false, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StatusDegraded, report.Status)
	assert.True(t, strings.Contains(strings.Join(report.Details, " "), "vendored binary present at"))
}

func TestVendorBinaryLayer_PerRepo_EnabledCallsVendorFn(t *testing.T) {
	client := &forge.FakeClient{}
	called := false
	vendorFn := func(ctx context.Context, c forge.Client, p *ui.Printer, owner, repo string) error {
		called = true
		assert.Equal(t, "test-org", owner)
		assert.Equal(t, "my-repo", repo)
		return nil
	}
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewVendorBinaryLayer("test-org", "my-repo", client, printer, true, vendorFn)

	err := layer.Install(context.Background())
	require.NoError(t, err)
	assert.True(t, called, "vendor function should have been called with per-repo args")
}

func TestVendorBinaryLayer_SetAnalyzeOptions_SourceAlignmentOk(t *testing.T) {
	modRoot, err := binary.ModuleRoot()
	if err != nil {
		t.Skip("not in fullsend checkout")
	}

	expectedFiles, err := scaffold.CollectVendoredAssets(modRoot, "")
	require.NoError(t, err)

	contents := map[string][]byte{
		"test-org/.fullsend/bin/fullsend": []byte("binary"),
	}
	for _, f := range expectedFiles {
		contents["test-org/.fullsend/"+f.Path] = f.Content
	}

	layer, _ := newVendorBinaryLayer(t, &forge.FakeClient{FileContents: contents}, true, nil)
	layer.SetAnalyzeOptions("", "dev")

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Contains(t, strings.Join(report.Details, " "), "source alignment: ok")
}

func TestVendorBinaryLayer_SetAnalyzeOptions_SourceAlignmentMissing(t *testing.T) {
	modRoot, err := binary.ModuleRoot()
	if err != nil {
		t.Skip("not in fullsend checkout")
	}

	expectedFiles, err := scaffold.CollectVendoredAssets(modRoot, "")
	require.NoError(t, err)
	require.NotEmpty(t, expectedFiles)

	contents := map[string][]byte{
		"test-org/.fullsend/bin/fullsend": []byte("binary"),
	}
	// Omit all vendored content paths.

	layer, _ := newVendorBinaryLayer(t, &forge.FakeClient{FileContents: contents}, true, nil)
	layer.SetAnalyzeOptions("", "dev")

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StatusDegraded, report.Status)
	assert.Contains(t, strings.Join(report.Details, " "), "source alignment:")
}

func TestVendorBinaryLayer_SetAnalyzeOptions_SkippedWithoutSource(t *testing.T) {
	layer, _ := newVendorBinaryLayer(t, &forge.FakeClient{}, true, nil)
	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)
	assert.Contains(t, strings.Join(report.Details, " "), "source alignment: skipped")
}

func TestContainsWouldFix(t *testing.T) {
	fixes := []string{"restore vendored path foo", "sync vendored path bar"}
	assert.True(t, containsWouldFix(fixes, "foo"))
	assert.True(t, containsWouldFix(fixes, "bar"))
	assert.False(t, containsWouldFix(fixes, "baz"))
}
