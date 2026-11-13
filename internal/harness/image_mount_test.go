package harness

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeImageMountsChildWinsByDestination(t *testing.T) {
	base := []ImageMount{
		{Image: "registry.example/base:v1", Dest: "/opt/tools"},
		{Image: "registry.example/config:v1", Dest: "/etc/tool"},
	}
	child := []ImageMount{
		{Image: "registry.example/custom-tools:v2", Dest: "/opt/tools"},
		{Image: "registry.example/extra:v1", Dest: "/opt/extra"},
	}

	got := mergeImageMounts(base, child)
	require.Equal(t, []ImageMount{
		{Image: "registry.example/custom-tools:v2", Dest: "/opt/tools"},
		{Image: "registry.example/config:v1", Dest: "/etc/tool"},
		{Image: "registry.example/extra:v1", Dest: "/opt/extra"},
	}, got)
	assert.Equal(t, "registry.example/base:v1", base[0].Image, "merge must not mutate its inputs")
}

func TestValidateImageMounts(t *testing.T) {
	tests := []struct {
		name      string
		mounts    []ImageMount
		hostFiles []HostFile
		wantError string
	}{
		{name: "valid tag", mounts: []ImageMount{{Image: "registry.example/team/tools:2026.11", Dest: "/opt/tools"}}},
		{name: "valid digest", mounts: []ImageMount{{Image: "registry.example/team/tools@sha256:0123456789abcdef", Dest: "/opt/tools"}}},
		{name: "empty image", mounts: []ImageMount{{Dest: "/opt/tools"}}, wantError: "image must be an OCI image reference"},
		{name: "image whitespace", mounts: []ImageMount{{Image: "registry.example/tools:latest ", Dest: "/opt/tools"}}, wantError: "image must be an OCI image reference"},
		{name: "image variable", mounts: []ImageMount{{Image: "registry.example/tools:${TAG}", Dest: "/opt/tools"}}, wantError: "image must be an OCI image reference"},
		{name: "image URL", mounts: []ImageMount{{Image: "https://registry.example/tools", Dest: "/opt/tools"}}, wantError: "image must be an OCI image reference"},
		{name: "missing destination", mounts: []ImageMount{{Image: "registry.example/tools:latest"}}, wantError: "dest is required"},
		{name: "relative destination", mounts: []ImageMount{{Image: "registry.example/tools:latest", Dest: "opt/tools"}}, wantError: "must be an absolute path"},
		{name: "parent component", mounts: []ImageMount{{Image: "registry.example/tools:latest", Dest: "/opt/../etc"}}, wantError: `must not contain a ".." path component`},
		{name: "unclean destination", mounts: []ImageMount{{Image: "registry.example/tools:latest", Dest: "/opt//tools"}}, wantError: "must use a clean absolute path"},
		{name: "container root", mounts: []ImageMount{{Image: "registry.example/tools:latest", Dest: "/"}}, wantError: "must not replace the container root"},
		{name: "same destination allowed for merge", mounts: []ImageMount{
			{Image: "registry.example/base:v1", Dest: "/opt/tools"},
			{Image: "registry.example/child:v1", Dest: "/opt/tools"},
		}},
		{name: "overlapping host file", mounts: []ImageMount{{Image: "registry.example/tools:latest", Dest: "/opt/tools"}}, hostFiles: []HostFile{{Src: "agent.env", Dest: "/opt/tools/env"}}, wantError: "overlaps host_files[0].dest"},
		{name: "overlapping image mounts", mounts: []ImageMount{
			{Image: "registry.example/parent:v1", Dest: "/opt/tools"},
			{Image: "registry.example/child:v1", Dest: "/opt/tools/bin"},
		}, wantError: "overlaps image_mounts[0].dest"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateImageMounts(tc.mounts, tc.hostFiles, "image_mounts")
			if tc.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantError)
		})
	}
}

func TestPathsOverlap(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{a: "/opt/tools", b: "/opt/tools", want: true},
		{a: "/opt/tools", b: "/opt/tools/bin", want: true},
		{a: "/opt/tools/bin", b: "/opt/tools", want: true},
		{a: "/opt/tools", b: "/opt/toolsmith", want: false},
		{a: "/opt/tools", b: "/etc/config", want: false},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, pathsOverlap(tc.a, tc.b), "%q and %q", tc.a, tc.b)
	}
}

func TestLoadWithBase_ImageMountsChildOverridesBase(t *testing.T) {
	dir := t.TempDir()
	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
image_mounts:
  - image: registry.example/base-tools:v1
    dest: /opt/fullsend
  - image: registry.example/config:v1
    dest: /etc/tool
`)
	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
image_mounts:
  - image: registry.example/custom-tools:v2
    dest: /opt/fullsend
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)
	assert.Equal(t, []ImageMount{
		{Image: "registry.example/custom-tools:v2", Dest: "/opt/fullsend"},
		{Image: "registry.example/config:v1", Dest: "/etc/tool"},
	}, h.ImageMounts)
}

func TestResolveForge_ImageMountsOverrideByDestination(t *testing.T) {
	h := &Harness{
		Agent:       "agents/test.md",
		ImageMounts: []ImageMount{{Image: "registry.example/base:v1", Dest: "/opt/tools"}},
		Forge: map[string]*ForgeConfig{
			"github": {ImageMounts: []ImageMount{
				{Image: "registry.example/github:v2", Dest: "/opt/tools"},
				{Image: "registry.example/extras:v1", Dest: "/opt/extras"},
			}},
		},
	}

	require.NoError(t, h.ResolveForge("github"))
	assert.Equal(t, []ImageMount{
		{Image: "registry.example/github:v2", Dest: "/opt/tools"},
		{Image: "registry.example/extras:v1", Dest: "/opt/extras"},
	}, h.ImageMounts)
}

func TestResolveOverlays_ImageMountsOverrideByDestination(t *testing.T) {
	h := &Harness{
		ImageMounts: []ImageMount{{Image: "registry.example/base:v1", Dest: "/opt/tools"}},
		Overlays: []OverlayEntry{{
			When: "true",
			ForgeConfig: ForgeConfig{ImageMounts: []ImageMount{
				{Image: "registry.example/overlay:v2", Dest: "/opt/tools"},
				{Image: "registry.example/extra:v1", Dest: "/opt/extra"},
			}},
		}},
	}

	require.NoError(t, h.ResolveOverlays(nil, "github", nil))
	assert.Equal(t, []ImageMount{
		{Image: "registry.example/overlay:v2", Dest: "/opt/tools"},
		{Image: "registry.example/extra:v1", Dest: "/opt/extra"},
	}, h.ImageMounts)
}

func TestValidateImageMountsAcrossHarnessLayers(t *testing.T) {
	h := &Harness{
		Agent:       "agents/test.md",
		Role:        "test",
		ImageMounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/tmp/tools"}},
		HostFiles:   []HostFile{{Src: "agent.env", Dest: "/tmp/tools/env"}},
	}
	err := h.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image_mounts[0].dest")
	assert.Contains(t, err.Error(), "overlaps host_files[0].dest")
}

func TestValidateImageMountsInForgeAndOverlay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		forge  map[string]*ForgeConfig
		overly []OverlayEntry
		want   string
	}{
		{
			name:  "forge",
			forge: map[string]*ForgeConfig{"github": {ImageMounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "relative"}}}},
			want:  "forge.github.image_mounts[0].dest",
		},
		{
			name:   "overlay",
			overly: []OverlayEntry{{When: "true", ForgeConfig: ForgeConfig{ImageMounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "relative"}}}}},
			want:   "overlays[0].image_mounts[0].dest",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Harness{Agent: "agents/test.md", Role: "test", Forge: tc.forge, Overlays: tc.overly}
			err := h.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
