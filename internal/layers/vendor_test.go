package layers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/binary"
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
	assert.Contains(t, msg, "--vendor-fullsend-binary not set")
}

func TestVendorCommitMessage_ReleaseTitle(t *testing.T) {
	msg := VendorCommitMessage(binary.SourceReleaseDownload, "v0.4.0", "bin/fullsend", 100)
	assert.True(t, strings.HasPrefix(msg, "chore: vendor fullsend v0.4.0 binary from release"))
}
