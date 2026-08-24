package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
