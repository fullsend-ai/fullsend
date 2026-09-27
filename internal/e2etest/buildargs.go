package e2etest

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// upstreamRefOverrideVar is the -X target that pins the CLI's scaffold
// workflow refs to one fullsend-ai/fullsend commit (see internal/cli/root.go).
const upstreamRefOverrideVar = "github.com/fullsend-ai/fullsend/internal/cli.upstreamRefOverride"

// upstreamRepoURL is where the rendered scaffold fetches fullsend-ai/fullsend
// scripts and actions from.
const upstreamRepoURL = "https://github.com/fullsend-ai/fullsend"

var fullCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// e2eUpstreamRef returns the commit to stamp as the e2e CLI's upstream ref:
// HEAD of the checkout at modRoot, when published reports it can be fetched
// from fullsend-ai/fullsend. That holds for a pushed branch, a fork PR head
// (via refs/pull) and a merge-group commit. A local commit that was never
// pushed returns "", and the scaffold falls back to main as before.
func e2eUpstreamRef(modRoot string, published func(sha string) bool) string {
	sha := headCommit(modRoot)
	if !fullCommitSHA.MatchString(sha) || !published(sha) {
		return ""
	}
	return sha
}

// publishedUpstream reports whether sha can be fetched from
// fullsend-ai/fullsend, the same fetch the rendered workflow makes.
func publishedUpstream(sha string) bool {
	return fetchable(upstreamRepoURL, sha)
}

// fetchable reports whether sha can be fetched from the repository at url.
// It fetches into a throwaway repository so the checkout under test is not
// touched.
func fetchable(url, sha string) bool {
	dir, err := os.MkdirTemp("", "e2e-upstream-ref-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	if exec.Command("git", "init", "-q", dir).Run() != nil {
		return false
	}
	fetch := exec.Command("git", "-C", dir, "fetch", "-q", "--no-tags", "--depth=1", "--filter=tree:0", url, sha)
	return fetch.Run() == nil
}

// cliBuildArgs returns the go build arguments for the e2e CLI. A full commit
// SHA is stamped as the upstream ref override, so the scaffold the CLI
// renders runs that commit's scripts instead of main's (#7622). Any other
// value builds an unstamped CLI.
func cliBuildArgs(binary, headSHA string) []string {
	args := []string{"build"}
	if fullCommitSHA.MatchString(headSHA) {
		args = append(args, "-ldflags", "-X "+upstreamRefOverrideVar+"="+headSHA)
	}
	return append(args, "-o", binary, "./cmd/fullsend/")
}

// headCommit returns the HEAD commit of the git checkout rooted at modRoot,
// or "" when modRoot is not the top of a git checkout (for example, a module
// cache directory that happens to sit inside another repository).
func headCommit(modRoot string) string {
	top, err := exec.Command("git", "-C", modRoot, "rev-parse", "--show-toplevel").Output()
	if err != nil || !samePath(strings.TrimSpace(string(top)), modRoot) {
		return ""
	}
	sha, err := exec.Command("git", "-C", modRoot, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(sha))
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
