package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/runtime"
)

// tirithRepoPolicyPath is where a repository keeps the tirith policy that
// tightens command scanning for agent runs on it.
const tirithRepoPolicyPath = ".tirith/policy.yaml"

// tirithRepoPolicyMaxBytes bounds the staged policy. A tirith policy is a
// short YAML document; anything larger is refused rather than uploaded.
const tirithRepoPolicyMaxBytes = 256 << 10

// tirithBaseFetchTimeout bounds fetching a request's base commit when the
// checkout does not have it.
const tirithBaseFetchTimeout = 2 * time.Minute

// gitObjectIDPattern matches a full SHA-1 or SHA-256 object name. The base SHA
// comes from the event payload, so it is checked before it reaches git.
var gitObjectIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// tirithPolicySource says which commit holds the copy of the repository
// policy the run trusts.
type tirithPolicySource struct {
	// requestRun is set for pull and merge request runs, whose checkout may
	// be the request's head.
	requestRun bool
	// baseSHA is the request's base commit from the event payload, when
	// known. Only consulted for request runs.
	baseSHA string
}

// tirithPolicyNote is the outcome of staging the repository policy, for the
// run log and, when omitted is set, for the operator.
type tirithPolicyNote struct {
	// text is the line to print. Empty when there is nothing to say.
	text string
	// omitted is set when a pull or merge request run stages no repository
	// policy because its base commit is unavailable. The run continues on
	// tirith's baseline; the caller reports it beyond the run log.
	omitted bool
}

// omittedTirithPolicy builds the note for a request run that stages nothing.
// Keep it within the status comment's 200-character detail limit.
func omittedTirithPolicy(reason string) tirithPolicyNote {
	return tirithPolicyNote{
		text: "Repository tirith policy not staged: the " + reason +
			". This request run uses tirith's built-in rules; the head copy is never used.",
		omitted: true,
	}
}

// mergeRequestBaseResolver returns the base commit of the merge request the
// run is for, from the forge API. Used when the event payload carries none.
type mergeRequestBaseResolver func(ctx context.Context) (string, error)

// tirithPolicySourceFromEnv classifies the run from the environment the
// dispatch workflows set. Review and fix runs on GitHub set PR_NUMBER and
// GITHUB_PR_URL; GitLab merge request jobs set MR_IID or the CI merge request
// variables. The base SHA is read from the GitHub event payload, directly or
// from the dispatch workflow's event_payload input.
func tirithPolicySourceFromEnv(getenv func(string) string) tirithPolicySource {
	src := tirithPolicySource{}
	for _, name := range []string{"PR_NUMBER", "GITHUB_PR_URL", "MR_IID", "CI_MERGE_REQUEST_IID"} {
		if strings.TrimSpace(getenv(name)) != "" {
			src.requestRun = true
		}
	}
	if strings.TrimSpace(getenv("FULLSEND_NOTE_TARGET")) == "merge_requests" {
		src.requestRun = true
	}
	if base := prBaseSHAFromEventPath(getenv("GITHUB_EVENT_PATH")); base != "" {
		src.requestRun = true
		src.baseSHA = base
	}
	return src
}

// prBaseSHAFromEventPath returns pull_request.base.sha from a GitHub event
// file, or from the JSON event_payload input a dispatch workflow carries.
// Mirrors prHeadSHAFromEventPath.
func prBaseSHAFromEventPath(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	type prBase struct {
		PullRequest struct {
			Base struct {
				SHA string `json:"sha"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	var event struct {
		prBase
		Inputs struct {
			EventPayload string `json:"event_payload"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return ""
	}
	if event.PullRequest.Base.SHA != "" {
		return event.PullRequest.Base.SHA
	}
	if event.Inputs.EventPayload == "" {
		return ""
	}
	var payload prBase
	if err := json.Unmarshal([]byte(event.Inputs.EventPayload), &payload); err != nil {
		return ""
	}
	return payload.PullRequest.Base.SHA
}

// trustedTirithRepoPolicy returns the repository's .tirith/policy.yaml from
// the commit the run trusts, plus a note for the run log. It reads git
// objects, never the working tree, so nothing written to the checkout after
// it was made changes what is staged.
//
// For an ordinary run the trusted commit is the checkout's HEAD (the default
// branch or the branch the run targets). For a pull or merge request run the
// checkout may be the request's head, so only the base commit is used. When
// the checkout does not have it (a shallow checkout), that one commit is
// fetched from the checkout's origin remote. When the base commit is unknown
// or cannot be fetched, nothing is staged and the note is marked omitted:
// the head copy is never used.
//
// A missing file, a directory that is not a git checkout, or an empty file
// stage nothing. A path that is not a regular file (a symlink or submodule)
// or a file over tirithRepoPolicyMaxBytes is an error, so an owner's policy
// is never silently dropped.
func trustedTirithRepoPolicy(ctx context.Context, repoDir string, src tirithPolicySource) ([]byte, tirithPolicyNote, error) {
	rev := "HEAD"
	if src.requestRun {
		base := strings.ToLower(strings.TrimSpace(src.baseSHA))
		if !gitObjectIDPattern.MatchString(base) {
			return nil, omittedTirithPolicy("base commit is not known"), nil
		}
		if !gitCommitExists(ctx, repoDir, base) {
			if err := fetchGitCommit(ctx, repoDir, base); err != nil || !gitCommitExists(ctx, repoDir, base) {
				// The git error is not shown: a remote URL in it can carry a
				// credential, and the note is posted beyond the run log.
				return nil, omittedTirithPolicy(fmt.Sprintf("base commit %s is missing from the checkout and could not be fetched", shortRev(base))), nil
			}
		}
		rev = base
	} else if !gitCommitExists(ctx, repoDir, rev) {
		// Not a git checkout, or one with no commits: there is no trusted ref.
		return nil, tirithPolicyNote{}, nil
	}

	data, err := readTirithRepoPolicyAt(ctx, repoDir, rev)
	if err != nil || data == nil {
		return nil, tirithPolicyNote{}, err
	}
	return data, tirithPolicyNote{text: fmt.Sprintf("Staging repository tirith policy %s from %s", tirithRepoPolicyPath, shortRev(rev))}, nil
}

// readTirithRepoPolicyAt reads the policy file from commit rev. A missing or
// empty file returns nil.
func readTirithRepoPolicyAt(ctx context.Context, repoDir, rev string) ([]byte, error) {
	out, err := runGit(ctx, repoDir, "ls-tree", "-z", rev, "--", tirithRepoPolicyPath)
	if err != nil {
		return nil, fmt.Errorf("listing %s at %s: %w", tirithRepoPolicyPath, rev, err)
	}
	entry := strings.TrimSuffix(string(out), "\x00")
	if entry == "" {
		return nil, nil
	}
	// "<mode> SP <type> SP <object> TAB <path>"
	meta, _, ok := strings.Cut(entry, "\t")
	fields := strings.Fields(meta)
	if !ok || len(fields) != 3 {
		return nil, fmt.Errorf("unexpected git ls-tree output for %s at %s", tirithRepoPolicyPath, rev)
	}
	mode, kind, oid := fields[0], fields[1], fields[2]
	if kind != "blob" || (mode != "100644" && mode != "100755") {
		return nil, fmt.Errorf("%s at %s is not a regular file (mode %s, %s); refusing to stage it", tirithRepoPolicyPath, rev, mode, kind)
	}
	if !gitObjectIDPattern.MatchString(oid) {
		return nil, fmt.Errorf("unexpected object name for %s at %s", tirithRepoPolicyPath, rev)
	}
	sizeOut, err := runGit(ctx, repoDir, "cat-file", "-s", oid)
	if err != nil {
		return nil, fmt.Errorf("sizing %s at %s: %w", tirithRepoPolicyPath, rev, err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeOut)), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("sizing %s at %s: %w", tirithRepoPolicyPath, rev, err)
	}
	if size > tirithRepoPolicyMaxBytes {
		return nil, fmt.Errorf("%s at %s is %d bytes, over the %d-byte limit; refusing to stage it", tirithRepoPolicyPath, rev, size, tirithRepoPolicyMaxBytes)
	}
	if size == 0 {
		return nil, nil
	}
	data, err := runGit(ctx, repoDir, "cat-file", "blob", oid)
	if err != nil {
		return nil, fmt.Errorf("reading %s at %s: %w", tirithRepoPolicyPath, rev, err)
	}
	return data, nil
}

// attachTirithRepoPolicy stages the trusted repository tirith policy on a
// bootstrap input that installs sandbox hooks, and returns the note to print.
// Inputs without hooks (security disabled) are left alone, as is an empty
// repoDir: without a target repository there is no policy to read.
//
// resolveBase, when not nil, supplies the base commit of a merge request run
// whose event payload carries none (GitLab). It is only called for request
// runs.
func attachTirithRepoPolicy(ctx context.Context, boot runtime.BootstrapInput, repoDir string, getenv func(string) string, resolveBase mergeRequestBaseResolver) (tirithPolicyNote, error) {
	hb, ok := boot.(*harnessBootstrapWithHooks)
	if !ok || repoDir == "" {
		return tirithPolicyNote{}, nil
	}
	src := tirithPolicySourceFromEnv(getenv)
	if src.requestRun && src.baseSHA == "" && resolveBase != nil {
		base, err := resolveBase(ctx)
		if err != nil {
			// The API error is not shown: the note is posted beyond the run log.
			return omittedTirithPolicy("merge request's base commit could not be read from the forge API"), nil
		}
		src.baseSHA = base
	}
	policy, note, err := trustedTirithRepoPolicy(ctx, repoDir, src)
	if err != nil {
		return tirithPolicyNote{}, err
	}
	hb.hooks = hb.hooks.WithTirithRepoPolicy(policy)
	return note, nil
}

// mergeRequestBaseSHA returns the base commit of merge request number in
// project ("namespace/project") from the forge. project and number must come
// from the signed dispatch, not from overridable CI variables.
func mergeRequestBaseSHA(ctx context.Context, client forge.Client, project string, number int) (string, error) {
	owner, name, ok := splitProjectPath(project)
	if !ok {
		return "", fmt.Errorf("project must be in namespace/project format, got %q", project)
	}
	info, err := client.GetPullRequestInfo(ctx, owner, name, number)
	if err != nil {
		return "", err
	}
	if info.BaseSHA == "" {
		return "", fmt.Errorf("merge request !%d reports no base commit", number)
	}
	return info.BaseSHA, nil
}

// gitLabMergeRequestBaseResolver returns a resolver that reads the base
// commit of the run's GitLab merge request through the forge API, or nil
// when the run is not on GitLab or names no merge request. The project and
// number are --status-repo and --status-number, which the GitLab job sets
// from the pinned project path and the signed dispatch IID, never from the
// overridable CI_MERGE_REQUEST_* variables.
func gitLabMergeRequestBaseResolver(forgePlatform string, sOpts statusOpts) mergeRequestBaseResolver {
	if forgePlatform != "gitlab" || sOpts.statusRepo == "" || sOpts.statusNum <= 0 {
		return nil
	}
	return func(ctx context.Context) (string, error) {
		client, err := newGitLabClientFromEnv("reading the merge request base commit")
		if err != nil {
			return "", err
		}
		return mergeRequestBaseSHA(ctx, client, sOpts.statusRepo, sOpts.statusNum)
	}
}

// ciWarningAnnotation returns msg as a GitHub Actions warning annotation,
// which the workflow run's summary page lists, or "" outside GitHub Actions.
// The message is escaped so it stays one workflow command.
func ciWarningAnnotation(getenv func(string) string, msg string) string {
	if getenv("GITHUB_ACTIONS") != "true" {
		return ""
	}
	escaped := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(msg)
	return "::warning::" + escaped
}

// fetchGitCommit fetches commit sha, which must be a full object name, from
// the checkout's origin remote, using whatever credentials the checkout was
// configured with. A shallow checkout fetches only that commit, so a
// shallow review checkout stays shallow; a full checkout fetches the
// objects it is missing. Hooks are disabled and git never prompts.
func fetchGitCommit(ctx context.Context, repoDir, sha string) error {
	if !gitObjectIDPattern.MatchString(sha) {
		return fmt.Errorf("not a full object name: %q", sha)
	}
	ctx, cancel := context.WithTimeout(ctx, tirithBaseFetchTimeout)
	defer cancel()
	args := []string{"-c", "core.hooksPath=" + os.DevNull, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules"}
	if out, err := runGit(ctx, repoDir, "rev-parse", "--is-shallow-repository"); err == nil && strings.TrimSpace(string(out)) == "true" {
		args = append(args, "--depth=1")
	}
	args = append(args, "origin", sha)
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoDir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run()
}

// gitCommitExists reports whether rev names a commit in repoDir.
func gitCommitExists(ctx context.Context, repoDir, rev string) bool {
	_, err := runGit(ctx, repoDir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	return err == nil
}

// runGit runs a read-only git command in repoDir and returns its stdout.
func runGit(ctx context.Context, repoDir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoDir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// shortRev abbreviates a full object name for the run log.
func shortRev(rev string) string {
	if gitObjectIDPattern.MatchString(rev) {
		return rev[:12]
	}
	return rev
}
