package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const behaviourScriptRelPath = "behaviour/current-scenario.yaml"
const behaviourResultsFile = "behaviour-results.json"

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
type DummyRuntime struct{}

func (DummyRuntime) Name() string { return "dummy" }

func (DummyRuntime) ConfigDir() string { return sandbox.SandboxWorkspace + "/.dummy" }

func (DummyRuntime) WorkspaceDir() string { return sandbox.SandboxWorkspace }

func (DummyRuntime) EnvExports() []string { return nil }

func (DummyRuntime) Bootstrap(input BootstrapInput) error {
	sandboxName := input.SandboxName()
	mkdirCmd := fmt.Sprintf("mkdir -p %s/output %s/.dummy", sandbox.SandboxWorkspace, sandbox.SandboxWorkspace)
	_, _, _, err := sandbox.Exec(sandboxName, mkdirCmd, 10*time.Second)
	return err
}

func (r DummyRuntime) Run(params RunParams, printer *ui.Printer, _ time.Time, _ *RunMetrics) (int, error) {
	scriptPath := filepath.Join(params.FullsendDir, behaviourScriptRelPath)
	script, err := LoadBehaviourScript(scriptPath)
	if err != nil {
		return 1, err
	}

	results, execErr := executeBehaviourScript(params.SandboxName, params.RepoDir, script)
	if writeErr := writeBehaviourResults(params.SandboxName, results); writeErr != nil && execErr == nil {
		execErr = writeErr
	}

	if execErr != nil {
		printer.StepWarn("Dummy runtime: " + execErr.Error())
	}

	exitCode := 0
	for _, res := range results.Operations {
		if !res.Success {
			exitCode = 1
			break
		}
	}
	return exitCode, execErr
}

func (r DummyRuntime) ClearIterationArtifacts(sandboxName string) error {
	clearCmd := fmt.Sprintf("rm -rf %s/output/*", r.WorkspaceDir())
	_, _, _, err := sandbox.Exec(sandboxName, clearCmd, 10*time.Second)
	return err
}

func (DummyRuntime) ExtractTranscripts(_ string, _ string, _ string) error { return nil }

func (DummyRuntime) ExtractDebugLog(_ string, _ string, _ string) error { return nil }

func (DummyRuntime) ParseTranscriptErrors(_ string) []TranscriptError { return nil }

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

func executeBehaviourScript(sandboxName, repoDir string, script *BehaviourScript) (BehaviourResults, error) {
	var results BehaviourResults
	for _, op := range script.Ops {
		res := BehaviourOpResult{Description: op.Description}
		if err := executeBehaviourOp(sandboxName, repoDir, op); err != nil {
			res.Success = false
			res.Error = err.Error()
		} else {
			res.Success = true
		}
		results.Operations = append(results.Operations, res)
	}
	return results, nil
}

func executeBehaviourOp(sandboxName, repoDir string, op BehaviourOperation) error {
	switch op.Op {
	case "read_file":
		path := strings.TrimSpace(op.Args)
		if path == "" {
			return fmt.Errorf("read_file requires a path")
		}
		remotePath := resolveSandboxPath(repoDir, path)
		cmd := fmt.Sprintf("test -r %s", shellQuote(remotePath))
		_, stderr, exitCode, err := sandbox.Exec(sandboxName, cmd, 30*time.Second)
		if err != nil {
			return fmt.Errorf("read_file exec: %w", err)
		}
		if exitCode != 0 {
			return fmt.Errorf("read_file failed: %s", strings.TrimSpace(stderr))
		}
		return nil
	case "url_get":
		url := strings.TrimSpace(op.Args)
		if url == "" {
			return fmt.Errorf("url_get requires a URL")
		}
		cmd := fmt.Sprintf("curl -sf %s -o /dev/null", shellQuote(url))
		_, stderr, exitCode, err := sandbox.Exec(sandboxName, cmd, 60*time.Second)
		if err != nil {
			return fmt.Errorf("url_get exec: %w", err)
		}
		if exitCode != 0 {
			return fmt.Errorf("url_get failed: %s", strings.TrimSpace(stderr))
		}
		return nil
	case "run_command":
		command := strings.TrimSpace(op.Args)
		if command == "" {
			return fmt.Errorf("run_command requires a shell command")
		}
		_, stderr, exitCode, err := sandbox.Exec(sandboxName, command, 5*time.Minute)
		if err != nil {
			return fmt.Errorf("run_command exec: %w", err)
		}
		if exitCode != 0 {
			return fmt.Errorf("run_command exited %d: %s", exitCode, strings.TrimSpace(stderr))
		}
		return nil
	case "write_fixture":
		dest, content, err := resolveWriteFixture(op)
		if err != nil {
			return err
		}
		remoteDest := resolveSandboxPath(sandbox.SandboxWorkspace, dest)
		mkdirCmd := fmt.Sprintf("mkdir -p $(dirname %s)", shellQuote(remoteDest))
		if _, _, _, err := sandbox.Exec(sandboxName, mkdirCmd, 10*time.Second); err != nil {
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
		if err := sandbox.Upload(sandboxName, tmp.Name(), remoteDest); err != nil {
			return fmt.Errorf("write_fixture upload: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown op %q", op.Op)
	}
}

func resolveWriteFixture(op BehaviourOperation) (dest string, content string, err error) {
	parts := strings.SplitN(op.Args, ",", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("write_fixture args must be dest_path, fixture_path")
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

func resolveSandboxPath(base, rel string) string {
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, sandbox.SandboxWorkspace) {
		return rel
	}
	return filepath.Join(base, rel)
}

func writeBehaviourResults(sandboxName string, results BehaviourResults) error {
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
	return sandbox.Upload(sandboxName, tmp.Name(), remotePath)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
