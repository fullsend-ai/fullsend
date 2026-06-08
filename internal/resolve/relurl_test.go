package resolve

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveRelativeURL(t *testing.T) {
	tests := []struct {
		name      string
		parentURL string
		relRef    string
		want      string
		wantErr   bool
	}{
		{
			name:      "sibling reference",
			parentURL: "https://example.com/skills/rust/SKILL.md",
			relRef:    "../common/SKILL.md",
			want:      "https://example.com/skills/common/SKILL.md",
		},
		{
			name:      "child reference",
			parentURL: "https://example.com/skills/rust/SKILL.md",
			relRef:    "policies/sandbox.yaml",
			want:      "https://example.com/skills/rust/policies/sandbox.yaml",
		},
		{
			name:      "absolute URL passthrough",
			parentURL: "https://example.com/skills/rust/SKILL.md",
			relRef:    "https://other.com/skills/common/SKILL.md#sha256=abc",
			want:      "https://other.com/skills/common/SKILL.md#sha256=abc",
		},
		{
			name:      "path traversal resolves correctly",
			parentURL: "https://github.com/org/skills/rust/SKILL.md",
			relRef:    "../../../../attacker/evil.md",
			want:      "https://github.com/attacker/evil.md",
		},
		{
			name:      "multiple parent segments",
			parentURL: "https://example.com/a/b/c/d/SKILL.md",
			relRef:    "../../other/sub/SKILL.md",
			want:      "https://example.com/a/b/other/sub/SKILL.md",
		},
		{
			name:      "fragment preservation",
			parentURL: "https://example.com/skills/rust/SKILL.md",
			relRef:    "../common/SKILL.md#sha256=abc123",
			want:      "https://example.com/skills/common/SKILL.md#sha256=abc123",
		},
		{
			name:      "bare fragment reference",
			parentURL: "https://example.com/skills/rust/SKILL.md",
			relRef:    "#sha256=abc123",
			want:      "https://example.com/skills/rust/SKILL.md#sha256=abc123",
		},
		{
			name:      "invalid parent URL",
			parentURL: "://bad-url",
			relRef:    "../sibling.md",
			wantErr:   true,
		},
		{
			name:      "invalid relRef percent-encoding",
			parentURL: "https://example.com/skills/rust/SKILL.md",
			relRef:    "%xy/invalid.md",
			wantErr:   true,
		},
		{
			name:      "empty relRef resolves to parent URL",
			parentURL: "https://example.com/skills/rust/SKILL.md",
			relRef:    "",
			want:      "https://example.com/skills/rust/SKILL.md",
		},
		{
			name:      "empty parentURL with relative ref",
			parentURL: "",
			relRef:    "other/SKILL.md",
			want:      "/other/SKILL.md",
		},
		{
			name:      "parent URL with no path component",
			parentURL: "https://example.com",
			relRef:    "../foo",
			want:      "https://example.com/foo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveRelativeURL(tt.parentURL, tt.relRef)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
