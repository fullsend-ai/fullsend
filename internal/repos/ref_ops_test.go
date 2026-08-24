package repos

import (
	"fmt"
	"strings"
	"testing"
)

func TestReplaceShimRef(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		newRef   string
		newTag   string
		wantRef  string
		wantDiff bool
	}{
		{
			name:     "simple ref replacement",
			input:    "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0\n",
			newRef:   "v2.3.0",
			wantRef:  "@v2.3.0",
			wantDiff: true,
		},
		{
			name:     "ref with tag comment",
			input:    "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@abc123 # v2.1.0\n",
			newRef:   "v2.3.0",
			wantRef:  "@v2.3.0",
			wantDiff: true,
		},
		{
			name:     "new ref with tag",
			input:    "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0\n",
			newRef:   "def456",
			newTag:   "v2.3.0",
			wantRef:  "@def456 # v2.3.0",
			wantDiff: true,
		},
		{
			name:     "same ref no change",
			input:    "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0\n",
			newRef:   "v2.3.0",
			wantRef:  "@v2.3.0",
			wantDiff: false,
		},
		{
			name:     "no matching uses line",
			input:    "    uses: actions/checkout@v4\n",
			newRef:   "v2.3.0",
			wantDiff: false,
		},
		{
			name: "multiple uses lines",
			input: `    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0
    uses: fullsend-ai/fullsend/.github/actions/mint-token@v2.1.0
`,
			newRef:   "v2.3.0",
			wantRef:  "@v2.3.0",
			wantDiff: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, changed := replaceShimRef([]byte(tt.input), tt.newRef, tt.newTag, GitHubForgeConfig(), ForgeGitHub)
			if changed != tt.wantDiff {
				t.Errorf("changed = %v, want %v", changed, tt.wantDiff)
			}
			if tt.wantRef != "" && changed {
				content := string(result)
				if !strings.Contains(content, tt.wantRef) {
					t.Errorf("result should contain %q, got:\n%s", tt.wantRef, content)
				}
			}
		})
	}
}

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v2.1.0", "v2.3.0", -1},
		{"v2.3.0", "v2.1.0", 1},
		{"v2.3.0", "v2.3.0", 0},
		{"v1.0.0", "v2.0.0", -1},
		{"v2.0.0", "v1.0.0", 1},
		{"v2.3.1", "v2.3.0", 1},
		{"v2.3.0", "v2.3.1", -1},
		{"v10.0.0", "v2.0.0", 1},
		{"v0.1.0", "v0.2.0", -1},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_vs_%s", tt.a, tt.b), func(t *testing.T) {
			got := compareSemver(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestIsSemver(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"v2.3.0", true},
		{"v0.1.0", true},
		{"v10.20.30", true},
		{"v2.3.0-rc1", true},
		{"latest", false},
		{"main", false},
		{"abc123", false},
		{"v0", false},
		{"v1", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got := isSemver(tt.ref)
			if got != tt.want {
				t.Errorf("isSemver(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestReplaceShimRef_TagMatchesRef(t *testing.T) {
	input := "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0\n"
	result, changed := replaceShimRef([]byte(input), "v2.3.0", "v2.3.0", GitHubForgeConfig(), ForgeGitHub)
	if !changed {
		t.Error("expected change")
	}
	content := string(result)
	if strings.Contains(content, "# v2.3.0") {
		t.Error("should not add comment when tag == ref")
	}
}

func TestReplaceShimRef_EmptyTag(t *testing.T) {
	input := "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0\n"
	result, changed := replaceShimRef([]byte(input), "v2.3.0", "", GitHubForgeConfig(), ForgeGitHub)
	if !changed {
		t.Error("expected change")
	}
	content := string(result)
	if strings.Contains(content, "#") {
		t.Error("should not add comment when tag is empty")
	}
}

func TestReplaceShimRef_MultiWordComment(t *testing.T) {
	input := "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0 # version 2.1.0\n"
	result, changed := replaceShimRef([]byte(input), "v2.3.0", "", GitHubForgeConfig(), ForgeGitHub)
	if !changed {
		t.Fatal("expected content to change")
	}
	want := "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.3.0\n"
	if string(result) != want {
		t.Errorf("got %q, want %q", string(result), want)
	}
}

func TestCompareSemver_BuildMetadataIgnored(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v1.0.0-rc1+build123", "v1.0.0-rc1+build456", 0},
		{"v1.0.0+build1", "v1.0.0+build2", 0},
		{"v1.0.0-rc1+build", "v1.0.0-rc2", -1},
	}
	for _, tt := range tests {
		got := compareSemver(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestCompareSemver_NonSemver(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"a non-semver", "abc123", "v2.3.0", 0},
		{"b non-semver", "v2.3.0", "abc123", 0},
		{"both non-semver", "abc", "def", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareSemver(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCompareSemver_PrereleaseHandling(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v2.3.0", "v2.3.0-rc1", 1},
		{"v2.3.0-rc1", "v2.3.0", -1},
		{"v2.3.0-rc1", "v2.3.0-rc2", -1},
		{"v2.3.0-rc2", "v2.3.0-rc1", 1},
		{"v2.3.0-alpha", "v2.3.0-beta", -1},
		{"v2.3.0-rc1", "v2.3.0-rc1", 0},
		{"v1.0.0", "v2.0.0-rc1", -1},
		{"v2.0.0-rc1", "v1.0.0", 1},
		// semver 2.0.0 §11: numeric identifiers compared as integers
		{"v1.0.0-2", "v1.0.0-10", -1},
		{"v1.0.0-10", "v1.0.0-2", 1},
		// numeric < string
		{"v1.0.0-1", "v1.0.0-alpha", -1},
		{"v1.0.0-alpha", "v1.0.0-1", 1},
		// dot-separated: more fields is greater when prefix matches
		{"v1.0.0-alpha", "v1.0.0-alpha.1", -1},
		{"v1.0.0-alpha.1", "v1.0.0-alpha", 1},
		// dot-separated numeric comparison
		{"v1.0.0-1.2", "v1.0.0-1.10", -1},
		{"v1.0.0-1.10", "v1.0.0-1.2", 1},
		// mixed dot-separated: alpha.1 < alpha.beta (1 is numeric < string)
		{"v1.0.0-alpha.1", "v1.0.0-alpha.beta", -1},
	}
	for _, tt := range tests {
		got := compareSemver(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestIsValidRef(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"v1.0.0", true},
		{"v2.3.0-rc1", true},
		{"main", true},
		{"abc123def", true},
		{"v1.0.0_beta", true},
		{"", false},
		{"v1.0.0$bad", false},
		{"ref with spaces", false},
		{"ref@sha", false},
		{"ref#comment", false},
		{"ref\nnewline", false},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			if got := IsValidRef(tt.ref); got != tt.want {
				t.Errorf("IsValidRef(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestIsSHARef(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		{"abc123def456789012345678901234567890abcd", true}, // full 40-char SHA
		{"abc123d", true},  // 7-char short SHA
		{"deadbeef", true}, // 8-char hex
		{"0123456789abcdef0123456789abcdef01234567", true}, // all hex chars
		{"v2.3.0", false},       // semver tag
		{"v0", false},           // partial version
		{"main", false},         // branch name (non-hex 'm')
		{"latest", false},       // non-hex chars
		{"", false},             // empty
		{"abcde", false},        // too short (5 chars)
		{"abcdef", false},       // too short (6 chars)
		{"ABCDEF1234567", true}, // uppercase hex (case-insensitive match)
		{"abc12g", false},       // non-hex char 'g'
		{"v1.0.0-rc1", false},   // prerelease tag
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got := isSHARef(tt.ref)
			if got != tt.want {
				t.Errorf("isSHARef(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestSkipReasonForNoChange(t *testing.T) {
	tests := []struct {
		name       string
		currentRef string
		targetRef  string
		want       string
	}{
		{
			name:       "same tag",
			currentRef: "v2.3.0",
			targetRef:  "v2.3.0",
			want:       "already at v2.3.0",
		},
		{
			name:       "sha pinned current ref",
			currentRef: "abc123def456789012345678901234567890abcd",
			targetRef:  "v2.3.0",
			want:       "already at v2.3.0",
		},
		{
			name:       "different tags no match",
			currentRef: "v2.1.0",
			targetRef:  "v2.3.0",
			want:       "no uses: lines matched for replacement",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := skipReasonForNoChange(tt.currentRef, tt.targetRef)
			if got != tt.want {
				t.Errorf("skipReasonForNoChange(%q, %q) = %q, want %q",
					tt.currentRef, tt.targetRef, got, tt.want)
			}
		})
	}
}

func TestReplaceShimRef_StandaloneCommentPreserved(t *testing.T) {
	input := `    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.1.0
    # This is a standalone comment on the next line
    with:
`
	result, changed := replaceShimRef([]byte(input), "v2.3.0", "", GitHubForgeConfig(), ForgeGitHub)
	if !changed {
		t.Fatal("expected content to change")
	}
	content := string(result)
	if !strings.Contains(content, "# This is a standalone comment") {
		t.Errorf("standalone comment on the next line was deleted; got:\n%s", content)
	}
	if !strings.Contains(content, "@v2.3.0") {
		t.Errorf("ref should be updated to v2.3.0; got:\n%s", content)
	}
}

func TestReplaceShimRef_DollarSignInRef(t *testing.T) {
	content := []byte("    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v1.0.0\n")
	result, changed := replaceShimRef(content, "v2.0.0$test", "", GitHubForgeConfig(), ForgeGitHub)
	if !changed {
		t.Fatal("expected content to change")
	}
	want := "    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@v2.0.0$test\n"
	if string(result) != want {
		t.Errorf("got %q, want %q", string(result), want)
	}
}

func TestValidateConcurrency(t *testing.T) {
	if err := validateConcurrency(1); err != nil {
		t.Errorf("expected 1 to be valid: %v", err)
	}
	if err := validateConcurrency(32); err != nil {
		t.Errorf("expected 32 to be valid: %v", err)
	}
	if err := validateConcurrency(0); err == nil {
		t.Error("expected 0 to be invalid")
	}
	if err := validateConcurrency(33); err == nil {
		t.Error("expected 33 to be invalid")
	}
}
