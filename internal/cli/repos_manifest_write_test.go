package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
)

// sharedManifestClient runs another install the first time this run reads a
// file from the forge, which is after this run loaded repos.yaml and before
// it writes it, as when two installs that use the same file overlap.
type sharedManifestClient struct {
	*forge.FakeClient
	other func()
}

func (c *sharedManifestClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	if f := c.other; f != nil {
		c.other = nil
		f()
	}
	return c.FakeClient.GetFileContent(ctx, owner, repo, path)
}

// An install whose repos.yaml another install rewrote after this run loaded
// it does not overwrite the file, so the other install's edits survive: here
// each records the first-install tracker default and the trace_context of
// its own repository. The refused run writes no secret or variable of its
// repository, and a plain retry from the file records this run's edits as
// well and provisions its key.
func TestRunReposInstall_ManifestChangedMeanwhileIsNotOverwritten(t *testing.T) {
	const target = "- fullsend-ai/fullsend\n"
	manifestPath := writeTestManifest(t, twoRepoManifestYAML)
	fc := newInstallFakeClient("acme/api", "acme/web")
	only := func(repo string) *reposInstallConfig {
		opts := githubManagedInstallOpts(manifestPath, fc)
		opts.repoFilter = []string{repo}
		return opts
	}
	var otherFile string
	var otherErr error
	opts := only("acme/api")
	opts.testClient = &sharedManifestClient{FakeClient: fc, other: func() {
		otherErr = runReposInstall(context.Background(), only("acme/web"))
		otherFile = mustReadFile(t, manifestPath)
	}}

	err := runReposInstall(context.Background(), opts)
	require.NoError(t, otherErr)
	requireTraceKeyWritten(t, fc, "acme/web", otherFile)
	require.Equal(t, 1, strings.Count(otherFile, target), "the other install recorded the tracker default of acme/web")
	webInstallation := traceValue(traceInstallationIDRe, otherFile)
	require.NotEmpty(t, webInstallation, "the other install recorded the trace_context of acme/web")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "changed after this run loaded it")
	assert.Equal(t, otherFile, mustReadFile(t, manifestPath), "the other install's edits are kept")
	for _, s := range fc.CreatedSecrets {
		assert.NotEqual(t, "acme/api", s.Owner+"/"+s.Repo, "the refused run writes no acme/api secret")
	}
	for _, v := range fc.Variables {
		assert.NotEqual(t, "acme/api", v.Owner+"/"+v.Repo, "the refused run writes no acme/api variable")
	}

	require.NoError(t, runReposInstall(context.Background(), only("acme/api")), "a plain retry from the file converges")
	final := mustReadFile(t, manifestPath)
	requireTraceKeyWritten(t, fc, "acme/api", final)
	requireTraceKeyWritten(t, fc, "acme/web", final)
	assert.Equal(t, 2, strings.Count(final, target), "both tracker defaults are recorded")
	assert.Len(t, traceInstallationIDRe.FindAllString(final, -1), 2, "both entries record trace_context")
	assert.Contains(t, final, webInstallation, "acme/web's trace_context survives")
}

// A repos.yaml that cannot be read again just before the write stops install
// with that read error, not with the refusal for a file another install
// changed, and before install writes to the forge.
func TestRunReposInstall_ManifestUnreadableBeforeWriteIsReported(t *testing.T) {
	manifestPath := writeTestManifest(t, twoRepoManifestYAML)
	fc := newInstallFakeClient("acme/api", "acme/web")
	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.repoFilter = []string{"acme/api"}
	opts.testClient = &sharedManifestClient{FakeClient: fc, other: func() { require.NoError(t, os.Remove(manifestPath)) }}

	err := runReposInstall(context.Background(), opts)
	require.ErrorIs(t, err, fs.ErrNotExist)
	assert.NotContains(t, err.Error(), "changed after this run loaded it")
	assertNoInstallWrites(t, fc)
}

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

// An install that would rewrite repos.yaml past the size limit that loading
// a manifest enforces is refused before it writes the file or anything to the
// forge, in a dry run as well. This hand-written file loads, but install
// renders it with deeper indentation, and the edits are the first-install
// tracker default and the trace_context of acme/api.
func TestRunReposInstall_ManifestOverSizeLimitIsRefused(t *testing.T) {
	const limit = 1 << 20 // LoadManifest's limit
	var b strings.Builder
	b.WriteString(githubUnmanagedManifestYAML)
	for i := 0; b.Len() < limit-50000; i++ {
		fmt.Fprintf(&b, "    - name: acme/%s-%05d\n", strings.Repeat("r", 20), i)
	}
	manifest := b.String()
	for _, dryRun := range []bool{true, false} {
		t.Run(fmt.Sprintf("dry run %t", dryRun), func(t *testing.T) {
			manifestPath := writeTestManifest(t, manifest)
			loaded, err := repos.LoadManifest(context.Background(), manifestPath)
			require.NoError(t, err, "the file loads")
			rendered, err := repos.MarshalWithHeader(loaded)
			require.NoError(t, err)
			require.Greater(t, len(rendered), limit, "install renders the file past the limit")

			fc := newInstallFakeClient("acme/api")
			opts := githubManagedInstallOpts(manifestPath, fc)
			opts.repoFilter = []string{"acme/api"}
			opts.dryRun = dryRun
			captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
			require.Error(t, err)
			assert.Contains(t, err.Error(), fmt.Sprintf("would exceed the maximum size of %d bytes", limit))
			assert.Equal(t, manifest, mustReadFile(t, manifestPath), "repos.yaml is not written")
			assertNoInstallWrites(t, fc)
		})
	}
}

// An uninstall that must remove entries from a read-only manifest is refused
// before the teardown, in a dry run as well, so no repository is torn down
// while its entry stays. --manifest-only, which tears nothing down, gets
// the write's own refusal, and its dry run reports it too. --uninstall-only
// leaves the manifest alone and still tears down.
func TestRunReposUninstall_ReadOnlyManifest(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		manifestOnly, uninstallOnly bool
		wantErr                     string
	}{
		{"default", false, false, "the uninstall must remove acme/api from it; no changes were made"},
		{"manifest only", true, false, "is read-only; apply the equivalent edit to its source"},
		{"uninstall only", false, true, ""},
	} {
		for _, dryRun := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/dry run %t", tc.name, dryRun), func(t *testing.T) {
				r, w, err := os.Pipe()
				require.NoError(t, err)
				t.Cleanup(func() { _ = r.Close() })
				_, err = w.WriteString(twoRepoManifestYAML)
				require.NoError(t, err)
				require.NoError(t, w.Close())
				fc := newInstalledFakeClientCLI("acme/api", "acme/web")
				var runErr error
				captureStdout(t, func() {
					runErr = runReposUninstall(context.Background(), &reposUninstallConfig{
						manifest:      fmt.Sprintf("/dev/fd/%d", r.Fd()),
						dryRun:        dryRun,
						yes:           true,
						concurrency:   1,
						manifestOnly:  tc.manifestOnly,
						uninstallOnly: tc.uninstallOnly,
						testClient:    fc,
					}, []string{"acme/api"})
				})
				if tc.wantErr == "" {
					require.NoError(t, runErr)
				} else {
					require.ErrorContains(t, runErr, tc.wantErr)
				}
				_, kept := fc.VariableValues["acme/api/FULLSEND_PER_REPO_INSTALL"]
				assert.Equal(t, tc.wantErr != "" || dryRun, kept, "only a live --uninstall-only run tears down")
			})
		}
	}
}
