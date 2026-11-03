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
				Forge:    "github",
				Owner:    "fullsend-ai",
				Repo:     "library",
				Ref:      "8cd3799abc",
				Path:     "skills/pr-review",
				PathType: "tree",
			},
		},
		{
			name:  "valid GitHub blob URL",
			input: "https://github.com/fullsend-ai/library/blob/8cd3799abc/agents/code.md",
			want: &ForgeURLInfo{
				Forge:    "github",
				Owner:    "fullsend-ai",
				Repo:     "library",
				Ref:      "8cd3799abc",
				Path:     "agents/code.md",
				PathType: "blob",
			},
		},
		{
			name:  "URL with sha256 fragment stripped",
			input: "https://github.com/fullsend-ai/library/tree/abc123/skills/rust#sha256=def456abcdef0123456789abcdef0123456789abcdef0123456789abcdef01",
			want: &ForgeURLInfo{
				Forge:    "github",
				Owner:    "fullsend-ai",
				Repo:     "library",
				Ref:      "abc123",
				Path:     "skills/rust",
				PathType: "tree",
			},
		},
		{
			name:  "root path with no path after ref",
			input: "https://github.com/fullsend-ai/library/tree/abc123",
			want: &ForgeURLInfo{
				Forge:    "github",
				Owner:    "fullsend-ai",
				Repo:     "library",
				Ref:      "abc123",
				Path:     "",
				PathType: "tree",
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
				Forge:    "github",
				Owner:    "org",
				Repo:     "repo",
				Ref:      "main",
				Path:     "a/b/c/d",
				PathType: "tree",
			},
		},
		{
			name:  "tag as ref",
			input: "https://github.com/org/repo/tree/v1.2.3/skills/foo",
			want: &ForgeURLInfo{
				Forge:    "github",
				Owner:    "org",
				Repo:     "repo",
				Ref:      "v1.2.3",
				Path:     "skills/foo",
				PathType: "tree",
			},
		},
		{
			name:  "GitLab tree URL",
			input: "https://gitlab.com/my-group/my-repo/-/tree/main/src/lib",
			want: &ForgeURLInfo{
				Forge:    "gitlab",
				Owner:    "my-group",
				Repo:     "my-repo",
				Ref:      "main",
				Path:     "src/lib",
				PathType: "tree",
			},
		},
		{
			name:  "GitLab blob URL",
			input: "https://gitlab.com/my-group/my-repo/-/blob/abc123/README.md",
			want: &ForgeURLInfo{
				Forge:    "gitlab",
				Owner:    "my-group",
				Repo:     "my-repo",
				Ref:      "abc123",
				Path:     "README.md",
				PathType: "blob",
			},
		},
		{
			name:  "GitLab nested groups",
			input: "https://gitlab.com/top/sub/deep/my-repo/-/tree/v1.0/docs",
			want: &ForgeURLInfo{
				Forge:    "gitlab",
				Owner:    "top/sub/deep",
				Repo:     "my-repo",
				Ref:      "v1.0",
				Path:     "docs",
				PathType: "tree",
			},
		},
		{
			name:  "GitLab root path no trailing path",
			input: "https://gitlab.com/group/repo/-/tree/main",
			want: &ForgeURLInfo{
				Forge:    "gitlab",
				Owner:    "group",
				Repo:     "repo",
				Ref:      "main",
				Path:     "",
				PathType: "tree",
			},
		},
		{
			name:    "GitLab URL missing dash separator",
			input:   "https://gitlab.com/group/repo",
			wantErr: "URL path too short",
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
				Forge:    "github",
				Owner:    "fullsend-ai",
				Repo:     "fullsend",
				Ref:      "a06f8626",
				Path:     "skills/pr-review",
				PathType: "blob",
			},
		},
		{
			name:  "URL with fragment stripped",
			input: "https://raw.githubusercontent.com/org/repo/abc123/path/to/file#sha256=def456",
			want: &ForgeURLInfo{
				Forge:    "github",
				Owner:    "org",
				Repo:     "repo",
				Ref:      "abc123",
				Path:     "path/to/file",
				PathType: "blob",
			},
		},
		{
			name:  "ref only, no path",
			input: "https://raw.githubusercontent.com/owner/repo/main",
			want: &ForgeURLInfo{
				Forge:    "github",
				Owner:    "owner",
				Repo:     "repo",
				Ref:      "main",
				Path:     "",
				PathType: "blob",
			},
		},
		{
			name:  "deep nested path",
			input: "https://raw.githubusercontent.com/org/repo/v1.0.0/a/b/c/d/e",
			want: &ForgeURLInfo{
				Forge:    "github",
				Owner:    "org",
				Repo:     "repo",
				Ref:      "v1.0.0",
				Path:     "a/b/c/d/e",
				PathType: "blob",
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

func TestParseRawContentURLOnHost(t *testing.T) {
	const ghe = "https://ghe.example.com"
	tests := []struct {
		name      string
		input     string
		githubURL string
		want      *ForgeURLInfo
		wantClone string
		wantErr   string
	}{
		{
			name:      "raw.githubusercontent.com unchanged with host configured",
			input:     "https://raw.githubusercontent.com/o/r/main/a/b.md",
			githubURL: ghe,
			want:      &ForgeURLInfo{Forge: "github", Owner: "o", Repo: "r", Ref: "main", Path: "a/b.md", PathType: "blob"},
			wantClone: "https://github.com/o/r.git",
		},
		{
			name:      "configured host",
			input:     "https://ghe.example.com/o/r/raw/main/a/b.md",
			githubURL: ghe,
			want:      &ForgeURLInfo{Forge: "ghe.example.com", Owner: "o", Repo: "r", Ref: "main", Path: "a/b.md", PathType: "blob"},
			wantClone: "https://ghe.example.com/o/r.git",
		},
		{
			name:      "configured host with trailing slash and mixed case",
			input:     "https://GHE.example.com/o/r/raw/abc123/skills/x/SKILL.md#sha256=def456",
			githubURL: "https://ghe.EXAMPLE.com/",
			want:      &ForgeURLInfo{Forge: "ghe.example.com", Owner: "o", Repo: "r", Ref: "abc123", Path: "skills/x/SKILL.md", PathType: "blob"},
			wantClone: "https://ghe.example.com/o/r.git",
		},
		{
			name:      "configured host with port",
			input:     "https://ghe.example.com:8443/o/r/raw/main/a.md",
			githubURL: "https://ghe.example.com:8443",
			want:      &ForgeURLInfo{Forge: "ghe.example.com:8443", Owner: "o", Repo: "r", Ref: "main", Path: "a.md", PathType: "blob"},
			wantClone: "https://ghe.example.com:8443/o/r.git",
		},
		{
			name:      "configured host directory root",
			input:     "https://ghe.example.com/o/r/raw/main",
			githubURL: ghe,
			want:      &ForgeURLInfo{Forge: "ghe.example.com", Owner: "o", Repo: "r", Ref: "main", Path: "", PathType: "blob"},
			wantClone: "https://ghe.example.com/o/r.git",
		},
		{
			name:    "host not configured",
			input:   "https://ghe.example.com/o/r/raw/main/a/b.md",
			wantErr: "not a raw.githubusercontent.com URL: ghe.example.com",
		},
		{
			name:      "different host configured",
			input:     "https://ghe.example.com/o/r/raw/main/a/b.md",
			githubURL: "https://other.example.com",
			wantErr:   "not a raw.githubusercontent.com URL: ghe.example.com",
		},
		{
			name:      "port mismatch",
			input:     "https://ghe.example.com:8443/o/r/raw/main/a.md",
			githubURL: ghe,
			wantErr:   "not a raw.githubusercontent.com URL: ghe.example.com",
		},
		{
			name:      "github.com configured keeps github.com rejected",
			input:     "https://github.com/o/r/raw/main/a.md",
			githubURL: "https://github.com",
			wantErr:   "not a raw.githubusercontent.com URL: github.com",
		},
		{
			name:      "unparseable configured URL",
			input:     "https://ghe.example.com/o/r/raw/main/a.md",
			githubURL: "https://ghe.example.com:bad port",
			wantErr:   "not a raw.githubusercontent.com URL: ghe.example.com",
		},
		{
			name:      "configured host without raw segment",
			input:     "https://ghe.example.com/o/r/blob/main/a.md",
			githubURL: ghe,
			wantErr:   `unsupported path type "blob": expected "raw"`,
		},
		{
			name:      "configured host path too short",
			input:     "https://ghe.example.com/o/r/raw",
			githubURL: ghe,
			wantErr:   "URL path too short: need at least /{owner}/{repo}/raw/{ref}",
		},
		{
			name:      "configured host over http",
			input:     "http://ghe.example.com/o/r/raw/main/a.md",
			githubURL: ghe,
			wantErr:   "unsupported scheme",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRawContentURLOnHost(tt.input, tt.githubURL)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantClone, got.CloneURL())
		})
	}
}

func TestForgeURLInfo_CloneURL(t *testing.T) {
	tests := []struct {
		name string
		info ForgeURLInfo
		want string
	}{
		{
			name: "github",
			info: ForgeURLInfo{Forge: "github", Owner: "fullsend-ai", Repo: "library"},
			want: "https://github.com/fullsend-ai/library.git",
		},
		{
			name: "gitlab",
			info: ForgeURLInfo{Forge: "gitlab", Owner: "my-org", Repo: "my-repo"},
			want: "https://gitlab.com/my-org/my-repo.git",
		},
		{
			name: "unknown forge uses forge as host",
			info: ForgeURLInfo{Forge: "gitea.example.com", Owner: "org", Repo: "repo"},
			want: "https://gitea.example.com/org/repo.git",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.info.CloneURL())
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
		{"gitlab.com — recognized but no fetch support yet", "gitlab.com", false},
		{"example.com", "example.com", false},
		{"empty string", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsSupportedForge(tt.hostname))
		})
	}
}

func TestConfiguredGitHubURL(t *testing.T) {
	t.Setenv(GitHubServerURLEnv, "")
	assert.Equal(t, "", ConfiguredGitHubURL())
	t.Setenv(GitHubServerURLEnv, " https://ghe.example.com \n")
	assert.Equal(t, "https://ghe.example.com", ConfiguredGitHubURL())
}
