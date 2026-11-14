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
		{name: "mounted Fullsend tools image", mounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/opt/fullsend"}}},
		{name: "reserved workspace binaries", mounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/sandbox/workspace/bin"}}, wantError: "overlaps runner-owned path"},
		{name: "reserved workspace environment", mounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/sandbox/workspace/.env"}}, wantError: "overlaps runner-owned path"},
		{name: "reserved runtime config", mounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/sandbox/claude-config"}}, wantError: "overlaps runner-owned path"},
		{name: "ancestor of runner paths", mounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/sandbox"}}, wantError: "overlaps runner-owned path"},
		{name: "descendant of runtime config", mounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/sandbox/opencode-config/hooks"}}, wantError: "overlaps runner-owned path"},
		{name: "sibling with similar prefix", mounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/sandbox/workspace/bin-tools"}}},
		{name: "reserved workspace metadata", mounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/sandbox/workspace/.fullsend/output.json"}}, wantError: "overlaps runner-owned path"},
		{name: "reserved security metadata", mounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/sandbox/workspace/.security/findings.json"}}, wantError: "overlaps runner-owned path"},
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
  - image: registry.example/base-extra:v1
    dest: /opt/extra
`)
	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
image_mounts:
  - image: registry.example/custom-tools:v2
    dest: /opt/fullsend
  - image: registry.example/custom-extra:v2
    dest: /opt/extra
`)

	h, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.NoError(t, err)
	assert.Equal(t, []ImageMount{
		{Image: "registry.example/custom-tools:v2", Dest: "/opt/fullsend"},
		{Image: "registry.example/config:v1", Dest: "/etc/tool"},
		{Image: "registry.example/custom-extra:v2", Dest: "/opt/extra"},
	}, h.ImageMounts)
}

func TestLoadWithOpts_NormalizesStandaloneImageMounts(t *testing.T) {
	path := writeTestHarness(t, t.TempDir(), "standalone.yaml", `
agent: agents/test.md
role: test
image_mounts:
  - image: registry.example/base:v1
    dest: /opt/fullsend
  - image: registry.example/extra:v1
    dest: /opt/extra
  - image: registry.example/override:v2
    dest: /opt/fullsend
`)

	h, err := LoadWithOpts(path, LoadOpts{})
	require.NoError(t, err)
	assert.Equal(t, []ImageMount{
		{Image: "registry.example/override:v2", Dest: "/opt/fullsend"},
		{Image: "registry.example/extra:v1", Dest: "/opt/extra"},
	}, h.ImageMounts)
}

func TestLoadWithBase_RejectsRunnerOwnedMount(t *testing.T) {
	dir := t.TempDir()
	writeTestHarness(t, dir, "base.yaml", `
agent: agents/base.md
role: test
image_mounts:
  - image: registry.example/tools:v1
    dest: /sandbox
`)
	path := writeTestHarness(t, dir, "child.yaml", `
base: base.yaml
agent: agents/child.md
role: test
`)
	_, _, err := LoadWithBase(context.Background(), path, ComposeOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `image_mounts[0].dest "/sandbox" overlaps runner-owned path`)
}

func TestLoadRaw_PreservesImageMountOmissionAndDuplicates(t *testing.T) {
	dir := t.TempDir()
	omitted := writeTestHarness(t, dir, "omitted.yaml", `
agent: agents/test.md
forge:
  github:
    pre_script: scripts/github.sh
overlays:
  - when: "true"
`)
	h, err := LoadRaw(omitted)
	require.NoError(t, err)
	assert.Nil(t, h.ImageMounts, "raw omission stays nil")
	assert.Nil(t, h.Forge["github"].ImageMounts, "raw forge omission stays nil")
	assert.Nil(t, h.Overlays[0].ImageMounts, "raw overlay omission stays nil")

	duplicates := writeTestHarness(t, dir, "duplicates.yaml", `
agent: agents/test.md
image_mounts:
  - image: registry.example/base:v1
    dest: /opt/fullsend
  - image: registry.example/override:v2
    dest: /opt/fullsend
forge:
  github:
    image_mounts:
      - image: registry.example/forge:v1
        dest: /opt/tools
      - image: registry.example/forge-override:v2
        dest: /opt/tools
overlays:
  - when: "true"
    image_mounts:
      - image: registry.example/overlay:v1
        dest: /opt/overlay
      - image: registry.example/overlay-override:v2
        dest: /opt/overlay
`)
	h, err = LoadRaw(duplicates)
	require.NoError(t, err)
	require.Len(t, h.ImageMounts, 2)
	assert.Equal(t, "registry.example/base:v1", h.ImageMounts[0].Image)
	assert.Equal(t, "registry.example/override:v2", h.ImageMounts[1].Image)
	require.Len(t, h.Forge["github"].ImageMounts, 2)
	assert.Equal(t, "registry.example/forge:v1", h.Forge["github"].ImageMounts[0].Image)
	assert.Equal(t, "registry.example/forge-override:v2", h.Forge["github"].ImageMounts[1].Image)
	require.Len(t, h.Overlays[0].ImageMounts, 2)
	assert.Equal(t, "registry.example/overlay:v1", h.Overlays[0].ImageMounts[0].Image)
	assert.Equal(t, "registry.example/overlay-override:v2", h.Overlays[0].ImageMounts[1].Image)
}

func TestResolveForge_ImageMountsOverrideByDestination(t *testing.T) {
	h := &Harness{
		Agent:       "agents/test.md",
		ImageMounts: []ImageMount{{Image: "registry.example/base:v1", Dest: "/opt/tools"}},
		Forge: map[string]*ForgeConfig{
			"github": {ImageMounts: []ImageMount{
				{Image: "registry.example/old-github:v1", Dest: "/opt/tools"},
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
		ImageMounts: []ImageMount{
			{Image: "registry.example/base:v1", Dest: "/opt/tools"},
			{Image: "registry.example/extra:v1", Dest: "/opt/base-extra"},
			{Image: "registry.example/base-override:v2", Dest: "/opt/tools"},
		},
		Overlays: []OverlayEntry{{
			When: "true",
			ForgeConfig: ForgeConfig{ImageMounts: []ImageMount{
				{Image: "registry.example/old-overlay:v1", Dest: "/opt/tools"},
				{Image: "registry.example/overlay:v2", Dest: "/opt/tools"},
				{Image: "registry.example/extra:v1", Dest: "/opt/extra"},
			}},
		}},
	}

	require.NoError(t, h.ResolveOverlays(nil, "github", nil))
	assert.Equal(t, []ImageMount{
		{Image: "registry.example/overlay:v2", Dest: "/opt/tools"},
		{Image: "registry.example/extra:v1", Dest: "/opt/base-extra"},
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

func TestValidateImageMountsReservedPathsAcrossConfigs(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    *Harness
		want string
	}{
		{
			name: "top-level",
			h:    &Harness{Agent: "agents/test.md", Role: "test", ImageMounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/sandbox/workspace/bin"}}},
			want: "image_mounts[0].dest",
		},
		{
			name: "forge",
			h: &Harness{Agent: "agents/test.md", Role: "test", Forge: map[string]*ForgeConfig{
				"github": {ImageMounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/sandbox/codex-config/hooks"}}},
			}},
			want: "forge.github.image_mounts[0].dest",
		},
		{
			name: "overlay",
			h: &Harness{Agent: "agents/test.md", Role: "test", Overlays: []OverlayEntry{{
				When: "true", ForgeConfig: ForgeConfig{ImageMounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/sandbox/workspace/.security/log"}}},
			}}},
			want: "overlays[0].image_mounts[0].dest",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.h.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "overlaps runner-owned path")
		})
	}
}

func TestValidateImageMountHostFileDiagnosticPrefixes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  func() error
		want string
	}{
		{
			name: "top-level",
			err: func() error {
				h := &Harness{Agent: "agents/test.md", Role: "test",
					ImageMounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/tmp/tools"}},
					HostFiles:   []HostFile{{Src: "env", Dest: "/tmp/tools/env"}},
				}
				return h.Validate()
			},
			want: "host_files[0].dest",
		},
		{
			name: "forge",
			err: func() error {
				h := &Harness{Forge: map[string]*ForgeConfig{"github": {
					ImageMounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/tmp/tools"}},
					HostFiles:   []HostFile{{Src: "env", Dest: "/tmp/tools/env"}},
				}}}
				return h.validateForge()
			},
			want: "forge.github.host_files[0].dest",
		},
		{
			name: "overlay",
			err: func() error {
				h := &Harness{Overlays: []OverlayEntry{{When: "true", ForgeConfig: ForgeConfig{
					ImageMounts: []ImageMount{{Image: "registry.example/tools:v1", Dest: "/tmp/tools"}},
					HostFiles:   []HostFile{{Src: "env", Dest: "/tmp/tools/env"}},
				}}}}
				return h.validateOverlays()
			},
			want: "overlays[0].host_files[0].dest",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.err()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "overlaps "+tc.want)
		})
	}
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
