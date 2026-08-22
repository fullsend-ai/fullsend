package steps

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/artifacts"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// suiteInstallRuntime is the runtime every pool repo is installed with
// (`github setup … --runtime dummy`, asserted post-install).
const suiteInstallRuntime = "dummy"

// Runtime steps cover the runtime layer that every other scenario takes
// for granted: the repo's `runtime:` selects the backend (core, runs under
// the dummy runtime on every suite run), and a real runtime can be
// exercised end to end on the same leased repo by flipping that key for
// one scenario (pi today — see features/runtime/pi.feature).
func registerRuntimeSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the repository runtime is "([^"]+)"$`, func(ctx context.Context, name string) (context.Context, error) {
		return ctx, givenRepositoryRuntime(world.FromContext(ctx), name)
	})
	sc.Step(`^a pi agent "([^"]+)" defined as:$`, func(ctx context.Context, name, doc string) (context.Context, error) {
		return ctx, givenPiAgent(world.FromContext(ctx), name, doc)
	})
	sc.Step(`^the run selected the "([^"]+)" runtime$`, func(ctx context.Context, name string) (context.Context, error) {
		return ctx, assertRunSelectedRuntime(world.FromContext(ctx), name)
	})
	sc.Step(`^the run metrics report tokens$`, func(ctx context.Context) (context.Context, error) {
		return ctx, assertRunMetricsReportTokens(world.FromContext(ctx))
	})
	sc.Step(`^the pi session transcript records at least one tool call$`, func(ctx context.Context) (context.Context, error) {
		return ctx, assertPiTranscriptHasToolCall(world.FromContext(ctx))
	})
}

// givenRepositoryRuntime commits `runtime: <name>` into the enrolled
// repo's .fullsend/config.yaml (the only place a runtime is selected —
// there is no harness key or CLI flag) and records the previous value so
// CleanupScenario can restore it. Mirrors the kill-switch step.
func givenRepositoryRuntime(w *world.World, name string) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before runtime operations")
	}
	name = strings.TrimSpace(name)
	valid := false
	for _, v := range config.ValidRuntimes() {
		if v == name {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("runtime %q is not one of %s", name, strings.Join(config.ValidRuntimes(), ", "))
	}
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfg, err := readPerRepoConfig(w, cfgPath)
	if err != nil {
		return err
	}
	if !w.RuntimeOverridden {
		// ConfigRuntime resolves through the defaults layer (code default
		// "claude" when the key is absent). The suite installs every pool
		// repo with an explicit `runtime: dummy` and validates that after
		// install, so anything else here means a slot is in an unexpected
		// state; refuse rather than restore a real runtime later.
		original := cfg.ConfigRuntime()
		if original != suiteInstallRuntime {
			return fmt.Errorf("repo runtime is %q before override, want %q (suite invariant; refusing to record it for restore)", original, suiteInstallRuntime)
		}
		w.RuntimeOriginal = original
	}
	cfg.SetRuntime(name)
	merged, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, "behaviour: select runtime "+name, merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	w.RuntimeOverridden = true
	return nil
}

// fixturePlaceholder marks `{{fixture:<path>}}` in an agent body; the path
// is relative to the fixtures root and is inlined verbatim, so a real
// runtime can be told to write a schema-valid result file deterministically.
var fixturePlaceholder = regexp.MustCompile(`\{\{fixture:([^}]+)\}\}`)

// givenPiAgent commits a complete agent definition (frontmatter + body) to
// `.fullsend/agents/<name>.md` on the enrolled repo. The custom-harness
// step commits a placeholder for any relative `agent:` path (the per-repo
// scaffold ships no agents — fleet agents are URL-sourced), which is fine
// under the dummy runtime but gives a real runtime no task; this step runs
// after it and replaces the placeholder with a body whose tool use is
// deliberate, so the transcript assertions are grounded.
func givenPiAgent(w *world.World, name, doc string) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before agent operations")
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("agent name %q must be a bare file name", name)
	}
	doc = strings.TrimSpace(doc)
	if !strings.HasPrefix(doc, "---") {
		return fmt.Errorf("agent %q must start with a --- frontmatter block (name, description, tools)", name)
	}
	if strings.TrimSpace(w.FixturesRoot) == "" {
		return fmt.Errorf("world.FixturesRoot is not set")
	}
	moduleRoot, err := findModuleSubdir(w.FixturesRoot)
	if err != nil {
		return err
	}
	var expandErr error
	body := fixturePlaceholder.ReplaceAllStringFunc(doc, func(m string) string {
		rel := strings.TrimSpace(fixturePlaceholder.FindStringSubmatch(m)[1])
		content, readErr := os.ReadFile(filepath.Join(moduleRoot, rel))
		if readErr != nil && expandErr == nil {
			expandErr = fmt.Errorf("reading fixture %s: %w", rel, readErr)
		}
		return strings.TrimSpace(string(content))
	})
	if expandErr != nil {
		return expandErr
	}
	agentPath := filepath.Join(".fullsend", "agents", name+".md")
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, agentPath, "behaviour: define agent "+name, []byte(body+"\n")); err != nil {
		return fmt.Errorf("committing agent %s: %w", agentPath, err)
	}
	return nil
}

// readPerRepoConfig loads the enrolled repo's config.yaml as the per-repo
// writer (the generic ConfigWriter has no runtime accessors).
func readPerRepoConfig(w *world.World, cfgPath string) (config.PerRepoConfigWriter, error) {
	cfgData, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	parsed, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	cfg, ok := parsed.(config.PerRepoConfigWriter)
	if !ok {
		return nil, fmt.Errorf("config at %s is not a per-repo config (%T)", cfgPath, parsed)
	}
	return cfg, nil
}

// RestoreRuntime puts the install-time runtime back. Exported so
// CleanupScenario can call it during scenario teardown.
func RestoreRuntime(w *world.World) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before runtime operations")
	}
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfg, err := readPerRepoConfig(w, cfgPath)
	if err != nil {
		return err
	}
	restore := w.RuntimeOriginal
	if restore == "" {
		restore = suiteInstallRuntime
	}
	cfg.SetRuntime(restore)
	merged, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, "behaviour: restore runtime", merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

// runMetrics is the subset of the runner's metrics.json the steps read.
type runMetrics struct {
	Runtime    string `json:"runtime"`
	TokenUsage struct {
		Input  int `json:"input"`
		Output int `json:"output"`
	} `json:"token_usage"`
}

func readRunMetrics(w *world.World) (runMetrics, error) {
	var m runMetrics
	if err := ensureRunArtifacts(w, "metrics.json"); err != nil {
		return m, err
	}
	data, err := artifacts.FindOutputFile(w.ArtifactDir, "metrics.json")
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("parsing metrics.json: %w", err)
	}
	return m, nil
}

// assertRunSelectedRuntime checks the runtime the runner recorded in
// metrics.json — the end-to-end proof that the repo's `runtime:` reached
// backend selection, for any runtime.
func assertRunSelectedRuntime(w *world.World, want string) error {
	m, err := readRunMetrics(w)
	if err != nil {
		return err
	}
	if m.Runtime != want {
		return fmt.Errorf("metrics.json runtime = %q, want %q", m.Runtime, want)
	}
	return nil
}

func assertRunMetricsReportTokens(w *world.World) error {
	m, err := readRunMetrics(w)
	if err != nil {
		return err
	}
	if m.TokenUsage.Input <= 0 || m.TokenUsage.Output <= 0 {
		return fmt.Errorf("metrics.json token_usage = %+v, want input and output > 0", m.TokenUsage)
	}
	return nil
}

// assertPiTranscriptHasToolCall finds an extracted pi session file
// (first line is the pi session header) and requires at least one
// assistant toolCall block: the agent ran a tool through pi. With security
// enabled the run refuses to start unless the fullsend hook adapter is
// present and intact (exit 97 guard), so a tool call in such a run was
// mediated by the adapter; this step does not inspect hook output itself.
func assertPiTranscriptHasToolCall(w *world.World) error {
	if err := ensureRunArtifacts(w, "metrics.json"); err != nil {
		return err
	}
	var sessions, withToolCall int
	err := filepath.WalkDir(w.ArtifactDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".jsonl" || filepath.Base(filepath.Dir(path)) != "transcripts" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !isPiSessionFile(data) {
			return nil
		}
		sessions++
		if bytes.Contains(data, []byte(`"type":"toolCall"`)) || bytes.Contains(data, []byte(`"type": "toolCall"`)) {
			withToolCall++
		}
		return nil
	})
	if err != nil {
		return err
	}
	if sessions == 0 {
		return fmt.Errorf("no pi session transcript under %s", w.ArtifactDir)
	}
	if withToolCall == 0 {
		return fmt.Errorf("%d pi session transcript(s) under %s but none records a toolCall", sessions, w.ArtifactDir)
	}
	return nil
}

func isPiSessionFile(data []byte) bool {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	if !scanner.Scan() {
		return false
	}
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return false
	}
	return header.Type == "session"
}
