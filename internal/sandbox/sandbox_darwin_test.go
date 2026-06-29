//go:build darwin

package sandbox

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDarwinBsdtar_CopyfileDisableSuppressesAppleDouble exercises real macOS bsdtar
// to verify COPYFILE_DISABLE=1 prevents ._* files in tarballs. This validates OS-level
// behavior; the companion TestUploadDir_TarIncludesCopyfileDisable (sandbox_test.go:418)
// verifies UploadDir sets the env var.
func TestDarwinBsdtar_CopyfileDisableSuppressesAppleDouble(t *testing.T) {
	srcDir := t.TempDir()
	testFile := filepath.Join(srcDir, "pack-abc.idx")
	require.NoError(t, os.WriteFile(testFile, []byte("idx content"), 0o644))

	xattrCmd := exec.Command("xattr", "-w", "com.apple.quarantine",
		"0083;00000000;Safari;", testFile)
	if out, err := xattrCmd.CombinedOutput(); err != nil {
		t.Fatalf("xattr command failed (unexpected on macOS): %v: %s", err, out)
	}

	listTarMembers := func(tarPath string) []string {
		t.Helper()
		f, err := os.Open(tarPath)
		require.NoError(t, err)
		defer f.Close()
		gz, err := gzip.NewReader(f)
		require.NoError(t, err)
		defer gz.Close()
		tr := tar.NewReader(gz)
		var members []string
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			members = append(members, hdr.Name)
		}
		return members
	}

	hasAppleDouble := func(members []string) bool {
		for _, m := range members {
			if strings.HasPrefix(filepath.Base(m), "._") {
				return true
			}
		}
		return false
	}

	// Negative control: tar WITHOUT COPYFILE_DISABLE should produce ._* files.
	controlEnv := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "COPYFILE_DISABLE=") {
			controlEnv = append(controlEnv, e)
		}
	}
	controlTar := filepath.Join(t.TempDir(), "control.tar.gz")
	controlCmd := exec.Command("tar", "-czf", controlTar, "-C", srcDir, ".")
	controlCmd.Env = controlEnv
	if out, err := controlCmd.CombinedOutput(); err != nil {
		t.Fatalf("control tar failed: %v: %s", err, out)
	}
	// Subject: tar WITH COPYFILE_DISABLE=1 (matching UploadDir) must produce no ._* files.
	// Run unconditionally — this is the actual assertion under test.
	subjectTar := filepath.Join(t.TempDir(), "subject.tar.gz")
	subjectCmd := exec.Command("tar", "-czf", subjectTar, "-C", srcDir, ".")
	subjectCmd.Env = append(controlEnv, "COPYFILE_DISABLE=1")
	out, err := subjectCmd.CombinedOutput()
	require.NoError(t, err, "tar with COPYFILE_DISABLE=1 failed: %s", out)

	subjectMembers := listTarMembers(subjectTar)
	assert.False(t, hasAppleDouble(subjectMembers),
		"tarball must contain no ._* members when COPYFILE_DISABLE=1; got: %v", subjectMembers)

	// Bonus: verify the negative control produced ._* files, proving the test can detect regressions.
	controlMembers := listTarMembers(controlTar)
	if !hasAppleDouble(controlMembers) {
		t.Log("warning: control tar without COPYFILE_DISABLE produced no ._* files — xattr may not have applied")
	}
}
