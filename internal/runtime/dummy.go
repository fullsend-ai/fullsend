package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	clearStrayProcesses(r.execFn(), sandboxName, os.Stderr)
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
		if err := executeBehaviourOp(rt, sandboxName, repoDir, op); err != nil {
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
		if err := validateHTTPURL(rawURL); err != nil {
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

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url_get invalid URL: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("url_get requires http or https scheme, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("url_get requires a host")
	}
	return nil
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
