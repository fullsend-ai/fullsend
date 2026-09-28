package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	// preflightGitHubTimeout is the maximum time to wait for a single GitHub
	// API connectivity probe inside the sandbox. Short because this is a
	// fast pre-flight — if the proxy is blocking, the connection attempt
	// fails quickly (HTTP 403 on CONNECT).
	preflightGitHubTimeout = 30 * time.Second

	// preflightResultsPath is the sandbox-side file that records the
	// preflight outcome so the agent and post-run retro analysis can see
	// what the harness determined. See #4016.
	preflightResultsPath = sandbox.SandboxWorkspace + "/.preflight-results.json"

	preflightOutcomePass = "pass"
	preflightOutcomeSkip = "skip"
	preflightOutcomeFail = "fail"

	preflightCheckConnect = "proxy_connect"
	preflightCheckREST    = "rest_rate_limit"
	preflightCheckGraphQL = "graphql"

	githubPreflightRESTEndpoint   = "/rate_limit"
	githubPreflightGraphQLQuery   = "query { rateLimit { remaining } }"
	githubPreflightTokenPrefixLen = 4
)

// githubConnectScript is a raw CONNECT probe: it talks to the HTTPS proxy
// (or the origin, when no proxy is set) without issuing an HTTP method or
// path. REST GET /rate_limit can succeed while POST /graphql is blocked
// (#4016), so the CONNECT probe must not itself be a REST GET.
//
// It runs under `node`, not `python3`: the sandbox's GitHub egress profile
// allowlists connections opened by `gh` and `node` (see
// profiles/fullsend-github-ro.yaml `binaries:`), and OpenShell's OPA policy
// denies a raw socket opened by any other binary regardless of the target
// host — including python3. A python3-based probe is blocked by that binary
// check before it ever exercises the proxy, so it always reports a false
// "proxy allowlist" failure in a correctly locked-down sandbox.
const githubConnectScript = `const net=require("net");
const host="api.github.com",port=443;
const proxy=process.env.HTTPS_PROXY||process.env.https_proxy||process.env.HTTP_PROXY||process.env.http_proxy;
let decided=false;
function done(ok,msg){if(decided){return;}decided=true;console.log(ok?"CONNECT_OK":"CONNECT_FAIL",msg||"");process.exit(ok?0:1);}
try{
  if(!proxy){
    const s=net.connect({host:host,port:port,timeout:10000});
    s.on("connect",function(){s.destroy();done(true,"direct");});
    s.on("timeout",function(){s.destroy();done(false,"timeout");});
    s.on("error",function(e){done(false,String(e&&e.message||e));});
  } else {
    const u=new URL(proxy);
    const phost=u.hostname,pport=Number(u.port)||80;
    const s=net.connect({host:phost,port:pport,timeout:10000});
    let buf="";
    s.on("connect",function(){s.write("CONNECT "+host+":"+port+" HTTP/1.1\r\nHost: "+host+":"+port+"\r\n\r\n");});
    s.on("data",function(chunk){
      buf+=chunk.toString("latin1");
      const idx=buf.indexOf("\r\n");
      if(idx<0){return;}
      const line=buf.slice(0,idx);
      s.destroy();
      const parts=line.split(" ");
      const code=parts[1]||"";
      done(/^2\d\d$/.test(code),line);
    });
    s.on("timeout",function(){s.destroy();done(false,"timeout");});
    s.on("error",function(e){done(false,String(e&&e.message||e));});
    s.on("close",function(){done(false,"connection closed before a complete status line: "+JSON.stringify(buf));});
  }
}catch(e){done(false,String(e&&e.message||e));}`

// sandboxExec is the sandbox exec used by the GitHub preflight. Tests
// substitute a fake.
var sandboxExec = sandbox.Exec

// preflightClock is UTC "now" for checked_at timestamps. Tests substitute
// a fixed time.
var preflightClock = func() time.Time { return time.Now().UTC() }

// githubFailKind classifies a failed GitHub probe so CONNECT-blocked
// failures are not reported as invalid-token failures and vice versa.
type githubFailKind int

const (
	githubFailUnknown githubFailKind = iota
	githubFailAuth
	githubFailDNS
	githubFailConnection
	githubFailProxyCONNECT
	githubFailForbidden
)

// preflightGitHubResult captures the outcome of a sandbox-side GitHub API
// connectivity check. It is also the document written to
// preflightResultsPath.
type preflightGitHubResult struct {
	// Outcome is pass, skip, or fail.
	Outcome string `json:"outcome"`
	// Skipped is true when the check could not run (e.g., GH_TOKEN not set
	// or gh not on PATH inside the sandbox).
	Skipped bool `json:"skipped"`
	// SkipReason explains why the check was skipped.
	SkipReason string `json:"skip_reason,omitempty"`
	// CheckedAt is an RFC3339 UTC timestamp for when the check ran.
	CheckedAt string `json:"checked_at"`
	// Token holds non-secret metadata about GH_TOKEN. Never includes the
	// token value.
	Token *preflightTokenMeta `json:"token,omitempty"`
	// Checks records per-stage results (proxy CONNECT, REST, GraphQL).
	Checks map[string]preflightCheckResult `json:"checks,omitempty"`
	// Error is the fatal diagnostic when Outcome is fail.
	Error string `json:"error,omitempty"`
	// PersistError is set when the results file could not be written. It
	// is not serialized into that file.
	PersistError string `json:"-"`
}

// preflightTokenMeta is non-secret metadata about the sandbox GH_TOKEN.
type preflightTokenMeta struct {
	Prefix    string `json:"prefix"`
	Length    int    `json:"length,omitempty"`
	Type      string `json:"type,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	MintedAt  string `json:"minted_at,omitempty"`
}

// preflightCheckResult is the outcome of one preflight stage.
type preflightCheckResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type githubTokenMintMeta struct {
	mu        sync.Mutex
	expiresAt string
	mintedAt  time.Time
}

// recordedGitHubTokenMint holds expiry/mint time from the most recent
// successful mintAgentTokenAtLevel call so the preflight can log them
// without exposing the token.
var recordedGitHubTokenMint githubTokenMintMeta

func recordGitHubTokenMint(expiresAt string, mintedAt time.Time) {
	recordedGitHubTokenMint.mu.Lock()
	defer recordedGitHubTokenMint.mu.Unlock()
	recordedGitHubTokenMint.expiresAt = expiresAt
	recordedGitHubTokenMint.mintedAt = mintedAt
}

func recordedGitHubTokenMintCopy() (expiresAt string, mintedAt time.Time) {
	recordedGitHubTokenMint.mu.Lock()
	defer recordedGitHubTokenMint.mu.Unlock()
	return recordedGitHubTokenMint.expiresAt, recordedGitHubTokenMint.mintedAt
}

type persistPreflightFunc func(sandboxName string, result *preflightGitHubResult) error

// checkSandboxGitHubConnectivity runs a GitHub API check inside the sandbox
// to verify that api.github.com is reachable through the proxy and that
// GH_TOKEN is valid.
//
// The check sources the sandbox .env file (to pick up GH_TOKEN and PATH)
// and then runs three probes:
//  1. a raw HTTPS CONNECT to api.github.com:443 (proxy reachability)
//  2. an authenticated REST GET to /rate_limit (token validity)
//  3. an authenticated GraphQL query (the path gh CLI uses for most reads)
//
// REST GET /rate_limit alone cannot detect an L7 proxy that allows GET but
// blocks POST /graphql — the failure mode that let retro agents start with
// no usable GitHub API (#4016, originally reported via #2143).
//
// Returns a non-nil error when any required probe fails. Returns a nil
// error with Skipped=true when the check cannot run (no GH_TOKEN or no gh
// binary). The result is always non-nil so callers can log token metadata
// and inspect PersistError even on failure. Callers should treat a non-nil
// error as fatal — the agent will waste its entire timeout retrying doomed
// API calls.
func checkSandboxGitHubConnectivity(sandboxName string) (*preflightGitHubResult, error) {
	return checkSandboxGitHubConnectivityWith(
		sandboxName,
		sandboxExec,
		persistPreflightGitHubResult(sandboxExec),
	)
}

func checkSandboxGitHubConnectivityWith(
	sandboxName string,
	execFn sandboxExecFunc,
	persist persistPreflightFunc,
) (*preflightGitHubResult, error) {
	result := &preflightGitHubResult{
		CheckedAt: preflightClock().Format(time.RFC3339),
		Checks:    map[string]preflightCheckResult{},
	}
	envFile := sandbox.SandboxWorkspace + "/.env"

	finish := func(err error) (*preflightGitHubResult, error) {
		if err != nil {
			result.Outcome = preflightOutcomeFail
			result.Error = sanitizePreflightText(err.Error())
		} else if result.Skipped {
			result.Outcome = preflightOutcomeSkip
		} else {
			result.Outcome = preflightOutcomePass
		}
		// The check details above are raw combined stdout+stderr from the
		// sandbox's gh/node probe commands (#4016 follow-up). Unlike the
		// PR/issue status comment path (sanitizeDetail in
		// internal/statuscomment), this is a new persisted file with no
		// existing length cap or secret redaction, so apply the same class
		// of protection here before it is written to disk.
		for name, check := range result.Checks {
			check.Detail = sanitizePreflightText(check.Detail)
			result.Checks[name] = check
		}
		if persist != nil {
			if perr := persist(sandboxName, result); perr != nil {
				result.PersistError = perr.Error()
			}
		}
		return result, err
	}

	probeCmd := githubPreflightProbeCmd(envFile)
	stdout, stderr, exitCode, err := execFn(sandboxName, probeCmd, 10*time.Second)
	if err != nil {
		result.Skipped = true
		result.SkipReason = "probe command failed: " + err.Error()
		return finish(nil)
	}
	prefix, length, status := parseTokenProbe(stdout)
	switch {
	case exitCode != 0 || status == "NOTOKEN" || status == "":
		result.Skipped = true
		result.SkipReason = "GH_TOKEN not set in sandbox"
		return finish(nil)
	case status == "NOGH":
		result.Skipped = true
		result.SkipReason = "gh CLI not available in sandbox"
		return finish(nil)
	}

	result.Token = enrichTokenMeta(&preflightTokenMeta{
		Prefix: sanitizeTokenPrefix(prefix),
		Length: length,
		Type:   tokenTypeFromPrefix(sanitizeTokenPrefix(prefix)),
	})

	connectCmd := githubPreflightConnectCmd(envFile)
	stdout, stderr, exitCode, err = execFn(sandboxName, connectCmd, preflightGitHubTimeout)
	output := combinedOutput(stdout, stderr)
	switch {
	case err != nil:
		result.Checks[preflightCheckConnect] = preflightCheckResult{OK: false, Detail: err.Error()}
		return finish(diagnoseGitHubAPIFailure(preflightCheckConnect, err.Error(), -1))
	case strings.HasPrefix(strings.TrimSpace(stdout), "CONNECT_SKIP"):
		// The probe never ran (e.g., no node on PATH), so this is not a pass —
		// report OK:false so a consumer branching only on Checks[...].OK does
		// not conclude the CONNECT stage succeeded. Detail still explains why.
		result.Checks[preflightCheckConnect] = preflightCheckResult{
			OK:     false,
			Detail: "skipped: " + strings.TrimSpace(output),
		}
	case exitCode != 0 || strings.Contains(output, "CONNECT_FAIL"):
		result.Checks[preflightCheckConnect] = preflightCheckResult{OK: false, Detail: strings.TrimSpace(output)}
		return finish(diagnoseGitHubAPIFailure(preflightCheckConnect, output, exitCode))
	default:
		result.Checks[preflightCheckConnect] = preflightCheckResult{OK: true, Detail: strings.TrimSpace(output)}
	}

	restCmd := githubPreflightRESTCmd(envFile)
	stdout, stderr, exitCode, err = execFn(sandboxName, restCmd, preflightGitHubTimeout)
	output = combinedOutput(stdout, stderr)
	if err != nil {
		result.Checks[preflightCheckREST] = preflightCheckResult{OK: false, Detail: err.Error()}
		return finish(diagnoseGitHubAPIFailure(preflightCheckREST, err.Error(), -1))
	}
	if exitCode != 0 {
		result.Checks[preflightCheckREST] = preflightCheckResult{OK: false, Detail: strings.TrimSpace(output)}
		return finish(diagnoseGitHubAPIFailure(preflightCheckREST, output, exitCode))
	}
	result.Checks[preflightCheckREST] = preflightCheckResult{OK: true, Detail: "gh api " + githubPreflightRESTEndpoint + " exit 0"}

	graphqlCmd := githubPreflightGraphQLCmd(envFile)
	stdout, stderr, exitCode, err = execFn(sandboxName, graphqlCmd, preflightGitHubTimeout)
	output = combinedOutput(stdout, stderr)
	if err != nil {
		result.Checks[preflightCheckGraphQL] = preflightCheckResult{OK: false, Detail: err.Error()}
		return finish(diagnoseGitHubAPIFailure(preflightCheckGraphQL, err.Error(), -1))
	}
	if exitCode != 0 {
		result.Checks[preflightCheckGraphQL] = preflightCheckResult{OK: false, Detail: strings.TrimSpace(output)}
		return finish(diagnoseGitHubAPIFailure(preflightCheckGraphQL, output, exitCode))
	}
	result.Checks[preflightCheckGraphQL] = preflightCheckResult{OK: true, Detail: "gh api graphql exit 0"}

	return finish(nil)
}

func githubPreflightProbeCmd(envFile string) string {
	return fmt.Sprintf(". %s 2>/dev/null; "+
		"if [ -z \"${GH_TOKEN:-}\" ]; then echo NOTOKEN; exit 0; fi; "+
		"if ! command -v gh >/dev/null 2>&1; then echo NOGH; exit 0; fi; "+
		"printf 'TOKEN_PREFIX %%s\\n' \"$(printf '%%s' \"${GH_TOKEN}\" | cut -c1-4)\"; "+
		"printf 'TOKEN_LEN %%s\\n' \"${#GH_TOKEN}\"; "+
		"echo OK", envFile)
}

func githubPreflightConnectCmd(envFile string) string {
	return fmt.Sprintf(". %s 2>/dev/null; "+
		"if ! command -v node >/dev/null 2>&1; then echo CONNECT_SKIP nonode; exit 0; fi; "+
		"node -e '%s'", envFile, githubConnectScript)
}

func githubPreflightRESTCmd(envFile string) string {
	return fmt.Sprintf(". %s 2>/dev/null && gh api %s 2>&1", envFile, githubPreflightRESTEndpoint)
}

func githubPreflightGraphQLCmd(envFile string) string {
	return fmt.Sprintf(". %s 2>/dev/null && gh api graphql -f query='%s' 2>&1",
		envFile, githubPreflightGraphQLQuery)
}

func githubPreflightPersistCmd(js []byte) string {
	b64 := base64.StdEncoding.EncodeToString(js)
	return fmt.Sprintf("printf '%%s' '%s' | base64 -d > %s", b64, preflightResultsPath)
}

func persistPreflightGitHubResult(execFn sandboxExecFunc) persistPreflightFunc {
	return func(sandboxName string, result *preflightGitHubResult) error {
		js, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling preflight results: %w", err)
		}
		_, stderr, exitCode, err := execFn(sandboxName, githubPreflightPersistCmd(js), 10*time.Second)
		if err != nil {
			return err
		}
		if exitCode != 0 {
			detail := strings.TrimSpace(stderr)
			if detail == "" {
				detail = fmt.Sprintf("exit %d", exitCode)
			}
			return fmt.Errorf("writing %s: %s", preflightResultsPath, detail)
		}
		return nil
	}
}

func parseTokenProbe(stdout string) (prefix string, length int, status string) {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "NOTOKEN" || line == "NOGH" || line == "OK":
			status = line
		case strings.HasPrefix(line, "TOKEN_PREFIX "):
			prefix = sanitizeTokenPrefix(strings.TrimSpace(strings.TrimPrefix(line, "TOKEN_PREFIX ")))
		case strings.HasPrefix(line, "TOKEN_LEN "):
			n, convErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "TOKEN_LEN ")))
			if convErr == nil && n > 0 {
				length = n
			}
		}
	}
	return prefix, length, status
}

func sanitizeTokenPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if len(prefix) > githubPreflightTokenPrefixLen {
		return prefix[:githubPreflightTokenPrefixLen]
	}
	return prefix
}

func tokenTypeFromPrefix(prefix string) string {
	switch prefix {
	case "ghs_":
		return "installation"
	case "ghp_":
		return "pat"
	case "gho_":
		return "oauth"
	case "ghu_":
		return "user-to-server"
	case "ghr_":
		return "refresh"
	case "gith":
		return "fine-grained"
	default:
		return "unknown"
	}
}

func enrichTokenMeta(meta *preflightTokenMeta) *preflightTokenMeta {
	if meta == nil {
		meta = &preflightTokenMeta{}
	}
	expiresAt, mintedAt := recordedGitHubTokenMintCopy()
	if expiresAt != "" {
		meta.ExpiresAt = expiresAt
	}
	if !mintedAt.IsZero() {
		meta.MintedAt = mintedAt.UTC().Format(time.RFC3339)
	}
	if meta.Prefix == "" && meta.Type == "" && meta.Length == 0 && meta.ExpiresAt == "" && meta.MintedAt == "" {
		return nil
	}
	return meta
}

func combinedOutput(stdout, stderr string) string {
	return strings.TrimSpace(stdout + "\n" + stderr)
}

// maxPreflightTextLen caps free-form probe output before it is persisted to
// preflightResultsPath. This file is diagnostic context for the agent and
// retro analyses (#4016), not a one-line status comment, so the cap is more
// generous than statuscomment's maxDetailLen — but it still needs a bound,
// since the underlying text is raw combined stdout+stderr from sandbox
// gh/node commands with no other size limit.
const maxPreflightTextLen = 4000

// sanitizePreflightText scrubs recognizable credentials out of probe-derived
// free text and caps its length before persisting it to
// preflightResultsPath. Mirrors the protection sanitizeDetail
// (internal/statuscomment) already applies on the PR/issue status-comment
// path, which this new results file otherwise lacks.
func sanitizePreflightText(s string) string {
	if s == "" {
		return s
	}
	if res := security.NewSecretRedactor().Scan(s); res.Sanitized != "" {
		s = res.Sanitized
	}
	if len(s) > maxPreflightTextLen {
		s = s[:maxPreflightTextLen] + "... [truncated]"
	}
	return s
}

func classifyGitHubAPIFailure(output string) githubFailKind {
	o := strings.ToLower(output)
	switch {
	case strings.Contains(o, "bad credentials"),
		strings.Contains(o, "http 401"),
		strings.Contains(o, "401 unauthorized"),
		strings.Contains(o, "requires authentication"),
		strings.Contains(o, "resource not accessible by integration"):
		return githubFailAuth
	case strings.Contains(o, "could not resolve host"),
		strings.Contains(o, "name or service not known"),
		strings.Contains(o, "nodename nor servname"):
		return githubFailDNS
	case strings.Contains(o, "connection refused"),
		strings.Contains(o, "connection timed out"),
		strings.Contains(o, "network is unreachable"):
		return githubFailConnection
	case strings.Contains(o, "from proxy after connect"),
		strings.Contains(o, "connect tunnel"),
		strings.Contains(o, "tunnel connection failed"),
		strings.Contains(o, "received http code 403 from proxy"),
		strings.Contains(o, "proxy returned 403"):
		return githubFailProxyCONNECT
	case strings.Contains(o, "403") || strings.Contains(o, "forbidden"):
		return githubFailForbidden
	default:
		return githubFailUnknown
	}
}

func diagnoseGitHubAPIFailure(stage, output string, exitCode int) error {
	output = strings.TrimSpace(output)
	kind := classifyGitHubAPIFailure(output)
	// Only report the GraphQL-specific "proxy allows REST but blocks
	// GraphQL" diagnosis for kinds that actually indicate an HTTP/proxy
	// block. A plain exec timeout or DNS/connection error is not evidence
	// of an L7 GraphQL allow-list gap — that's the same misdiagnosis this
	// PR already fixed for the CONNECT stage below.
	if stage == preflightCheckGraphQL &&
		(kind == githubFailForbidden || kind == githubFailProxyCONNECT || kind == githubFailAuth) {
		return fmt.Errorf(
			"GitHub GraphQL API unreachable from sandbox:\n%s\n\n"+
				"REST GET /rate_limit succeeded but GraphQL POST /graphql failed. "+
				"The sandbox proxy likely allows REST while blocking GraphQL "+
				"(the path gh CLI uses for most reads). "+
				"Check the OpenShell L7 policy for POST /graphql on api.github.com",
			output)
	}
	if stage == preflightCheckConnect && (kind == githubFailProxyCONNECT || kind == githubFailForbidden) {
		return fmt.Errorf(
			"GitHub API unreachable from sandbox (HTTP 403 — proxy allowlist issue):\n%s\n\n"+
				"The sandbox proxy is blocking HTTPS CONNECT to api.github.com. "+
				"Check the OpenShell gateway network policy and proxy allowlist configuration",
			output)
	}

	switch kind {
	case githubFailAuth:
		return fmt.Errorf(
			"GitHub API authentication failed from sandbox:\n%s\n\n"+
				"The proxy CONNECT succeeded but the token was rejected (HTTP 401/403). "+
				"The token may be invalid, expired, or missing required permissions",
			output)
	case githubFailDNS:
		return fmt.Errorf(
			"GitHub API unreachable from sandbox (DNS resolution failed):\n%s\n\n"+
				"The sandbox cannot resolve api.github.com. "+
				"Check DNS configuration and network policies",
			output)
	case githubFailConnection:
		return fmt.Errorf(
			"GitHub API unreachable from sandbox (connection failed):\n%s\n\n"+
				"The sandbox cannot connect to api.github.com or the HTTPS proxy. "+
				"Check network policies and proxy availability",
			output)
	case githubFailProxyCONNECT:
		return fmt.Errorf(
			"GitHub API unreachable from sandbox (HTTP 403 — proxy allowlist issue):\n%s\n\n"+
				"The sandbox proxy is blocking HTTPS CONNECT to api.github.com. "+
				"Check the OpenShell gateway network policy and proxy allowlist configuration",
			output)
	case githubFailForbidden:
		return fmt.Errorf(
			"GitHub API returned HTTP 403 from sandbox after proxy CONNECT succeeded:\n%s\n\n"+
				"This may be an invalid or expired token, or an L7 proxy path filter. "+
				"Inspect the response to distinguish GitHub authorization from a proxy allowlist issue",
			output)
	default:
		if output == "" {
			output = fmt.Sprintf("exit %d", exitCode)
		}
		return fmt.Errorf("GitHub API connectivity check failed (exit %d):\n%s", exitCode, output)
	}
}

func logGitHubPreflight(printer *ui.Printer, result *preflightGitHubResult) {
	if printer == nil || result == nil {
		return
	}
	if tok := result.Token; tok != nil {
		if tok.Prefix != "" {
			printer.KeyValue("Token prefix", tok.Prefix)
		}
		if tok.Type != "" {
			printer.KeyValue("Token type", tok.Type)
		}
		if tok.ExpiresAt != "" {
			printer.KeyValue("Token expires", tok.ExpiresAt)
		}
		if tok.MintedAt != "" {
			printer.KeyValue("Token minted", tok.MintedAt)
		}
	}
	if result.PersistError != "" {
		printer.StepWarn("Could not write preflight results: " + result.PersistError)
		return
	}
	if result.Outcome != "" {
		printer.KeyValue("Preflight results", preflightResultsPath)
	}
}
