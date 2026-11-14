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

	"github.com/fullsend-ai/fullsend/internal/runtime"
)

// tirithRepoPolicyPath is where a repository keeps the tirith policy that
// tightens command scanning for agent runs on it.
const tirithRepoPolicyPath = ".tirith/policy.yaml"

// tirithRepoPolicyMaxBytes bounds the staged policy. A tirith policy is a
// short YAML document; anything larger is refused rather than uploaded.
const tirithRepoPolicyMaxBytes = 256 << 10

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
// checkout may be the request's head, so only the base commit from the event
// payload is used. When that commit is unknown or not in the checkout,
// nothing is staged and the note says so: the head copy is never used.
//
// A missing file, a directory that is not a git checkout, or an empty file
// stage nothing. A path that is not a regular file (a symlink or submodule)
// or a file over tirithRepoPolicyMaxBytes is an error, so an owner's policy
// is never silently dropped.
func trustedTirithRepoPolicy(ctx context.Context, repoDir string, src tirithPolicySource) ([]byte, string, error) {
	rev := "HEAD"
	if src.requestRun {
		base := strings.ToLower(strings.TrimSpace(src.baseSHA))
		if !gitObjectIDPattern.MatchString(base) || !gitCommitExists(ctx, repoDir, base) {
			return nil, "Repository tirith policy not staged: this is a pull/merge request run and " +
				"the base commit is not available in the checkout (the head copy is never used)", nil
		}
		rev = base
	} else if !gitCommitExists(ctx, repoDir, rev) {
		// Not a git checkout, or one with no commits: there is no trusted ref.
		return nil, "", nil
	}

	out, err := runGit(ctx, repoDir, "ls-tree", "-z", rev, "--", tirithRepoPolicyPath)
	if err != nil {
		return nil, "", fmt.Errorf("listing %s at %s: %w", tirithRepoPolicyPath, rev, err)
	}
	entry := strings.TrimSuffix(string(out), "\x00")
	if entry == "" {
		return nil, "", nil
	}
	// "<mode> SP <type> SP <object> TAB <path>"
	meta, _, ok := strings.Cut(entry, "\t")
	fields := strings.Fields(meta)
	if !ok || len(fields) != 3 {
		return nil, "", fmt.Errorf("unexpected git ls-tree output for %s at %s", tirithRepoPolicyPath, rev)
	}
	mode, kind, oid := fields[0], fields[1], fields[2]
	if kind != "blob" || (mode != "100644" && mode != "100755") {
		return nil, "", fmt.Errorf("%s at %s is not a regular file (mode %s, %s); refusing to stage it", tirithRepoPolicyPath, rev, mode, kind)
	}
	if !gitObjectIDPattern.MatchString(oid) {
		return nil, "", fmt.Errorf("unexpected object name for %s at %s", tirithRepoPolicyPath, rev)
	}
	sizeOut, err := runGit(ctx, repoDir, "cat-file", "-s", oid)
	if err != nil {
		return nil, "", fmt.Errorf("sizing %s at %s: %w", tirithRepoPolicyPath, rev, err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeOut)), 10, 64)
	if err != nil {
		return nil, "", fmt.Errorf("sizing %s at %s: %w", tirithRepoPolicyPath, rev, err)
	}
	if size > tirithRepoPolicyMaxBytes {
		return nil, "", fmt.Errorf("%s at %s is %d bytes, over the %d-byte limit; refusing to stage it", tirithRepoPolicyPath, rev, size, tirithRepoPolicyMaxBytes)
	}
	if size == 0 {
		return nil, "", nil
	}
	data, err := runGit(ctx, repoDir, "cat-file", "blob", oid)
	if err != nil {
		return nil, "", fmt.Errorf("reading %s at %s: %w", tirithRepoPolicyPath, rev, err)
	}
	return data, fmt.Sprintf("Staging repository tirith policy %s from %s", tirithRepoPolicyPath, shortRev(rev)), nil
}

// attachTirithRepoPolicy stages the trusted repository tirith policy on a
// bootstrap input that installs sandbox hooks, and returns the note to print.
// Inputs without hooks (security disabled) are left alone, as is an empty
// repoDir: without a target repository there is no policy to read.
func attachTirithRepoPolicy(ctx context.Context, boot runtime.BootstrapInput, repoDir string, getenv func(string) string) (string, error) {
	hb, ok := boot.(*harnessBootstrapWithHooks)
	if !ok || repoDir == "" {
		return "", nil
	}
	policy, note, err := trustedTirithRepoPolicy(ctx, repoDir, tirithPolicySourceFromEnv(getenv))
	if err != nil {
		return "", err
	}
	hb.hooks = hb.hooks.WithTirithRepoPolicy(policy)
	return note, nil
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
