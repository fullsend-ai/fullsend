package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge/forgejo"
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
	// commits holds the files at each recorded commit; tags maps
	// owner/repo@tag to a commit.
	commits map[string]map[string]string
	tags    map[string]string
}

func newForgejoServer(t *testing.T, prefix string) *forgejoServer {
	t.Helper()
	f := &forgejoServer{prefix: prefix, files: map[string]string{}, auth: map[string]bool{},
		commits: map[string]map[string]string{}, tags: map[string]string{}}
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
	if repo, ok := strings.CutSuffix(key, "/contents"); ok && r.Method == http.MethodPost {
		f.changeFiles(w, r, repo)
		return
	}
	if repo, tag, ok := strings.Cut(key, "/tags"); ok {
		f.serveTags(w, r, repo, strings.TrimPrefix(tag, "/"))
		return
	}
	key = strings.Replace(key, "/contents/", "/", 1)
	content, exists := f.files[key]
	if ref := r.URL.Query().Get("ref"); ref != "" {
		content, exists = f.commits[ref][key]
	}
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

// snapshot records the current files as a commit and returns its SHA.
func (f *forgejoServer) snapshot() string {
	sha := fmt.Sprintf("%040x", len(f.commits)+1)
	snap := make(map[string]string, len(f.files))
	maps.Copy(snap, f.files)
	f.commits[sha] = snap
	return sha
}

// changeFiles serves POST /repos/{owner}/{repo}/contents.
func (f *forgejoServer) changeFiles(w http.ResponseWriter, r *http.Request, repo string) {
	var body struct {
		Files []struct {
			Operation, Path, Content string
		} `json:"files"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	for _, op := range body.Files {
		key := repo + "/" + op.Path
		if op.Operation == "delete" {
			delete(f.files, key)
			continue
		}
		data, _ := base64.StdEncoding.DecodeString(op.Content)
		f.files[key] = string(data)
	}
	f.writes++
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": f.snapshot()}})
}

// serveTags serves GET /repos/{owner}/{repo}/tags/{tag} and POST .../tags.
func (f *forgejoServer) serveTags(w http.ResponseWriter, r *http.Request, repo, tag string) {
	if r.Method == http.MethodPost {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		target := body["target"]
		if target == "" {
			target = f.snapshot()
		}
		f.tags[repo+"@"+body["tag_name"]] = target
		f.writes++
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": target}})
		return
	}
	sha, ok := f.tags[repo+"@"+tag]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": sha}})
}

// forgejoCLIManifest vendors the workflow bundle into every repository, so
// it needs no workflow host.
func forgejoCLIManifest(home, other *forgejoServer) string {
	return `version: 1
defaults:
  vendor: true
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
	assert.Equal(t, 4, home.writes, "a config write and a workflow commit per repository")
	assert.Contains(t, home.files["org/app/.fullsend/config.yaml"], "managed by 'fullsend repos'")
	assert.Contains(t, home.files["org/app/.forgejo/workflows/fullsend.yml"], "uses: ./.forgejo/workflows/fullsend-dispatch.yml")
	require.NoError(t, install("forgejo:home/org/*"))
	assert.Equal(t, 4, home.writes, "reinstall converges without changes")
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

func (failingForgejo) GetFile(context.Context, string, string, string) (*repos.ForgejoFile, error) {
	return nil, os.ErrPermission
}

func (failingForgejo) CreateFile(context.Context, string, string, string, string, []byte) error {
	return os.ErrPermission
}

func (failingForgejo) UpdateFile(context.Context, string, string, string, string, []byte, string) error {
	return os.ErrPermission
}

func (failingForgejo) DeleteFile(context.Context, string, string, string, string, string) error {
	return os.ErrPermission
}

func (failingForgejo) GetFileAt(context.Context, string, string, string, string) (*repos.ForgejoFile, error) {
	return nil, os.ErrPermission
}

func (failingForgejo) ChangeFiles(context.Context, string, string, string, []repos.ForgejoFileChange) (string, error) {
	return "", os.ErrPermission
}

func (failingForgejo) GetTag(context.Context, string, string, string) (string, error) {
	return "", os.ErrPermission
}

func (failingForgejo) CreateTag(context.Context, string, string, string, string, string) (string, error) {
	return "", os.ErrPermission
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

// cliForgejoOAuth is a repos.ForgejoOAuth that records each login.
type cliForgejoOAuth struct {
	cached map[string]string
	logins []string
}

func (o *cliForgejoOAuth) CachedToken(_ context.Context, req repos.ForgejoOAuthRequest) (string, string, error) {
	if tok := o.cached[req.Alias]; tok != "" {
		return tok, "", nil
	}
	return "", "no cached OAuth token", nil
}

func (o *cliForgejoOAuth) Login(_ context.Context, req repos.ForgejoOAuthRequest) (string, error) {
	o.logins = append(o.logins, req.Alias)
	return "oauth-" + req.Alias, nil
}

func TestReposForgejo_OAuthLoginOnlyForSelectedInstances(t *testing.T) {
	home := newForgejoServer(t, "/forgejo")
	other := newForgejoServer(t, "")
	manifest := `version: 1
forgejo:
  instances:
    - alias: home
      url: ` + home.url() + `
      oauth:
        client_id: home-client
      repos:
        - name: org/app
          vendor: true
    - alias: other
      url: ` + other.url() + `
      oauth:
        client_id: other-client
      repos:
        - name: org/app
          vendor: true
`
	manifestPath := writeTestManifest(t, manifest)
	oauth := &cliForgejoOAuth{}
	hooks := &forgejoTestHooks{lookupEnv: func(string) (string, bool) { return "", false }, oauth: oauth, interactive: true}

	var runErr error
	stdout := captureStdout(t, func() {
		runErr = runReposInstall(context.Background(), &reposInstallConfig{
			manifest: manifestPath, concurrency: 4, repoFilter: []string{"forgejo:home/org/app"}, testForgejo: hooks,
		})
	})
	require.NoError(t, runErr)
	assert.Equal(t, []string{"home"}, oauth.logins, "only the selected instance is asked to log in")
	assert.Equal(t, map[string]bool{"token oauth-home": true}, home.auth)
	assert.Empty(t, other.auth, "the unselected instance is never contacted")
	assert.NotContains(t, stdout, "oauth-home")
	data, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "oauth-home", "the OAuth token is never written to repos.yaml")

	// A cached token is reused without a prompt, also non-interactively.
	oauth.cached = map[string]string{"home": "oauth-home"}
	hooks.interactive = false
	_, err = runForgejoStatus(t, manifestPath, []string{"forgejo:home/org/app"}, hooks, false)
	require.NoError(t, err)
	assert.Len(t, oauth.logins, 1)

	// Without a cached token a non-interactive command fails and names
	// the variable, before contacting the instance.
	_, err = runForgejoStatus(t, manifestPath, []string{"forgejo:other/org/app"}, hooks, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FULLSEND_FORGEJO_OTHER_TOKEN")
	assert.Contains(t, err.Error(), "rerun in a terminal to log in with OAuth")
	assert.Len(t, oauth.logins, 1)
	assert.Empty(t, other.auth)
}

func TestNewForgejoLifecycle_Wiring(t *testing.T) {
	m := &repos.Manifest{}
	lc := newForgejoLifecycle(m, false, nil)
	assert.IsType(t, liveForgejoOAuth{}, lc.OAuth, "production uses the keyring-backed OAuth fallback")

	oauth := &cliForgejoOAuth{}
	lc = newForgejoLifecycle(m, false, &forgejoTestHooks{oauth: oauth, interactive: true})
	assert.Same(t, oauth, lc.OAuth)
	assert.True(t, lc.Interactive)
	assert.Nil(t, newForgejoLifecycle(m, false, &forgejoTestHooks{}).OAuth, "test hooks never reach the OS keyring")
}

func TestLiveForgejoOAuth_Adapter(t *testing.T) {
	store := &cliMemStore{values: map[string]string{}}
	o := liveForgejoOAuth{&forgejo.OAuth{Store: store, In: strings.NewReader("n\n"), Out: &bytes.Buffer{}}}
	req := repos.ForgejoOAuthRequest{Alias: "home", BaseURL: "https://git.example.com", ClientID: "c", TokenEnvVar: "FULLSEND_FORGEJO_HOME_TOKEN"}
	tok, reason, err := o.CachedToken(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, tok)
	assert.Equal(t, "no cached OAuth token", reason)
	_, err = o.Login(context.Background(), req)
	assert.ErrorContains(t, err, "declined")
}

type cliMemStore struct{ values map[string]string }

func (s *cliMemStore) Get(key string) (string, error) {
	v, ok := s.values[key]
	if !ok {
		return "", forgejo.ErrTokenNotStored
	}
	return v, nil
}

func (s *cliMemStore) Set(key, value string) error { s.values[key] = value; return nil }

func (s *cliMemStore) Delete(key string) error { delete(s.values, key); return nil }

func TestForgejoStatusError(t *testing.T) {
	assert.NoError(t, forgejoStatusError(nil))
	assert.NoError(t, forgejoStatusError([]repos.ForgejoRepoResult{{State: repos.ForgejoStateUpToDate}}))
	assert.ErrorContains(t, forgejoStatusError([]repos.ForgejoRepoResult{{State: repos.ForgejoStateUnmanaged}}), "1 unmanaged")
}

func TestConfirmBulkAction_CountsForgejoSelectors(t *testing.T) {
	printer := ui.New(&bytes.Buffer{})
	manifest := &repos.Manifest{}
	require.NoError(t, confirmBulkAction(printer, "uninstall", nil, manifest, os.Stdin, "forgejo:a/o/r"))
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	_ = w.Close()
	err = confirmBulkAction(printer, "uninstall", nil, manifest, r, "forgejo:a/o/r", "forgejo:b/o/r")
	assert.ErrorContains(t, err, "use --yes")
}

const mixedForgeManifest = `version: 1
defaults:
  inference:
    auth: vertex-wif
github:
  repos:
    - name: gh/app
    - name: gh/web
forgejo:
  instances:
    - alias: home
      url: %s
      repos:
        - name: org/lib
`

// A selection spanning Forgejo and github/gitlab repositories is confirmed
// once as a whole, before anything is torn down or the manifest changes.
func TestReposForgejo_MixedUninstallConfirmsWholeSelectionBeforeAnyChange(t *testing.T) {
	for _, args := range [][]string{
		{"forgejo:home/org/lib", "gh/app"},
		{"forgejo:home/org/lib", "gh/app", "gh/web"},
		{"forgejo:home/org/lib", "gh/*"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := newForgejoServer(t, "")
			home.files["org/lib/.fullsend/config.yaml"] = "# managed by 'fullsend repos'\n"
			p := writeTestManifest(t, strings.Replace(mixedForgeManifest, "%s", home.url(), 1))
			before, err := os.ReadFile(p)
			require.NoError(t, err)

			// os.Stdin is not a terminal under go test, so the prompt
			// cannot be answered and the run must fail closed.
			var runErr error
			captureStdout(t, func() {
				runErr = runReposUninstall(context.Background(), &reposUninstallConfig{
					manifest: p, concurrency: 4, testForgejo: forgejoEnv(map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "t"}),
				}, args)
			})
			require.Error(t, runErr)
			assert.ErrorContains(t, runErr, "use --yes")
			assert.Zero(t, home.writes, "no Forgejo teardown before the confirmation")
			assert.Empty(t, home.auth, "the Forgejo instance is not contacted")
			after, err := os.ReadFile(p)
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after), "the manifest is unchanged")
		})
	}
}

// A selector left for github/gitlab that the manifest does not declare must
// fail the install before the Forgejo lifecycle writes anything.
func TestReposForgejo_InstallValidatesRemainingSelectorsBeforeForgejoWrites(t *testing.T) {
	home := newForgejoServer(t, "")
	p := writeTestManifest(t, strings.Replace(mixedForgeManifest, "%s", home.url(), 1))
	var runErr error
	captureStdout(t, func() {
		runErr = runReposInstall(context.Background(), &reposInstallConfig{
			manifest: p, concurrency: 4, repoFilter: []string{"forgejo:home/org/lib", "nope/missing"},
			testForgejo: forgejoEnv(map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "t"}),
		})
	})
	require.Error(t, runErr)
	assert.ErrorContains(t, runErr, "not declared in repos.yaml")
	assert.Zero(t, home.writes)
	assert.Empty(t, home.files)
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

// A workflow host publishes the bundle and a consumer pins it on the same
// instance; the host cannot be uninstalled while the consumer is declared.
func TestReposForgejo_WorkflowHostPublishPinAndProtect(t *testing.T) {
	home := newForgejoServer(t, "/forgejo")
	p := writeTestManifest(t, `version: 1
forgejo:
  instances:
    - alias: home
      url: `+home.url()+`
      repos:
        - name: fullsend/workflows
          workflow_host: true
        - name: org/app
`)
	hooks := forgejoEnv(map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "t"})
	var runErr error
	captureStdout(t, func() {
		runErr = runReposInstall(context.Background(), &reposInstallConfig{manifest: p, concurrency: 4, testForgejo: hooks})
	})
	require.NoError(t, runErr)
	require.Len(t, home.tags, 1)
	var pin string
	for _, sha := range home.tags {
		pin = sha
	}
	assert.Contains(t, home.files, "fullsend/workflows/.forgejo/workflows/fullsend-dispatch.yml")
	assert.Contains(t, home.files["org/app/.forgejo/workflows/fullsend.yml"],
		"uses: "+home.url()+"/fullsend/workflows/.forgejo/workflows/fullsend-dispatch.yml@"+pin)

	out, err := runForgejoStatus(t, p, nil, hooks, false)
	require.NoError(t, err)
	assert.Contains(t, out, "bundle fullsend-bundle-")
	assert.Contains(t, out, "at "+pin)

	captureStdout(t, func() {
		runErr = runReposUninstall(context.Background(), &reposUninstallConfig{manifest: p, concurrency: 4, yes: true, testForgejo: hooks},
			[]string{"forgejo:home/fullsend/workflows"})
	})
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "still serves 1 consumer(s) declared in repos.yaml: forgejo:home/org/app")
	m, err := repos.LoadManifest(context.Background(), p)
	require.NoError(t, err)
	assert.Len(t, m.Forgejo.Instances[0].Repos, 2, "the refused host stays declared")
	assert.Contains(t, home.files, "fullsend/workflows/.forgejo/workflows/fullsend-dispatch.yml")

	captureStdout(t, func() {
		runErr = runReposUninstall(context.Background(), &reposUninstallConfig{manifest: p, concurrency: 4, yes: true, testForgejo: hooks}, nil)
	})
	require.NoError(t, runErr)
	assert.Empty(t, home.files)
	m, err = repos.LoadManifest(context.Background(), p)
	require.NoError(t, err)
	assert.Empty(t, m.Forgejo.Instances[0].Repos)
}

// A consumer whose workflow index lost its ownership marker keeps its shim,
// so it stays declared and a later host-only uninstall is still refused.
func TestReposForgejo_UnownedConsumerStaysDeclaredAndProtectsHost(t *testing.T) {
	home := newForgejoServer(t, "/forgejo")
	p := writeTestManifest(t, `version: 1
forgejo:
  instances:
    - alias: home
      url: `+home.url()+`
      repos:
        - name: fullsend/workflows
          workflow_host: true
        - name: org/app
`)
	hooks := forgejoEnv(map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "t"})
	var runErr error
	captureStdout(t, func() {
		runErr = runReposInstall(context.Background(), &reposInstallConfig{manifest: p, concurrency: 4, testForgejo: hooks})
	})
	require.NoError(t, runErr)

	idx := "org/app/.fullsend/forgejo-workflows.yaml"
	require.Contains(t, home.files, idx)
	home.files[idx] = strings.TrimPrefix(home.files[idx], "# fullsend-managed: forgejo-workflows\n")

	captureStdout(t, func() {
		runErr = runReposUninstall(context.Background(), &reposUninstallConfig{manifest: p, concurrency: 4, yes: true, testForgejo: hooks},
			[]string{"forgejo:home/org/app"})
	})
	require.NoError(t, runErr)
	assert.Contains(t, home.files, "org/app/.forgejo/workflows/fullsend.yml", "the unowned shim is left in place")
	m, err := repos.LoadManifest(context.Background(), p)
	require.NoError(t, err)
	assert.Len(t, m.Forgejo.Instances[0].Repos, 2, "the consumer stays declared")

	captureStdout(t, func() {
		runErr = runReposUninstall(context.Background(), &reposUninstallConfig{manifest: p, concurrency: 4, yes: true, testForgejo: hooks},
			[]string{"forgejo:home/fullsend/workflows"})
	})
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "still serves 1 consumer(s) declared in repos.yaml: forgejo:home/org/app")
	assert.Contains(t, home.files, "fullsend/workflows/.forgejo/workflows/fullsend-dispatch.yml")
}

// A workflow host whose ownership index is gone keeps its bundle files, so
// it stays declared even though its consumer is removed in the same run.
func TestReposForgejo_UnownedHostStaysDeclared(t *testing.T) {
	home := newForgejoServer(t, "/forgejo")
	p := writeTestManifest(t, `version: 1
forgejo:
  instances:
    - alias: home
      url: `+home.url()+`
      repos:
        - name: fullsend/workflows
          workflow_host: true
        - name: org/app
`)
	hooks := forgejoEnv(map[string]string{"FULLSEND_FORGEJO_HOME_TOKEN": "t"})
	var runErr error
	captureStdout(t, func() {
		runErr = runReposInstall(context.Background(), &reposInstallConfig{manifest: p, concurrency: 4, testForgejo: hooks})
	})
	require.NoError(t, runErr)

	idx := "fullsend/workflows/.fullsend/forgejo-workflows.yaml"
	require.Contains(t, home.files, idx)
	delete(home.files, idx)

	captureStdout(t, func() {
		runErr = runReposUninstall(context.Background(), &reposUninstallConfig{manifest: p, concurrency: 4, yes: true, testForgejo: hooks}, nil)
	})
	require.NoError(t, runErr)
	assert.Contains(t, home.files, "fullsend/workflows/.forgejo/workflows/fullsend-dispatch.yml", "the unowned bundle is left in place")
	m, err := repos.LoadManifest(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, m.Forgejo.Instances[0].Repos, 1)
	assert.Equal(t, "fullsend/workflows", m.Forgejo.Instances[0].Repos[0].Name, "the retained host stays declared")
}
