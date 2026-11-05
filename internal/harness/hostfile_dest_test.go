package harness

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateHostFileDest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		dest    string
		wantErr string
	}{
		// Paths harnesses use today.
		{dest: "/sandbox/workspace/.env.d/gcp-vertex.env"},
		{dest: "/sandbox/workspace/.env.d/extra-path.env"},
		{dest: "/tmp/.gcp-credentials.json"},
		{dest: "/sandbox/workspace/.gcp-oidc-token"},
		{dest: "/sandbox/workspace/notes.txt"},
		{dest: "/sandbox/workspace/bin/my-tool.sh"},
		{dest: "/sandbox/workspace/.npmrc"},
		{dest: "/tmp/workspace/.env.d/triage.env"},
		{dest: "/sandbox/sa.json"},
		// Near misses that stay allowed.
		{dest: "/sandbox/workspace/.env.local"},
		{dest: "/sandbox/workspace/bin/claude.sh"},
		{dest: "/sandbox/workspace/bin/sub/claude"},
		{dest: "/sandbox/workspace/repo/bin/claude"},
		{dest: "/sandbox/claude-config-extra/x"},
		{dest: "/sandbox/workspace/.fullsendrc"},

		// The workspace .env.
		{dest: "/sandbox/workspace/.env", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace//.env", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace/.env/", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace/.env.d/../.env", wantErr: "must not contain a \"..\" path component"},
		{dest: "/sandbox/./workspace/.env", wantErr: "reserved for the runner"},
		// Runner and runtime binaries on PATH.
		{dest: "/sandbox/workspace/bin/claude", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace/bin/codex", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace/bin/pi", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace/bin/opencode", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace/bin/fullsend", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace/bin/fullsend-check-output", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace/bin//claude", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace/bin/claude/", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace/bin/x/../claude", wantErr: "must not contain a \"..\" path component"},
		{dest: "//sandbox/workspace/bin/claude", wantErr: "reserved for the runner"},
		// Runtime config homes and the runner's .fullsend directory.
		{dest: "/sandbox/claude-config", wantErr: "reserved for the runner"},
		{dest: "/sandbox/claude-config/hooks.json", wantErr: "reserved for the runner"},
		{dest: "/sandbox/claude-config/hooks/tirith_check.py", wantErr: "reserved for the runner"},
		{dest: "/sandbox/codex-config/config.toml", wantErr: "reserved for the runner"},
		{dest: "/sandbox/pi-config/settings.json", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace/.fullsend/output-schema.json", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace/.fullsend/iteration.env", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace/repo/../.fullsend/output-schema.json", wantErr: "must not contain a \"..\" path component"},
		// PATH directories under /sandbox that precede the image's own.
		{dest: "/sandbox/.venv/bin/python3", wantErr: "reserved for the runner"},
		{dest: "/sandbox/go/bin/node", wantErr: "reserved for the runner"},
		// Only /sandbox and /tmp: other roots reach the same files by
		// another name, or are not the sandbox user's to write.
		{dest: "/proc/self/root/sandbox/workspace/bin/fullsend", wantErr: "must be under /sandbox/ or /tmp/"},
		{dest: "/dev/fd/3", wantErr: "must be under /sandbox/ or /tmp/"},
		{dest: "/sys/kernel/x", wantErr: "must be under /sandbox/ or /tmp/"},
		{dest: "/etc/ssl/certs/ca-certificates.crt", wantErr: "must be under /sandbox/ or /tmp/"},
		{dest: "/run/secrets/forge.env", wantErr: "must be under /sandbox/ or /tmp/"},
		{dest: "/sandbox", wantErr: "must be under /sandbox/ or /tmp/"},
		{dest: "/tmp", wantErr: "must be under /sandbox/ or /tmp/"},
		{dest: "/sandboxed/x", wantErr: "must be under /sandbox/ or /tmp/"},
		{dest: "/sandbox/../etc/passwd", wantErr: "must not contain a \"..\" path component"},
		{dest: "/sandbox/workspace/notes/../notes.txt", wantErr: "must not contain a \"..\" path component"},
		{dest: "/tmp/..", wantErr: "must not contain a \"..\" path component"},
		{dest: "/sandbox/workspace/..hidden"},
		{dest: "/sandbox/workspace/notes..txt"},
		{dest: "/sandbox/workspace/.security/findings.jsonl", wantErr: "reserved for the runner"},
		{dest: "/sandbox/workspace/.security", wantErr: "reserved for the runner"},
		// Relative paths would resolve against the exec working directory.
		{dest: "workspace/.env", wantErr: "must be an absolute path"},
		{dest: "./bin/claude", wantErr: "must be an absolute path"},
	}
	for _, tt := range tests {
		t.Run(tt.dest, func(t *testing.T) {
			t.Parallel()
			err := ValidateHostFileDest(tt.dest)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, err.Error(), tt.dest)
		})
	}
}

func TestValidate_HostFileReservedDest(t *testing.T) {
	t.Parallel()
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		HostFiles: []HostFile{
			{Src: "env/ok.env", Dest: "/sandbox/workspace/.env.d/ok.env"},
			{Src: "scripts/claude", Dest: "/sandbox/workspace/bin/claude"},
		},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `host_files[1]: dest "/sandbox/workspace/bin/claude" is reserved for the runner`)
}

func TestValidate_ForgeHostFileReservedDest(t *testing.T) {
	t.Parallel()
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Forge: map[string]*ForgeConfig{
			"github": {
				HostFiles: []HostFile{{Src: "env/runner.env", Dest: "/sandbox/workspace/.env"}},
			},
		},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `forge.github.host_files[0]: dest "/sandbox/workspace/.env" is reserved for the runner`)
}

func TestValidateOverlayForgeConfig_HostFileReservedDest(t *testing.T) {
	t.Parallel()
	fc := &ForgeConfig{
		HostFiles: []HostFile{{Src: "hooks.json", Dest: "/sandbox/claude-config/hooks.json"}},
	}
	err := validateOverlayForgeConfig(0, fc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `overlays[0].host_files[0]: dest "/sandbox/claude-config/hooks.json" is reserved for the runner`)
}

func TestValidate_OverlayHostFileReservedDest(t *testing.T) {
	t.Parallel()
	h := &Harness{
		Agent: "agents/test.md",
		Role:  "test",
		Overlays: []OverlayEntry{{
			When: `runtime.forge == "github"`,
			ForgeConfig: ForgeConfig{
				HostFiles: []HostFile{{Src: "codex", Dest: "/sandbox/workspace/bin/codex"}},
			},
		}},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `overlays[0].host_files[0]: dest "/sandbox/workspace/bin/codex" is reserved for the runner`)
}

// A reserved dest inherited from a base harness is refused after
// composition, not only when it is written in the child.
func TestLoadWithBase_LocalBase_HostFileReservedDest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	writeTestHarness(t, dir, "base.yaml", `
agent: agents/test.md
role: test
host_files:
  - src: base-claude
    dest: /sandbox/workspace/bin/claude
`)

	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
host_files:
  - src: child-env
    dest: /sandbox/workspace/.env.d/child.env
`)

	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is reserved for the runner")
}
