package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	playlistRelPath     = "results/playlist.yaml"
	playbackCommentFile = "playback-comment-url"
	resultFileName      = "result.json"
)

// Playlist is the YAML committed to .fullsend/results/playlist.yaml.
// Results are 1-indexed: current=1 serves results[0].
type Playlist struct {
	Current int      `yaml:"current"`
	Results []string `yaml:"results"`
}

// PlaybackEntry is a single entry in a playback playlist, used by the Gherkin
// step definitions to build the playlist before committing it.
type PlaybackEntry struct {
	// Result is the subdirectory name under .fullsend/results/ that contains
	// this entry's result.json and optional companion files.
	Result string
}

// gitCommitFunc is the function signature for committing playlist advances.
type gitCommitFunc func(playlistPath string, playlist *Playlist) error

// forgeAPIFunc executes a forge CLI API call and returns stdout.
// The default implementation shells out to the CLI binary (gh or glab).
type forgeAPIFunc func(ctx context.Context, cli string, args ...string) ([]byte, error)

// DummyPlaybackRuntime replays canned agent results from an ordered playlist.
// Each invocation serves the result at the current index, writes result.json to
// output/agent-result.json, copies any companion files into the workspace, and
// advances the index via a git commit+push.
//
// ExecFn, UploadFn, GitCommitFn, and ForgeAPIFn are optional test overrides;
// production uses sandbox.Exec, sandbox.Upload, a real git commit+push, and
// exec.CommandContext for forge API calls.
type DummyPlaybackRuntime struct {
	ExecFn      sandboxExecFunc
	UploadFn    sandboxUploadFunc
	GitCommitFn gitCommitFunc
	ForgeAPIFn  forgeAPIFunc
}

func (r DummyPlaybackRuntime) execFn() sandboxExecFunc {
	if r.ExecFn != nil {
		return r.ExecFn
	}
	return sandbox.Exec
}

func (r DummyPlaybackRuntime) uploadFn() sandboxUploadFunc {
	if r.UploadFn != nil {
		return r.UploadFn
	}
	return sandbox.Upload
}

func (r DummyPlaybackRuntime) forgeAPIFn() forgeAPIFunc {
	if r.ForgeAPIFn != nil {
		return r.ForgeAPIFn
	}
	return defaultForgeAPI
}

// defaultForgeAPI shells out to the forge CLI binary.
func defaultForgeAPI(ctx context.Context, cli string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, cli, args...)
	return cmd.Output()
}

func (DummyPlaybackRuntime) Name() string { return "dummy-playback" }

func (DummyPlaybackRuntime) System() string { return "fullsend.dummy-playback" }

func (DummyPlaybackRuntime) ConfigDir() string { return sandbox.SandboxWorkspace + "/.dummy-playback" }

func (DummyPlaybackRuntime) WorkspaceDir() string { return sandbox.SandboxWorkspace }

func (DummyPlaybackRuntime) EnvExports() []string { return nil }

func (r DummyPlaybackRuntime) Bootstrap(input BootstrapInput) error {
	sandboxName := input.SandboxName()
	mkdirCmd := fmt.Sprintf("mkdir -p %s/output %s/.dummy-playback", sandbox.SandboxWorkspace, sandbox.SandboxWorkspace)
	_, stderr, exitCode, err := r.execFn()(sandboxName, mkdirCmd, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dummy-playback bootstrap exec: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("dummy-playback bootstrap failed: %s", strings.TrimSpace(stderr))
	}
	return nil
}

func (r DummyPlaybackRuntime) Run(ctx context.Context, params RunParams, printer *ui.Printer, _ time.Time, _ *RunMetrics) (int, error) {
	playlistPath := filepath.Join(params.FullsendDir, playlistRelPath)
	playlist, err := loadPlaylist(playlistPath)
	if err != nil {
		return 1, err
	}

	var commentRef playbackCommentRef
	if current, ref, ok := readPlaybackComment(ctx, params.FullsendDir, r.forgeAPIFn()); ok {
		playlist.Current = current
		commentRef = ref
	} else if _, statErr := os.Stat(filepath.Join(params.FullsendDir, playbackCommentFile)); statErr == nil {
		printer.StepWarn("dummy-playback: playback-comment-url file exists but could not read tracking comment; using local playlist position")
	}

	if err := ctx.Err(); err != nil {
		return 1, fmt.Errorf("dummy-playback cancelled: %w", err)
	}

	idx := playlist.Current - 1
	if idx < 0 || idx >= len(playlist.Results) {
		if commentRef.path != "" {
			return 1, fmt.Errorf("tracking comment returned invalid position: current=%d, results=%d", playlist.Current, len(playlist.Results))
		}
		return 1, fmt.Errorf("playlist exhausted: current=%d, results=%d", playlist.Current, len(playlist.Results))
	}

	entryName := strings.TrimSpace(playlist.Results[idx])
	if entryName == "" {
		return 1, fmt.Errorf("playlist entry %d is empty", playlist.Current)
	}

	entryDir := filepath.Join(params.FullsendDir, "results", entryName)
	resultsBase := filepath.Clean(filepath.Join(params.FullsendDir, "results"))
	if !strings.HasPrefix(filepath.Clean(entryDir), resultsBase+string(filepath.Separator)) {
		return 1, fmt.Errorf("entry name %q escapes results directory", entryName)
	}
	resultPath := filepath.Join(entryDir, resultFileName)
	// Guard against symlinks in the result file, matching the companion-file
	// symlink rejection in copyCompanionFiles.
	if fi, lstatErr := os.Lstat(resultPath); lstatErr != nil {
		return 1, fmt.Errorf("reading result %s: %w", entryName, lstatErr)
	} else if fi.Mode()&fs.ModeSymlink != 0 {
		return 1, fmt.Errorf("symlinks are not allowed in playlist entries: %s", resultPath)
	}
	content, err := os.ReadFile(resultPath)
	if err != nil {
		return 1, fmt.Errorf("reading result %s: %w", entryName, err)
	}

	content = injectReviewMetadata(content, params.Forge, printer)

	if err := r.writeAgentResult(params.SandboxName, content); err != nil {
		return 1, err
	}

	hasRepoFiles, err := r.copyCompanionFiles(params.SandboxName, params.RepoDir, entryDir, printer)
	if err != nil {
		return 1, err
	}

	if hasRepoFiles {
		if strings.HasPrefix(entryName, "fix/") {
			if err := r.commitToCurrentBranch(params.SandboxName, params.RepoDir, entryName, printer); err != nil {
				return 1, err
			}
		} else {
			if err := r.createFeatureBranch(params.SandboxName, params.RepoDir, entryName, printer); err != nil {
				return 1, err
			}
		}
	}

	printer.StepInfo(fmt.Sprintf("dummy-playback: served result %d/%d (%s)", playlist.Current, len(playlist.Results), entryName))

	playlist.Current++
	if err := r.advancePlaylist(playlistPath, playlist); err != nil {
		printer.StepWarn(fmt.Sprintf("dummy-playback: failed to advance playlist: %v", err))
	}
	if commentRef.path != "" {
		if err := updatePlaybackComment(ctx, commentRef, playlist.Current, r.forgeAPIFn()); err != nil {
			printer.StepWarn(fmt.Sprintf("dummy-playback: failed to update tracking comment: %v", err))
		}
	}

	return 0, nil
}

// copyCompanionFiles walks the entry directory and uploads any file that is not
// result.json into the sandbox. Files under a repo/ subdirectory are placed
// relative to repoDir (the target repo checkout inside the sandbox), simulating
// code changes the agent would have made. All other files are placed relative
// to the workspace root. Files in a forge-specific subdirectory (e.g. gitlab/)
// are skipped here — forge resolution happens at commit time in the Gherkin
// step, so the entry directory the runtime sees already has the correct files.
func (r DummyPlaybackRuntime) copyCompanionFiles(sandboxName, repoDir, entryDir string, printer *ui.Printer) (bool, error) {
	if info, err := os.Lstat(entryDir); err != nil {
		return false, fmt.Errorf("stat entry dir: %w", err)
	} else if info.Mode()&fs.ModeSymlink != 0 {
		return false, fmt.Errorf("symlinks are not allowed as entry directories: %s", entryDir)
	}
	hasRepoFiles := false
	err := filepath.WalkDir(entryDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not allowed in playlist entries: %s", path)
		}
		if path == filepath.Join(entryDir, resultFileName) {
			return nil
		}

		relPath, relErr := filepath.Rel(entryDir, path)
		if relErr != nil {
			return fmt.Errorf("computing relative path for %s: %w", path, relErr)
		}

		var remoteDest string
		if strings.HasPrefix(relPath, "repo/") || strings.HasPrefix(relPath, "repo"+string(filepath.Separator)) {
			repoRel := strings.TrimPrefix(relPath, "repo/")
			remoteDest = filepath.Join(repoDir, repoRel)
			hasRepoFiles = true
		} else {
			remoteDest = filepath.Join(sandbox.SandboxWorkspace, relPath)
		}

		parentDir := filepath.Dir(remoteDest)
		mkdirCmd := fmt.Sprintf("mkdir -p %s", shellQuote(parentDir))
		if _, _, _, execErr := r.execFn()(sandboxName, mkdirCmd, 10*time.Second); execErr != nil {
			return fmt.Errorf("dummy-playback mkdir for companion %s: %w", relPath, execErr)
		}

		if uploadErr := r.uploadFn()(sandboxName, path, remoteDest); uploadErr != nil {
			return fmt.Errorf("dummy-playback upload companion %s: %w", relPath, uploadErr)
		}

		printer.StepInfo(fmt.Sprintf("dummy-playback: copied companion file %s", relPath))
		return nil
	})
	return hasRepoFiles, err
}

// createFeatureBranch creates a git branch inside the sandbox and commits all
// changes in the target repo. This simulates what a real code agent does —
// without a feature branch the post-code script skips PR creation.
func (r DummyPlaybackRuntime) createFeatureBranch(sandboxName, repoDir, entryName string, printer *ui.Printer) error {
	branchName := "fullsend/playback-" + strings.ReplaceAll(entryName, "/", "-")
	cmds := []string{
		fmt.Sprintf("cd %s && git checkout -b %s", shellQuote(repoDir), shellQuote(branchName)),
		fmt.Sprintf("cd %s && git add -A", shellQuote(repoDir)),
		fmt.Sprintf("cd %s && git -c user.name=fullsend-playback -c user.email=playback@fullsend.invalid commit -m %s", shellQuote(repoDir), shellQuote("chore: playback "+entryName)),
	}
	for _, cmd := range cmds {
		if _, _, _, err := r.execFn()(sandboxName, cmd, 30*time.Second); err != nil {
			return fmt.Errorf("dummy-playback branch setup: %w", err)
		}
	}
	printer.StepInfo(fmt.Sprintf("dummy-playback: created feature branch %s", branchName))
	return nil
}

// commitToCurrentBranch commits companion file changes to whatever branch is
// already checked out. Used for fix entries where the sandbox repo is already
// on the PR branch — creating a new branch would break the post-fix push.
func (r DummyPlaybackRuntime) commitToCurrentBranch(sandboxName, repoDir, entryName string, printer *ui.Printer) error {
	cmds := []string{
		fmt.Sprintf("cd %s && git add -A", shellQuote(repoDir)),
		fmt.Sprintf("cd %s && git -c user.name=fullsend-playback -c user.email=playback@fullsend.invalid commit -m %s", shellQuote(repoDir), shellQuote("fix: playback "+entryName)),
	}
	for _, cmd := range cmds {
		if _, _, _, err := r.execFn()(sandboxName, cmd, 30*time.Second); err != nil {
			return fmt.Errorf("dummy-playback fix commit: %w", err)
		}
	}
	printer.StepInfo("dummy-playback: committed fix to current branch")
	return nil
}

// ClearIterationArtifacts sweeps stray processes (dummy-playback execs run in
// the real sandbox, so it gets the same between-iteration hygiene as the
// agent runtimes), then removes the previous iteration's output.
func (r DummyPlaybackRuntime) ClearIterationArtifacts(sandboxName string) error {
	clearStrayProcesses(r.execFn(), sandboxName, os.Stderr)
	clearCmd := fmt.Sprintf("rm -rf %s/output/*", r.WorkspaceDir())
	_, stderr, exitCode, err := r.execFn()(sandboxName, clearCmd, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dummy-playback clear iteration artifacts exec: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("dummy-playback clear iteration artifacts failed: %s", strings.TrimSpace(stderr))
	}
	return nil
}

func (DummyPlaybackRuntime) ExtractTranscripts(_ string, _ string, _ string) error { return nil }

func (DummyPlaybackRuntime) ExtractDebugLog(_ string, _ string, _ string) error { return nil }

func (DummyPlaybackRuntime) ParseTranscriptErrors(_ string) []TranscriptError { return nil }

func (DummyPlaybackRuntime) ParseTranscriptFile(_ string) (TranscriptError, bool) {
	return TranscriptError{}, false
}

func (DummyPlaybackRuntime) EmitTranscriptErrors(w io.Writer, summaries []TranscriptError) {
	emitTranscriptErrors(w, summaries)
}

func loadPlaylist(path string) (*Playlist, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading playlist %s: %w", path, err)
	}
	var playlist Playlist
	if err := yaml.Unmarshal(data, &playlist); err != nil {
		return nil, fmt.Errorf("parsing playlist %s: %w", path, err)
	}
	if len(playlist.Results) == 0 {
		return nil, fmt.Errorf("playlist %s has no results", path)
	}
	if playlist.Current < 1 {
		return nil, fmt.Errorf("playlist %s: current must be >= 1, got %d", path, playlist.Current)
	}
	return &playlist, nil
}

func (r DummyPlaybackRuntime) writeAgentResult(sandboxName string, content []byte) error {
	remoteDest := filepath.Join(sandbox.SandboxWorkspace, "output", "agent-result.json")
	parentDir := filepath.Dir(remoteDest)
	mkdirCmd := fmt.Sprintf("mkdir -p %s", shellQuote(parentDir))
	if _, _, _, err := r.execFn()(sandboxName, mkdirCmd, 10*time.Second); err != nil {
		return fmt.Errorf("dummy-playback mkdir: %w", err)
	}

	tmp, err := os.CreateTemp("", "playback-result-*")
	if err != nil {
		return fmt.Errorf("dummy-playback temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("dummy-playback write temp: %w", err)
	}
	tmp.Close()

	if err := r.uploadFn()(sandboxName, tmp.Name(), remoteDest); err != nil {
		return fmt.Errorf("dummy-playback upload: %w", err)
	}
	return nil
}

func (r DummyPlaybackRuntime) advancePlaylist(playlistPath string, playlist *Playlist) error {
	if r.GitCommitFn != nil {
		return r.GitCommitFn(playlistPath, playlist)
	}
	return localAdvancePlaylist(playlistPath, playlist)
}

// playbackCommentRef holds the CLI tool and API path for tracking comment
// operations. The file format is "<cli>\n<path>" (e.g. "gh\n/repos/…" or
// "glab\n/projects/…"). Legacy single-line files default to gh.
type playbackCommentRef struct {
	cli    string // "gh" or "glab"
	path   string // forge-specific REST API path
	method string // HTTP method for updates: "PATCH" (GitHub) or "PUT" (GitLab)
}

func parsePlaybackCommentRef(data string) (playbackCommentRef, bool) {
	data = strings.TrimRight(data, "\n\r ")
	cli, path, found := strings.Cut(data, "\n")
	cli = strings.TrimSpace(cli)
	if cli == "" {
		return playbackCommentRef{}, false
	}
	if !found {
		// Legacy single-line format: the entire value is the API path.
		// Validate it starts with "/" to prevent argument injection
		// (e.g. "--hostname=attacker.com" interpreted as a flag by gh).
		if !strings.HasPrefix(cli, "/") {
			return playbackCommentRef{}, false
		}
		return playbackCommentRef{cli: "gh", path: cli, method: "PATCH"}, true
	}
	if cli != "gh" && cli != "glab" {
		return playbackCommentRef{}, false
	}
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsAny(path, "\n\r") {
		return playbackCommentRef{}, false
	}
	// Validate path starts with "/" to prevent argument injection.
	if !strings.HasPrefix(path, "/") {
		return playbackCommentRef{}, false
	}
	method := "PATCH"
	if cli == "glab" {
		method = "PUT"
	}
	return playbackCommentRef{cli: cli, path: path, method: method}, true
}

func readPlaybackComment(ctx context.Context, fullsendDir string, apiFn forgeAPIFunc) (int, playbackCommentRef, bool) {
	data, err := os.ReadFile(filepath.Join(fullsendDir, playbackCommentFile))
	if err != nil {
		return 0, playbackCommentRef{}, false
	}
	ref, ok := parsePlaybackCommentRef(string(data))
	if !ok {
		return 0, playbackCommentRef{}, false
	}
	apiCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := apiFn(apiCtx, ref.cli, "api", ref.path, "--jq", ".body")
	if err != nil {
		return 0, playbackCommentRef{}, false
	}
	body := strings.TrimSpace(string(out))
	if !strings.HasPrefix(body, "playback-current: ") {
		return 0, playbackCommentRef{}, false
	}
	val, err := strconv.Atoi(strings.TrimPrefix(body, "playback-current: "))
	if err != nil {
		return 0, playbackCommentRef{}, false
	}
	if val < 1 {
		return 0, playbackCommentRef{}, false
	}
	return val, ref, true
}

func updatePlaybackComment(ctx context.Context, ref playbackCommentRef, newValue int, apiFn forgeAPIFunc) error {
	apiCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body := fmt.Sprintf("playback-current: %d", newValue)
	_, err := apiFn(apiCtx, ref.cli, "api", "--method", ref.method, ref.path,
		"-f", fmt.Sprintf("body=%s", body))
	if err != nil {
		return fmt.Errorf("updating playback comment: %w", err)
	}
	return nil
}

func injectReviewMetadata(content []byte, forge string, printer *ui.Printer) []byte {
	var result map[string]any
	if err := json.Unmarshal(content, &result); err != nil {
		return content
	}

	action, ok := result["action"]
	if !ok {
		return content
	}
	actionStr, _ := action.(string)
	if !isReviewAction(actionStr) {
		return content
	}

	injected := false

	if sha := os.Getenv("PR_HEAD_SHA"); sha != "" {
		result["head_sha"] = sha
		display := sha
		if len(display) > 12 {
			display = display[:12]
		}
		printer.StepInfo(fmt.Sprintf("dummy-playback: injected head_sha=%s", display))
		injected = true
	}

	if numStr := os.Getenv("STATUS_NUMBER"); numStr != "" {
		if num, err := strconv.Atoi(numStr); err == nil {
			result["pr_number"] = num
			printer.StepInfo(fmt.Sprintf("dummy-playback: injected pr_number=%d", num))
			injected = true
		}
	}

	if repo := repoFromEnv(forge); repo != "" {
		result["repo"] = repo
		printer.StepInfo(fmt.Sprintf("dummy-playback: injected repo=%s", repo))
		injected = true
	}

	if !injected {
		return content
	}

	modified, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return content
	}
	return modified
}

func repoFromEnv(forge string) string {
	switch forge {
	case "gitlab":
		return os.Getenv("CI_PROJECT_PATH")
	default:
		return os.Getenv("GITHUB_REPOSITORY")
	}
}

func isReviewAction(action string) bool {
	switch action {
	case "approve", "request-changes", "comment", "reject", "failure":
		return true
	}
	return false
}

func localAdvancePlaylist(playlistPath string, playlist *Playlist) error {
	data, err := yaml.Marshal(playlist)
	if err != nil {
		return fmt.Errorf("marshaling playlist: %w", err)
	}
	if err := os.WriteFile(playlistPath, data, 0o644); err != nil {
		return fmt.Errorf("writing playlist: %w", err)
	}
	return nil
}
