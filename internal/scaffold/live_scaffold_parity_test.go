package scaffold

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLivePerRepoScaffoldFilesMatchInstallRender checks that this
// repository's own per-repo shim and thin callers are byte-for-byte what
// `fullsend repos install` and `fullsend github setup` render for a
// repository installed from the default upstream ref with the default
// runner. The workflow_call alignment tests compare inputs and secrets
// only, so without this check a fix that lands in a template does not
// reach the live workflow, and a hand edit to the live workflow is lost on
// the next converge (#8513).
func TestLivePerRepoScaffoldFilesMatchInstallRender(t *testing.T) {
	files, err := CollectPerRepoInstallFiles(false, "", "")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, f := range files {
		live, err := os.ReadFile(filepath.Join("..", "..", f.Path))
		require.NoError(t, err, "live scaffold file %s", f.Path)
		require.Equal(t, string(f.Content), string(live),
			"%s differs from the per-repo install render of its template; change the template (and re-render the live file) or revert the live edit so both agree", f.Path)
	}
}
