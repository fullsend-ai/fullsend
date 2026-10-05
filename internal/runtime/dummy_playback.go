package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
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

// DummyPlaybackRuntime replays canned agent results from an ordered playlist.
// Each invocation serves the result at the current index, writes result.json to
// output/agent-result.json, copies any companion files into the workspace, and
// advances the index via a git commit+push.
//
// ExecFn, UploadFn, and GitCommitFn are optional test overrides; production
// uses sandbox.Exec, sandbox.Upload, and a real git commit+push. Forge API
// calls (reading/updating the tracking comment) go through
// RunParams.ForgeClient — per the forge-abstraction rule, this runtime must
// not shell out to `gh`/`glab` itself.
type DummyPlaybackRuntime struct {
	ExecFn      sandboxExecFunc
	UploadFn    sandboxUploadFunc
	GitCommitFn gitCommitFunc
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

func (DummyPlaybackRuntime) Name() string { return "dummy-playback" }

func (DummyPlaybackRuntime) System() string { return "fullsend.dummy-playback" }

func (DummyPlaybackRuntime) ConfigDir() string { return sandbox.SandboxWorkspace + "/.dummy-playback" }

func (DummyPlaybackRuntime) WorkspaceDir() string { return sandbox.SandboxWorkspace }

func (DummyPlaybackRuntime) EnvExports() []string { return nil }

func (r DummyPlaybackRuntime) Bootstrap(input BootstrapInput) error {
	sandboxName := input.SandboxName()
	// Same contract as DummyRuntime: every declared plugin (ADR 0094) is
	// named, with its format, and skipped rather than silently dropped.
	for _, e := range input.Plugins() {
		if e.Path != "" {
			fmt.Fprintf(os.Stderr, "Plugin %q (%s): skipped — the dummy-playback runtime loads no plugins (see docs/runtimes.md)\n", e.SandboxName(), e.Kind)
		}
	}
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
	if current, ref, ok := readPlaybackComment(ctx, params.FullsendDir, params.ForgeClient); ok {
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
		if !commentRef.isZero() {
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
	if !commentRef.isZero() {
		if err := updatePlaybackComment(ctx, params.ForgeClient, commentRef, playlist.Current); err != nil {
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
	clearStrayProcesses(r.execFn(), sandboxName, os.Stderr, "the previous iteration")
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

// githubCommentPathRe and gitlabCommentPathRe recognize the REST API path
// shapes playback_driver.go commits into the tracking-comment file, and
// extract the owner, repo, and comment/note ID (plus, for GitLab, the
// noteable type and IID) so the runtime can address the comment through
// forge.Client instead of replaying the path against the forge CLI. The
// GitLab project segment is percent-encoded ("org%2Frepo").
//
// Owner and repo segments are restricted to the charset GitHub/GitLab
// actually allow in identifiers (alphanumerics, hyphens, underscores,
// dots): a wider class such as `[^/]+` would also accept URL delimiters
// (`?`, `#`) or encoded separators, letting a crafted path redirect the
// request to a different endpoint or smuggle query data once owner/repo
// are interpolated back into a request path (see decodeCommentPath and
// the GitHub client's GetIssueComment/UpdateIssueComment).
//
// The GitLab project segment's capture group only restricts the overall
// charset (still excluding URL delimiters); it does not anchor the first
// and last characters, because those rules differ per decoded path
// component (and GitLab allows a leading underscore on a namespace and a
// trailing underscore/hyphen on a project name). decodeCommentPath
// validates each decoded namespace/project component individually against
// GitLab's own identifier rules.
var (
	githubCommentPathRe = regexp.MustCompile(`^/repos/([A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)/([A-Za-z0-9._-]+)/issues/comments/(\d+)$`)
	gitlabCommentPathRe = regexp.MustCompile(`^/projects/([A-Za-z0-9._%-]+)/(issues|merge_requests)/(\d+)/notes/(\d+)$`)
)

// playbackCommentRef identifies the forge comment the dummy-playback
// runtime uses to track its position across sandbox invocations, resolved
// to the fields forge.Client (and, for GitLab, forge.GitLabExtensions)
// operate on. The committed file format is "<cli>\n<path>" (e.g.
// "gh\n/repos/…" or "glab\n/projects/…"); legacy single-line files default
// to gh. cli only selects which regex decodes path — the actual forge call
// goes through whichever forge.Client the caller supplies via
// RunParams.ForgeClient, which must already correspond to the active
// Forge platform.
//
// noteableType and noteableIID are populated only for GitLab refs (empty
// and zero for GitHub). They identify the note's parent (an issue or a
// merge request) directly, so readPlaybackComment/updatePlaybackComment
// can address it through forge.GitLabExtensions instead of the ID-only
// scan GetIssueComment/UpdateIssueComment fall back to — a scan that is
// driven by the resolved client's own fixed noteTarget and so cannot find
// a note whose actual parent type differs from how that client was built
// (e.g. an issue tracking reference during MR CI).
type playbackCommentRef struct {
	owner        string
	repo         string
	commentID    int
	noteableType string
	noteableIID  int
}

func parsePlaybackCommentRef(data string) (playbackCommentRef, bool) {
	data = strings.TrimRight(data, "\n\r ")
	cli, path, found := strings.Cut(data, "\n")
	cli = strings.TrimSpace(cli)
	if cli == "" {
		return playbackCommentRef{}, false
	}
	if !found {
		// Legacy single-line format: the entire value is the GitHub API path.
		return decodeCommentPath("gh", cli)
	}
	if cli != "gh" && cli != "glab" {
		return playbackCommentRef{}, false
	}
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsAny(path, "\n\r") {
		return playbackCommentRef{}, false
	}
	return decodeCommentPath(cli, path)
}

// decodeCommentPath extracts owner, repo, and commentID from a GitHub or
// GitLab REST API path. Rejecting anything that does not match one of the
// two known shapes is deliberate: the path previously doubled as literal
// CLI arguments (and so was validated only against argument injection);
// now that it is parsed into structured fields instead of being replayed
// on a command line, requiring an exact match is the natural tightening
// of that same validation.
func decodeCommentPath(cli, path string) (playbackCommentRef, bool) {
	switch cli {
	case "gh":
		m := githubCommentPathRe.FindStringSubmatch(path)
		if m == nil {
			return playbackCommentRef{}, false
		}
		commentID, err := strconv.Atoi(m[3])
		if err != nil {
			return playbackCommentRef{}, false
		}
		return playbackCommentRef{owner: m[1], repo: m[2], commentID: commentID}, true
	case "glab":
		m := gitlabCommentPathRe.FindStringSubmatch(path)
		if m == nil {
			return playbackCommentRef{}, false
		}
		decoded, err := url.PathUnescape(m[1])
		if err != nil {
			return playbackCommentRef{}, false
		}
		// GitLab project paths can be nested ("group/subgroup/project"), so
		// the repo (leaf project name) is everything after the last slash.
		// Each namespace component and the project leaf have different
		// GitLab-compatible identifier rules (see isGitLabNamespaceComponent
		// and isGitLabProjectComponent), so validate them individually
		// rather than requiring the whole decoded path to start and end
		// alphanumeric.
		parts := strings.Split(decoded, "/")
		if len(parts) < 2 {
			return playbackCommentRef{}, false
		}
		namespaces, repo := parts[:len(parts)-1], parts[len(parts)-1]
		for _, ns := range namespaces {
			if !isGitLabNamespaceComponent(ns) {
				return playbackCommentRef{}, false
			}
		}
		if !isGitLabProjectComponent(repo) {
			return playbackCommentRef{}, false
		}
		owner := strings.Join(namespaces, "/")
		noteableType := m[2]
		noteableIID, err := strconv.Atoi(m[3])
		if err != nil {
			return playbackCommentRef{}, false
		}
		commentID, err := strconv.Atoi(m[4])
		if err != nil {
			return playbackCommentRef{}, false
		}
		return playbackCommentRef{
			owner:        owner,
			repo:         repo,
			commentID:    commentID,
			noteableType: noteableType,
			noteableIID:  noteableIID,
		}, true
	default:
		return playbackCommentRef{}, false
	}
}

// isGitLabNamespaceComponent reports whether s is a valid single path
// segment for a GitLab user or group name, mirroring upstream's
// NAMESPACE_FORMAT_REGEX (see
// https://github.com/gitlabhq/gitlabhq/blob/master/lib/gitlab/path_regex.rb):
// a single character must be alphanumeric or an underscore; a longer
// segment may start with an alphanumeric, underscore, or dot, may
// contain alphanumerics, underscores, hyphens, and dots in the middle,
// and must end with an alphanumeric, underscore, or hyphen. Notably, a
// leading underscore is allowed (e.g. "_namespace").
func isGitLabNamespaceComponent(s string) bool {
	if s == "" {
		return false
	}
	if len(s) == 1 {
		c := s[0]
		return isAlphanumByte(c) || c == '_'
	}
	first := s[0]
	if !isAlphanumByte(first) && first != '_' && first != '.' {
		return false
	}
	last := s[len(s)-1]
	if !isAlphanumByte(last) && last != '_' && last != '-' {
		return false
	}
	for i := 1; i < len(s)-1; i++ {
		c := s[i]
		if !isAlphanumByte(c) && c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

// isGitLabProjectComponent reports whether s is a valid GitLab project
// (repository) path segment, mirroring upstream's
// PROJECT_PATH_FORMAT_REGEX: it must start with an alphanumeric,
// underscore, or dot, and may otherwise contain alphanumerics,
// underscores, hyphens, and dots anywhere else in the segment — including
// as the last character, so a trailing underscore or hyphen (e.g.
// "repo_", "repo-") is valid. A trailing ".git" or ".atom" suffix is
// reserved by GitLab and rejected.
func isGitLabProjectComponent(s string) bool {
	if s == "" {
		return false
	}
	first := s[0]
	if !isAlphanumByte(first) && first != '_' && first != '.' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !isAlphanumByte(c) && c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	return !strings.HasSuffix(s, ".git") && !strings.HasSuffix(s, ".atom")
}

func isAlphanumByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// isZero reports whether ref is the zero value, i.e. parsing or resolving
// the tracking comment failed and there is nothing to read or update.
func (ref playbackCommentRef) isZero() bool {
	return ref == playbackCommentRef{}
}

func readPlaybackComment(ctx context.Context, fullsendDir string, client forge.Client) (int, playbackCommentRef, bool) {
	data, err := os.ReadFile(filepath.Join(fullsendDir, playbackCommentFile))
	if err != nil {
		return 0, playbackCommentRef{}, false
	}
	ref, ok := parsePlaybackCommentRef(string(data))
	if !ok || client == nil {
		return 0, playbackCommentRef{}, false
	}
	apiCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	comment, err := fetchPlaybackComment(apiCtx, client, ref)
	if err != nil {
		return 0, playbackCommentRef{}, false
	}
	body := strings.TrimSpace(comment.Body)
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

func updatePlaybackComment(ctx context.Context, client forge.Client, ref playbackCommentRef, newValue int) error {
	if client == nil {
		return fmt.Errorf("updating playback comment: no forge client available")
	}
	apiCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body := fmt.Sprintf("playback-current: %d", newValue)
	if err := updatePlaybackCommentBody(apiCtx, client, ref, body); err != nil {
		return fmt.Errorf("updating playback comment: %w", err)
	}
	return nil
}

// fetchPlaybackComment reads the tracking comment identified by ref. For a
// GitLab ref (ref.noteableType set) it addresses the note directly by its
// parent's type and IID via forge.GitLabExtensions when the resolved
// client supports it, instead of client.GetIssueComment's ID-only scan —
// a scan bounded by, and driven by, the client's own fixed noteTarget, so
// it cannot find a note whose actual parent type differs from how that
// client was built (e.g. an issue tracking reference during MR CI). A
// GitHub ref, or a GitLab client that does not implement the extension,
// falls back to the cross-forge GetIssueComment.
func fetchPlaybackComment(ctx context.Context, client forge.Client, ref playbackCommentRef) (*forge.IssueComment, error) {
	if ref.noteableType != "" {
		if gl, ok := client.(forge.GitLabExtensions); ok {
			return gl.GetNoteOnParent(ctx, ref.owner, ref.repo, ref.noteableType, ref.noteableIID, ref.commentID)
		}
	}
	return client.GetIssueComment(ctx, ref.owner, ref.repo, ref.commentID)
}

// updatePlaybackCommentBody updates the tracking comment identified by
// ref. See fetchPlaybackComment for the direct-addressing rationale.
func updatePlaybackCommentBody(ctx context.Context, client forge.Client, ref playbackCommentRef, body string) error {
	if ref.noteableType != "" {
		if gl, ok := client.(forge.GitLabExtensions); ok {
			return gl.UpdateNoteOnParent(ctx, ref.owner, ref.repo, ref.noteableType, ref.noteableIID, ref.commentID, body)
		}
	}
	return client.UpdateIssueComment(ctx, ref.owner, ref.repo, ref.commentID, body)
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
