package layers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/binary"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestVendorCommitMessage_HasTitleAndBody(t *testing.T) {
	tests := []struct {
		name   string
		source binary.Source
		ver    string
		path   string
		size   int64
		want   []string
	}{
		{
			name:   "explicit path",
			source: binary.SourceExplicitPath,
			ver:    "dev",
			path:   ".fullsend/bin/fullsend",
			size:   1024,
			want:   []string{"Source: --fullsend-binary", "Path: .fullsend/bin/fullsend", "Size: 1024 bytes"},
		},
		{
			name:   "checkout build",
			source: binary.SourceCheckoutBuild,
			ver:    "dev",
			path:   "bin/fullsend",
			size:   2048,
			want:   []string{"Source: cross-compiled from checkout", "Binary stamp: dev-vendored", "Path: bin/fullsend"},
		},
		{
			name:   "release download",
			source: binary.SourceReleaseDownload,
			ver:    "0.4.0",
			path:   "bin/fullsend",
			size:   4096,
			want:   []string{"Source: GitHub Release v0.4.0", "no -vendored suffix"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := VendorCommitMessage(tt.source, tt.ver, tt.path, tt.size)
			require.Contains(t, msg, "\n\n", "commit message must have title and body separated by blank line")
			for _, line := range tt.want {
				assert.Contains(t, msg, line)
			}
		})
	}
}

func TestRemoveStaleBinaryCommitMessage_HasTitleAndBody(t *testing.T) {
	msg := RemoveStaleBinaryCommitMessage(".fullsend/bin/fullsend")
	require.Contains(t, msg, "\n\n")
	assert.Contains(t, msg, "chore: remove vendored fullsend binary")
	assert.Contains(t, msg, "Path: .fullsend/bin/fullsend")
	assert.Contains(t, msg, "--vendor not set")
}

func TestVendorCommitMessage_ReleaseTitle(t *testing.T) {
	msg := VendorCommitMessage(binary.SourceReleaseDownload, "v0.4.0", "bin/fullsend", 100)
	assert.True(t, strings.HasPrefix(msg, "chore: vendor fullsend v0.4.0 binary from release"))
}

func TestVendorContentCommitMessage(t *testing.T) {
	msg := VendorContentCommitMessage("0.4.0", ".fullsend/", 42)
	require.Contains(t, msg, "\n\n")
	assert.Contains(t, msg, "CLI version: 0.4.0")
	assert.Contains(t, msg, "Prefix: .fullsend/")
	assert.Contains(t, msg, "Files: 42")
}

func TestRemoveStaleContentCommitMessage(t *testing.T) {
	msg := RemoveStaleContentCommitMessage(".defaults/action.yml")
	require.Contains(t, msg, "\n\n")
	assert.Contains(t, msg, "Path: .defaults/action.yml")
}

func TestRemoveStaleVendoredAssetsCommitMessage(t *testing.T) {
	msg := RemoveStaleVendoredAssetsCommitMessage([]string{"bin/fullsend", ".defaults/action.yml"})
	require.Contains(t, msg, "\n\n")
	assert.Contains(t, msg, "Paths: 2")
	assert.Contains(t, msg, "- bin/fullsend")
}

func TestVendorBinary_Upload(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fullsend")
	require.NoError(t, os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755))

	client := &forge.FakeClient{}
	err := VendorBinary(context.Background(), client, "org", forge.ConfigRepoName, VendoredBinaryPath, binPath, "chore: vendor binary")
	require.NoError(t, err)

	key := "org/" + forge.ConfigRepoName + "/" + VendoredBinaryPath
	assert.Contains(t, client.FileContents, key)
}

func TestVendorBinary_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	err := VendorBinary(context.Background(), &forge.FakeClient{}, "org", forge.ConfigRepoName, VendoredBinaryPath, dir, "msg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is a directory")
}

func TestVendorBinary_RejectsMissingFile(t *testing.T) {
	err := VendorBinary(context.Background(), &forge.FakeClient{}, "org", forge.ConfigRepoName, VendoredBinaryPath, "/nonexistent/fullsend", "msg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stat binary")
}

func TestVendorBinary_UploadError(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fullsend")
	require.NoError(t, os.WriteFile(binPath, []byte("bin"), 0o755))

	client := &forge.FakeClient{
		Errors: map[string]error{
			"CreateOrUpdateFile": errors.New("upload denied"),
		},
	}
	err := VendorBinary(context.Background(), client, "org", forge.ConfigRepoName, VendoredBinaryPath, binPath, "msg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uploading vendored binary")
}

func TestDeleteVendoredPaths(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"org/.fullsend/bin/fullsend":         []byte("x"),
			"org/.fullsend/.defaults/action.yml": []byte("y"),
		},
	}
	removed, err := DeleteVendoredPaths(context.Background(), client, "org", forge.ConfigRepoName,
		[]string{"bin/fullsend", ".defaults/action.yml"})
	require.NoError(t, err)
	assert.Equal(t, 2, removed)
}

func TestVendorCommitMessage_UnknownSource(t *testing.T) {
	msg := VendorCommitMessage(binary.Source(99), "dev", "bin/fullsend", 512)
	assert.Contains(t, msg, "chore: vendor fullsend binary for development")
	assert.Contains(t, msg, "Path: bin/fullsend")
}
