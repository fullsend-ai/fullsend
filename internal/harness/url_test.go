package harness

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
		{"relative path with dots", "../agents/code.md", false},
		{"absolute path", "/opt/agents/code.md", false},
		{"empty host", "https:///path", false},
		{"scheme only", "https://", false},
		{"userinfo", "https://user:pass@example.com/path", false},
		{"userinfo user only", "https://user@example.com/path", false},
		{"plain text", "not a url at all", false},
		{"just a word", "https", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsURL(tt.input))
		})
	}
}

func TestIsAbsPath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"absolute unix", "/opt/agents/code.md", true},
		{"relative", "agents/code.md", false},
		{"relative with dots", "../agents/code.md", false},
		{"url", "https://example.com/path", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsAbsPath(tt.input))
		})
	}
}

func TestIsRelPath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"relative", "agents/code.md", true},
		{"relative with dots", "../agents/code.md", true},
		{"dot slash", "./agents/code.md", true},
		{"bare filename", "code.md", true},
		{"empty string", "", false},
		{"absolute path", "/opt/agents/code.md", false},
		{"url", "https://example.com/path", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsRelPath(tt.input))
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
			name:        "hash too short 63 chars",
			input:       "https://example.com/file.md#sha256=" + validHash[:63],
			wantURL:     "https://example.com/file.md#sha256=" + validHash[:63],
			wantHash:    "",
			wantHasHash: false,
		},
		{
			name:        "hash too long 65 chars",
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
			name:        "path traversal in hash rejected",
			input:       "https://example.com/file.md#sha256=../../../../../../etc/shadow//////////////////////////////////",
			wantURL:     "https://example.com/file.md#sha256=../../../../../../etc/shadow//////////////////////////////////",
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
