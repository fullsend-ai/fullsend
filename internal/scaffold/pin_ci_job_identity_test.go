package scaffold

import (
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writePinCIJobIdentityScripts(t *testing.T) (root, pinScript string) {
	t.Helper()
	root = t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	pinScript = writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	return root, pinScript
}

func sourcePinScript(t *testing.T, root, script string, extraEnv []string) (combined string, err error) {
	t.Helper()
	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"; echo PINNED_PROJECT=$FULLSEND_PINNED_PROJECT_ID; echo PINNED_PIPELINE=$FULLSEND_PINNED_PIPELINE_ID; echo PINNED_REF=$FULLSEND_PINNED_REF; echo PINNED_SOURCE=$FULLSEND_PINNED_PIPELINE_SOURCE")
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"FULLSEND_ADMIT_SOURCE=api",
		"CI_JOB_TOKEN=job-token",
	}, extraEnv...)
	out, runErr := cmd.CombinedOutput()
	return string(out), runErr
}

type pinAPIState struct {
	jobJSON        string
	projectJSON    string
	branchJSON     string
	pipelineJSON   string
	jobStatus      int
	projectStatus  int
	branchStatus   int
	pipelineStatus int
	// projectPATStatus/branchPATStatus, when non-zero, override the
	// response given to a PRIVATE-TOKEN (Poller-role PAT) request on the
	// project-detail / branch-protection endpoints, independent of the
	// JOB-TOKEN-path status above. This simulates the self-hosted GitLab
	// EE 19.2.7 behavior from #7965 where JOB-TOKEN 404s on these two
	// calls but a PAT succeeds (or also fails, to test the no-fallback
	// path is still fail-closed).
	projectPATStatus int
	projectPATJSON   string
	branchPATStatus  int
	branchPATJSON    string
	jobHits          atomic.Int32
	sawPAT           atomic.Bool
	jobAuth          atomic.Bool
}

func buildPinAPIMux(st *pinAPIState) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/job", func(w http.ResponseWriter, r *http.Request) {
		st.jobHits.Add(1)
		if r.Header.Get("JOB-TOKEN") == "job-token" {
			st.jobAuth.Store(true)
		}
		if r.Header.Get("PRIVATE-TOKEN") != "" {
			st.sawPAT.Store(true)
		}
		status := st.jobStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(st.jobJSON))
	})
	mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		isPAT := r.Header.Get("PRIVATE-TOKEN") != ""
		if isPAT {
			st.sawPAT.Store(true)
		}
		path := r.URL.Path
		status := http.StatusOK
		body := ""
		switch {
		case strings.Contains(path, "/repository/branches/"):
			if isPAT && st.branchPATStatus != 0 {
				status = st.branchPATStatus
				body = st.branchPATJSON
			} else {
				status = st.branchStatus
				body = st.branchJSON
			}
		case strings.Contains(path, "/pipelines/"):
			status = st.pipelineStatus
			body = st.pipelineJSON
		default:
			if isPAT && st.projectPATStatus != 0 {
				status = st.projectPATStatus
				body = st.projectPATJSON
			} else {
				status = st.projectStatus
				body = st.projectJSON
			}
		}
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	return mux
}

func startPinAPI(t *testing.T, st *pinAPIState) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(buildPinAPIMux(st))
}

// startPinAPIOnIPv6Loopback serves the same mock API as startPinAPI, but
// bound to the IPv6 loopback address so its httptest.Server.URL is a
// bracketed IPv6 literal (https://[::1]:PORT) — used to prove the identity
// pin's CI_API_V4_URL validation admits a real IPv6 API root rather than
// only asserting the validation didn't reject a fabricated one.
func startPinAPIOnIPv6Loopback(t *testing.T, st *pinAPIState) *httptest.Server {
	t.Helper()
	l, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable in this environment: %v", err)
	}
	srv := httptest.NewUnstartedServer(buildPinAPIMux(st))
	srv.Listener.Close()
	srv.Listener = l
	srv.StartTLS()
	return srv
}

func pinJobJSON(projectID, pipelineID, ref string) string {
	return fmt.Sprintf(`{"id":9,"ref":%q,"pipeline":{"id":%s,"project_id":%s,"ref":%q}}`, ref, pipelineID, projectID, ref)
}

func pinTLSEnv(t *testing.T, srv *httptest.Server) []string {
	t.Helper()
	return []string{
		"CI_API_V4_URL=" + srv.URL + "/api/v4",
		"CI_SERVER_TLS_CA_FILE=" + writeServerCA(t, srv),
	}
}

func writeServerCA(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	cert := srv.Certificate()
	require.NotNil(t, cert)
	path := filepath.Join(t.TempDir(), "server-ca.pem")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	}), 0o644))
	return path
}

func TestPinCIJobIdentity_AdmitsMatchingAPISource(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"api","user":{"id":7}}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, pinTLSEnv(t, srv))
	require.NoError(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "PINNED_PROJECT=42")
	assert.Contains(t, out, "PINNED_PIPELINE=100")
	assert.Contains(t, out, "PINNED_REF=main")
	assert.Contains(t, out, "PINNED_SOURCE=api")
	assert.True(t, st.jobAuth.Load(), "job lookup must send JOB-TOKEN")
	assert.False(t, st.sawPAT.Load(), "gate curls must not send the role PAT")
}

func TestPinCIJobIdentity_DeniesParentPipelineSource(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"parent_pipeline","user":{"id":7}}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, pinTLSEnv(t, srv))
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "parent_pipeline")
	assert.Contains(t, out, "disjoint allowlist deny")
}

func TestPinCIJobIdentity_DeniesScheduleWhenAgentAdmitsAPI(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"schedule"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, pinTLSEnv(t, srv))
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "schedule")
	assert.Contains(t, out, "disjoint allowlist deny")
}

func TestPinCIJobIdentity_DeniesProtectedNonDefaultRef(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "release-1.0"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"release-1.0","protected":true}`,
		pipelineJSON: `{"id":100,"source":"api"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, pinTLSEnv(t, srv))
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "protected-but-non-default")
	assert.Contains(t, out, "release-1.0")
}

func TestPinCIJobIdentity_DeniesUnprotectedDefaultBranch(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":false}`,
		pipelineJSON: `{"id":100,"source":"api"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, pinTLSEnv(t, srv))
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "is not protected")
}

func TestPinCIJobIdentity_InvalidJobTokenFailsClosed(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobStatus: http.StatusUnauthorized,
		jobJSON:   `{"message":"401 Unauthorized"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, pinTLSEnv(t, srv))
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "CI_JOB_TOKEN job lookup failed")
}

func TestPinCIJobIdentity_ProjectLookupFailsClosed(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:       pinJobJSON("42", "100", "main"),
		projectStatus: http.StatusForbidden,
		projectJSON:   `{"message":"403 Forbidden"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, pinTLSEnv(t, srv))
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "cannot read pinned project")
}

func TestPinCIJobIdentity_BranchLookupFailsClosed(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchStatus: http.StatusForbidden,
		branchJSON:   `{"message":"403 Forbidden"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, pinTLSEnv(t, srv))
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "cannot read pinned branch")
}

func TestPinCIJobIdentity_PipelineLookupFailsClosed(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:        pinJobJSON("42", "100", "main"),
		projectJSON:    `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:     `{"name":"main","protected":true}`,
		pipelineStatus: http.StatusForbidden,
		pipelineJSON:   `{"message":"403 Forbidden"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, pinTLSEnv(t, srv))
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "cannot read pinned pipeline")
}

// TestPinCIJobIdentity_ProjectLookupFallsBackToPollerPAT is a regression
// test for #7965: self-hosted GitLab EE 19.2.7 returns 404 ("Project Not
// Found") for GET /projects/:id authenticated with JOB-TOKEN, breaking every
// poll/agent job's identity pin even though the CI_JOB_TOKEN itself is
// valid (GET /job above already succeeded with it). The already-provisioned
// Poller-role PAT must serve this same-project read as a fallback so the
// pin still succeeds.
func TestPinCIJobIdentity_ProjectLookupFallsBackToPollerPAT(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:          pinJobJSON("42", "100", "main"),
		projectStatus:    http.StatusNotFound,
		projectJSON:      `{"message":"404 Project Not Found"}`,
		projectPATStatus: http.StatusOK,
		projectPATJSON:   `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:       `{"name":"main","protected":true}`,
		pipelineJSON:     `{"id":100,"source":"api","user":{"id":7}}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, append(pinTLSEnv(t, srv),
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
	))
	require.NoError(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "PINNED_PROJECT=42")
	assert.True(t, st.sawPAT.Load(), "project lookup must retry with the Poller-role PAT after JOB-TOKEN 404s")
}

// TestPinCIJobIdentity_ProjectLookupFailsClosedWhenPollerPATAlsoFails proves
// the fallback added for #7965 preserves fail-closed semantics: if JOB-TOKEN
// 404s and the Poller-role PAT fallback also fails, the pin must still abort
// with a specific, actionable error rather than a generic retry-exhaustion
// message.
func TestPinCIJobIdentity_ProjectLookupFailsClosedWhenPollerPATAlsoFails(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:          pinJobJSON("42", "100", "main"),
		projectStatus:    http.StatusNotFound,
		projectJSON:      `{"message":"404 Project Not Found"}`,
		projectPATStatus: http.StatusForbidden,
		projectPATJSON:   `{"message":"403 Forbidden"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, append(pinTLSEnv(t, srv),
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
	))
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "cannot read pinned project")
	assert.Contains(t, out, "Poller-role PAT fallback")
	assert.True(t, st.sawPAT.Load(), "must have attempted the Poller-role PAT fallback before failing closed")
}

// TestPinCIJobIdentity_BranchLookupFallsBackToPollerPAT mirrors
// TestPinCIJobIdentity_ProjectLookupFallsBackToPollerPAT for the
// branch-protection read: #7965 observed this endpoint 404 with JOB-TOKEN
// on self-hosted EE 19.2.7 too ("Project Not Found", not a branch-specific
// rejection), so it gets the same Poller-role PAT fallback.
func TestPinCIJobIdentity_BranchLookupFallsBackToPollerPAT(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:         pinJobJSON("42", "100", "main"),
		projectJSON:     `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchStatus:    http.StatusNotFound,
		branchJSON:      `{"message":"404 Project Not Found"}`,
		branchPATStatus: http.StatusOK,
		branchPATJSON:   `{"name":"main","protected":true}`,
		pipelineJSON:    `{"id":100,"source":"api","user":{"id":7}}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, append(pinTLSEnv(t, srv),
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
	))
	require.NoError(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "PINNED_PROJECT=42")
	assert.True(t, st.sawPAT.Load(), "branch lookup must retry with the Poller-role PAT after JOB-TOKEN 404s")
}

// TestPinCIJobIdentity_BranchLookupFailsClosedWithoutPollerPATConfigured
// proves the fallback is only attempted when a Poller-role PAT is actually
// provisioned: with none configured, a JOB-TOKEN 404 on the branch read
// must still fail closed (matching TestPinCIJobIdentity_BranchLookupFailsClosed,
// which covers the same case via a 403 instead of a 404).
func TestPinCIJobIdentity_BranchLookupFailsClosedWithoutPollerPATConfigured(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchStatus: http.StatusNotFound,
		branchJSON:   `{"message":"404 Project Not Found"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, pinTLSEnv(t, srv))
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "cannot read pinned branch")
	assert.False(t, st.sawPAT.Load(), "no PAT configured — fallback must not be attempted")
}

func TestPinCIJobIdentity_EmptyJobTokenFailsClosed(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	out, err := sourcePinScript(t, root, script, []string{
		"CI_JOB_TOKEN=",
		"CI_API_V4_URL=https://gitlab.example/api/v4",
	})
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "CI_JOB_TOKEN is empty")
}

func TestPinCIJobIdentity_RejectsNonHTTPSAPIURL(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	out, err := sourcePinScript(t, root, script, []string{
		"CI_API_V4_URL=http://gitlab.example/api/v4",
	})
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "not an https GitLab API root")
}

func TestPinCIJobIdentity_RejectsBraceGlobAPIURL(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	out, err := sourcePinScript(t, root, script, []string{
		"CI_API_V4_URL=https://{attacker.example,gitlab.example}/api/v4",
	})
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "not permitted in a GitLab API root")
}

func TestPinCIJobIdentity_RejectsBracketRangeAPIURL(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	out, err := sourcePinScript(t, root, script, []string{
		"CI_API_V4_URL=https://gitlab[1-2].example/api/v4",
	})
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "not permitted in a GitLab API root")
}

func TestPinCIJobIdentity_AllowsIPv6LiteralAPIURL(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"api","user":{"id":7}}`,
	}
	srv := startPinAPIOnIPv6Loopback(t, st)
	t.Cleanup(srv.Close)
	require.Contains(t, srv.URL, "[::1]", "test setup: expected an IPv6-literal server URL")

	out, err := sourcePinScript(t, root, script, pinTLSEnv(t, srv))
	require.NoError(t, err, "stdout/stderr: %s", out)
	assert.NotContains(t, out, "not permitted in a GitLab API root")
	assert.Contains(t, out, "PINNED_PROJECT=42")
}

func TestPinCIJobIdentity_RejectsUserinfoAPIURL(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	out, err := sourcePinScript(t, root, script, []string{
		"CI_API_V4_URL=https://gitlab.example@attacker.example/api/v4",
	})
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "not permitted in a GitLab API root")
}

func TestPinCIJobIdentity_RejectsUnknownAdmitSource(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	out, err := sourcePinScript(t, root, script, []string{
		"FULLSEND_ADMIT_SOURCE=api,schedule",
		"CI_API_V4_URL=https://gitlab.example/api/v4",
	})
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "disjoint per-job allowlist")
}

func TestPinCIJobIdentity_IgnoresHTTPProxy(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"api"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	proxyHits := atomic.Int32{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		http.Error(w, "proxy should not be used", http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)

	out, err := sourcePinScript(t, root, script, append(pinTLSEnv(t, srv),
		"HTTP_PROXY="+proxy.URL,
		"HTTPS_PROXY="+proxy.URL,
		"ALL_PROXY="+proxy.URL,
		"http_proxy="+proxy.URL,
		"https_proxy="+proxy.URL,
		"all_proxy="+proxy.URL,
	))
	require.NoError(t, err, "stdout/stderr: %s", out)
	assert.Equal(t, int32(0), proxyHits.Load(), "gate curls must ignore trigger-influenced proxy settings")
	assert.Contains(t, out, "PINNED_SOURCE=api")
}

// TestPinCIJobIdentity_UnsetsProxyEnvInParentShellOnSuccess is a regression
// test for the secret-exposure finding: fullsend_gate_curl only strips
// proxy env vars for its own curl invocations (env -u ...); it does not
// remove them from the sourcing shell. The subsequent fullsend poll/run Go
// processes inherit that shell's exported environment and build their
// http.Client with a nil Transport (ProxyFromEnvironment), so a
// trigger-influenced HTTP_PROXY/HTTPS_PROXY left set after a successful pin
// could still CONNECT-proxy PAT-bearing API traffic. After the pin
// succeeds, HTTP_PROXY/HTTPS_PROXY/http_proxy/https_proxy (the names Go's
// ProxyFromEnvironment honors) must be unset in the parent shell.
func TestPinCIJobIdentity_UnsetsProxyEnvInParentShellOnSuccess(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"api"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	cmd := exec.Command("bash", "-c",
		"set -euo pipefail; . \"$SCRIPT\"; "+
			"echo POST_PIN_HTTP_PROXY=\"${HTTP_PROXY:-UNSET}\"; "+
			"echo POST_PIN_HTTPS_PROXY=\"${HTTPS_PROXY:-UNSET}\"; "+
			"echo POST_PIN_http_proxy=\"${http_proxy:-UNSET}\"; "+
			"echo POST_PIN_https_proxy=\"${https_proxy:-UNSET}\"; "+
			"echo POST_PIN_ALL_PROXY=\"${ALL_PROXY:-UNSET}\"; "+
			"echo POST_PIN_all_proxy=\"${all_proxy:-UNSET}\"")
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"FULLSEND_ADMIT_SOURCE=api",
		"CI_JOB_TOKEN=job-token",
		"HTTP_PROXY=http://attacker.example:8080",
		"HTTPS_PROXY=http://attacker.example:8080",
		"http_proxy=http://attacker.example:8080",
		"https_proxy=http://attacker.example:8080",
		"ALL_PROXY=http://attacker.example:8080",
		"all_proxy=http://attacker.example:8080",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stdout/stderr: %s", out)
	got := string(out)

	assert.Contains(t, got, "POST_PIN_HTTP_PROXY=UNSET")
	assert.Contains(t, got, "POST_PIN_HTTPS_PROXY=UNSET")
	assert.Contains(t, got, "POST_PIN_http_proxy=UNSET")
	assert.Contains(t, got, "POST_PIN_https_proxy=UNSET")
	assert.Contains(t, got, "POST_PIN_ALL_PROXY=UNSET")
	assert.Contains(t, got, "POST_PIN_all_proxy=UNSET")
}

// TestPinCIJobIdentity_AuthHeaderNotOnCurlArgv verifies that
// fullsend_gate_curl rewrites a caller's `-H "JOB-TOKEN: ..."` into
// `-H @tempfile` before invoking curl, so the token value itself never
// appears in curl's argv (visible via /proc/<pid>/cmdline, `ps`, or
// execve audit logs otherwise). A shim `curl` binary ahead of the real
// one on PATH logs its raw argv, then execs the real curl so the pin
// script still completes normally.
func TestPinCIJobIdentity_AuthHeaderNotOnCurlArgv(t *testing.T) {
	realCurl, err := exec.LookPath("curl")
	require.NoError(t, err)

	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"api"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	argvLog := filepath.Join(t.TempDir(), "argv.log")
	shimDir := t.TempDir()
	shimScript := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + argvLog + "\nexec " + realCurl + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(shimDir, "curl"), []byte(shimScript), 0o755))

	out, err := sourcePinScript(t, root, script, append(pinTLSEnv(t, srv),
		"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	))
	require.NoError(t, err, "stdout/stderr: %s", out)

	logged, readErr := os.ReadFile(argvLog)
	require.NoError(t, readErr)
	loggedStr := string(logged)
	assert.NotContains(t, loggedStr, "job-token", "the raw JOB-TOKEN value must not appear in curl's argv")
	assert.Contains(t, loggedStr, "-H\n@", "the auth header must be passed to curl via an @file, not a literal argument")
}

func TestPinCIJobIdentity_PollerAdmitsScheduleOnly(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"schedule"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, append(pinTLSEnv(t, srv),
		"FULLSEND_ADMIT_SOURCE=schedule",
	))
	require.NoError(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "PINNED_SOURCE=schedule")
}

func TestPinCIJobIdentity_PollerDeniesAPISource(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"api"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	out, err := sourcePinScript(t, root, script, append(pinTLSEnv(t, srv),
		"FULLSEND_ADMIT_SOURCE=schedule",
	))
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, out, "disjoint allowlist deny")
}

// Run*JobScript_* integration tests that exercise run-agent-job.sh /
// run-poll-job.sh end to end (not just the sourced
// pin-ci-job-identity.sh helper) live in gitlab_job_scripts_test.go,
// alongside the rest of that script-level coverage.
