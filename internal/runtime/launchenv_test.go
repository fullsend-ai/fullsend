package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestLaunchEnvExportsFor pins the launch-export contract: runtimes whose
// launch commands export environment variables beyond EnvExports() list
// those bare names, and runtimes without such exports report none. Expected
// values are literals on purpose so a constant rename cannot silently
// drift the published sandbox env manifest contract.
func TestLaunchEnvExportsFor(t *testing.T) {
	tests := []struct {
		name string
		rt   Runtime
		want []string
	}{
		{
			name: "pi",
			rt:   PiRuntime{},
			want: []string{
				"FULLSEND_RUNTIME",
				"FULLSEND_PI_MANIFEST",
				"FULLSEND_PI_MANIFEST_SHA256",
				"FULLSEND_PI_HOOK_PYTHON",
				"FULLSEND_PI_HOOK_PATH",
				"FULLSEND_PI_CHILD_BIN",
				"FULLSEND_PI_CHILD_NODE",
				"GOOGLE_CLOUD_LOCATION",
				"GOOGLE_CLOUD_PROJECT",
				"XAI_VERTEX_PROJECT_ID",
			},
		},
		{
			name: "codex",
			rt:   CodexRuntime{},
			want: []string{
				"FULLSEND_RUNTIME",
				"RUST_LOG",
				"FULLSEND_CODEX_HOOK_DIGESTS",
				"FULLSEND_CODEX_SPAWN_DIGESTS",
			},
		},
		{
			name: "opencode",
			rt:   OpenCodeRuntime{},
			want: []string{
				"FULLSEND_RUNTIME",
				"GOOGLE_CLOUD_PROJECT",
				"GOOGLE_CLOUD_LOCATION",
			},
		},
		{
			name: "claude has no launch exports beyond EnvExports",
			rt:   ClaudeRuntime{},
			want: nil,
		},
		{
			name: "dummy has no launch exports",
			rt:   DummyRuntime{},
			want: nil,
		},
		{
			name: "dummy-playback has no launch exports",
			rt:   DummyPlaybackRuntime{},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, LaunchEnvExportsFor(tt.rt))
		})
	}
}
