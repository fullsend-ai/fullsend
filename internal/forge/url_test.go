package forge

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseForgeURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *ForgeURLInfo
		wantErr string
	}{
		{
			name:  "valid GitHub tree URL with commit SHA",
			input: "https://github.com/fullsend-ai/library/tree/8cd3799abc/skills/pr-review",
			want: &ForgeURLInfo{
				Forge: "github",
				Owner: "fullsend-ai",
				Repo:  "library",
				Ref:   "8cd3799abc",
				Path:  "skills/pr-review",
			},
		},
		{
			name:  "valid GitHub blob URL",
			input: "https://github.com/fullsend-ai/library/blob/8cd3799abc/agents/code.md",
			want: &ForgeURLInfo{
				Forge: "github",
				Owner: "fullsend-ai",
				Repo:  "library",
				Ref:   "8cd3799abc",
				Path:  "agents/code.md",
			},
		},
		{
			name:  "URL with sha256 fragment stripped",
			input: "https://github.com/fullsend-ai/library/tree/abc123/skills/rust#sha256=def456abcdef0123456789abcdef0123456789abcdef0123456789abcdef01",
			want: &ForgeURLInfo{
				Forge: "github",
				Owner: "fullsend-ai",
				Repo:  "library",
				Ref:   "abc123",
				Path:  "skills/rust",
			},
		},
		{
			name:  "root path with no path after ref",
			input: "https://github.com/fullsend-ai/library/tree/abc123",
			want: &ForgeURLInfo{
				Forge: "github",
				Owner: "fullsend-ai",
				Repo:  "library",
				Ref:   "abc123",
				Path:  "",
			},
		},
		{
			name:    "non-forge domain",
			input:   "https://example.com/foo/bar",
			wantErr: "unsupported forge host",
		},
		{
			name:    "HTTP not HTTPS",
			input:   "http://github.com/owner/repo/tree/ref/path",
			wantErr: "unsupported scheme",
		},
		{
			name:    "missing type segment",
			input:   "https://github.com/owner/repo",
			wantErr: "URL path too short",
		},
		{
			name:    "invalid type segment",
			input:   "https://github.com/owner/repo/commits/ref/path",
			wantErr: "unsupported path type",
		},
		{
			name:  "deep nested path",
			input: "https://github.com/org/repo/tree/main/a/b/c/d",
			want: &ForgeURLInfo{
				Forge: "github",
				Owner: "org",
				Repo:  "repo",
				Ref:   "main",
				Path:  "a/b/c/d",
			},
		},
		{
			name:  "tag as ref",
			input: "https://github.com/org/repo/tree/v1.2.3/skills/foo",
			want: &ForgeURLInfo{
				Forge: "github",
				Owner: "org",
				Repo:  "repo",
				Ref:   "v1.2.3",
				Path:  "skills/foo",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseForgeURL(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseRawContentURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *ForgeURLInfo
		wantErr string
	}{
		{
			name:  "valid raw URL with SHA ref and path",
			input: "https://raw.githubusercontent.com/fullsend-ai/fullsend/a06f8626/skills/pr-review",
			want: &ForgeURLInfo{
				Forge: "github",
				Owner: "fullsend-ai",
				Repo:  "fullsend",
				Ref:   "a06f8626",
				Path:  "skills/pr-review",
			},
		},
		{
			name:  "URL with fragment stripped",
			input: "https://raw.githubusercontent.com/org/repo/abc123/path/to/file#sha256=def456",
			want: &ForgeURLInfo{
				Forge: "github",
				Owner: "org",
				Repo:  "repo",
				Ref:   "abc123",
				Path:  "path/to/file",
			},
		},
		{
			name:  "ref only, no path",
			input: "https://raw.githubusercontent.com/owner/repo/main",
			want: &ForgeURLInfo{
				Forge: "github",
				Owner: "owner",
				Repo:  "repo",
				Ref:   "main",
				Path:  "",
			},
		},
		{
			name:  "deep nested path",
			input: "https://raw.githubusercontent.com/org/repo/v1.0.0/a/b/c/d/e",
			want: &ForgeURLInfo{
				Forge: "github",
				Owner: "org",
				Repo:  "repo",
				Ref:   "v1.0.0",
				Path:  "a/b/c/d/e",
			},
		},
		{
			name:    "wrong host",
			input:   "https://github.com/owner/repo/tree/main/path",
			wantErr: "not a raw.githubusercontent.com URL",
		},
		{
			name:    "HTTP not HTTPS",
			input:   "http://raw.githubusercontent.com/owner/repo/ref",
			wantErr: "unsupported scheme",
		},
		{
			name:    "too few segments — only owner and repo",
			input:   "https://raw.githubusercontent.com/owner/repo",
			wantErr: "URL path too short",
		},
		{
			name:    "too few segments — only owner",
			input:   "https://raw.githubusercontent.com/owner",
			wantErr: "URL path too short",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRawContentURL(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsSupportedForge(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		want     bool
	}{
		{"github.com", "github.com", true},
		{"gitlab.com not yet supported", "gitlab.com", false},
		{"example.com", "example.com", false},
		{"empty string", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsSupportedForge(tt.hostname))
		})
	}
}
