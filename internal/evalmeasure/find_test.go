package evalmeasure

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindPlatformTelemetry_IgnoresNestedIterationCopy(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	runDir := filepath.Join(outputDir, "agent-review-3311-1")
	nested := filepath.Join(runDir, "iteration-1", "output")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	platform := filepath.Join(runDir, PlatformTelemetryFile)
	planted := filepath.Join(nested, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(platform, []byte("platform\n"), 0o644))
	require.NoError(t, os.WriteFile(planted, []byte("planted\n"), 0o644))

	got, err := FindPlatformTelemetry(outputDir, "")
	require.NoError(t, err)
	require.Equal(t, []string{platform}, got)
}

func TestFindPlatformTelemetry_RunDirIgnoresImmediateChild(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	child := filepath.Join(runDir, "planted-child")
	require.NoError(t, os.MkdirAll(child, 0o755))
	platform := filepath.Join(runDir, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(platform, []byte("platform\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(child, PlatformTelemetryFile), []byte("planted\n"), 0o644))

	got, err := FindPlatformTelemetry(runDir, "")
	require.NoError(t, err)
	require.Equal(t, []string{platform}, got)
}

func TestFindPlatformTelemetry_RunDirDirect(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	nested := filepath.Join(runDir, "iteration-1", "output")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	platform := filepath.Join(runDir, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(platform, []byte("platform\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(nested, PlatformTelemetryFile), []byte("planted\n"), 0o644))

	got, err := FindPlatformTelemetry(runDir, "")
	require.NoError(t, err)
	require.Equal(t, []string{platform}, got)
}

func TestFindPlatformTelemetry_MissingDir(t *testing.T) {
	t.Parallel()
	got, err := FindPlatformTelemetry(filepath.Join(t.TempDir(), "nope"), "")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFindPlatformTelemetry_EmptyWhenOnlyNested(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	nested := filepath.Join(outputDir, "agent-x", "iteration-1", "output")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nested, PlatformTelemetryFile), []byte("planted\n"), 0o644))

	got, err := FindPlatformTelemetry(outputDir, "")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFindPlatformTelemetry_IgnoresSiblingLeftoverRunDir(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	leftover := filepath.Join(outputDir, "agent-triage-1-1")
	current := filepath.Join(outputDir, "agent-review-9-9")
	require.NoError(t, os.MkdirAll(leftover, 0o755))
	require.NoError(t, os.MkdirAll(current, 0o755))
	leftFile := filepath.Join(leftover, PlatformTelemetryFile)
	curFile := filepath.Join(current, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(leftFile, []byte("old\n"), 0o644))
	require.NoError(t, os.WriteFile(curFile, []byte("new\n"), 0o644))

	got, err := FindPlatformTelemetry(outputDir, "review")
	require.NoError(t, err)
	require.Equal(t, []string{curFile}, got)

	gotAllNewest, err := FindPlatformTelemetry(outputDir, "")
	require.NoError(t, err)
	require.Len(t, gotAllNewest, 1)
}

func TestFindPlatformTelemetry_DoesNotMatchLongerAgentPrefix(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	codeReview := filepath.Join(outputDir, "agent-code-review-1-1")
	require.NoError(t, os.MkdirAll(codeReview, 0o755))
	f := filepath.Join(codeReview, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(f, []byte("x\n"), 0o644))

	got, err := FindPlatformTelemetry(outputDir, "code")
	require.NoError(t, err)
	assert.Empty(t, got, "agent-code must not match agent-code-review-*")

	gotReview, err := FindPlatformTelemetry(outputDir, "code-review")
	require.NoError(t, err)
	require.Equal(t, []string{f}, gotReview)
}

func TestFindPlatformTelemetry_MatchesUnderscorePrefixedAgent(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	runDir := filepath.Join(outputDir, "agent-_helper-7-9")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	f := filepath.Join(runDir, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(f, []byte("x\n"), 0o644))

	got, err := FindPlatformTelemetry(outputDir, "_helper")
	require.NoError(t, err)
	require.Equal(t, []string{f}, got)
}

func TestFindPlatformTelemetry_PrefersRunDirOverPlantedRoot(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	planted := filepath.Join(outputDir, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(planted, []byte("planted\n"), 0o644))

	runDir := filepath.Join(outputDir, "agent-review-3-4")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	platform := filepath.Join(runDir, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(platform, []byte("platform\n"), 0o644))

	got, err := FindPlatformTelemetry(outputDir, "review")
	require.NoError(t, err)
	require.Equal(t, []string{platform}, got)

	gotAll, err := FindPlatformTelemetry(outputDir, "")
	require.NoError(t, err)
	require.Equal(t, []string{platform}, gotAll)
}

func TestFindPlatformTelemetry_EmptyMatchingRunDirIgnoresPlantedRoot(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	planted := filepath.Join(outputDir, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(planted, []byte("planted\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(outputDir, "agent-review-5-6"), 0o755))

	got, err := FindPlatformTelemetry(outputDir, "review")
	require.NoError(t, err)
	assert.Empty(t, got, "matching empty runDir must not fall back to planted root")
}

// --- Tests for the current fs-<slug>-<hex> naming scheme ---

func TestFindPlatformTelemetry_NewFormatRunDir(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	runDir := filepath.Join(outputDir, "fs-rev-a1b2c3d4e5f6")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	platform := filepath.Join(runDir, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(platform, []byte("platform\n"), 0o644))

	got, err := FindPlatformTelemetry(outputDir, "review")
	require.NoError(t, err)
	require.Equal(t, []string{platform}, got)
}

func TestFindPlatformTelemetry_NewFormatNoAgentFilter(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	runDir := filepath.Join(outputDir, "fs-tri-deadbeef1234")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	platform := filepath.Join(runDir, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(platform, []byte("platform\n"), 0o644))

	got, err := FindPlatformTelemetry(outputDir, "")
	require.NoError(t, err)
	require.Equal(t, []string{platform}, got)
}

func TestFindPlatformTelemetry_NewFormatAgentMismatch(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	runDir := filepath.Join(outputDir, "fs-cod-abcdef012345")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, PlatformTelemetryFile), []byte("x\n"), 0o644))

	got, err := FindPlatformTelemetry(outputDir, "triage")
	require.NoError(t, err)
	assert.Empty(t, got, "fs-cod-* must not match agent=triage")
}

func TestFindPlatformTelemetry_MixedOldAndNewFormat(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	// Legacy dir — older mod time.
	oldDir := filepath.Join(outputDir, "agent-triage-1-1")
	require.NoError(t, os.MkdirAll(oldDir, 0o755))
	oldFile := filepath.Join(oldDir, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(oldFile, []byte("old\n"), 0o644))

	// New-format dir — newer mod time.
	newDir := filepath.Join(outputDir, "fs-tri-aabbccddee00")
	require.NoError(t, os.MkdirAll(newDir, 0o755))
	newFile := filepath.Join(newDir, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(newFile, []byte("new\n"), 0o644))

	// Without agent filter: newest file wins.
	got, err := FindPlatformTelemetry(outputDir, "")
	require.NoError(t, err)
	require.Len(t, got, 1)

	// With agent filter: both formats match "triage" (legacy full name,
	// new slug "tri"); newest wins.
	gotAgent, err := FindPlatformTelemetry(outputDir, "triage")
	require.NoError(t, err)
	require.Len(t, gotAgent, 1)
}

func TestFindPlatformTelemetry_NewFormatIgnoresNestedCopy(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	runDir := filepath.Join(outputDir, "fs-rev-1234567890ab")
	nested := filepath.Join(runDir, "iteration-1", "output")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	platform := filepath.Join(runDir, PlatformTelemetryFile)
	planted := filepath.Join(nested, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(platform, []byte("platform\n"), 0o644))
	require.NoError(t, os.WriteFile(planted, []byte("planted\n"), 0o644))

	got, err := FindPlatformTelemetry(outputDir, "")
	require.NoError(t, err)
	require.Equal(t, []string{platform}, got)
}

func TestFindPlatformTelemetry_NewFormatPrefersRunDirOverPlantedRoot(t *testing.T) {
	t.Parallel()
	outputDir := t.TempDir()
	planted := filepath.Join(outputDir, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(planted, []byte("planted\n"), 0o644))

	runDir := filepath.Join(outputDir, "fs-rev-ffeeddccbbaa")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	platform := filepath.Join(runDir, PlatformTelemetryFile)
	require.NoError(t, os.WriteFile(platform, []byte("platform\n"), 0o644))

	got, err := FindPlatformTelemetry(outputDir, "review")
	require.NoError(t, err)
	require.Equal(t, []string{platform}, got)
}

func TestIsHostRunDir(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"agent-review-3311-1", "fs-cod-0a1b2c"} {
		assert.True(t, IsHostRunDir(name), name)
	}
	for _, name := range []string{"iteration-1", "security", "output", "fs-code-0a1b", "agent-review"} {
		assert.False(t, IsHostRunDir(name), name)
	}
}
