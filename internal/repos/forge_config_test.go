package repos

import "testing"

func TestForgeConfigFor(t *testing.T) {
	t.Run("github", func(t *testing.T) {
		fc := ForgeConfigFor("github")
		if len(fc.WorkflowPaths) == 0 {
			t.Fatal("expected workflow paths for github")
		}
		if fc.WorkflowPaths[0] != ".github/workflows/fullsend.yml" {
			t.Errorf("unexpected github workflow path: %s", fc.WorkflowPaths[0])
		}
	})

	t.Run("gitlab", func(t *testing.T) {
		fc := ForgeConfigFor("gitlab")
		if len(fc.WorkflowPaths) == 0 {
			t.Fatal("expected workflow paths for gitlab")
		}
		if fc.WorkflowPaths[0] != ".gitlab/ci/fullsend-dispatch.yml" {
			t.Errorf("unexpected gitlab workflow path: %s", fc.WorkflowPaths[0])
		}
	})

	t.Run("empty defaults to github", func(t *testing.T) {
		fc := ForgeConfigFor("")
		if fc.WorkflowPaths[0] != ".github/workflows/fullsend.yml" {
			t.Errorf("expected github default, got: %s", fc.WorkflowPaths[0])
		}
	})

	t.Run("unknown defaults to github", func(t *testing.T) {
		fc := ForgeConfigFor("unknown")
		if fc.WorkflowPaths[0] != ".github/workflows/fullsend.yml" {
			t.Errorf("expected github default, got: %s", fc.WorkflowPaths[0])
		}
	})
}

func TestGitLabWorkflowRefPattern_Multiline(t *testing.T) {
	fc := GitLabForgeConfig()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "ref on its own line",
			content: "ref: v1.0.0",
			want:    "v1.0.0",
		},
		{
			name:    "ref with single quotes",
			content: "ref: 'v1.0.0'",
			want:    "v1.0.0",
		},
		{
			name:    "ref with double quotes",
			content: `ref: "v1.0.0"`,
			want:    "v1.0.0",
		},
		{
			name: "ref not at end of text",
			content: `include:
  - project: 'fullsend-ai/fullsend'
    file: '.gitlab/ci/fullsend-dispatch.yml'
    ref: 'v1.0.0'
stages:
  - dispatch`,
			want: "v1.0.0",
		},
		{
			name:    "no ref line",
			content: "stages:\n  - dispatch\n",
			want:    "",
		},
		{
			name:    "head_ref in jq template is not matched",
			content: "              head_ref: $head_ref,\n              base_ref: $base_ref,",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractWorkflowRef([]byte(tt.content), fc)
			if got != tt.want {
				t.Errorf("extractWorkflowRef() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGitLabShimRefPattern_ReplacesCorrectly(t *testing.T) {
	fc := GitLabForgeConfig()

	tests := []struct {
		name     string
		input    string
		newRef   string
		newTag   string
		want     string
		wantDiff bool
	}{
		{
			name:     "unquoted ref",
			input:    "    ref: v1.0.0\n",
			newRef:   "v2.0.0",
			want:     "    ref: v2.0.0\n",
			wantDiff: true,
		},
		{
			name:     "single-quoted ref preserves valid YAML",
			input:    "    ref: 'v1.0.0'\n",
			newRef:   "v2.0.0",
			want:     "    ref: v2.0.0\n",
			wantDiff: true,
		},
		{
			name:     "double-quoted ref preserves valid YAML",
			input:    "    ref: \"v1.0.0\"\n",
			newRef:   "v2.0.0",
			want:     "    ref: v2.0.0\n",
			wantDiff: true,
		},
		{
			name: "ref not at end of text (multiline)",
			input: `include:
  - project: 'fullsend-ai/fullsend'
    file: '.gitlab/ci/fullsend-dispatch.yml'
    ref: 'v1.0.0'
stages:
  - dispatch
`,
			newRef: "v2.0.0",
			want: `include:
  - project: 'fullsend-ai/fullsend'
    file: '.gitlab/ci/fullsend-dispatch.yml'
    ref: v2.0.0
stages:
  - dispatch
`,
			wantDiff: true,
		},
		{
			name:     "SHA ref with tag comment",
			input:    "    ref: abc123\n",
			newRef:   "def456",
			newTag:   "v2.0.0",
			want:     "    ref: def456 # v2.0.0\n",
			wantDiff: true,
		},
		{
			name:     "same ref no change",
			input:    "    ref: v2.0.0\n",
			newRef:   "v2.0.0",
			want:     "    ref: v2.0.0\n",
			wantDiff: false,
		},
		{
			name:     "no matching ref line",
			input:    "stages:\n  - dispatch\n",
			newRef:   "v2.0.0",
			wantDiff: false,
		},
		{
			name:     "head_ref in jq template is not matched",
			input:    "              head_ref: $head_ref,\n              base_ref: $base_ref,\n",
			newRef:   "v2.0.0",
			wantDiff: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, changed := replaceShimRef([]byte(tt.input), tt.newRef, tt.newTag, fc)
			if changed != tt.wantDiff {
				t.Errorf("changed = %v, want %v", changed, tt.wantDiff)
			}
			if tt.want != "" && string(result) != tt.want {
				t.Errorf("result:\n%s\nwant:\n%s", string(result), tt.want)
			}
		})
	}
}
