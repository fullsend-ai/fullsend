package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A manifest read from a pipe, as -f <(cmd) gives, is read once and is
// read-only, like an HTTPS URL. An install that must record the first-install
// tracker default in it is refused, in a dry run and a live run alike, before
// it writes to the forge; one whose entry declares its tracker needs no edit
// and installs.
func TestRunReposInstall_ManifestFromPipeIsReadOnly(t *testing.T) {
	declared := strings.Replace(githubUnmanagedManifestYAML, "    - name: acme/api\n",
		"    - name: acme/api\n      config:\n        tracker:\n          github:\n            allowed_targets:\n              repos:\n                - acme/other\n", 1)
	for _, tc := range []struct {
		name, manifest string
		refused        bool
	}{
		{"needs the tracker default", githubUnmanagedManifestYAML, true},
		{"declares its tracker", declared, false},
	} {
		for _, dryRun := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/dry run %t", tc.name, dryRun), func(t *testing.T) {
				r, w, err := os.Pipe()
				require.NoError(t, err)
				t.Cleanup(func() { _ = r.Close() })
				_, err = w.WriteString(tc.manifest)
				require.NoError(t, err)
				require.NoError(t, w.Close())
				fc := newInstallFakeClient("acme/api")
				opts := githubManagedInstallOpts(fmt.Sprintf("/dev/fd/%d", r.Fd()), fc)
				opts.dryRun = dryRun
				var runErr error
				captureStdout(t, func() { runErr = runReposInstall(context.Background(), opts) })
				if !tc.refused {
					require.NoError(t, runErr)
					return
				}
				require.Error(t, runErr)
				assert.Contains(t, runErr.Error(), "is read-only")
				assertNoInstallWrites(t, fc)
			})
		}
	}
}
