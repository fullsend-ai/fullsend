package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const behaviourScriptRelPath = "behaviour/current-scenario.yaml"
const behaviourResultsFile = "behaviour-results.json"

var envVarNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var jsonPathPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$`)

// branchNamePattern restricts checkout_branch to plain branch names: each
// slash-separated segment starts with an alphanumeric and continues with
// alphanumerics, dots, underscores, or dashes. Combined with the explicit
// ".." rejection in executeBehaviourOp this forbids option injection
// (leading dash), refspec tricks, and path traversal.
var branchNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)

type sandboxExecFunc func(sandboxName, cmd string, timeout time.Duration) (stdout, stderr string, exitCode int, err error)

type sandboxUploadFunc func(sandboxName, localPath, remotePath string) error

type writeBehaviourResultsFunc func(sandboxName string, results BehaviourResults) error

// BehaviourOperation is a single scripted step for the dummy runtime.
type BehaviourOperation struct {
	Description string `yaml:"description" json:"description"`
	Op          string `yaml:"op" json:"op"`
	Args        string `yaml:"args" json:"args"`
	Content     string `yaml:"content,omitempty" json:"content,omitempty"`
	// http_probe fields (#8280). Fixed fields only — the op is not a
	// general HTTP client. When they are empty, http_probe parses Args as
	// "METHOD URL HEADER_ENV [BODY]" (see ParseHTTPProbeArgs).
	Method    string `yaml:"method,omitempty" json:"method,omitempty"`
	URL       string `yaml:"url,omitempty" json:"url,omitempty"`
	HeaderEnv string `yaml:"header_env,omitempty" json:"header_env,omitempty"`
	Body      string `yaml:"body,omitempty" json:"body,omitempty"`
}

// BehaviourScript is the YAML committed to .fullsend/behaviour/current-scenario.yaml.
type BehaviourScript struct {
	Ops []BehaviourOperation `yaml:"ops"`
}

// BehaviourOpResult records the outcome of one scripted operation.
type BehaviourOpResult struct {
	Description string `json:"description"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
	// HTTPStatus and ResponseBody are recorded by http_probe only. The
	// body is capped at httpProbeBodyLimit bytes, with every JWT-shaped
	// substring replaced by httpProbeJWTRedaction; BodyHadJWT records that
	// one was there, so a custody assertion can still fail on it.
	HTTPStatus   int    `json:"http_status,omitempty"`
	ResponseBody string `json:"response_body,omitempty"`
	BodyHadJWT   bool   `json:"body_had_jwt,omitempty"`
}

// BehaviourResults is written to output/behaviour-results.json in the sandbox.
type BehaviourResults struct {
	Operations []BehaviourOpResult `json:"operations"`
}

// DummyRuntime executes scripted operations in the real OpenShell sandbox.
// ExecFn, UploadFn, and WriteResultsFn are optional test overrides; production
// uses sandbox.Exec, sandbox.Upload, and writeBehaviourResults.
type DummyRuntime struct {
	ExecFn         sandboxExecFunc
	UploadFn       sandboxUploadFunc
	WriteResultsFn writeBehaviourResultsFunc
}

func (r DummyRuntime) execFn() sandboxExecFunc {
	if r.ExecFn != nil {
		return r.ExecFn
	}
	return sandbox.Exec
}

func (r DummyRuntime) uploadFn() sandboxUploadFunc {
	if r.UploadFn != nil {
		return r.UploadFn
	}
	return sandbox.Upload
}

func (r DummyRuntime) writeResultsFn() writeBehaviourResultsFunc {
	if r.WriteResultsFn != nil {
		return r.WriteResultsFn
	}
	return r.writeBehaviourResults
}

func (DummyRuntime) Name() string { return "dummy" }

func (DummyRuntime) System() string { return "fullsend.dummy" }

func (DummyRuntime) ConfigDir() string { return sandbox.SandboxWorkspace + "/.dummy" }

func (DummyRuntime) WorkspaceDir() string { return sandbox.SandboxWorkspace }

func (DummyRuntime) EnvExports() []string { return nil }

func (r DummyRuntime) Bootstrap(input BootstrapInput) error {
	sandboxName := input.SandboxName()

	// Mirror of ClaudeRuntime.Bootstrap: the dummy runtime runs scripted
	// operations rather than an agent, so every declared plugin (ADR 0094)
	// is named — with the format it is in — and skipped rather than
	// silently dropped.
	for _, e := range input.Plugins() {
		if e.Path != "" {
			fmt.Fprintf(os.Stderr, "Plugin %q (%s): skipped — the dummy runtime loads no plugins (see docs/runtimes.md)\n", e.SandboxName(), e.Kind)
		}
	}

	mkdirCmd := fmt.Sprintf("mkdir -p %s/output %s/.dummy", sandbox.SandboxWorkspace, sandbox.SandboxWorkspace)
	_, stderr, exitCode, err := r.execFn()(sandboxName, mkdirCmd, 10*time.Second)
	if err != nil {
		return fmt.Errorf("bootstrap exec: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("bootstrap failed: %s", strings.TrimSpace(stderr))
	}
	return nil
}

func (r DummyRuntime) Run(ctx context.Context, params RunParams, printer *ui.Printer, _ time.Time, _ *RunMetrics) (int, error) {
	scriptPath := filepath.Join(params.FullsendDir, behaviourScriptRelPath)
	script, err := LoadBehaviourScript(scriptPath)
	if err != nil {
		return 1, err
	}

	results, scriptErr := executeBehaviourScript(ctx, r, params.SandboxName, params.RepoDir, script)
	if writeErr := r.writeResultsFn()(params.SandboxName, results); writeErr != nil {
		return 1, writeErr
	}

	if scriptErr != nil {
		printer.StepWarn("Dummy runtime: " + scriptErr.Error())
	}

	// Non-zero exitCode mirrors ClaudeRuntime: run.go warns on non-zero exit but
	// only aborts on a non-nil Go error (infrastructure failures).
	exitCode := 0
	for _, res := range results.Operations {
		if !res.Success {
			exitCode = 1
			break
		}
	}
	return exitCode, nil
}

// ClearIterationArtifacts sweeps stray processes (the dummy runtime's ops run
// in the real sandbox, so it gets the same between-iteration hygiene as the
// agent runtimes), then removes the previous iteration's output.
func (r DummyRuntime) ClearIterationArtifacts(sandboxName string) error {
	clearStrayProcesses(r.execFn(), sandboxName, os.Stderr, "the previous iteration")
	clearCmd := fmt.Sprintf("rm -rf %s/output/*", r.WorkspaceDir())
	_, stderr, exitCode, err := r.execFn()(sandboxName, clearCmd, 10*time.Second)
	if err != nil {
		return fmt.Errorf("clear iteration artifacts exec: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("clear iteration artifacts failed: %s", strings.TrimSpace(stderr))
	}
	return nil
}

func (DummyRuntime) ExtractTranscripts(_ string, _ string, _ string) error { return nil }

func (DummyRuntime) ExtractDebugLog(_ string, _ string, _ string) error { return nil }

func (DummyRuntime) ParseTranscriptErrors(_ string) []TranscriptError { return nil }

func (DummyRuntime) ParseTranscriptFile(_ string) (TranscriptError, bool) {
	return TranscriptError{}, false
}

func (DummyRuntime) EmitTranscriptErrors(w io.Writer, summaries []TranscriptError) {
	emitTranscriptErrors(w, summaries)
}

// LoadBehaviourScript reads and parses a behaviour scenario script from disk.
func LoadBehaviourScript(path string) (*BehaviourScript, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading behaviour script %s: %w", path, err)
	}
	var script BehaviourScript
	if err := yaml.Unmarshal(data, &script); err != nil {
		return nil, fmt.Errorf("parsing behaviour script %s: %w", path, err)
	}
	if len(script.Ops) == 0 {
		return nil, fmt.Errorf("behaviour script %s has no operations", path)
	}
	return &script, nil
}

func executeBehaviourScript(ctx context.Context, rt DummyRuntime, sandboxName, repoDir string, script *BehaviourScript) (BehaviourResults, error) {
	var results BehaviourResults
	var firstErr error
	for _, op := range script.Ops {
		if err := ctx.Err(); err != nil {
			return results, fmt.Errorf("behaviour script cancelled: %w", err)
		}
		res := BehaviourOpResult{Description: op.Description}
		var err error
		switch op.Op {
		case "http_probe":
			res.HTTPStatus, res.ResponseBody, res.BodyHadJWT, err = executeHTTPProbe(rt, sandboxName, op)
		case "wait":
			err = executeWait(ctx, op)
		default:
			err = executeBehaviourOp(rt, sandboxName, repoDir, op)
		}
		if err != nil {
			res.Success = false
			res.Error = err.Error()
			if firstErr == nil {
				firstErr = err
			}
		} else {
			res.Success = true
		}
		results.Operations = append(results.Operations, res)
	}
	return results, firstErr
}

func executeBehaviourOp(rt DummyRuntime, sandboxName, repoDir string, op BehaviourOperation) error {
	switch op.Op {
	case "read_file":
		path := strings.TrimSpace(op.Args)
		if path == "" {
			return fmt.Errorf("read_file requires a path")
		}
		remotePath, err := resolveSandboxPath(repoDir, path)
		if err != nil {
			return err
		}
		cmd := fmt.Sprintf("test -r %s", shellQuote(remotePath))
		_, stderr, exitCode, err := rt.execFn()(sandboxName, cmd, 30*time.Second)
		if err != nil {
			return fmt.Errorf("read_file exec: %w", err)
		}
		if exitCode != 0 {
			return fmt.Errorf("read_file failed: %s", strings.TrimSpace(stderr))
		}
		return nil
	case "url_get":
		rawURL := strings.TrimSpace(op.Args)
		if rawURL == "" {
			return fmt.Errorf("url_get requires a URL")
		}
		if err := validateHTTPURL("url_get", rawURL); err != nil {
			return err
		}
		cmd := fmt.Sprintf("curl -sf -- %s -o /dev/null", shellQuote(rawURL))
		_, stderr, exitCode, err := rt.execFn()(sandboxName, cmd, 60*time.Second)
		if err != nil {
			return fmt.Errorf("url_get exec: %w", err)
		}
		if exitCode != 0 {
			return fmt.Errorf("url_get failed: %s", strings.TrimSpace(stderr))
		}
		return nil
	case "write_fixture":
		dest, content, err := resolveWriteFixture(op)
		if err != nil {
			return err
		}
		remoteDest, err := resolveSandboxPath(sandbox.SandboxWorkspace, dest)
		if err != nil {
			return err
		}
		parentDir := filepath.Dir(remoteDest)
		mkdirCmd := fmt.Sprintf("mkdir -p %s", shellQuote(parentDir))
		if _, _, _, err := rt.execFn()(sandboxName, mkdirCmd, 10*time.Second); err != nil {
			return fmt.Errorf("write_fixture mkdir: %w", err)
		}
		tmp, err := os.CreateTemp("", "behaviour-fixture-*")
		if err != nil {
			return fmt.Errorf("write_fixture temp file: %w", err)
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.WriteString(content); err != nil {
			tmp.Close()
			return fmt.Errorf("write_fixture write temp: %w", err)
		}
		tmp.Close()
		if err := rt.uploadFn()(sandboxName, tmp.Name(), remoteDest); err != nil {
			return fmt.Errorf("write_fixture upload: %w", err)
		}
		return nil
	case "checkout_branch":
		name := strings.TrimSpace(op.Args)
		if name == "" {
			return fmt.Errorf("checkout_branch requires a branch name")
		}
		if !branchNamePattern.MatchString(name) || strings.Contains(name, "..") {
			return fmt.Errorf("checkout_branch invalid branch name %q", name)
		}
		cmd := checkoutBranchCommand(repoDir, name)
		_, stderr, exitCode, err := rt.execFn()(sandboxName, cmd, 120*time.Second)
		if err != nil {
			return fmt.Errorf("checkout_branch exec: %w", err)
		}
		if exitCode != 0 {
			return fmt.Errorf("checkout_branch %s failed: %s", name, strings.TrimSpace(stderr))
		}
		return nil
	case "assert_env":
		varName := strings.TrimSpace(op.Args)
		if varName == "" {
			return fmt.Errorf("assert_env requires a variable name")
		}
		if !envVarNamePattern.MatchString(varName) {
			return fmt.Errorf("assert_env invalid variable name %q", varName)
		}
		cmd := fmt.Sprintf("v=$(printenv -- %s); test -n \"$v\"", shellQuote(varName))
		_, stderr, exitCode, err := rt.execFn()(sandboxName, cmd, 30*time.Second)
		if err != nil {
			return fmt.Errorf("assert_env exec: %w", err)
		}
		if exitCode != 0 {
			return fmt.Errorf("assert_env %s unset or empty: %s", varName, strings.TrimSpace(stderr))
		}
		return nil
	case "assert_not_jwt":
		varName := strings.TrimSpace(op.Args)
		if varName == "" {
			return fmt.Errorf("assert_not_jwt requires a variable name")
		}
		if !envVarNamePattern.MatchString(varName) {
			return fmt.Errorf("assert_not_jwt invalid variable name %q", varName)
		}
		_, stderr, exitCode, err := rt.execFn()(sandboxName, assertNotJWTCommand(varName), 30*time.Second)
		if err != nil {
			return fmt.Errorf("assert_not_jwt exec: %w", err)
		}
		switch exitCode {
		case 0:
			return nil
		case assertNotJWTUnset:
			return fmt.Errorf("assert_not_jwt %s unset or empty", varName)
		case assertNotJWTFound:
			return fmt.Errorf("assert_not_jwt %s holds a JWT, or names a file that does", varName)
		}
		return fmt.Errorf("assert_not_jwt %s failed: %s", varName, strings.TrimSpace(stderr))
	case "assert_file":
		path := strings.TrimSpace(op.Args)
		if path == "" {
			return fmt.Errorf("assert_file requires a path")
		}
		remotePath, err := resolveSandboxPath(sandbox.SandboxWorkspace, path)
		if err != nil {
			return err
		}
		cmd := fmt.Sprintf("test -r %s", shellQuote(remotePath))
		_, stderr, exitCode, err := rt.execFn()(sandboxName, cmd, 30*time.Second)
		if err != nil {
			return fmt.Errorf("assert_file exec: %w", err)
		}
		if exitCode != 0 {
			return fmt.Errorf("assert_file %s missing: %s", path, strings.TrimSpace(stderr))
		}
		return nil
	case "assert_json":
		parts := strings.SplitN(op.Args, ",", 2)
		if len(parts) != 2 {
			return fmt.Errorf("assert_json args must be path,json_path")
		}
		path := strings.TrimSpace(parts[0])
		jsonPath := strings.TrimSpace(parts[1])
		if !jsonPathPattern.MatchString(jsonPath) {
			return fmt.Errorf("assert_json invalid json_path %q", jsonPath)
		}
		remotePath, err := resolveSandboxPath(sandbox.SandboxWorkspace, path)
		if err != nil {
			return err
		}
		cmd := fmt.Sprintf("jq -e %s %s >/dev/null", shellQuote("."+jsonPath), shellQuote(remotePath))
		_, stderr, exitCode, err := rt.execFn()(sandboxName, cmd, 30*time.Second)
		if err != nil {
			return fmt.Errorf("assert_json exec: %w", err)
		}
		if exitCode != 0 {
			return fmt.Errorf("assert_json %s at %s: %s", jsonPath, path, strings.TrimSpace(stderr))
		}
		return nil
	// http_probe is not handled here: executeBehaviourScript runs it
	// through executeHTTPProbe, which also returns what it records.
	default:
		return fmt.Errorf("unknown op %q", op.Op)
	}
}

// checkoutBranchCommand builds the shell command for the checkout_branch
// op. The branch name must already be validated against branchNamePattern.
//
// Semantics (deliberately a single narrow capability, not a general
// shell op):
//   - Probe the remote for the ref with `git ls-remote --exit-code`.
//     Exit 2 means the ref does not exist — base the branch off the
//     current HEAD. Any other non-zero exit (network, auth) fails the
//     op instead of being silently collapsed into the HEAD fallback,
//     which would make scenarios pass or fail for the wrong reason.
//   - When the ref exists, fetch it and base the branch on FETCH_HEAD
//     so the branch carries the remote ref's commits.
//   - Record one marker commit on the branch. This gives the applier
//     post-script real content to push and — because the local tip now
//     differs from every remote tip — makes a wrongful push move the
//     target branch, so "branch ... is unchanged" assertions can
//     actually detect it. The commit subject uses a conventional-commit
//     prefix because post-code derives the applier's PR title from it.
func checkoutBranchCommand(repoDir, name string) string {
	quoted := shellQuote(name)
	// Scoped to refs/heads/ throughout — the ls-remote probe already
	// restricts to --heads, so the fetch must resolve the same ref
	// rather than git's default disambiguation order (which would
	// prefer a same-named tag over the branch).
	refspec := shellQuote("refs/heads/" + name)
	return fmt.Sprintf(
		"cd %s"+
			" && if git ls-remote --exit-code --heads origin %s >/dev/null 2>&1; then"+
			" git fetch origin %s && git checkout -B %s FETCH_HEAD;"+
			" else rc=$?; if [ \"$rc\" -ne 2 ]; then echo \"checkout_branch: ls-remote failed with $rc\" >&2; exit 1; fi;"+
			" git checkout -B %s; fi"+
			" && mkdir -p behaviour && echo %s > behaviour/marker.txt"+
			" && git add behaviour/marker.txt"+
			" && git -c user.name=fullsend-behaviour -c user.email=behaviour@fullsend.invalid commit -m %s",
		shellQuote(repoDir), quoted, refspec, quoted, quoted,
		shellQuote("scripted marker for "+name),
		shellQuote("test: add scripted marker commit"))
}

// validateHTTPURL checks an http(s) URL for the named op (url_get or
// http_probe), which prefixes every error.
func validateHTTPURL(op, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s invalid URL: %w", op, err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("%s requires http or https scheme, got %q", op, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%s requires a host", op)
	}
	return nil
}

// httpProbeBodyLimit caps the response body http_probe reads and records.
const httpProbeBodyLimit = 4096

// httpProbeHeaderEnvs are the only variables http_probe may send as the
// bearer. The probe sends the named variable to a URL the scenario picks,
// so an open list would let a script ship any sandbox variable (a forge
// token, for example) to any host egress allows. These two hold OpenShell
// placeholders that the proxy swaps for the real credential only on the
// inference endpoints their profiles bind.
var httpProbeHeaderEnvs = []string{"INFERENCE_GATEWAY_API_KEY", "OPENAI_API_KEY"}

// httpProbeJWTPattern matches a JWT-shaped substring (three base64url
// segments, the last possibly empty), such as a forge OIDC token.
// httpProbeScript applies the same pattern in the sandbox.
const httpProbeJWTPattern = `eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`

// httpProbeJWTRedaction replaces each JWT-shaped substring in a recorded
// response body.
const httpProbeJWTRedaction = "<redacted-jwt>"

var httpProbeJWTRe = regexp.MustCompile(httpProbeJWTPattern)

// httpProbeScript is the fixed node program http_probe runs in the
// sandbox. It runs through node so the request leaves through a binary
// the inference profiles allow (`**/node`). Every caller-supplied value
// arrives as one base64-encoded JSON argument — nothing is interpolated
// into the code or the shell, and base64 never starts with "-", so node
// cannot read the argument as an option. The bearer value is read from
// the named environment variable inside the sandbox (the OpenShell
// placeholder the proxy replaces), never passed on the command line.
//
// The body is read incrementally: at most httpProbeBodyLimit bytes are
// kept and the stream is cancelled once the limit is reached, so a large
// or endless reply is never buffered whole. JWT-shaped substrings are
// replaced by httpProbeJWTRedaction before anything is printed, including
// a JWT cut off at the limit (a trailing "eyJ..." run). It prints one JSON
// line: {"status":N,"body":"...","body_had_jwt":B} or {"error":"..."}.
var httpProbeScript = `const L=` + strconv.Itoa(httpProbeBodyLimit) + `;` +
	`const J=/` + httpProbeJWTPattern + `/g;` +
	`const T=/eyJ[A-Za-z0-9_-]*(\.[A-Za-z0-9_-]*){0,2}$/;` +
	`const p=JSON.parse(Buffer.from(process.argv[1],"base64").toString("utf8"));` +
	`const t=process.env[p.header_env];` +
	`if(!t){console.log(JSON.stringify({error:"environment variable "+p.header_env+" is unset or empty"}));process.exit(3);}` +
	`const h={authorization:"Bearer "+t};` +
	`if(p.body){h["content-type"]="application/json";}` +
	`fetch(p.url,{method:p.method,headers:h,body:p.body?p.body:undefined,redirect:"manual",signal:AbortSignal.timeout(45000)})` +
	`.then(async r=>{const c=[];let n=0,cut=false;` +
	`if(r.body){const rd=r.body.getReader();` +
	`for(;;){const x=await rd.read();if(x.done)break;` +
	`const k=Math.min(x.value.length,L-n);c.push(Buffer.from(x.value.buffer,x.value.byteOffset,k));n+=k;` +
	`if(n>=L){cut=true;await rd.cancel().catch(()=>{});break;}}}` +
	`let j=false;let b=Buffer.concat(c).toString("utf8").replace(J,()=>{j=true;return "` + httpProbeJWTRedaction + `";});` +
	`if(cut&&T.test(b)){j=true;b=b.replace(T,"` + httpProbeJWTRedaction + `");}` +
	`console.log(JSON.stringify({status:r.status,body:b,body_had_jwt:j}));})` +
	`.catch(e=>{console.log(JSON.stringify({error:String((e&&e.cause)||e)}));process.exit(2);});`

// HTTPProbe holds the validated fields of one http_probe op.
type HTTPProbe struct {
	Method    string `json:"method"`
	URL       string `json:"url"`
	HeaderEnv string `json:"header_env"`
	Body      string `json:"body,omitempty"`
}

// ParseHTTPProbeArgs splits the compact table form "METHOD URL HEADER_ENV
// [BODY]" (whitespace-separated; BODY is the untouched remainder, so it may
// contain spaces) and validates the result.
func ParseHTTPProbeArgs(args string) (HTTPProbe, error) {
	rest := strings.TrimSpace(args)
	var fields [3]string
	for i := range fields {
		if rest == "" {
			return HTTPProbe{}, fmt.Errorf("http_probe args must be METHOD URL HEADER_ENV [BODY]")
		}
		idx := strings.IndexAny(rest, " \t")
		if idx < 0 {
			fields[i], rest = rest, ""
		} else {
			fields[i], rest = rest[:idx], strings.TrimSpace(rest[idx:])
		}
	}
	p := HTTPProbe{Method: fields[0], URL: fields[1], HeaderEnv: fields[2], Body: rest}
	return p, p.Validate()
}

// httpProbeFromOp prefers the op's explicit fields and falls back to Args.
func httpProbeFromOp(op BehaviourOperation) (HTTPProbe, error) {
	if op.Method == "" && op.URL == "" && op.HeaderEnv == "" && op.Body == "" {
		return ParseHTTPProbeArgs(op.Args)
	}
	p := HTTPProbe{Method: strings.TrimSpace(op.Method), URL: strings.TrimSpace(op.URL), HeaderEnv: strings.TrimSpace(op.HeaderEnv), Body: op.Body}
	return p, p.Validate()
}

// Validate checks the probe's fixed fields.
func (p HTTPProbe) Validate() error {
	switch p.Method {
	case "GET", "POST":
	default:
		return fmt.Errorf("http_probe method must be GET or POST, got %q", p.Method)
	}
	if p.URL == "" {
		return fmt.Errorf("http_probe requires a url")
	}
	if err := validateHTTPURL("http_probe", p.URL); err != nil {
		return err
	}
	if !slices.Contains(httpProbeHeaderEnvs, p.HeaderEnv) {
		return fmt.Errorf("http_probe header_env %q is not allowed (allowed: %s)", p.HeaderEnv, strings.Join(httpProbeHeaderEnvs, ", "))
	}
	if p.Method == "GET" && p.Body != "" {
		return fmt.Errorf("http_probe body is only allowed with POST")
	}
	return nil
}

// httpProbeCommand builds the sandbox command for a validated probe.
// NODE_USE_ENV_PROXY asks node's fetch to honour the sandbox's proxy
// variables. It is best-effort: node reads it only from 22.21 on, and the
// sandbox image requires only node >= 22.19; where egress is transparent
// it is a no-op either way.
func httpProbeCommand(p HTTPProbe) (string, error) {
	payload, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("http_probe encode: %w", err)
	}
	arg := base64.StdEncoding.EncodeToString(payload)
	return fmt.Sprintf("NODE_USE_ENV_PROXY=1 node -e %s %s", shellQuote(httpProbeScript), shellQuote(arg)), nil
}

// maxWaitSeconds bounds the wait op, so a scenario cannot hold a sandbox
// for longer than a CI job reasonably allows. Several GitHub OIDC token
// lifetimes (300 s) fit.
const maxWaitSeconds = 1200

// executeWait runs one wait op: it sleeps for the op's whole number of
// seconds (1 to maxWaitSeconds) on the host, between the ops before and
// after it, so a scenario can outlast a credential lifetime. It returns
// early with an error when ctx ends.
func executeWait(ctx context.Context, op BehaviourOperation) error {
	raw := strings.TrimSpace(op.Args)
	secs, err := strconv.Atoi(raw)
	if err != nil || secs < 1 || secs > maxWaitSeconds {
		return fmt.Errorf("wait requires a whole number of seconds from 1 to %d, got %q", maxWaitSeconds, raw)
	}
	timer := time.NewTimer(time.Duration(secs) * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait cancelled: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// assert_not_jwt exit codes.
const (
	assertNotJWTUnset = 3
	assertNotJWTFound = 4
)

// assertNotJWTCommand checks, inside the sandbox, that the variable
// varName is set and does not hold a JWT and, when its value is a readable
// file path (a token-file variable), that the file does not hold one
// either. A JWT is three base64url segments, the first starting "eyJ".
// varName has been checked against envVarNamePattern.
func assertNotJWTCommand(varName string) string {
	jwt := shellQuote(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.`)
	return fmt.Sprintf(`v=$(printenv -- %s); test -n "$v" || exit %d; `+
		`if printf '%%s' "$v" | grep -Eq %s; then exit %d; fi; `+
		`case "$v" in /*) if test -f "$v" && test -r "$v"; then if grep -Eq %s -- "$v"; then exit %d; fi; fi ;; esac; exit 0`,
		shellQuote(varName), assertNotJWTUnset, jwt, assertNotJWTFound, jwt, assertNotJWTFound)
}

// executeHTTPProbe runs one http_probe op and returns the HTTP status,
// the (capped, JWT-redacted) response body and whether a JWT-shaped
// substring was redacted. The op succeeds only on a 2xx response; a
// non-2xx status is still recorded so scenarios can assert on it (for
// example a 403 from the gateway versus a refusal by the egress proxy).
// The body is redacted again here, so a JWT never reaches the results
// file whatever the sandbox printed.
func executeHTTPProbe(rt DummyRuntime, sandboxName string, op BehaviourOperation) (int, string, bool, error) {
	p, err := httpProbeFromOp(op)
	if err != nil {
		return 0, "", false, err
	}
	cmd, err := httpProbeCommand(p)
	if err != nil {
		return 0, "", false, err
	}
	stdout, stderr, exitCode, err := rt.execFn()(sandboxName, cmd, 60*time.Second)
	if err != nil {
		return 0, "", false, fmt.Errorf("http_probe exec: %w", err)
	}
	var out struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
		HadJWT bool   `json:"body_had_jwt"`
		Error  string `json:"error"`
	}
	line := strings.TrimSpace(stdout)
	if i := strings.LastIndex(line, "\n"); i >= 0 {
		line = line[i+1:]
	}
	if jsonErr := json.Unmarshal([]byte(line), &out); jsonErr != nil {
		return 0, "", false, fmt.Errorf("http_probe %s %s: exit %d, unreadable output: %s", p.Method, p.URL, exitCode, strings.TrimSpace(stderr))
	}
	if httpProbeJWTRe.MatchString(out.Body) {
		out.Body = httpProbeJWTRe.ReplaceAllString(out.Body, httpProbeJWTRedaction)
		out.HadJWT = true
	}
	if len(out.Body) > httpProbeBodyLimit {
		out.Body = out.Body[:httpProbeBodyLimit]
	}
	if out.Error != "" || exitCode != 0 {
		msg := out.Error
		if msg == "" {
			msg = strings.TrimSpace(stderr)
		}
		return out.Status, out.Body, out.HadJWT, fmt.Errorf("http_probe %s %s failed: %s", p.Method, p.URL, msg)
	}
	if out.Status < 200 || out.Status > 299 {
		return out.Status, out.Body, out.HadJWT, fmt.Errorf("http_probe %s %s returned HTTP %d", p.Method, p.URL, out.Status)
	}
	return out.Status, out.Body, out.HadJWT, nil
}

func resolveWriteFixture(op BehaviourOperation) (dest string, content string, err error) {
	parts := strings.SplitN(op.Args, ",", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("write_fixture args must be dest_path, fixture_path (fixture path is for test embedding; runtime uses op.content)")
	}
	dest = strings.TrimSpace(parts[0])
	if dest == "" {
		return "", "", fmt.Errorf("write_fixture requires dest_path")
	}
	if op.Content != "" {
		return dest, op.Content, nil
	}
	return "", "", fmt.Errorf("write_fixture requires embedded content in script")
}

func resolveSandboxPath(base, rel string) (string, error) {
	baseClean := filepath.Clean(base)
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, sandbox.SandboxWorkspace) {
		clean := filepath.Clean(rel)
		wsClean := filepath.Clean(sandbox.SandboxWorkspace)
		if clean != wsClean && !strings.HasPrefix(clean, wsClean+string(os.PathSeparator)) {
			return "", fmt.Errorf("path %q escapes sandbox workspace", rel)
		}
		return clean, nil
	}
	resolved := filepath.Clean(filepath.Join(baseClean, rel))
	if resolved != baseClean && !strings.HasPrefix(resolved, baseClean+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes base %q", rel, base)
	}
	return resolved, nil
}

func (r DummyRuntime) writeBehaviourResults(sandboxName string, results BehaviourResults) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling behaviour results: %w", err)
	}
	tmp, err := os.CreateTemp("", "behaviour-results-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	remotePath := filepath.Join(sandbox.SandboxWorkspace, "output", behaviourResultsFile)
	return r.uploadFn()(sandboxName, tmp.Name(), remotePath)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
