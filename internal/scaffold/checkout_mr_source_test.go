package scaffold

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func checkoutMRSourceScript(t *testing.T) string {
	t.Helper()
	return writeGitLabScript(t, t.TempDir(), gitlabCheckoutMRSourceScriptPath)
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=testrunner",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=testrunner",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=commit.gpgsign",
		"GIT_CONFIG_VALUE_0=false",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s in %s: %s", strings.Join(args, " "), dir, out)
	return strings.TrimSpace(string(out))
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	gitCmd(t, dir, "init", "-b", "main")
	gitCmd(t, dir, "config", "user.name", "testrunner")
	gitCmd(t, dir, "config", "user.email", "test@example.com")
}

// initGitRepoSHA256 mirrors initGitRepo but initializes a SHA-256
// repository (git init --object-format=sha256), so tests can exercise an
// actual SHA-256 source rather than only validating the SHA-256 object
// name's length. Skips (rather than fails) when the installed git does
// not support the --object-format flag.
func initGitRepoSHA256(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	cmd := exec.Command("git", "init", "-b", "main", "--object-format=sha256")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git does not support --object-format=sha256: %s", out)
	}
	gitCmd(t, dir, "config", "user.name", "testrunner")
	gitCmd(t, dir, "config", "user.email", "test@example.com")
}

func commitFile(t *testing.T, dir, rel, contents, msg string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	gitCmd(t, dir, "add", rel)
	gitCmd(t, dir, "commit", "-m", msg)
	return gitCmd(t, dir, "rev-parse", "HEAD")
}

// writeFullsendResolveStub stands in for the real `fullsend
// resolve-mr-source` and `fullsend check-protected-branch` subcommands
// (internal/cli/resolvemrsource.go, internal/cli/checkprotectedbranch.go).
// checkout-mr-source.sh only depends on each subcommand's I/O contract
// — flags in, a JSON object/empty output (or a failure) on
// stdout/stderr — so the stub reproduces that contract without a real
// GitLab API round trip. The Go implementations themselves (forge.Client
// wiring, source-project identity, JSON shape, protected-branch
// semantics) are covered by internal/cli/resolvemrsource_test.go and
// internal/cli/checkprotectedbranch_test.go.
//
// check-protected-branch defaults to reporting "not protected" (exit 0)
// so every existing checkout test — which only cares about
// resolve-mr-source's behavior — keeps exercising the happy path on
// this gate without opting in. Tests that specifically exercise the
// protected-branch gate set CHECK_PROTECTED_RESULT to a non-empty
// string, which the stub echoes to stderr and fails on.
func writeFullsendResolveStub(t *testing.T, binDir string) {
	t.Helper()
	path := filepath.Join(binDir, "fullsend")
	require.NoError(t, os.WriteFile(path, []byte(`#!/bin/sh
case "$1" in
  resolve-mr-source)
    if [ -n "${RESOLVE_ARGS_FILE:-}" ]; then
      printf '%s\n' "$@" > "${RESOLVE_ARGS_FILE}"
    fi
    if [ -n "${RESOLVE_ERROR:-}" ]; then
      echo "${RESOLVE_ERROR}" >&2
      exit 1
    fi
    if [ -n "${RESOLVE_JSON_FILE:-}" ] && [ -f "${RESOLVE_JSON_FILE}" ]; then
      cat "${RESOLVE_JSON_FILE}"
      exit 0
    fi
    echo "no RESOLVE_JSON_FILE" >&2
    exit 1
    ;;
  check-protected-branch)
    if [ -n "${CHECK_PROTECTED_ARGS_FILE:-}" ]; then
      printf '%s\n' "$@" > "${CHECK_PROTECTED_ARGS_FILE}"
    fi
    if [ -n "${CHECK_PROTECTED_RESULT:-}" ]; then
      echo "${CHECK_PROTECTED_RESULT}" >&2
      exit 1
    fi
    exit 0
    ;;
  *)
    echo "unexpected fullsend subcommand: $1" >&2
    exit 1
    ;;
esac
`), 0o755))
}

// stubResolveMRSource stubs `fullsend resolve-mr-source` so it reports
// the given branch/sha/source-project-path. checkout-mr-source.sh
// always resolves through this subcommand (fast-path
// CI_MERGE_REQUEST_SOURCE_* variables are cross-checked against it or
// against FULLSEND_PINNED_PROJECT_ID/PATH directly, never trusted
// outright), so any test that sets those fast-path variables also
// needs a matching stub response or resolution fails first.
func stubResolveMRSource(t *testing.T, branch, sha, sourceProjectPath string) (extraEnv []string, pathPrefix string) {
	t.Helper()
	bin := t.TempDir()
	writeFullsendResolveStub(t, bin)
	jsonPath := filepath.Join(t.TempDir(), "resolve.json")
	payload, err := json.Marshal(map[string]any{
		"source_branch":       branch,
		"source_sha":          sha,
		"source_project_path": sourceProjectPath,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(jsonPath, payload, 0o644))
	return []string{"RESOLVE_JSON_FILE=" + jsonPath}, bin + string(os.PathListSeparator)
}

// gitHTTPBackendPath locates the git-http-backend CGI binary shipped
// alongside the git installation. Tests that need it skip (rather than
// fail) when it is not present, since it is not a Go module dependency.
func gitHTTPBackendPath(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "--exec-path").Output()
	require.NoError(t, err)
	p := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("git-http-backend not available: %v", err)
	}
	return p
}

// startAuthedGitHTTPSServer serves reposRoot over HTTPS via git-http-backend,
// requiring HTTP basic auth with wantUser/wantPassword before any request
// reaches the backend. This exercises the same protocol GitLab exposes to
// CI jobs (https + oauth2 bearer-as-password), so the credential-helper
// branch in checkout-mr-source.sh — never reached by the filesystem-path
// tests elsewhere in this file — actually runs end to end.
func startAuthedGitHTTPSServer(t *testing.T, reposRoot, wantUser, wantPassword string) *httptest.Server {
	t.Helper()
	backend := gitHTTPBackendPath(t)
	handler := &cgi.Handler{
		Path: backend,
		Env: []string{
			"GIT_PROJECT_ROOT=" + reposRoot,
			"GIT_HTTP_EXPORT_ALL=1",
			"PATH=" + os.Getenv("PATH"),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"HOME=" + t.TempDir(),
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != wantUser || pass != wantPassword {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// writeServerCAFile PEM-encodes the httptest server's certificate to a file
// so tests can point GIT_SSL_CAINFO at it and exercise the real TLS
// verification path, instead of disabling verification with
// GIT_SSL_NO_VERIFY=true.
func writeServerCAFile(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	require.NotEmpty(t, srv.Certificate().Raw, "test server certificate must be present")
	caPath := filepath.Join(t.TempDir(), "server-ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	require.NoError(t, os.WriteFile(caPath, caPEM, 0o644))
	return caPath
}

type checkoutEnv struct {
	script            string
	projectDir        string
	serverRoot        string
	targetProjectPath string
	sourceProjectPath string
	sourceSHA         string
	sourceBranch      string
	targetID          string
	sourceID          string
}

func seedSameProjectOrigin(t *testing.T) checkoutEnv {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	initGitRepo(t, work)
	require.NoError(t, os.WriteFile(filepath.Join(work, ".fullsend-config-marker"), []byte("trusted\n"), 0o644))
	commitFile(t, work, "README.md", "default branch\n", "main commit")
	gitCmd(t, work, "checkout", "-b", "feature")
	sourceSHA := commitFile(t, work, "reviewed.txt", "reviewed-file\n", "feature commit")

	serverRoot := filepath.Join(root, "gitlab")
	projectPath := "group/project"
	bare := filepath.Join(serverRoot, projectPath+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(bare), 0o755))
	gitCmd(t, work, "clone", "--bare", ".", bare)

	runner := filepath.Join(root, "runner")
	gitCmd(t, root, "clone", "--depth=1", "--branch", "main", bare, runner)
	require.NoError(t, os.WriteFile(filepath.Join(runner, ".fullsend-config-marker"), []byte("trusted-runner\n"), 0o644))

	script := checkoutMRSourceScript(t)
	return checkoutEnv{
		script:            script,
		projectDir:        runner,
		serverRoot:        serverRoot,
		targetProjectPath: projectPath,
		sourceProjectPath: projectPath,
		sourceSHA:         sourceSHA,
		sourceBranch:      "feature",
		targetID:          "10",
		sourceID:          "10",
	}
}

// seedForeignProjectOrigin builds a target project the runner clones
// from and a separate source project that holds the reviewed MR head.
// sourceProjectPath is the GitLab path_with_namespace of that source
// (fork/project, other-group/other-project, nested groups, etc.).
func seedForeignProjectOrigin(t *testing.T, sourceProjectPath string) checkoutEnv {
	t.Helper()
	root := t.TempDir()

	targetWork := filepath.Join(root, "target-work")
	initGitRepo(t, targetWork)
	require.NoError(t, os.WriteFile(filepath.Join(targetWork, ".fullsend-config-marker"), []byte("trusted\n"), 0o644))
	commitFile(t, targetWork, "README.md", "default branch\n", "main commit")

	serverRoot := filepath.Join(root, "gitlab")
	targetPath := "group/project"
	targetBare := filepath.Join(serverRoot, targetPath+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(targetBare), 0o755))
	gitCmd(t, targetWork, "clone", "--bare", ".", targetBare)

	sourceWork := filepath.Join(root, "source-work")
	initGitRepo(t, sourceWork)
	commitFile(t, sourceWork, "README.md", "source main\n", "source main")
	gitCmd(t, sourceWork, "checkout", "-b", "feature")
	sourceSHA := commitFile(t, sourceWork, "reviewed.txt", "reviewed-file\n", "feature commit")

	sourceBare := filepath.Join(serverRoot, sourceProjectPath+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(sourceBare), 0o755))
	gitCmd(t, sourceWork, "clone", "--bare", ".", sourceBare)

	runner := filepath.Join(root, "runner")
	gitCmd(t, root, "clone", "--depth=1", "--branch", "main", targetBare, runner)
	require.NoError(t, os.WriteFile(filepath.Join(runner, ".fullsend-config-marker"), []byte("trusted-runner\n"), 0o644))

	return checkoutEnv{
		script:            checkoutMRSourceScript(t),
		projectDir:        runner,
		serverRoot:        serverRoot,
		targetProjectPath: targetPath,
		sourceProjectPath: sourceProjectPath,
		sourceSHA:         sourceSHA,
		sourceBranch:      "feature",
		targetID:          "10",
		sourceID:          "200",
	}
}

// seedSameProjectOriginSHA256 mirrors seedSameProjectOrigin but builds the
// origin repository with git's SHA-256 object format
// (initGitRepoSHA256), to exercise an actual successful checkout against
// a SHA-256 source — not just validate the SHA-256 object name's length
// as TestCheckoutMRSource_AcceptsSHA256Length does with a fabricated,
// non-existent SHA.
func seedSameProjectOriginSHA256(t *testing.T) checkoutEnv {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	initGitRepoSHA256(t, work)
	require.NoError(t, os.WriteFile(filepath.Join(work, ".fullsend-config-marker"), []byte("trusted\n"), 0o644))
	commitFile(t, work, "README.md", "default branch\n", "main commit")
	gitCmd(t, work, "checkout", "-b", "feature")
	sourceSHA := commitFile(t, work, "reviewed.txt", "reviewed-file\n", "feature commit")
	require.Len(t, sourceSHA, 64, "a SHA-256 repository must produce 64-character object names")

	serverRoot := filepath.Join(root, "gitlab")
	projectPath := "group/project"
	bare := filepath.Join(serverRoot, projectPath+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(bare), 0o755))
	gitCmd(t, work, "clone", "--bare", ".", bare)

	runner := filepath.Join(root, "runner")
	gitCmd(t, root, "clone", "--depth=1", "--branch", "main", bare, runner)
	require.NoError(t, os.WriteFile(filepath.Join(runner, ".fullsend-config-marker"), []byte("trusted-runner\n"), 0o644))

	return checkoutEnv{
		script:            checkoutMRSourceScript(t),
		projectDir:        runner,
		serverRoot:        serverRoot,
		targetProjectPath: projectPath,
		sourceProjectPath: projectPath,
		sourceSHA:         sourceSHA,
		sourceBranch:      "feature",
		targetID:          "10",
		sourceID:          "10",
	}
}

func assertCheckedOutReviewedSource(t *testing.T, env checkoutEnv, out string) {
	t.Helper()
	assert.Contains(t, out, "SOURCE_SHA="+env.sourceSHA)
	assert.Contains(t, out, "HEAD="+env.sourceSHA)
	assert.Contains(t, out, "BRANCH="+env.sourceBranch)
	assert.Contains(t, out, "SOURCE_PROJECT_PATH="+env.sourceProjectPath)
	assert.Contains(t, out, "FIX_TARGET_REPO="+filepath.Join(env.projectDir, "target-repo"))

	reviewed, err := os.ReadFile(filepath.Join(env.projectDir, "target-repo", "reviewed.txt"))
	require.NoError(t, err)
	assert.Equal(t, "reviewed-file\n", string(reviewed))

	_, err = os.Stat(filepath.Join(env.projectDir, "reviewed.txt"))
	assert.Error(t, err, "runner checkout on default branch must not gain MR source files")
	marker, err := os.ReadFile(filepath.Join(env.projectDir, ".fullsend-config-marker"))
	require.NoError(t, err)
	assert.Equal(t, "trusted-runner\n", string(marker), "trusted runner tree must be left in place")
}

func runCheckoutScript(t *testing.T, env checkoutEnv, extra []string, pathPrefix string) (string, error) {
	t.Helper()
	serverURL := "https://gitlab.test"
	extraEnv := []string{}
	if strings.HasPrefix(env.serverRoot, "https://") {
		serverURL = env.serverRoot
	} else {
		// fullsend_git_fetch_mr_source's credentialed branch now pins
		// GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null (in addition to
		// unsetting GIT_CONFIG_COUNT/GIT_CONFIG_PARAMETERS/GIT_CONFIG), so a
		// GIT_CONFIG_GLOBAL-based url.*.insteadOf redirect is no longer
		// honored by that invocation. Serve env.serverRoot over a real authed
		// HTTPS server instead (the same startAuthedGitHTTPSServer fixture
		// the TestCheckoutMRSource_HTTPS* tests below already use) so the
		// credentialed fetch reaches the local bare fixture repo over an
		// actual network call.
		srv := startAuthedGitHTTPSServer(t, env.serverRoot, "oauth2", "***")
		serverURL = srv.URL
		extraEnv = append(extraEnv, "GIT_SSL_CAINFO="+writeServerCAFile(t, srv))
	}
	// Tests that poison GIT_DIR/GIT_WORK_TREE/GIT_OBJECT_DIRECTORY/
	// GIT_ALTERNATE_OBJECT_DIRECTORIES/GIT_COMMON_DIR to prove the script
	// under test ignores them would otherwise also redirect this harness's
	// own post-hoc verification commands below (they run in the same
	// shell, after sourcing, with no per-invocation env prefix) — unset
	// them first so the verification reads the real FIX_TARGET_REPO state
	// regardless of what a given test poisons. The trailing _FS_* echoes
	// use bash's ${var+yes} "is it set" expansion (not "is it non-empty")
	// to prove fullsend_checkout_mr_source's RETURN trap unset its own
	// temporary state (_FS_EMPTY_HOOKS/_FS_GIT_ENV/_FS_GIT_HOOK_ARGS)
	// instead of leaking it into this sourced shell.
	cmd := exec.Command("bash", "-c", `set -euo pipefail; . "$SCRIPT"; unset GIT_DIR GIT_WORK_TREE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR; echo FIX_TARGET_REPO="$FIX_TARGET_REPO"; echo SOURCE_SHA="$SOURCE_SHA"; echo SOURCE_BRANCH="$SOURCE_BRANCH"; echo SOURCE_PROJECT_PATH="$SOURCE_PROJECT_PATH"; echo HEAD="$(git -C "$FIX_TARGET_REPO" rev-parse HEAD)"; echo BRANCH="$(git -C "$FIX_TARGET_REPO" rev-parse --abbrev-ref HEAD)"; echo _FS_EMPTY_HOOKS_SET="${_FS_EMPTY_HOOKS+yes}"; echo _FS_GIT_ENV_SET="${_FS_GIT_ENV+yes}"; echo _FS_GIT_HOOK_ARGS_SET="${_FS_GIT_HOOK_ARGS+yes}"`)
	cmd.Dir = env.projectDir
	cmd.Env = append(append([]string{
		"SCRIPT=" + env.script,
		"PATH=" + pathPrefix + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + env.projectDir,
		"CI_SERVER_URL=" + serverURL,
		"CI_PROJECT_PATH=" + env.targetProjectPath,
		"CI_PROJECT_ID=" + env.targetID,
		// checkout-mr-source.sh resolves and fetches the MR source using
		// FULLSEND_PINNED_* (verified via the CI_JOB_TOKEN job record),
		// not CI_PROJECT_PATH/CI_PROJECT_ID/CI_SERVER_URL. In this base
		// env the pinned values match the CI_* values above; tests
		// proving the CI_* variables are ignored override these with
		// distinct, unpinned values instead.
		"FULLSEND_PINNED_PROJECT_ID=" + env.targetID,
		"FULLSEND_PINNED_PROJECT_PATH=" + env.targetProjectPath,
		"FULLSEND_PINNED_GITLAB_URL=" + serverURL,
		"FULLSEND_JOB_TOKEN=***",
		"MR_IID=7",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}, extraEnv...), extra...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCheckoutMRSource_SameProjectShallowRunnerCheckout(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, append([]string{
		"CI_MERGE_REQUEST_SOURCE_BRANCH_NAME=" + env.sourceBranch,
		"CI_MERGE_REQUEST_SOURCE_BRANCH_SHA=" + env.sourceSHA,
		"CI_MERGE_REQUEST_SOURCE_PROJECT_ID=" + env.sourceID,
		"CI_MERGE_REQUEST_SOURCE_PROJECT_PATH=" + env.sourceProjectPath,
	}, resolveExtra...), pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)
}

func TestCheckoutMRSource_ResolvesFromResolveMRSource(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)

	out, runErr := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.NoError(t, runErr, "stdout/stderr: %s", out)
	assert.Contains(t, out, "HEAD="+env.sourceSHA)
	reviewed, err := os.ReadFile(filepath.Join(env.projectDir, "target-repo", "reviewed.txt"))
	require.NoError(t, err)
	assert.Equal(t, "reviewed-file\n", string(reviewed))
}

func TestCheckoutMRSource_MissingSourceBranchFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, "", env.sourceSHA, env.sourceProjectPath)

	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "has no source branch")
}

func TestCheckoutMRSource_MissingSourceSHAFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, "feature", "", env.sourceProjectPath)

	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "has no source SHA")
}

func TestCheckoutMRSource_MissingSourceProjectPathFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, "")

	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "cannot resolve the source project path")
}

func TestCheckoutMRSource_MissingMRFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	out, err := runCheckoutScript(t, env, []string{"MR_IID=0"}, "")
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "numeric merge request IID")
}

// TestCheckoutMRSource_FetchFailureFailsClosed proves that a real git fetch
// failure (the resolved source branch does not exist on the server) fails
// closed.
func TestCheckoutMRSource_FetchFailureFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, "does-not-exist", env.sourceSHA, env.sourceProjectPath)

	out, runErr := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.Error(t, runErr, "stdout/stderr: %s", out)
	assert.Contains(t, out, "failed to fetch MR source")
}

func TestCheckoutMRSource_ForkSourceCheckedOut(t *testing.T) {
	env := seedForeignProjectOrigin(t, "fork/project")
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, append([]string{
		"CI_MERGE_REQUEST_SOURCE_BRANCH_NAME=" + env.sourceBranch,
		"CI_MERGE_REQUEST_SOURCE_BRANCH_SHA=" + env.sourceSHA,
		"CI_MERGE_REQUEST_SOURCE_PROJECT_ID=" + env.sourceID,
		"CI_MERGE_REQUEST_SOURCE_PROJECT_PATH=" + env.sourceProjectPath,
	}, resolveExtra...), pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)
}

func TestCheckoutMRSource_CrossProjectSourceCheckedOut(t *testing.T) {
	env := seedForeignProjectOrigin(t, "other-group/other-project")
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)
}

func TestCheckoutMRSource_NestedCrossProjectSourceCheckedOut(t *testing.T) {
	env := seedForeignProjectOrigin(t, "other-group/sub/project")
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)
}

// TestCheckoutMRSource_ProtectedSourceBranchFailsClosed proves the
// pre-push safety gate: when `fullsend check-protected-branch` reports
// the resolved source branch is protected (an exact-name rule in this
// case; internal/cli/checkprotectedbranch_test.go separately covers the
// exact-vs-wildcard distinction at the Go level since this shell stub
// does not model GitLab's rule matching), checkout-mr-source.sh refuses
// to fetch or check out the source at all.
func TestCheckoutMRSource_ProtectedSourceBranchFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	extra := append(append([]string{}, resolveExtra...), "CHECK_PROTECTED_RESULT=branch is protected")

	out, err := runCheckoutScript(t, env, extra, pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "is protected or its protection status could not be determined")
	_, statErr := os.Stat(filepath.Join(env.projectDir, "target-repo", ".git"))
	assert.Error(t, statErr, "a protected source branch must never be fetched")
}

// TestCheckoutMRSource_ProtectedForkSourceBranchFailsClosed is the same
// gate, exercised against a fork source project rather than the
// same-project case above — the protected-branch check runs against
// SOURCE_PROJECT_PATH (the fork), never FULLSEND_PINNED_PROJECT_PATH
// (the target project).
func TestCheckoutMRSource_ProtectedForkSourceBranchFailsClosed(t *testing.T) {
	env := seedForeignProjectOrigin(t, "fork/project")
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	argsFile := filepath.Join(t.TempDir(), "check-protected-args")
	extra := append(append([]string{}, resolveExtra...),
		"CHECK_PROTECTED_RESULT=branch is protected via wildcard rule",
		"CHECK_PROTECTED_ARGS_FILE="+argsFile,
	)

	out, err := runCheckoutScript(t, env, extra, pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)

	argsOut, readErr := os.ReadFile(argsFile)
	require.NoError(t, readErr)
	assert.Contains(t, string(argsOut), "--project\n"+env.sourceProjectPath)
	assert.Contains(t, string(argsOut), "--branch\n"+env.sourceBranch)
}

// TestCheckoutMRSource_ProjectPathMismatchFailsClosed proves the
// untrusted-input fix: an untrusted CI_MERGE_REQUEST_SOURCE_PROJECT_PATH
// that disagrees with the resolved source project must be rejected, not
// trusted outright as the fetch source.
func TestCheckoutMRSource_ProjectPathMismatchFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, append([]string{
		"CI_MERGE_REQUEST_SOURCE_PROJECT_PATH=missing/project",
	}, resolveExtra...), pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "does not match merge request")
}

// TestCheckoutMRSource_ProjectIDMismatchFailsClosed mirrors
// TestCheckoutMRSource_ProjectPathMismatchFailsClosed for the numeric
// fast-path variable: an untrusted CI_MERGE_REQUEST_SOURCE_PROJECT_ID
// that disagrees with CI_PROJECT_ID on a same-project MR must be
// rejected outright.
func TestCheckoutMRSource_ProjectIDMismatchFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, append([]string{
		"CI_MERGE_REQUEST_SOURCE_PROJECT_ID=999999",
	}, resolveExtra...), pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "does not match this project's FULLSEND_PINNED_PROJECT_ID")
}

func TestCheckoutMRSource_ForkProjectIDMatchesTargetFailsClosed(t *testing.T) {
	env := seedForeignProjectOrigin(t, "fork/project")
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, append([]string{
		"CI_MERGE_REQUEST_SOURCE_PROJECT_ID=" + env.targetID,
	}, resolveExtra...), pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "matches this project's FULLSEND_PINNED_PROJECT_ID")
}

// TestCheckoutMRSource_ResolvesUsingPinnedProjectAndGitLabURLNotUnpinnedCIVars
// is a regression test for the logic-error/secret-exposure findings:
// CI_PROJECT_PATH, CI_PROJECT_ID, and CI_SERVER_URL are ordinary,
// overridable pipeline variables — the same outrankable class ADR 0125
// already establishes for CI_PROJECT_ID elsewhere. checkout-mr-source.sh
// must resolve and fetch the MR source using
// FULLSEND_PINNED_PROJECT_PATH/FULLSEND_PINNED_GITLAB_URL (verified via
// the CI_JOB_TOKEN job record) even when those CI_* variables point
// somewhere else entirely — an authenticated dispatch that could
// override them must not be able to redirect the MR lookup or the
// credentialed fetch transport.
func TestCheckoutMRSource_ResolvesUsingPinnedProjectAndGitLabURLNotUnpinnedCIVars(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	argsFile := filepath.Join(t.TempDir(), "resolve-args.txt")
	out, err := runCheckoutScript(t, env, append([]string{
		"RESOLVE_ARGS_FILE=" + argsFile,
		"CI_PROJECT_PATH=unpinned/project",
		"CI_PROJECT_ID=999999",
		"CI_SERVER_URL=https://unpinned.example",
	}, resolveExtra...), pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "SOURCE_SHA="+env.sourceSHA)
	assert.Contains(t, out, "HEAD="+env.sourceSHA)

	argsRaw, readErr := os.ReadFile(argsFile)
	require.NoError(t, readErr)
	args := strings.Split(strings.TrimRight(string(argsRaw), "\n"), "\n")
	assert.Contains(t, args, env.targetProjectPath)
	assert.NotContains(t, args, "unpinned/project")
	assert.NotContains(t, args, "https://unpinned.example")
}

// TestCheckoutMRSource_MissingPinnedProjectPathFailsClosed and its two
// siblings below prove the fix fails closed rather than silently falling
// back to an overridable CI_* variable when the pinned identity
// (exported by pin-ci-job-identity.sh) is unexpectedly absent.
func TestCheckoutMRSource_MissingPinnedProjectPathFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	out, err := runCheckoutScript(t, env, []string{"FULLSEND_PINNED_PROJECT_PATH="}, "")
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "FULLSEND_PINNED_PROJECT_PATH is required")
}

func TestCheckoutMRSource_MissingPinnedProjectIDFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	out, err := runCheckoutScript(t, env, []string{"FULLSEND_PINNED_PROJECT_ID="}, "")
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "FULLSEND_PINNED_PROJECT_ID is required")
}

func TestCheckoutMRSource_MissingPinnedGitLabURLFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	out, err := runCheckoutScript(t, env, []string{"FULLSEND_PINNED_GITLAB_URL="}, "")
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "FULLSEND_PINNED_GITLAB_URL is required")
}

func TestCheckoutMRSource_TargetSHANotSubstitutedForFork(t *testing.T) {
	env := seedForeignProjectOrigin(t, "fork/project")
	targetSHA := gitCmd(t, env.projectDir, "rev-parse", "HEAD")
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, targetSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Regexp(t, "failed to (fetch|check out) MR source", out)
	_, statErr := os.Stat(filepath.Join(env.projectDir, "target-repo", "reviewed.txt"))
	assert.Error(t, statErr, "must not check out the fork working tree after a SHA mismatch")
}

func TestCheckoutMRSource_InvalidSourceProjectPathFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, "foo..bar/project")
	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "invalid MR source project path")
}

// TestCheckoutMRSource_BranchFastPathMismatchFailsClosed proves the
// auth-bypass fix: an untrusted CI_MERGE_REQUEST_SOURCE_BRANCH_NAME that
// disagrees with the resolved source branch must be rejected, not
// trusted outright. A plain project/group CI/CD variable can set this
// predefined-looking name; only resolve-mr-source's output is
// authoritative.
func TestCheckoutMRSource_BranchFastPathMismatchFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, append([]string{
		"CI_MERGE_REQUEST_SOURCE_BRANCH_NAME=some-other-branch",
	}, resolveExtra...), pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "does not match merge request")
}

func TestCheckoutMRSource_ExactSHANotBranchTip(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	initGitRepo(t, work)
	parent := commitFile(t, work, "README.md", "parent\n", "parent commit")
	gitCmd(t, work, "checkout", "-b", "feature")
	_ = commitFile(t, work, "reviewed.txt", "tip\n", "tip commit")

	serverRoot := filepath.Join(root, "gitlab")
	projectPath := "group/project"
	bare := filepath.Join(serverRoot, projectPath+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(bare), 0o755))
	gitCmd(t, work, "clone", "--bare", ".", bare)

	runner := filepath.Join(root, "runner")
	gitCmd(t, root, "clone", "--depth=1", "--branch", "main", bare, runner)

	env := checkoutEnv{
		script:            checkoutMRSourceScript(t),
		projectDir:        runner,
		serverRoot:        serverRoot,
		targetProjectPath: projectPath,
		sourceProjectPath: projectPath,
		sourceSHA:         parent,
		sourceBranch:      "feature",
		targetID:          "10",
		sourceID:          "10",
	}
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, parent, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "HEAD="+parent)
	_, statErr := os.Stat(filepath.Join(env.projectDir, "target-repo", "reviewed.txt"))
	assert.Error(t, statErr, "working tree must match the MR SHA, not the branch tip")
}

func TestCheckoutMRSource_UnknownSHAFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	missing := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, missing, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Regexp(t, "failed to (fetch|check out) MR source", out)
}

// TestCheckoutMRSource_AcceptsSHA256Length proves the SHA-256 fix:
// fullsend_validate_mr_source_sha must accept a 64-character hex SHA
// (a SHA-256 object name, which GitLab repositories can be configured
// to use) rather than rejecting anything longer than a 40-character
// SHA-1. The fake SHA below does not exist on the remote, so the
// script still fails — but it must fail at fetch, not be rejected
// outright as "invalid MR source SHA".
func TestCheckoutMRSource_AcceptsSHA256Length(t *testing.T) {
	env := seedSameProjectOrigin(t)
	sha256Len := strings.Repeat("a", 64)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, sha256Len, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.NotContains(t, out, "invalid MR source SHA")
	assert.Contains(t, out, "failed to fetch MR source")
}

// TestCheckoutMRSource_SHA256SourceCheckedOut proves the SHA-256 checkout
// fix end to end: a target repository initialized against a real SHA-256
// origin must fetch and check out successfully, not merely accept the
// SHA's length and then fail at fetch time (the gap
// TestCheckoutMRSource_AcceptsSHA256Length left — see
// fullsend_reset_target_repo_git_config and the _FS_OBJECT_FORMAT
// derivation in fullsend_checkout_mr_source).
func TestCheckoutMRSource_SHA256SourceCheckedOut(t *testing.T) {
	env := seedSameProjectOriginSHA256(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)

	targetRepo := filepath.Join(env.projectDir, "target-repo")
	formatOut, formatErr := exec.Command("git", "-C", targetRepo, "rev-parse", "--show-object-format").Output()
	require.NoError(t, formatErr)
	assert.Equal(t, "sha256", strings.TrimSpace(string(formatOut)), "target repo must be initialized with the source's SHA-256 object format")
}

func TestCheckoutMRSource_InvalidSHAFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, "not-a-sha", env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "invalid MR source SHA")
}

func TestCheckoutMRSource_InvalidBranchFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, "foo..bar", env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "invalid MR source branch")
}

// TestCheckoutMRSource_PlusInBranchNameCheckedOut proves that
// fullsend_validate_mr_source_ref's allowlist accepts legal git branch
// names carrying "+" (e.g. semver/Dependabot build metadata like
// "release/1.0.0+build.1"), not just the [A-Za-z0-9._/-] subset the
// allowlist previously enforced.
func TestCheckoutMRSource_PlusInBranchNameCheckedOut(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	initGitRepo(t, work)
	require.NoError(t, os.WriteFile(filepath.Join(work, ".fullsend-config-marker"), []byte("trusted\n"), 0o644))
	commitFile(t, work, "README.md", "default branch\n", "main commit")
	branch := "release/1.0.0+build.1"
	gitCmd(t, work, "checkout", "-b", branch)
	sourceSHA := commitFile(t, work, "reviewed.txt", "reviewed-file\n", "feature commit")

	serverRoot := filepath.Join(root, "gitlab")
	projectPath := "group/project"
	bare := filepath.Join(serverRoot, projectPath+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(bare), 0o755))
	gitCmd(t, work, "clone", "--bare", ".", bare)

	runner := filepath.Join(root, "runner")
	gitCmd(t, root, "clone", "--depth=1", "--branch", "main", bare, runner)
	require.NoError(t, os.WriteFile(filepath.Join(runner, ".fullsend-config-marker"), []byte("trusted-runner\n"), 0o644))

	env := checkoutEnv{
		script:            checkoutMRSourceScript(t),
		projectDir:        runner,
		serverRoot:        serverRoot,
		targetProjectPath: projectPath,
		sourceProjectPath: projectPath,
		sourceSHA:         sourceSHA,
		sourceBranch:      branch,
		targetID:          "10",
		sourceID:          "10",
	}

	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)
}

// TestCheckoutMRSource_IssueNumberPunctuationInBranchNameCheckedOut proves
// that fullsend_validate_mr_source_ref's "extended" branch validation,
// which now delegates to `git check-ref-format` instead of a hand-rolled
// character allowlist, accepts legal git branch names carrying "#" and
// "!" (e.g. issue-number branch names like "feature/#123" or
// "feature/abc!") that no finite allowlist addition previously covered.
func TestCheckoutMRSource_IssueNumberPunctuationInBranchNameCheckedOut(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	initGitRepo(t, work)
	require.NoError(t, os.WriteFile(filepath.Join(work, ".fullsend-config-marker"), []byte("trusted\n"), 0o644))
	commitFile(t, work, "README.md", "default branch\n", "main commit")
	branch := "feature/#123"
	gitCmd(t, work, "checkout", "-b", branch)
	sourceSHA := commitFile(t, work, "reviewed.txt", "reviewed-file\n", "feature commit")

	serverRoot := filepath.Join(root, "gitlab")
	projectPath := "group/project"
	bare := filepath.Join(serverRoot, projectPath+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(bare), 0o755))
	gitCmd(t, work, "clone", "--bare", ".", bare)

	runner := filepath.Join(root, "runner")
	gitCmd(t, root, "clone", "--depth=1", "--branch", "main", bare, runner)
	require.NoError(t, os.WriteFile(filepath.Join(runner, ".fullsend-config-marker"), []byte("trusted-runner\n"), 0o644))

	env := checkoutEnv{
		script:            checkoutMRSourceScript(t),
		projectDir:        runner,
		serverRoot:        serverRoot,
		targetProjectPath: projectPath,
		sourceProjectPath: projectPath,
		sourceSHA:         sourceSHA,
		sourceBranch:      branch,
		targetID:          "10",
		sourceID:          "10",
	}

	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)
}

// TestCheckoutMRSource_NonASCIIBranchNameCheckedOut proves that
// fullsend_validate_mr_source_ref's "extended" branch validation accepts
// legal non-ASCII git branch names (e.g. "fix/naïve"), which the
// previous [A-Za-z0-9._/+=@-] allowlist rejected outright.
func TestCheckoutMRSource_NonASCIIBranchNameCheckedOut(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	initGitRepo(t, work)
	require.NoError(t, os.WriteFile(filepath.Join(work, ".fullsend-config-marker"), []byte("trusted\n"), 0o644))
	commitFile(t, work, "README.md", "default branch\n", "main commit")
	branch := "fix/naïve"
	gitCmd(t, work, "checkout", "-b", branch)
	sourceSHA := commitFile(t, work, "reviewed.txt", "reviewed-file\n", "feature commit")

	serverRoot := filepath.Join(root, "gitlab")
	projectPath := "group/project"
	bare := filepath.Join(serverRoot, projectPath+".git")
	require.NoError(t, os.MkdirAll(filepath.Dir(bare), 0o755))
	gitCmd(t, work, "clone", "--bare", ".", bare)

	runner := filepath.Join(root, "runner")
	gitCmd(t, root, "clone", "--depth=1", "--branch", "main", bare, runner)
	require.NoError(t, os.WriteFile(filepath.Join(runner, ".fullsend-config-marker"), []byte("trusted-runner\n"), 0o644))

	env := checkoutEnv{
		script:            checkoutMRSourceScript(t),
		projectDir:        runner,
		serverRoot:        serverRoot,
		targetProjectPath: projectPath,
		sourceProjectPath: projectPath,
		sourceSHA:         sourceSHA,
		sourceBranch:      branch,
		targetID:          "10",
		sourceID:          "10",
	}

	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)
}

func TestCheckoutMRSource_ResolveFailureFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	bin := t.TempDir()
	writeFullsendResolveStub(t, bin)
	out, err := runCheckoutScript(t, env, []string{"RESOLVE_ERROR=api-down"}, bin+string(os.PathListSeparator))
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "cannot resolve merge request")
}

func TestCheckoutMRSource_DebugTraceAborts(t *testing.T) {
	env := seedSameProjectOrigin(t)
	out, err := runCheckoutScript(t, env, []string{"CI_DEBUG_TRACE=true"}, "")
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "CI_DEBUG_TRACE enabled")
}

// TestCheckoutMRSource_DebugTraceAbortsOnTruthyVariants is the
// checkout-mr-source.sh counterpart of
// TestRunAgentJobScript_DebugTraceAbortsOnTruthyVariants: this script is
// sourced from run-agent-job.sh's fix stage after that broader guard
// already ran, but its own guard must independently match the full
// truthy set gitlab-runner accepts for CI_DEBUG_TRACE, not just an
// exact "true", so it never becomes a weaker leftover check.
func TestCheckoutMRSource_DebugTraceAbortsOnTruthyVariants(t *testing.T) {
	for _, v := range []string{"1", "TRUE", "T"} {
		t.Run(v, func(t *testing.T) {
			env := seedSameProjectOrigin(t)
			out, err := runCheckoutScript(t, env, []string{"CI_DEBUG_TRACE=" + v}, "")
			require.Error(t, err, "stdout/stderr: %s", out)
			assert.Contains(t, out, "CI_DEBUG_TRACE enabled")
		})
	}
}

// TestCheckoutMRSource_ScopesTemporaryStateAfterReturn guards the fix
// that scopes _FS_EMPTY_HOOKS/_FS_GIT_ENV/_FS_GIT_HOOK_ARGS (and the
// RETURN trap that unsets them) to fullsend_checkout_mr_source's own
// execution. This script is sourced (not executed) into
// run-agent-job.sh's shell, so leaving these behind would leak into
// every later command that shell runs after the fix checkout.
func TestCheckoutMRSource_ScopesTemporaryStateAfterReturn(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "_FS_EMPTY_HOOKS_SET=\n")
	assert.Contains(t, out, "_FS_GIT_ENV_SET=\n")
	assert.Contains(t, out, "_FS_GIT_HOOK_ARGS_SET=\n")
}

// TestCheckoutMRSource_HTTPSCredentialHelperFetchesWithToken exercises the
// oauth2 credential-helper branch against a real HTTPS git server that
// requires the FULLSEND_JOB_TOKEN as the basic-auth password, the way
// GitLab does. Every other test in this file uses a filesystem-path
// CI_SERVER_URL, so this branch previously had zero coverage.
func TestCheckoutMRSource_HTTPSCredentialHelperFetchesWithToken(t *testing.T) {
	env := seedSameProjectOrigin(t)
	reposRoot := env.serverRoot
	const token = "***"
	srv := startAuthedGitHTTPSServer(t, reposRoot, "oauth2", token)
	env.serverRoot = srv.URL

	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, append([]string{
		"FULLSEND_JOB_TOKEN=" + token,
		"GIT_SSL_CAINFO=" + writeServerCAFile(t, srv),
	}, resolveExtra...), pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)
}

func TestCheckoutMRSource_HTTPSForkSourceFetchesWithToken(t *testing.T) {
	env := seedForeignProjectOrigin(t, "fork/project")
	reposRoot := env.serverRoot
	const token = "***"
	srv := startAuthedGitHTTPSServer(t, reposRoot, "oauth2", token)
	env.serverRoot = srv.URL

	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)
	out, err := runCheckoutScript(t, env, append([]string{
		"FULLSEND_JOB_TOKEN=" + token,
		"GIT_SSL_CAINFO=" + writeServerCAFile(t, srv),
	}, resolveExtra...), pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)
}

// TestCheckoutMRSource_HTTPSMissingJobTokenFailsClosed asserts that a
// missing FULLSEND_JOB_TOKEN never fetches the MR source — it fails
// closed before resolution or fetch, instead of falling back to an
// unauthenticated (and, for a private project, empty) fetch.
func TestCheckoutMRSource_HTTPSMissingJobTokenFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	reposRoot := env.serverRoot
	srv := startAuthedGitHTTPSServer(t, reposRoot, "oauth2", "***")
	env.serverRoot = srv.URL

	out, err := runCheckoutScript(t, env, []string{
		"FULLSEND_JOB_TOKEN=",
		"GIT_SSL_CAINFO=" + writeServerCAFile(t, srv),
	}, "")
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "FULLSEND_JOB_TOKEN")
}

// TestCheckoutMRSource_IgnoresGitConfigCountEnvOnCredentialedFetch guards
// the fix that unsets GIT_CONFIG_COUNT/GIT_CONFIG_KEY_n/GIT_CONFIG_VALUE_n
// and GIT_CONFIG_PARAMETERS for fullsend_git_fetch_mr_source's
// credentialed branch. Git gives this env-config layer precedence over
// configuration files, so an
// ordinary project/group CI/CD variable of this kind could otherwise
// rewrite the tokenized fetch URL via url.*.insteadOf. The attacker rule
// below targets the exact fetch URL — the longest possible match, so it
// would unambiguously win over the fixture's real authed HTTPS server (see
// startAuthedGitHTTPSServer) if this env-config layer were honored. The
// checkout must still land on the real source project.
func TestCheckoutMRSource_IgnoresGitConfigCountEnvOnCredentialedFetch(t *testing.T) {
	env := seedSameProjectOrigin(t)
	reposRoot := env.serverRoot
	const token = "***"
	srv := startAuthedGitHTTPSServer(t, reposRoot, "oauth2", token)
	env.serverRoot = srv.URL
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)

	attackerDir := filepath.Join(t.TempDir(), "attacker")
	sourceURL := srv.URL + "/" + env.sourceProjectPath + ".git"

	out, err := runCheckoutScript(t, env, append([]string{
		"FULLSEND_JOB_TOKEN=" + token,
		"GIT_SSL_CAINFO=" + writeServerCAFile(t, srv),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=url.file://" + attackerDir + "/.insteadOf",
		"GIT_CONFIG_VALUE_0=" + sourceURL,
	}, resolveExtra...), pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)
	_, statErr := os.Stat(attackerDir)
	assert.True(t, os.IsNotExist(statErr), "the credentialed fetch must never consult the attacker-redirected path")
}

// TestCheckoutMRSource_IgnoresGitConfigParametersEnvOnCredentialedFetch is
// the same guard as TestCheckoutMRSource_IgnoresGitConfigCountEnvOnCredentialedFetch
// but for GIT_CONFIG_PARAMETERS, the sibling env-config mechanism git also
// gives precedence over configuration files. The two mechanisms are
// independent: unsetting only GIT_CONFIG_COUNT would leave this one live.
func TestCheckoutMRSource_IgnoresGitConfigParametersEnvOnCredentialedFetch(t *testing.T) {
	env := seedSameProjectOrigin(t)
	reposRoot := env.serverRoot
	const token = "***"
	srv := startAuthedGitHTTPSServer(t, reposRoot, "oauth2", token)
	env.serverRoot = srv.URL
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)

	attackerDir := filepath.Join(t.TempDir(), "attacker")
	sourceURL := srv.URL + "/" + env.sourceProjectPath + ".git"

	out, err := runCheckoutScript(t, env, append([]string{
		"FULLSEND_JOB_TOKEN=" + token,
		"GIT_SSL_CAINFO=" + writeServerCAFile(t, srv),
		"GIT_CONFIG_PARAMETERS='url.file://" + attackerDir + "/.insteadOf=" + sourceURL + "'",
	}, resolveExtra...), pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)
	_, statErr := os.Stat(attackerDir)
	assert.True(t, os.IsNotExist(statErr), "the credentialed fetch must never consult the attacker-redirected path")
}

// TestCheckoutMRSource_IgnoresGitTemplateDirConfigOnInit guards against a
// CI/CD-supplied GIT_TEMPLATE_DIR seeding the freshly `git init`'d
// FIX_TARGET_REPO with a template-provided "config" file: `git init` copies
// template directory contents into .git/ verbatim, including a "config"
// file, and the resulting repo-local url.*.insteadOf/http.<url>.sslVerify/
// credential.<url>.helper keys take precedence over the "-c" overrides
// already passed to every git invocation in this script — the same
// repo-local-config threat class fullsend_reset_target_repo_git_config
// closes for the rest of the untrusted checkout lifecycle.
func TestCheckoutMRSource_IgnoresGitTemplateDirConfigOnInit(t *testing.T) {
	env := seedSameProjectOrigin(t)
	reposRoot := env.serverRoot
	const token = "***"
	srv := startAuthedGitHTTPSServer(t, reposRoot, "oauth2", token)
	env.serverRoot = srv.URL
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)

	attackerDir := filepath.Join(t.TempDir(), "attacker")
	sourceURL := srv.URL + "/" + env.sourceProjectPath + ".git"

	templateDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(templateDir, "config"), []byte(
		"[url \"file://"+attackerDir+"\"]\n\tinsteadOf = "+sourceURL+"\n",
	), 0o644))

	out, err := runCheckoutScript(t, env, append([]string{
		"FULLSEND_JOB_TOKEN=" + token,
		"GIT_SSL_CAINFO=" + writeServerCAFile(t, srv),
		"GIT_TEMPLATE_DIR=" + templateDir,
	}, resolveExtra...), pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)
	_, statErr := os.Stat(attackerDir)
	assert.True(t, os.IsNotExist(statErr), "a GIT_TEMPLATE_DIR-supplied config must never redirect the credentialed fetch")
}

// TestCheckoutMRSource_IgnoresGitConfigParametersEnvOnCheckout guards the
// fix that unsets GIT_CONFIG_COUNT/GIT_CONFIG_PARAMETERS/GIT_CONFIG (and
// pins core.hooksPath/core.pager/core.fsmonitor via _FS_GIT_HOOK_ARGS) for
// every git invocation fullsend_checkout_mr_source makes against the
// freshly created FIX_TARGET_REPO — including `git init` and the
// `git checkout --force` below — not only the credentialed fetch (see the
// two tests above). `git checkout --force` populates the working tree from
// the untrusted MR source content, whose own .gitattributes can name a
// filter driver; the freshly `git init`'d FIX_TARGET_REPO never has that
// driver declared in repo-local config, so the only way it could run is
// via the ambient GIT_CONFIG_PARAMETERS env-config layer — the same
// CI/CD-variable-poisoning threat class the credentialed-fetch tests above
// cover for url.*.insteadOf — while FULLSEND_JOB_TOKEN is already exported
// in the job environment.
func TestCheckoutMRSource_IgnoresGitConfigParametersEnvOnCheckout(t *testing.T) {
	env := seedSameProjectOrigin(t)
	root := t.TempDir()

	marker := filepath.Join(root, "filter-ran")
	filterPath := filepath.Join(root, "exfil-filter.sh")
	require.NoError(t, os.WriteFile(filterPath, []byte("#!/bin/sh\ntouch "+marker+"\ncat\n"), 0o755))

	// Extend the MR source branch with a commit naming the filter driver
	// via .gitattributes. The driver itself is declared only via
	// GIT_CONFIG_PARAMETERS below when the checkout script runs — never in
	// this (or any) repo-local config — to isolate the env-config gap.
	bare := filepath.Join(env.serverRoot, env.sourceProjectPath+".git")
	extend := filepath.Join(root, "extend")
	gitCmd(t, root, "clone", "--branch", env.sourceBranch, bare, extend)
	require.NoError(t, os.WriteFile(filepath.Join(extend, ".gitattributes"), []byte("poison.txt filter=exfil\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(extend, "poison.txt"), []byte("clean\n"), 0o644))
	gitCmd(t, extend, "add", ".gitattributes", "poison.txt")
	gitCmd(t, extend, "commit", "-m", "add poisoned smudge-filter fixture")
	gitCmd(t, extend, "push", "origin", env.sourceBranch)
	env.sourceSHA = gitCmd(t, extend, "rev-parse", "HEAD")

	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)

	out, err := runCheckoutScript(t, env, append([]string{
		"GIT_CONFIG_PARAMETERS='filter.exfil.smudge=" + filterPath + "'",
	}, resolveExtra...), pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)

	_, statErr := os.Stat(marker)
	assert.True(t, os.IsNotExist(statErr), "GIT_CONFIG_PARAMETERS-declared filter.exfil.smudge must never run while checking out the untrusted MR source")
}

// TestCheckoutMRSource_IgnoresGitDirEnvFamilyOnCheckout guards the fix
// that unsets GIT_DIR/GIT_WORK_TREE/GIT_OBJECT_DIRECTORY/
// GIT_ALTERNATE_OBJECT_DIRECTORIES/GIT_COMMON_DIR (via _FS_GIT_ENV) for
// every git invocation fullsend_checkout_mr_source makes against
// FIX_TARGET_REPO — `git init`, `remote add`, both fetch branches,
// `cat-file`, `git checkout --force`, and both `rev-parse` calls. `git -C`
// is ignored once GIT_DIR is an absolute path, so an ordinary
// project/group CI/CD variable named GIT_WORK_TREE or GIT_COMMON_DIR
// could otherwise redirect `git checkout --force` — which populates the
// working tree from the untrusted MR source — at a foreign directory
// instead of CI_PROJECT_DIR/target-repo, while FULLSEND_JOB_TOKEN remains
// exported.
func TestCheckoutMRSource_IgnoresGitDirEnvFamilyOnCheckout(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, env.sourceProjectPath)

	decoyWorkTree := filepath.Join(t.TempDir(), "decoy-worktree")
	decoyCommonDir := filepath.Join(t.TempDir(), "decoy-common")
	require.NoError(t, os.MkdirAll(decoyWorkTree, 0o755))
	require.NoError(t, os.MkdirAll(decoyCommonDir, 0o755))

	out, err := runCheckoutScript(t, env, append([]string{
		"GIT_WORK_TREE=" + decoyWorkTree,
		"GIT_COMMON_DIR=" + decoyCommonDir,
	}, resolveExtra...), pathPrefix)
	require.NoError(t, err, "stdout/stderr: %s", out)
	assertCheckedOutReviewedSource(t, env, out)

	_, statErr := os.Stat(filepath.Join(decoyWorkTree, "reviewed.txt"))
	assert.True(t, os.IsNotExist(statErr), "GIT_WORK_TREE-declared decoy directory must never receive the checked-out MR source")
	entries, err := os.ReadDir(decoyCommonDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "GIT_COMMON_DIR-declared decoy directory must never receive git-directory contents")
}
