package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRootCommand_HasVersion(t *testing.T) {
	cmd := newRootCmd()
	assert.Equal(t, "dev", cmd.Version)
}

func TestResolvedBuildSHA_FallsBackToMain(t *testing.T) {
	orig := buildSHA
	t.Cleanup(func() { buildSHA = orig })
	buildSHA = "dev"

	assert.Equal(t, "main", resolvedBuildSHA())
}

func TestFullsendRef_DevBuildWithSHA(t *testing.T) {
	orig := buildSHA
	origVer := version
	t.Cleanup(func() { buildSHA = orig; version = origVer })

	buildSHA = "abc1234abc1234abc1234abc1234abc1234abc123"
	version = "dev"

	ref := FullsendRef()
	assert.True(t, strings.HasPrefix(ref, buildSHA), "ref should start with SHA when set via ldflags")
	assert.Contains(t, ref, "# main (dev)", "dev build label")
}

func TestFullsendRef_DevBuildNoSHA(t *testing.T) {
	orig := buildSHA
	origVer := version
	t.Cleanup(func() { buildSHA = orig; version = origVer })

	buildSHA = "dev"
	version = "dev"

	ref := FullsendRef()
	assert.True(t, strings.HasPrefix(ref, "main"), "go run fallback should use 'main' as ref")
	assert.Contains(t, ref, "# main (dev)", "dev build label")
}

func TestFullsendRef_ReleaseBuild(t *testing.T) {
	orig := buildSHA
	origVer := version
	t.Cleanup(func() { buildSHA = orig; version = origVer })

	buildSHA = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	version = "1.2.3"

	ref := FullsendRef()
	assert.True(t, strings.HasPrefix(ref, buildSHA), "ref should start with SHA")
	assert.Contains(t, ref, "# v1.2.3", "release build label")
}

func TestFullsendRef_ReleaseBuildWithVPrefix(t *testing.T) {
	orig := buildSHA
	origVer := version
	t.Cleanup(func() { buildSHA = orig; version = origVer })

	buildSHA = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	version = "v1.2.3"

	ref := FullsendRef()
	assert.Contains(t, ref, "# v1.2.3", "version prefix should not be doubled")
	assert.NotContains(t, ref, "# vv", "version prefix must not be doubled")
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
