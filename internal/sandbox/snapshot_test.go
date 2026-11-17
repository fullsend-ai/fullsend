package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRestoreTrustedShallow_RejectsOversizedMarker rejects a marker above the
// whole-file limit and leaves it untouched.
func TestRestoreTrustedShallow_RejectsOversizedMarker(t *testing.T) {
	localDir := t.TempDir()
	gitDir := filepath.Join(localDir, ".git")
	require.NoError(t, os.Mkdir(gitDir, 0o755))
	shallowPath := writeFile(t, gitDir, "shallow", "")
	require.NoError(t, os.Truncate(shallowPath, maxShallowBytes+1))

	err := restoreTrustedShallow(localDir, ShallowState{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRepositoryRejected), "want ErrRepositoryRejected, got %v", err)
	assert.Contains(t, err.Error(), fmt.Sprintf("larger than %d bytes", maxShallowBytes))
	info, statErr := os.Stat(shallowPath)
	require.NoError(t, statErr)
	assert.Equal(t, int64(maxShallowBytes+1), info.Size(), "a rejected marker must be left untouched")
}

// TestRestoreTrustedShallow_EntrySets checks the boundary-entry contract:
// returned entries must be a subset of the trusted set, while order,
// duplicates, removals, and an empty marker are tolerated.
func TestRestoreTrustedShallow_EntrySets(t *testing.T) {
	first := strings.Repeat("1", 40)
	second := strings.Repeat("2", 40)
	root := newGitRepo(t).commitFile("root.txt", "root")
	present := func(entries ...string) ShallowState {
		return ShallowState{Present: true, Data: []byte(strings.Join(entries, "\n") + "\n")}
	}
	cases := []struct {
		name     string
		trusted  ShallowState
		returned string
		absent   bool
		wantErr  bool
	}{
		{name: "unchanged marker restores the trusted bytes", trusted: present(first), returned: first + "\n"},
		{name: "reordered and duplicated entries are allowed", trusted: present(first, second), returned: second + "\n" + first + "\n" + second + "\n"},
		{name: "removal is allowed", trusted: present(first, second), returned: first + "\n"},
		{name: "empty returned file is allowed when trusted is absent", returned: ""},
		{name: "absent returned marker is allowed when trusted is absent", absent: true},
		{name: "added cutoff is rejected", trusted: present(first), returned: first + "\n" + second + "\n", wantErr: true},
		{name: "malformed added entry is rejected", trusted: present(first), returned: first + "\njunk\n", wantErr: true},
		{name: "new zero-parent root cutoff is rejected", returned: root + "\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			localDir := t.TempDir()
			gitDir := filepath.Join(localDir, ".git")
			require.NoError(t, os.Mkdir(gitDir, 0o755))
			shallowPath := filepath.Join(gitDir, "shallow")
			if !tc.absent {
				require.NoError(t, os.WriteFile(shallowPath, []byte(tc.returned), 0o644))
			}

			err := restoreTrustedShallow(localDir, tc.trusted)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unsupported sandbox shallow boundary change")
				assert.Contains(t, err.Error(), "fetch --depth")
				got, readErr := os.ReadFile(shallowPath)
				require.NoError(t, readErr)
				assert.Equal(t, tc.returned, string(got), "a rejected boundary must be left untouched")
				return
			}
			require.NoError(t, err)
			got, readErr := os.ReadFile(shallowPath)
			if !tc.trusted.Present {
				assert.True(t, os.IsNotExist(readErr), "trusted absence must remove the marker")
				return
			}
			require.NoError(t, readErr)
			assert.Equal(t, tc.trusted.Data, got, "trusted bytes must be restored exactly")
		})
	}
}

func TestRestoreTrustedShallow_ErrorPaths(t *testing.T) {
	t.Run("shallow path cannot be inspected", func(t *testing.T) {
		localDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(localDir, ".git"), []byte("gitdir: elsewhere\n"), 0o644))

		err := restoreTrustedShallow(localDir, ShallowState{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inspecting git shallow metadata")
	})

	t.Run("git entry is not a directory", func(t *testing.T) {
		localDir := t.TempDir()
		require.NoError(t, os.Symlink("missing", filepath.Join(localDir, ".git")))

		err := restoreTrustedShallow(localDir, ShallowState{Present: true, Data: []byte("0123456789012345678901234567890123456789\n")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not a directory")
	})
}

func TestRestoreTrustedShallow_PermissionFailures(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}

	t.Run("removing untrusted shallow fails", func(t *testing.T) {
		localDir := t.TempDir()
		gitDir := filepath.Join(localDir, ".git")
		require.NoError(t, os.Mkdir(gitDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(gitDir, "shallow"), nil, 0o644))
		require.NoError(t, os.Chmod(gitDir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })

		err := restoreTrustedShallow(localDir, ShallowState{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "removing untrusted git shallow metadata")
	})

	t.Run("reading downloaded shallow fails", func(t *testing.T) {
		localDir := t.TempDir()
		gitDir := filepath.Join(localDir, ".git")
		require.NoError(t, os.Mkdir(gitDir, 0o755))
		shallowPath := filepath.Join(gitDir, "shallow")
		require.NoError(t, os.WriteFile(shallowPath, []byte("0123456789012345678901234567890123456789\n"), 0o644))
		require.NoError(t, os.Chmod(shallowPath, 0))
		t.Cleanup(func() { _ = os.Chmod(shallowPath, 0o644) })

		err := restoreTrustedShallow(localDir, ShallowState{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading downloaded git shallow metadata")
	})

	t.Run("writing trusted shallow fails", func(t *testing.T) {
		localDir := t.TempDir()
		gitDir := filepath.Join(localDir, ".git")
		require.NoError(t, os.Mkdir(gitDir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })

		err := restoreTrustedShallow(localDir, ShallowState{Present: true, Data: []byte("0123456789012345678901234567890123456789\n")})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "writing trusted git shallow metadata")
	})
}
