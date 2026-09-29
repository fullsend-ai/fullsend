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
// resolve-mr-source` subcommand (internal/cli/resolvemrsource.go).
// checkout-mr-source.sh only depends on that subcommand's I/O contract
// — flags in, a JSON object (or a failure) on stdout/stderr — so the
// stub reproduces that contract without a real GitLab API round trip.
// The Go implementation itself (forge.Client wiring, fork rejection,
// JSON shape) is covered by internal/cli/resolvemrsource_test.go.
func writeFullsendResolveStub(t *testing.T, binDir string) {
	t.Helper()
	path := filepath.Join(binDir, "fullsend")
	require.NoError(t, os.WriteFile(path, []byte(`#!/bin/sh
if [ "$1" != "resolve-mr-source" ]; then
  echo "unexpected fullsend subcommand: $1" >&2
  exit 1
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
`), 0o755))
}

// stubResolveMRSource stubs `fullsend resolve-mr-source` so it reports
// the given branch/sha/source-project-path. checkout-mr-source.sh
// always resolves through this subcommand (fast-path
// CI_MERGE_REQUEST_SOURCE_* variables are cross-checked against it or
// against CI_PROJECT_ID/CI_PROJECT_PATH directly, never trusted
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

func runCheckoutScript(t *testing.T, env checkoutEnv, extra []string, pathPrefix string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-c", `set -euo pipefail; . "$SCRIPT"; echo FIX_TARGET_REPO="$FIX_TARGET_REPO"; echo SOURCE_SHA="$SOURCE_SHA"; echo SOURCE_BRANCH="$SOURCE_BRANCH"; echo HEAD="$(git -C "$FIX_TARGET_REPO" rev-parse HEAD)"; echo BRANCH="$(git -C "$FIX_TARGET_REPO" rev-parse --abbrev-ref HEAD)"`)
	cmd.Dir = env.projectDir
	cmd.Env = append([]string{
		"SCRIPT=" + env.script,
		"PATH=" + pathPrefix + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + env.projectDir,
		"CI_SERVER_URL=" + env.serverRoot,
		"CI_PROJECT_PATH=" + env.targetProjectPath,
		"CI_PROJECT_ID=" + env.targetID,
		"FULLSEND_JOB_TOKEN=***",
		"MR_IID=7",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}, extra...)
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
	assert.Contains(t, out, "SOURCE_SHA="+env.sourceSHA)
	assert.Contains(t, out, "HEAD="+env.sourceSHA)
	assert.Contains(t, out, "BRANCH="+env.sourceBranch)
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

// TestCheckoutMRSource_CrossProjectSourceRejected proves the scoping fix:
// fullsend has no supported way to check out (or push a fix commit back
// to) a fork/cross-project merge request source, so a resolved source
// project path that differs from CI_PROJECT_PATH must fail closed here,
// as defense in depth alongside resolve-mr-source's own fork rejection
// (internal/cli/resolvemrsource_test.go).
func TestCheckoutMRSource_CrossProjectSourceRejected(t *testing.T) {
	env := seedSameProjectOrigin(t)
	resolveExtra, pathPrefix := stubResolveMRSource(t, env.sourceBranch, env.sourceSHA, "fork/project")

	out, err := runCheckoutScript(t, env, resolveExtra, pathPrefix)
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "cross-project MR source checkout is not supported")
}

// TestCheckoutMRSource_ProjectPathMismatchFailsClosed proves the
// untrusted-input fix: an untrusted CI_MERGE_REQUEST_SOURCE_PROJECT_PATH
// that disagrees with CI_PROJECT_PATH must be rejected, not trusted
// outright as the fetch source.
func TestCheckoutMRSource_ProjectPathMismatchFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	out, err := runCheckoutScript(t, env, []string{
		"CI_MERGE_REQUEST_SOURCE_PROJECT_PATH=missing/project",
	}, "")
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "does not match this project's CI_PROJECT_PATH")
}

// TestCheckoutMRSource_ProjectIDMismatchFailsClosed mirrors
// TestCheckoutMRSource_ProjectPathMismatchFailsClosed for the numeric
// fast-path variable: an untrusted CI_MERGE_REQUEST_SOURCE_PROJECT_ID
// that disagrees with CI_PROJECT_ID must be rejected outright, since
// this helper only supports same-project MRs.
func TestCheckoutMRSource_ProjectIDMismatchFailsClosed(t *testing.T) {
	env := seedSameProjectOrigin(t)
	out, err := runCheckoutScript(t, env, []string{
		"CI_MERGE_REQUEST_SOURCE_PROJECT_ID=999999",
	}, "")
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "does not match this project's CI_PROJECT_ID")
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
	assert.Contains(t, out, "SOURCE_SHA="+env.sourceSHA)
	assert.Contains(t, out, "HEAD="+env.sourceSHA)

	reviewed, err := os.ReadFile(filepath.Join(env.projectDir, "target-repo", "reviewed.txt"))
	require.NoError(t, err)
	assert.Equal(t, "reviewed-file\n", string(reviewed))
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
