package urlutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid https", "https://example.com/path/file.md", true},
		{"valid https with port", "https://example.com:8443/path", true},
		{"valid https with query", "https://example.com/path?q=1", true},
		{"valid https with fragment", "https://example.com/path#sha256=abc", true},
		{"http rejected", "http://example.com/path", false},
		{"file scheme rejected", "file:///etc/passwd", false},
		{"ftp rejected", "ftp://example.com/file", false},
		{"empty string", "", false},
		{"relative path", "agents/code.md", false},
		{"absolute path", "/opt/agents/code.md", false},
		{"empty host", "https:///path", false},
		{"scheme only", "https://", false},
		{"userinfo", "https://user:pass@example.com/path", false},
		{"userinfo user only", "https://user@example.com/path", false},
		{"plain text", "not a url at all", false},
		{"just a word", "https", false},
		{"at sign in host", "https://user@host.com/path", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsURL(tt.input))
		})
	}
}

func TestParseIntegrityHash(t *testing.T) {
	validHash := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

	tests := []struct {
		name        string
		input       string
		wantURL     string
		wantHash    string
		wantHasHash bool
	}{
		{
			name:        "valid hash",
			input:       "https://example.com/file.md#sha256=" + validHash,
			wantURL:     "https://example.com/file.md",
			wantHash:    validHash,
			wantHasHash: true,
		},
		{
			name:        "valid hash with query params",
			input:       "https://example.com/file.md?v=1#sha256=" + validHash,
			wantURL:     "https://example.com/file.md?v=1",
			wantHash:    validHash,
			wantHasHash: true,
		},
		{
			name:        "no fragment",
			input:       "https://example.com/file.md",
			wantURL:     "https://example.com/file.md",
			wantHash:    "",
			wantHasHash: false,
		},
		{
			name:        "non-sha256 fragment",
			input:       "https://example.com/file.md#section1",
			wantURL:     "https://example.com/file.md#section1",
			wantHash:    "",
			wantHasHash: false,
		},
		{
			name:        "wrong prefix",
			input:       "https://example.com/file.md#md5=abc123",
			wantURL:     "https://example.com/file.md#md5=abc123",
			wantHash:    "",
			wantHasHash: false,
		},
		{
			name:        "hash too short",
			input:       "https://example.com/file.md#sha256=" + validHash[:63],
			wantURL:     "https://example.com/file.md#sha256=" + validHash[:63],
			wantHash:    "",
			wantHasHash: false,
		},
		{
			name:        "hash too long",
			input:       "https://example.com/file.md#sha256=" + validHash + "a",
			wantURL:     "https://example.com/file.md#sha256=" + validHash + "a",
			wantHash:    "",
			wantHasHash: false,
		},
		{
			name:        "uppercase hex normalized",
			input:       "https://example.com/file.md#sha256=ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			wantURL:     "https://example.com/file.md",
			wantHash:    "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			wantHasHash: true,
		},
		{
			name:        "empty hash value",
			input:       "https://example.com/file.md#sha256=",
			wantURL:     "https://example.com/file.md#sha256=",
			wantHash:    "",
			wantHasHash: false,
		},
		{
			name:        "invalid hex chars",
			input:       "https://example.com/file.md#sha256=zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
			wantURL:     "https://example.com/file.md#sha256=zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
			wantHash:    "",
			wantHasHash: false,
		},
		{
			name:        "relative path unchanged",
			input:       "agents/code.md",
			wantURL:     "agents/code.md",
			wantHash:    "",
			wantHasHash: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotHash, gotHasHash := ParseIntegrityHash(tt.input)
			assert.Equal(t, tt.wantURL, gotURL)
			assert.Equal(t, tt.wantHash, gotHash)
			assert.Equal(t, tt.wantHasHash, gotHasHash)
		})
	}
}

func TestMatchingAllowedPrefixInList(t *testing.T) {
	allowlist := []string{
		"https://example.com/skills/",
		"https://cdn.example.com/policies/",
	}

	tests := []struct {
		name      string
		url       string
		allowlist []string
		want      string
	}{
		{"matching first prefix", "https://example.com/skills/summarize.md", allowlist, "https://example.com/skills/"},
		{"matching second prefix", "https://cdn.example.com/policies/readonly.yaml", allowlist, "https://cdn.example.com/policies/"},
		{"no match", "https://evil.com/skills/summarize.md", allowlist, ""},
		{"path traversal rejected", "https://example.com/skills/../evil/payload", allowlist, ""},
		{"case insensitive match", "https://Example.Com/Skills/test.md", []string{"https://example.com/skills/"}, "https://example.com/skills/"},
		{"percent-encoded traversal rejected", "https://example.com/skills/%2e%2e/evil", allowlist, ""},
		{"double encoding rejected", "https://example.com/skills/%252e%252e/evil", allowlist, ""},
		{"empty allowlist", "https://example.com/skills/test.md", nil, ""},
		{"empty URL", "", allowlist, ""},
		{"invalid prefix skipped", "https://example.com/skills/test.md", []string{"://bad", "https://example.com/skills/"}, "https://example.com/skills/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MatchingAllowedPrefixInList(tt.url, tt.allowlist))
		})
	}
}

func TestNormalizeURLPath(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
		wantOK bool
	}{
		{"simple path", "https://example.com/path/file.md", "https://example.com/path/file.md", true},
		{"dot segment cleaned", "https://example.com/a/../b/file.md", "https://example.com/b/file.md", true},
		{"trailing slash preserved", "https://example.com/path/", "https://example.com/path/", true},
		{"percent encoded path", "https://example.com/path%20with%20spaces/file.md", "https://example.com/path%20with%20spaces/file.md", true},
		{"backslash rejected", "https://example.com/path\\file.md", "", false},
		{"dot only path", "https://example.com/.", "https://example.com/", true},
		{"relative path", "agents/code.md", "agents/code.md", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := NormalizeURLPath(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
