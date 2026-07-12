package forge

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectForge(t *testing.T) {
	tests := []struct {
		name      string
		remoteURL string
		want      string
		wantErr   bool
		errSubstr string
	}{
		// HTTPS GitHub
		{
			name:      "HTTPS GitHub with .git",
			remoteURL: "https://github.com/org/repo.git",
			want:      "github",
		},
		{
			name:      "HTTPS GitHub without .git",
			remoteURL: "https://github.com/org/repo",
			want:      "github",
		},
		// HTTPS GitLab
		{
			name:      "HTTPS GitLab with .git",
			remoteURL: "https://gitlab.com/org/repo.git",
			want:      "gitlab",
		},
		{
			name:      "HTTPS GitLab without .git",
			remoteURL: "https://gitlab.com/org/repo",
			want:      "gitlab",
		},
		// SSH GitHub
		{
			name:      "SSH GitHub",
			remoteURL: "git@github.com:org/repo.git",
			want:      "github",
		},
		{
			name:      "SSH GitHub without .git",
			remoteURL: "git@github.com:org/repo",
			want:      "github",
		},
		// SSH GitLab
		{
			name:      "SSH GitLab",
			remoteURL: "git@gitlab.com:org/repo.git",
			want:      "gitlab",
		},
		{
			name:      "SSH GitLab without .git",
			remoteURL: "git@gitlab.com:org/repo",
			want:      "gitlab",
		},
		// Case insensitivity
		{
			name:      "case insensitive GitHub",
			remoteURL: "https://GitHub.com/org/repo.git",
			want:      "github",
		},
		{
			name:      "case insensitive GitLab uppercase",
			remoteURL: "https://GITLAB.COM/org/repo.git",
			want:      "gitlab",
		},
		{
			name:      "case insensitive SSH GitHub mixed case",
			remoteURL: "git@GitHub.Com:org/repo.git",
			want:      "github",
		},
		// Whitespace handling
		{
			name:      "trailing newline HTTPS",
			remoteURL: "https://github.com/org/repo.git\n",
			want:      "github",
		},
		{
			name:      "trailing newline SSH",
			remoteURL: "git@github.com:org/repo.git\n",
			want:      "github",
		},
		{
			name:      "leading and trailing whitespace",
			remoteURL: "  https://gitlab.com/org/repo.git  ",
			want:      "gitlab",
		},
		{
			name:      "trailing carriage return and newline",
			remoteURL: "https://github.com/org/repo.git\r\n",
			want:      "github",
		},
		// Self-hosted / unknown
		{
			name:      "self-hosted unknown host",
			remoteURL: "https://git.example.com/org/repo.git",
			wantErr:   true,
			errSubstr: "--forge flag",
		},
		{
			name:      "SSH unknown host",
			remoteURL: "git@git.example.com:org/repo.git",
			wantErr:   true,
			errSubstr: "--forge flag",
		},
		// Empty and malformed
		{
			name:      "empty string",
			remoteURL: "",
			wantErr:   true,
			errSubstr: "cannot extract host",
		},
		{
			name:      "bare hostname no scheme",
			remoteURL: "github.com",
			wantErr:   true,
			errSubstr: "cannot extract host",
		},
		{
			name:      "just a path",
			remoteURL: "/org/repo",
			wantErr:   true,
			errSubstr: "cannot extract host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DetectForge(tt.remoteURL)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errSubstr != "" {
					assert.Contains(t, err.Error(), tt.errSubstr)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractHost(t *testing.T) {
	tests := []struct {
		name      string
		remoteURL string
		want      string
	}{
		{
			name:      "HTTPS URL",
			remoteURL: "https://github.com/org/repo.git",
			want:      "github.com",
		},
		{
			name:      "SSH URL",
			remoteURL: "git@github.com:org/repo.git",
			want:      "github.com",
		},
		{
			name:      "SSH with custom user",
			remoteURL: "deploy@gitlab.com:org/repo.git",
			want:      "gitlab.com",
		},
		{
			name:      "empty string",
			remoteURL: "",
			want:      "",
		},
		{
			name:      "no host extractable",
			remoteURL: "justastring",
			want:      "",
		},
		{
			name:      "HTTP URL",
			remoteURL: "http://github.com/org/repo.git",
			want:      "github.com",
		},
		// ssh:// scheme
		{
			name:      "SSH scheme URL",
			remoteURL: "ssh://git@github.com/org/repo.git",
			want:      "github.com",
		},
		{
			name:      "SSH scheme with port",
			remoteURL: "ssh://git@github.com:22/org/repo.git",
			want:      "github.com",
		},
		// HTTPS with port
		{
			name:      "HTTPS with custom port",
			remoteURL: "https://gitlab.com:8443/org/repo.git",
			want:      "gitlab.com",
		},
		// Whitespace
		{
			name:      "trailing newline",
			remoteURL: "https://github.com/org/repo.git\n",
			want:      "github.com",
		},
		{
			name:      "trailing newline SSH shorthand",
			remoteURL: "git@github.com:org/repo.git\n",
			want:      "github.com",
		},
		{
			name:      "leading and trailing spaces",
			remoteURL: "  https://github.com/org/repo.git  ",
			want:      "github.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractHost(tt.remoteURL)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDetectForgeDistinctFromIsSupportedForge(t *testing.T) {
	forge, err := DetectForge("https://gitlab.com/org/repo.git")
	require.NoError(t, err)
	assert.Equal(t, "gitlab", forge)

	assert.False(t, IsSupportedForge("gitlab.com"),
		"IsSupportedForge gates fetch support (harness validation), not URL parsing or detection")
}
