package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// forgejoServer is an in-memory Forgejo contents API served under prefix.
type forgejoServer struct {
	mu     sync.Mutex
	prefix string
	files  map[string]string
	writes int
	auth   map[string]bool
	srv    *httptest.Server
}

func newForgejoServer(t *testing.T, prefix string) *forgejoServer {
	t.Helper()
	f := &forgejoServer{prefix: prefix, files: map[string]string{}, auth: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *forgejoServer) url() string { return f.srv.URL + f.prefix }

func (f *forgejoServer) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth[r.Header.Get("Authorization")] = true
	key, ok := strings.CutPrefix(r.URL.Path, f.prefix+"/api/v1/repos/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	key = strings.Replace(key, "/contents/", "/", 1)
	content, exists := f.files[key]
	switch r.Method {
	case http.MethodGet:
		if !exists {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"type": "file", "encoding": "base64", "sha": "s",
			"content": base64.StdEncoding.EncodeToString([]byte(content)),
		})
	case http.MethodPost, http.MethodPut:
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		data, _ := base64.StdEncoding.DecodeString(body["content"])
		f.files[key] = string(data)
		f.writes++
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		delete(f.files, key)
		f.writes++
	}
}

func forgejoCLIManifest(home, other *forgejoServer) string {
	return `version: 1
forgejo:
  instances:
    - alias: home
      url: ` + home.url() + `
      repos:
        - name: org/app
        - name: org/lib
    - alias: other
      url: ` + other.url() + `
      repos:
        - name: org/app
`
}

func forgejoEnv(env map[string]string) *forgejoTestHooks {
	return &forgejoTestHooks{lookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }}
}

func runForgejoStatus(t *testing.T, manifestPath string, filter []string, hooks *forgejoTestHooks, jsonOut bool) (string, error) {
	t.Helper()
	cmd := newReposStatusCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	err := runReposStatus(cmd, &reposStatusConfig{
		manifest: manifestPath, jsonOutput: jsonOut, concurrency: 4, repoFilter: filter, testForgejo: hooks,
	})
	return buf.String(), err
}

func TestReposForgejo_InstallStatusUninstallLifecycle(t *testing.T) {
	home := newForgejoServer(t, "/forgejo")
	other := newForgejoServer(t, "")
	manifestPath := writeTestManifest(t, forgejoCLIManifest(home, other))
	hooks := forgejoEnv(map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "home-tok"})

	// An ambiguous bare selector fails before any credential or forge use.
	_, err := runForgejoStatus(t, manifestPath, []string{"org/app"}, hooks, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forgejo:home/org/app, forgejo:other/org/app")

	out, err := runForgejoStatus(t, manifestPath, []string{"forgejo:home/org/app"}, hooks, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 not installed")
	assert.Contains(t, out, "forgejo:home/org/app")
	assert.Contains(t, out, "not-installed")
	assert.Zero(t, home.writes, "status is read-only")

	install := func(filter ...string) error {
		var runErr error
		captureStdout(t, func() {
			runErr = runReposInstall(context.Background(), &reposInstallConfig{
				manifest: manifestPath, concurrency: 4, repoFilter: filter, testForgejo: hooks,
			})
		})
		return runErr
	}
	require.NoError(t, install("forgejo:home/org/app", "org/lib"))
	assert.Equal(t, 2, home.writes)
	assert.Contains(t, home.files["org/app/.fullsend/config.yaml"], "managed by 'fullsend repos'")
	require.NoError(t, install("forgejo:home/org/*"))
	assert.Equal(t, 2, home.writes, "reinstall converges without changes")
	assert.Equal(t, map[string]bool{"token home-tok": true}, home.auth)
	assert.Empty(t, other.auth, "the unselected instance is never contacted")

	out, err = runForgejoStatus(t, manifestPath, []string{"forgejo:home/org/app"}, hooks, true)
	require.NoError(t, err)
	var result repos.StatusResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Len(t, result.Forgejo, 1)
	assert.Equal(t, repos.ForgejoStateUpToDate, result.Forgejo[0].State)

	// Every declared repository needs its own instance's token.
	err = install()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FULLSEND_FORGEJO_OTHER_TOKEN")
	assert.Empty(t, other.auth)

	var runErr error
	captureStdout(t, func() {
		runErr = runReposUninstall(context.Background(), &reposUninstallConfig{
			manifest: manifestPath, concurrency: 4, yes: true, testForgejo: hooks,
		}, []string{"forgejo:home/org/app"})
	})
	require.NoError(t, runErr)
	assert.NotContains(t, home.files, "org/app/.fullsend/config.yaml")
	assert.Contains(t, home.files, "org/lib/.fullsend/config.yaml")

	m, err := repos.LoadManifest(context.Background(), manifestPath)
	require.NoError(t, err)
	assert.Equal(t, []repos.ForgejoRepoEntry{{Name: "org/lib"}}, m.Forgejo.Instances[0].Repos)
	assert.Equal(t, []repos.ForgejoRepoEntry{{Name: "org/app"}}, m.Forgejo.Instances[1].Repos, "same path on another instance stays declared")
}

func TestReposForgejo_UninstallModes(t *testing.T) {
	home := newForgejoServer(t, "")
	other := newForgejoServer(t, "")
	hooks := forgejoEnv(map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "t"})
	run := func(opts *reposUninstallConfig, args ...string) error {
		var runErr error
		opts.concurrency, opts.yes, opts.testForgejo = 4, true, hooks
		captureStdout(t, func() { runErr = runReposUninstall(context.Background(), opts, args) })
		return runErr
	}
	declared := func(path string) []repos.ForgejoRepoEntry {
		m, err := repos.LoadManifest(context.Background(), path)
		require.NoError(t, err)
		return m.Forgejo.Instances[0].Repos
	}

	t.Run("dry run changes nothing", func(t *testing.T) {
		p := writeTestManifest(t, forgejoCLIManifest(home, other))
		var installErr error
		captureStdout(t, func() {
			installErr = runReposInstall(context.Background(), &reposInstallConfig{manifest: p, concurrency: 4, repoFilter: []string{"org/lib"}, testForgejo: hooks})
		})
		require.NoError(t, installErr)
		require.NoError(t, run(&reposUninstallConfig{manifest: p, dryRun: true}, "org/lib"))
		assert.Contains(t, home.files, "org/lib/.fullsend/config.yaml")
		assert.Len(t, declared(p), 2)
	})
	t.Run("uninstall-only keeps the manifest entry", func(t *testing.T) {
		p := writeTestManifest(t, forgejoCLIManifest(home, other))
		require.NoError(t, run(&reposUninstallConfig{manifest: p, uninstallOnly: true}, "org/lib"))
		assert.NotContains(t, home.files, "org/lib/.fullsend/config.yaml")
		assert.Len(t, declared(p), 2)
	})
	t.Run("manifest-only skips the forge", func(t *testing.T) {
		p := writeTestManifest(t, forgejoCLIManifest(home, other))
		writes := home.writes
		require.NoError(t, run(&reposUninstallConfig{manifest: p, manifestOnly: true}, "org/lib"))
		assert.Equal(t, writes, home.writes)
		assert.Equal(t, []repos.ForgejoRepoEntry{{Name: "org/app"}}, declared(p))
	})
	t.Run("teardown failure keeps the manifest entry", func(t *testing.T) {
		p := writeTestManifest(t, forgejoCLIManifest(home, other))
		failing := &forgejoTestHooks{lookupEnv: hooks.lookupEnv, newClient: func(string, string) (repos.ForgejoFileClient, error) {
			return failingForgejo{}, nil
		}}
		var runErr error
		captureStdout(t, func() {
			runErr = runReposUninstall(context.Background(), &reposUninstallConfig{manifest: p, concurrency: 4, yes: true, testForgejo: failing}, []string{"forgejo:home/org/lib"})
		})
		require.Error(t, runErr)
		assert.Contains(t, runErr.Error(), "forgejo uninstall")
		assert.Len(t, declared(p), 2)
	})
	t.Run("unknown alias", func(t *testing.T) {
		p := writeTestManifest(t, forgejoCLIManifest(home, other))
		assert.ErrorContains(t, run(&reposUninstallConfig{manifest: p}, "forgejo:nope/org/lib"), "unknown Forgejo instance")
	})
}

type failingForgejo struct{}

func (failingForgejo) GetFileContent(context.Context, string, string, string) ([]byte, error) {
	return nil, os.ErrPermission
}

func (failingForgejo) CreateOrUpdateFile(context.Context, string, string, string, string, []byte) error {
	return os.ErrPermission
}

func (failingForgejo) DeleteFile(context.Context, string, string, string, string) error {
	return os.ErrPermission
}

func TestReposForgejo_InstallFailureAndSelectorErrors(t *testing.T) {
	home := newForgejoServer(t, "")
	other := newForgejoServer(t, "")
	p := writeTestManifest(t, forgejoCLIManifest(home, other))
	hooks := &forgejoTestHooks{
		lookupEnv: func(string) (string, bool) { return "t", true },
		newClient: func(string, string) (repos.ForgejoFileClient, error) { return failingForgejo{}, nil },
	}
	var runErr error
	captureStdout(t, func() {
		runErr = runReposInstall(context.Background(), &reposInstallConfig{manifest: p, concurrency: 4, repoFilter: []string{"org/lib"}, testForgejo: hooks})
	})
	assert.ErrorContains(t, runErr, "forgejo install")

	captureStdout(t, func() {
		runErr = runReposInstall(context.Background(), &reposInstallConfig{manifest: p, concurrency: 4, repoFilter: []string{"org/app"}, testForgejo: hooks})
	})
	assert.ErrorContains(t, runErr, "ambiguous")

	_, err := runForgejoStatus(t, p, []string{"forgejo:home/org/lib"}, &forgejoTestHooks{lookupEnv: func(string) (string, bool) { return "", false }}, false)
	assert.ErrorContains(t, err, "FULLSEND_FORGEJO_HOME_TOKEN")
}

func TestForgejoStatusError(t *testing.T) {
	assert.NoError(t, forgejoStatusError(nil))
	assert.NoError(t, forgejoStatusError([]repos.ForgejoRepoResult{{State: repos.ForgejoStateUpToDate}}))
	assert.ErrorContains(t, forgejoStatusError([]repos.ForgejoRepoResult{{State: repos.ForgejoStateUnmanaged}}), "1 unmanaged")
}

func TestConfirmForgejoTargets(t *testing.T) {
	printer := ui.New(&bytes.Buffer{})
	require.NoError(t, confirmForgejoTargets(printer, "uninstall", []repos.ForgejoTarget{{Alias: "a"}}, os.Stdin))
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	_ = w.Close()
	err = confirmForgejoTargets(printer, "uninstall", []repos.ForgejoTarget{{Alias: "a"}, {Alias: "b"}}, r)
	assert.ErrorContains(t, err, "use --yes")
}

func TestOthersFilter(t *testing.T) {
	assert.Nil(t, othersFilter(nil, repos.ForgejoSelection{Rest: []string{"x"}}))
	assert.Equal(t, []string{"x"}, othersFilter([]string{"x", "forgejo:a/b/c"}, repos.ForgejoSelection{Rest: []string{"x"}}))
}

func TestNewForgejoLifecycle_DefaultClient(t *testing.T) {
	lc := newForgejoLifecycle(&repos.Manifest{}, true, nil)
	c, err := lc.NewClient("https://forgejo.example.com/sub", "tok")
	require.NoError(t, err)
	assert.NotNil(t, c)
	assert.True(t, lc.DryRun)
}
