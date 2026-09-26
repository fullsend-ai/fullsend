package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestPreflightGitHubResult_SkippedFields(t *testing.T) {
	r := &preflightGitHubResult{Skipped: true, SkipReason: "GH_TOKEN not set in sandbox"}
	assert.True(t, r.Skipped)
	assert.Equal(t, "GH_TOKEN not set in sandbox", r.SkipReason)
}

func TestPreflightGitHubResult_NotSkipped(t *testing.T) {
	r := &preflightGitHubResult{}
	assert.False(t, r.Skipped)
	assert.Empty(t, r.SkipReason)
}

func TestPreflightGitHubTimeout(t *testing.T) {
	require.Greater(t, preflightGitHubTimeout.Seconds(), float64(0))
	require.LessOrEqual(t, preflightGitHubTimeout.Seconds(), float64(60))
}

func TestSanitizeTokenPrefix(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "ghs_", sanitizeTokenPrefix("ghs_"))
	assert.Equal(t, "ghs_", sanitizeTokenPrefix("ghs_THIS_IS_A_SECRET_TOKEN"))
	assert.Equal(t, "ab", sanitizeTokenPrefix("ab"))
	assert.Equal(t, "", sanitizeTokenPrefix("  "))
}

func TestTokenTypeFromPrefix(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "installation", tokenTypeFromPrefix("ghs_"))
	assert.Equal(t, "pat", tokenTypeFromPrefix("ghp_"))
	assert.Equal(t, "oauth", tokenTypeFromPrefix("gho_"))
	assert.Equal(t, "user-to-server", tokenTypeFromPrefix("ghu_"))
	assert.Equal(t, "refresh", tokenTypeFromPrefix("ghr_"))
	assert.Equal(t, "fine-grained", tokenTypeFromPrefix("gith"))
	assert.Equal(t, "unknown", tokenTypeFromPrefix("xxxx"))
}

func TestParseTokenProbe(t *testing.T) {
	t.Parallel()
	pref, n, status := parseTokenProbe("TOKEN_PREFIX ghs_\nTOKEN_LEN 40\nOK\n")
	assert.Equal(t, "ghs_", pref)
	assert.Equal(t, 40, n)
	assert.Equal(t, "OK", status)

	pref, n, status = parseTokenProbe("NOTOKEN\n")
	assert.Empty(t, pref)
	assert.Zero(t, n)
	assert.Equal(t, "NOTOKEN", status)

	pref, n, status = parseTokenProbe("NOGH\n")
	assert.Equal(t, "NOGH", status)

	_, n, _ = parseTokenProbe("TOKEN_PREFIX ghs_SECRETOKENVALUE\nTOKEN_LEN notanumber\nOK")
	assert.Zero(t, n)
	pref, _, _ = parseTokenProbe("TOKEN_PREFIX ghs_SECRETOKENVALUE\nOK")
	assert.Equal(t, "ghs_", pref)
}

func TestClassifyGitHubAPIFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		kind githubFailKind
	}{
		{"HTTP 401: Bad credentials", githubFailAuth},
		{"401 Unauthorized", githubFailAuth},
		{"Requires authentication", githubFailAuth},
		{"Resource not accessible by integration", githubFailAuth},
		{"Could not resolve host: api.github.com", githubFailDNS},
		{"Name or service not known", githubFailDNS},
		{"Connection refused", githubFailConnection},
		{"Connection timed out", githubFailConnection},
		{"Network is unreachable", githubFailConnection},
		{"Received HTTP code 403 from proxy after CONNECT", githubFailProxyCONNECT},
		{"Tunnel connection failed: 403 Forbidden", githubFailProxyCONNECT},
		{"CONNECT tunnel to api.github.com failed", githubFailProxyCONNECT},
		{"proxy returned 403", githubFailProxyCONNECT},
		{"HTTP 403 Forbidden", githubFailForbidden},
		{"something else entirely", githubFailUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.kind, classifyGitHubAPIFailure(tc.in))
		})
	}
}

func TestDiagnoseGitHubAPIFailure(t *testing.T) {
	t.Parallel()
	err := diagnoseGitHubAPIFailure(preflightCheckConnect, "CONNECT_FAIL 403", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proxy allowlist")

	err = diagnoseGitHubAPIFailure(preflightCheckConnect, "Connection refused", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection failed")

	err = diagnoseGitHubAPIFailure(preflightCheckConnect, "Could not resolve host: api.github.com", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DNS resolution failed")

	err = diagnoseGitHubAPIFailure(preflightCheckGraphQL, "HTTP 403 Forbidden", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GraphQL")
	assert.Contains(t, err.Error(), "POST /graphql")

	// A GraphQL-stage failure that isn't evidence of an HTTP/proxy block
	// (a plain exec timeout, or a DNS failure) must not be reported as the
	// "proxy allows REST but blocks GraphQL" L7 diagnosis — that's the same
	// misdiagnosis already fixed for the CONNECT stage above.
	err = diagnoseGitHubAPIFailure(preflightCheckGraphQL, "command timed out after 30s", -1)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "REST GET /rate_limit succeeded but GraphQL POST /graphql failed")
	assert.Contains(t, err.Error(), "GitHub API connectivity check failed")

	err = diagnoseGitHubAPIFailure(preflightCheckGraphQL, "Could not resolve host: api.github.com", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DNS resolution failed")
	assert.NotContains(t, err.Error(), "REST GET /rate_limit succeeded but GraphQL POST /graphql failed")

	err = diagnoseGitHubAPIFailure(preflightCheckREST, "HTTP 401: Bad credentials", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
	assert.NotContains(t, err.Error(), "proxy allowlist")

	err = diagnoseGitHubAPIFailure(preflightCheckREST, "Could not resolve host: api.github.com", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DNS resolution failed")

	err = diagnoseGitHubAPIFailure(preflightCheckREST, "Connection refused", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection failed")

	err = diagnoseGitHubAPIFailure(preflightCheckREST, "Received HTTP code 403 from proxy after CONNECT", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proxy allowlist")

	err = diagnoseGitHubAPIFailure(preflightCheckREST, "HTTP 403 Forbidden", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after proxy CONNECT succeeded")

	err = diagnoseGitHubAPIFailure(preflightCheckREST, "weird failure", 7)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit 7")
	assert.Contains(t, err.Error(), "weird failure")

	err = diagnoseGitHubAPIFailure(preflightCheckREST, "", 9)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit 9")
}

func TestRecordGitHubTokenMint(t *testing.T) {
	recordGitHubTokenMint("", time.Time{})
	t.Cleanup(func() { recordGitHubTokenMint("", time.Time{}) })

	expires, minted := recordedGitHubTokenMintCopy()
	assert.Empty(t, expires)
	assert.True(t, minted.IsZero())

	ts := time.Date(2026, 6, 15, 11, 0, 0, 0, time.UTC)
	recordGitHubTokenMint("2026-06-15T12:00:00Z", ts)
	expires, minted = recordedGitHubTokenMintCopy()
	assert.Equal(t, "2026-06-15T12:00:00Z", expires)
	assert.Equal(t, ts, minted)

	meta := enrichTokenMeta(&preflightTokenMeta{Prefix: "ghs_", Type: "installation", Length: 40})
	require.NotNil(t, meta)
	assert.Equal(t, "2026-06-15T12:00:00Z", meta.ExpiresAt)
	assert.Equal(t, "2026-06-15T11:00:00Z", meta.MintedAt)

	nilMeta := enrichTokenMeta(nil)
	require.NotNil(t, nilMeta)
	assert.Equal(t, "2026-06-15T12:00:00Z", nilMeta.ExpiresAt)
	assert.Equal(t, "2026-06-15T11:00:00Z", nilMeta.MintedAt)

	recordGitHubTokenMint("", time.Time{})
	assert.Nil(t, enrichTokenMeta(&preflightTokenMeta{}))
}

func TestLogGitHubPreflight(t *testing.T) {
	t.Parallel()
	logGitHubPreflight(nil, &preflightGitHubResult{Outcome: preflightOutcomePass})
	var buf bytes.Buffer
	p := ui.New(&buf)
	logGitHubPreflight(p, nil)
	assert.Empty(t, buf.String())

	logGitHubPreflight(p, &preflightGitHubResult{
		Outcome: preflightOutcomePass,
		Token: &preflightTokenMeta{
			Prefix:    "ghs_",
			Type:      "installation",
			ExpiresAt: "2026-06-15T12:00:00Z",
			MintedAt:  "2026-06-15T11:00:00Z",
		},
	})
	out := buf.String()
	assert.Contains(t, out, "ghs_")
	assert.Contains(t, out, "installation")
	assert.Contains(t, out, "2026-06-15T12:00:00Z")
	assert.Contains(t, out, "2026-06-15T11:00:00Z")
	assert.Contains(t, out, preflightResultsPath)
	assert.NotContains(t, out, "ghs_secret")

	buf.Reset()
	logGitHubPreflight(p, &preflightGitHubResult{PersistError: "disk full"})
	assert.Contains(t, buf.String(), "Could not write preflight results")
	assert.Contains(t, buf.String(), "disk full")
}

type preflightExecResp struct {
	stdout, stderr string
	exit           int
	err            error
}

func stubPreflightExec(t *testing.T, resp map[string]preflightExecResp) sandboxExecFunc {
	t.Helper()
	return func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		key := preflightExecKey(cmd)
		r, ok := resp[key]
		if !ok {
			t.Fatalf("unexpected preflight command %q (key %q)", cmd, key)
		}
		return r.stdout, r.stderr, r.exit, r.err
	}
}

func preflightExecKey(cmd string) string {
	switch {
	case strings.Contains(cmd, "TOKEN_PREFIX"):
		return "probe"
	case strings.Contains(cmd, "require(\"net\")") || strings.Contains(cmd, "CONNECT_SKIP"):
		return "connect"
	case strings.Contains(cmd, "gh api graphql"):
		return "graphql"
	case strings.Contains(cmd, "gh api "+githubPreflightRESTEndpoint):
		return "rest"
	default:
		return "other"
	}
}

func successPreflightResps() map[string]preflightExecResp {
	return map[string]preflightExecResp{
		"probe":   {stdout: "TOKEN_PREFIX ghs_\nTOKEN_LEN 40\nOK\n"},
		"connect": {stdout: "CONNECT_OK HTTP/1.1 200 Connection Established\n"},
		"rest":    {stdout: `{"rate":{}}`},
		"graphql": {stdout: `{"data":{"rateLimit":{"remaining":5000}}}`},
	}
}

func TestCheckSandboxGitHubConnectivity_Success(t *testing.T) {
	recordGitHubTokenMint("2026-06-15T12:00:00Z", time.Date(2026, 6, 15, 11, 0, 0, 0, time.UTC))
	t.Cleanup(func() { recordGitHubTokenMint("", time.Time{}) })
	preflightClock = func() time.Time { return time.Date(2026, 6, 15, 11, 30, 0, 0, time.UTC) }
	t.Cleanup(func() { preflightClock = func() time.Time { return time.Now().UTC() } })

	var persisted *preflightGitHubResult
	result, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, successPreflightResps()),
		func(_ string, r *preflightGitHubResult) error {
			persisted = r
			return nil
		})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Skipped)
	assert.Equal(t, preflightOutcomePass, result.Outcome)
	require.NotNil(t, result.Token)
	assert.Equal(t, "ghs_", result.Token.Prefix)
	assert.Equal(t, "installation", result.Token.Type)
	assert.Equal(t, 40, result.Token.Length)
	assert.Equal(t, "2026-06-15T12:00:00Z", result.Token.ExpiresAt)
	assert.Equal(t, "2026-06-15T11:00:00Z", result.Token.MintedAt)
	assert.Equal(t, "2026-06-15T11:30:00Z", result.CheckedAt)
	assert.True(t, result.Checks[preflightCheckConnect].OK)
	assert.True(t, result.Checks[preflightCheckREST].OK)
	assert.True(t, result.Checks[preflightCheckGraphQL].OK)
	require.NotNil(t, persisted)
	assert.Equal(t, preflightOutcomePass, persisted.Outcome)

	js, marshalErr := json.Marshal(result)
	require.NoError(t, marshalErr)
	assert.NotContains(t, string(js), "ghs_secret")
	assert.NotContains(t, string(js), "GH_TOKEN=")
}

func TestCheckSandboxGitHubConnectivity_SkipNoToken(t *testing.T) {
	resp := map[string]preflightExecResp{"probe": {stdout: "NOTOKEN\n"}}
	var persisted *preflightGitHubResult
	result, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, resp),
		func(_ string, r *preflightGitHubResult) error {
			persisted = r
			return nil
		})
	require.NoError(t, err)
	assert.True(t, result.Skipped)
	assert.Equal(t, preflightOutcomeSkip, result.Outcome)
	assert.Equal(t, "GH_TOKEN not set in sandbox", result.SkipReason)
	require.NotNil(t, persisted)
}

func TestCheckSandboxGitHubConnectivity_SkipNoGh(t *testing.T) {
	resp := map[string]preflightExecResp{"probe": {stdout: "NOGH\n"}}
	result, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, resp), nil)
	require.NoError(t, err)
	assert.True(t, result.Skipped)
	assert.Equal(t, "gh CLI not available in sandbox", result.SkipReason)
}

func TestCheckSandboxGitHubConnectivity_SkipProbeError(t *testing.T) {
	resp := map[string]preflightExecResp{"probe": {err: fmt.Errorf("openshell exec failed to start")}}
	result, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, resp), nil)
	require.NoError(t, err)
	assert.True(t, result.Skipped)
	assert.Contains(t, result.SkipReason, "probe command failed")
}

func TestCheckSandboxGitHubConnectivity_SkipProbeNonZero(t *testing.T) {
	resp := map[string]preflightExecResp{"probe": {stdout: "oops", exit: 1}}
	result, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, resp), nil)
	require.NoError(t, err)
	assert.True(t, result.Skipped)
	assert.Equal(t, "GH_TOKEN not set in sandbox", result.SkipReason)
}

func TestCheckSandboxGitHubConnectivity_SanitizesPersistedError(t *testing.T) {
	// result.Error and each Checks[...].Detail are raw combined stdout+stderr
	// from sandbox-run gh/node commands (#4016 follow-up). Before this fix
	// neither was redacted or length-capped before being written to
	// preflightResultsPath, unlike the equivalent PR/issue status-comment
	// path (sanitizeDetail). Use a fake-but-pattern-matching GitHub PAT to
	// verify redaction, and an oversized blob to verify the length cap.
	fakeToken := "ghp_" + strings.Repeat("a", 40)
	resp := successPreflightResps()
	resp["connect"] = preflightExecResp{
		stdout: "CONNECT_FAIL HTTP/1.1 403 Forbidden token=" + fakeToken + " " + strings.Repeat("x", maxPreflightTextLen+500),
		exit:   1,
	}
	var persisted *preflightGitHubResult
	result, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, resp),
		func(_ string, r *preflightGitHubResult) error {
			persisted = r
			return nil
		})
	require.Error(t, err)
	require.NotNil(t, persisted)

	// The error returned to the caller is not mangled by persistence-time
	// sanitization.
	assert.Contains(t, err.Error(), fakeToken)

	assert.NotContains(t, result.Error, fakeToken)
	assert.NotContains(t, result.Checks[preflightCheckConnect].Detail, fakeToken)
	assert.LessOrEqual(t, len(result.Error), maxPreflightTextLen+len("... [truncated]"))
	assert.LessOrEqual(t, len(result.Checks[preflightCheckConnect].Detail), maxPreflightTextLen+len("... [truncated]"))

	js, marshalErr := json.Marshal(persisted)
	require.NoError(t, marshalErr)
	assert.NotContains(t, string(js), fakeToken)
}

func TestSanitizePreflightText(t *testing.T) {
	t.Parallel()
	assert.Empty(t, sanitizePreflightText(""))

	fakeToken := "ghp_" + strings.Repeat("b", 40)
	assert.NotContains(t, sanitizePreflightText("leaked "+fakeToken+" here"), fakeToken)

	long := strings.Repeat("z", maxPreflightTextLen+100)
	out := sanitizePreflightText(long)
	assert.LessOrEqual(t, len(out), maxPreflightTextLen+len("... [truncated]"))
	assert.Contains(t, out, "[truncated]")

	assert.Equal(t, "short detail", sanitizePreflightText("short detail"))
}

func TestCheckSandboxGitHubConnectivity_ConnectBlocked(t *testing.T) {
	resp := successPreflightResps()
	resp["connect"] = preflightExecResp{
		stdout: "CONNECT_FAIL HTTP/1.1 403 Forbidden",
		exit:   1,
	}
	result, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, resp),
		func(string, *preflightGitHubResult) error { return nil })
	require.Error(t, err)
	assert.Equal(t, preflightOutcomeFail, result.Outcome)
	assert.False(t, result.Checks[preflightCheckConnect].OK)
	assert.Contains(t, err.Error(), "proxy allowlist")
	assert.Contains(t, result.Error, "proxy allowlist")
	_, restRan := result.Checks[preflightCheckREST]
	assert.False(t, restRan, "REST must not run after CONNECT failure")
}

func TestCheckSandboxGitHubConnectivity_ConnectExecError(t *testing.T) {
	// A plain exec error (e.g. an exec-layer timeout) does not classify as a
	// proxy CONNECT/forbidden failure, so it must fall through to the generic
	// diagnostic rather than being reported as a proxy-allowlist 403.
	resp := successPreflightResps()
	resp["connect"] = preflightExecResp{err: fmt.Errorf("command timed out after 30s")}
	result, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, resp), nil)
	require.Error(t, err)
	assert.False(t, result.Checks[preflightCheckConnect].OK)
	assert.NotContains(t, err.Error(), "proxy allowlist")
	assert.Contains(t, err.Error(), "GitHub API connectivity check failed")
}

func TestCheckSandboxGitHubConnectivity_ConnectSkipNoNode(t *testing.T) {
	resp := successPreflightResps()
	resp["connect"] = preflightExecResp{stdout: "CONNECT_SKIP nonode\n"}
	result, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, resp), nil)
	require.NoError(t, err)
	assert.False(t, result.Checks[preflightCheckConnect].OK, "a skipped probe never ran and must not be reported as a pass")
	assert.Contains(t, result.Checks[preflightCheckConnect].Detail, "skipped")
	assert.True(t, result.Checks[preflightCheckREST].OK)
	assert.True(t, result.Checks[preflightCheckGraphQL].OK)
}

func TestCheckSandboxGitHubConnectivity_RESTUnauthorized(t *testing.T) {
	resp := successPreflightResps()
	resp["rest"] = preflightExecResp{stdout: "HTTP 401: Bad credentials", exit: 1}
	result, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, resp), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication failed")
	assert.NotContains(t, err.Error(), "proxy allowlist")
	assert.True(t, result.Checks[preflightCheckConnect].OK)
	assert.False(t, result.Checks[preflightCheckREST].OK)
	_, graphqlRan := result.Checks[preflightCheckGraphQL]
	assert.False(t, graphqlRan, "GraphQL must not run after REST auth failure")
}

func TestCheckSandboxGitHubConnectivity_RESTDNS(t *testing.T) {
	resp := successPreflightResps()
	resp["rest"] = preflightExecResp{stdout: "Could not resolve host: api.github.com", exit: 1}
	_, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, resp), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DNS resolution failed")
}

func TestCheckSandboxGitHubConnectivity_RESTExecError(t *testing.T) {
	resp := successPreflightResps()
	resp["rest"] = preflightExecResp{err: fmt.Errorf("command timed out after 30s")}
	result, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, resp), nil)
	require.Error(t, err)
	assert.False(t, result.Checks[preflightCheckREST].OK)
}

func TestCheckSandboxGitHubConnectivity_GraphQLBlocked(t *testing.T) {
	resp := successPreflightResps()
	resp["graphql"] = preflightExecResp{
		stdout: "HTTP 403: Forbidden from L7 policy",
		exit:   1,
	}
	result, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, resp),
		func(string, *preflightGitHubResult) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GraphQL")
	assert.Contains(t, err.Error(), "POST /graphql")
	assert.True(t, result.Checks[preflightCheckConnect].OK)
	assert.True(t, result.Checks[preflightCheckREST].OK)
	assert.False(t, result.Checks[preflightCheckGraphQL].OK)
}

func TestCheckSandboxGitHubConnectivity_GraphQLExecError(t *testing.T) {
	// A plain exec error (e.g. an exec-layer timeout) is not evidence that
	// the proxy allows REST but blocks GraphQL, so it must fall through to
	// the generic diagnostic rather than the GraphQL-specific L7 message.
	resp := successPreflightResps()
	resp["graphql"] = preflightExecResp{err: fmt.Errorf("command timed out after 30s")}
	result, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, resp), nil)
	require.Error(t, err)
	assert.False(t, result.Checks[preflightCheckGraphQL].OK)
	assert.NotContains(t, err.Error(), "REST GET /rate_limit succeeded but GraphQL POST /graphql failed")
	assert.Contains(t, err.Error(), "GitHub API connectivity check failed")
}

func TestCheckSandboxGitHubConnectivity_PersistErrorOnPass(t *testing.T) {
	result, err := checkSandboxGitHubConnectivityWith("sb", stubPreflightExec(t, successPreflightResps()),
		func(string, *preflightGitHubResult) error {
			return fmt.Errorf("upload failed")
		})
	require.NoError(t, err)
	assert.Equal(t, preflightOutcomePass, result.Outcome)
	assert.Equal(t, "upload failed", result.PersistError)
}

func TestCheckSandboxGitHubConnectivity_WrapperSkip(t *testing.T) {
	orig := sandboxExec
	t.Cleanup(func() { sandboxExec = orig })
	sandboxExec = func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		if strings.Contains(cmd, "TOKEN_PREFIX") {
			return "NOTOKEN\n", "", 0, nil
		}
		return "", "", 0, nil
	}
	result, err := checkSandboxGitHubConnectivity("sb")
	require.NoError(t, err)
	assert.True(t, result.Skipped)
	assert.Equal(t, preflightOutcomeSkip, result.Outcome)
}

func TestPersistPreflightGitHubResult(t *testing.T) {
	doc := &preflightGitHubResult{
		Outcome:   preflightOutcomePass,
		CheckedAt: "2026-06-15T11:30:00Z",
		Token:     &preflightTokenMeta{Prefix: "ghs_", Type: "installation", Length: 40},
	}
	var gotCmd string
	execFn := func(_ string, cmd string, _ time.Duration) (string, string, int, error) {
		gotCmd = cmd
		return "", "", 0, nil
	}
	require.NoError(t, persistPreflightGitHubResult(execFn)("sb", doc))
	assert.Contains(t, gotCmd, preflightResultsPath)
	assert.Contains(t, gotCmd, "base64 -d")

	decoded := decodePersistPayload(t, gotCmd)
	var got preflightGitHubResult
	require.NoError(t, json.Unmarshal(decoded, &got))
	assert.Equal(t, preflightOutcomePass, got.Outcome)
	require.NotNil(t, got.Token)
	assert.Equal(t, "ghs_", got.Token.Prefix)
	assert.NotContains(t, string(decoded), "ghs_secretvalue")

	execFn = func(string, string, time.Duration) (string, string, int, error) {
		return "", "", 0, fmt.Errorf("exec failed")
	}
	err := persistPreflightGitHubResult(execFn)("sb", doc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exec failed")

	execFn = func(string, string, time.Duration) (string, string, int, error) {
		return "", "permission denied", 1, nil
	}
	err = persistPreflightGitHubResult(execFn)("sb", doc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")

	execFn = func(string, string, time.Duration) (string, string, int, error) {
		return "", "", 2, nil
	}
	err = persistPreflightGitHubResult(execFn)("sb", doc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit 2")
}

// TestGithubConnectScript_StatusLineParsing runs the actual node CONNECT
// probe script (not a Go re-implementation of its logic) against a fake
// local proxy, to lock in that status-code matching is a proper 3-digit
// "2xx" check rather than a leading-"2" substring match, and that the
// pass/fail decision waits for a complete CRLF-terminated status line
// instead of deciding off the first, possibly-partial, data event.
func TestGithubConnectScript_StatusLineParsing(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}

	cases := []struct {
		name    string
		respond func(conn net.Conn)
		wantOK  bool
	}{
		{
			name: "200 is a pass",
			respond: func(conn net.Conn) {
				_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
			},
			wantOK: true,
		},
		{
			name: "403 is a fail",
			respond: func(conn net.Conn) {
				_, _ = conn.Write([]byte("HTTP/1.1 403 Forbidden\r\n\r\n"))
			},
			wantOK: false,
		},
		{
			name: "500 is a fail",
			respond: func(conn net.Conn) {
				_, _ = conn.Write([]byte("HTTP/1.1 500 Internal Server Error\r\n\r\n"))
			},
			wantOK: false,
		},
		{
			name: "a truncated leading 2 with no complete status line is a fail",
			respond: func(conn net.Conn) {
				// No CRLF is ever sent, so a leading "2" byte must not be
				// mistaken for a 2xx status the way the old
				// code.indexOf("2")===0 check would.
				_, _ = conn.Write([]byte("HTTP/1.1 2"))
				_ = conn.Close()
			},
			wantOK: false,
		},
		{
			name: "a status line split across TCP segments still resolves",
			respond: func(conn net.Conn) {
				_, _ = conn.Write([]byte("HTTP/1.1 2"))
				time.Sleep(20 * time.Millisecond)
				_, _ = conn.Write([]byte("00 Connection Established\r\n\r\n"))
			},
			wantOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer ln.Close()

			accepted := make(chan struct{})
			go func() {
				defer close(accepted)
				conn, acceptErr := ln.Accept()
				if acceptErr != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				buf := make([]byte, 4096)
				for {
					n, readErr := conn.Read(buf)
					if readErr != nil {
						return
					}
					if bytes.Contains(buf[:n], []byte("\r\n\r\n")) {
						break
					}
				}
				tc.respond(conn)
			}()

			cmd := exec.Command("node", "-e", githubConnectScript)
			cmd.Env = append(cmd.Environ(), "HTTPS_PROXY=http://"+ln.Addr().String())
			out, _ := cmd.CombinedOutput()
			<-accepted

			if tc.wantOK {
				assert.Contains(t, string(out), "CONNECT_OK", "output: %s", out)
			} else {
				assert.Contains(t, string(out), "CONNECT_FAIL", "output: %s", out)
			}
		})
	}
}

func TestGitHubPreflightCommands(t *testing.T) {
	t.Parallel()
	env := "/sandbox/workspace/.env"
	assert.Contains(t, githubPreflightProbeCmd(env), env)
	assert.Contains(t, githubPreflightProbeCmd(env), "TOKEN_PREFIX")
	assert.Contains(t, githubPreflightConnectCmd(env), "node -e")
	assert.Contains(t, githubPreflightConnectCmd(env), "require(\"net\")")
	assert.Contains(t, githubPreflightConnectCmd(env), "CONNECT_SKIP nonode")
	assert.Contains(t, githubPreflightRESTCmd(env), "gh api "+githubPreflightRESTEndpoint)
	assert.Contains(t, githubPreflightGraphQLCmd(env), "gh api graphql")
	assert.Contains(t, githubPreflightGraphQLCmd(env), githubPreflightGraphQLQuery)
	assert.NotContains(t, githubPreflightProbeCmd(env), "gh api "+githubPreflightRESTEndpoint,
		"probe must not conflate token presence with REST reachability")
}

func decodePersistPayload(t *testing.T, cmd string) []byte {
	t.Helper()
	const prefix = "printf '%s' '"
	start := strings.Index(cmd, prefix)
	require.GreaterOrEqual(t, start, 0, cmd)
	rest := cmd[start+len(prefix):]
	end := strings.Index(rest, "'")
	require.GreaterOrEqual(t, end, 0, cmd)
	decoded, err := base64.StdEncoding.DecodeString(rest[:end])
	require.NoError(t, err)
	return decoded
}
