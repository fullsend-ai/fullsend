//go:build e2e || behaviour

package e2etest

import (
	"debug/buildinfo"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildCLI(t *testing.T) {
	binary := BuildCLIBinary(t)
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("binary not found at %s: %v", binary, err)
	}
	sha := gitHeadSHA(ModuleRoot(t))
	if sha == "" {
		return
	}

	// Assert the -ldflags setting recorded in the binary's build info, not
	// a raw byte search: Go's default VCS stamping already embeds HEAD's
	// SHA as `vcs.revision` even when -X .../cli.commitSHA=... is omitted
	// entirely, so a plain require.Contains on the binary bytes would pass
	// whether or not commitSHA was actually stamped.
	info, err := buildinfo.ReadFile(binary)
	require.NoError(t, err)
	want := commitSHALdflags(sha)
	var got string
	for _, s := range info.Settings {
		if s.Key == "-ldflags" {
			got = s.Value
			break
		}
	}
	require.Contains(t, got, want,
		"e2e CLI must stamp commitSHA with HEAD via -ldflags so scaffold refs pin to the commit under test")
}

func TestBuildModuleBinary(t *testing.T) {
	binary := BuildModuleBinary(t, "github.com/fullsend-ai/fullsend")
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("binary not found at %s: %v", binary, err)
	}
}

func TestModuleDir_Invalid(t *testing.T) {
	_, err := moduleDir("github.com/fullsend-ai/fullsend/not-a-real-module-path")
	require.Error(t, err)
}

func TestGitHeadSHA_NonGitDir(t *testing.T) {
	require.Empty(t, gitHeadSHA(t.TempDir()))
}

// TestGitHeadSHA_NestedInGitCheckout guards against the ancestor-.git
// discovery flake: `git -C dir rev-parse HEAD` walks up parent directories
// to find `.git`, so a non-git dir nested inside a real checkout would
// otherwise report that checkout's HEAD instead of "".
func TestGitHeadSHA_NestedInGitCheckout(t *testing.T) {
	nested, err := os.MkdirTemp(ModuleRoot(t), "githeadsha-nested-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(nested) })

	require.Empty(t, gitHeadSHA(nested))
}
