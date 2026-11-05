package cli

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCommand_HasVersion(t *testing.T) {
	cmd := newRootCmd()
	assert.Equal(t, "dev", cmd.Version)
}

func TestRootCommand_HasAdminSubcommand(t *testing.T) {
	cmd := newRootCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Use == "admin" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected admin subcommand")
}

func TestRootCommand_SilencesUsageOnError(t *testing.T) {
	cmd := newRootCmd()
	assert.True(t, cmd.SilenceUsage)
	assert.True(t, cmd.SilenceErrors)
}

func TestResolveBuildVersion(t *testing.T) {
	tests := []struct {
		name    string
		sha     string
		ver     string
		wantSHA string
		wantTag string
	}{
		{"dev build", "dev", "dev", "", ""},
		{"empty SHA", "", "dev", "", ""},
		{"release without v prefix", "abc123def456", "0.19.0", "abc123def456", "v0.19.0"},
		{"release with v prefix", "abc123def456", "v0.19.0", "abc123def456", "v0.19.0"},
		{"real SHA, dev version", "abc123def456", "dev", "", ""},
		{"real SHA, empty version", "abc123def456", "", "", ""},
		{"real SHA, bare v prefix", "abc123def456", "v", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origSHA, origVer := commitSHA, version
			t.Cleanup(func() { commitSHA, version = origSHA, origVer })
			commitSHA, version = tt.sha, tt.ver
			sha, tag := resolveBuildVersion()
			assert.Equal(t, tt.wantSHA, sha)
			assert.Equal(t, tt.wantTag, tag)
		})
	}
}

func TestResolveUpstreamRef(t *testing.T) {
	tests := []struct {
		name    string
		sha     string
		ver     string
		wantRef string
		wantTag string
	}{
		{"dev build", "dev", "dev", "", ""},
		{"empty SHA", "", "dev", "", ""},
		{"release", "abc123def456", "0.19.0", "abc123def456", "v0.19.0"},
		{"release with v prefix", "abc123def456", "v0.19.0", "abc123def456", "v0.19.0"},
		{"real SHA, dev version", "abc123def456", "dev", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origSHA, origVer := commitSHA, version
			t.Cleanup(func() { commitSHA, version = origSHA, origVer })
			commitSHA, version = tt.sha, tt.ver
			ref, tag := resolveUpstreamRef()
			assert.Equal(t, tt.wantRef, ref)
			assert.Equal(t, tt.wantTag, tag)
		})
	}
}

func TestResolveUpstreamRef_BuildOverrideWinsWithoutTag(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	for _, build := range []struct{ sha, ver string }{
		{"dev", "dev"},
		{"abc123def456", "0.19.0"},
	} {
		origSHA, origVer, origOverride := commitSHA, version, upstreamRefOverride
		commitSHA, version, upstreamRefOverride = build.sha, build.ver, sha
		ref, tag := resolveUpstreamRef()
		_, agentsGitRef := resolveAgentsRef()
		commitSHA, version, upstreamRefOverride = origSHA, origVer, origOverride

		assert.Equal(t, sha, ref, "build %s/%s", build.sha, build.ver)
		assert.Empty(t, tag, "build %s/%s", build.sha, build.ver)
		// The override pins the scaffold only; the agents ref still follows the build.
		if build.ver == "dev" {
			assert.Equal(t, "heads/main", agentsGitRef)
		} else {
			assert.Equal(t, "tags/v0.19.0", agentsGitRef)
		}
	}
}

// The e2e build stamps upstreamRefOverride by its -X path. Keep the two in
// sync: a renamed variable would silently drop the stamp.
func TestUpstreamRefOverride_MatchesE2EBuildStamp(t *testing.T) {
	src, err := os.ReadFile("../e2etest/buildargs.go")
	require.NoError(t, err)
	assert.Contains(t, string(src), `"github.com/fullsend-ai/fullsend/internal/cli.upstreamRefOverride"`)
}
