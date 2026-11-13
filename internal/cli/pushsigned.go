package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func newPushSignedCmd() *cobra.Command {
	var (
		repo    string
		branch  string
		base    string
		repoDir string
		token   string
	)

	cmd := &cobra.Command{
		Use:   "push-signed",
		Short: "Push local commits to a remote branch as signed commits via the GitHub GraphQL API",
		Long: `Pushes local git commits to a remote GitHub branch using the
createCommitOnBranch GraphQL mutation. GitHub signs these commits
server-side, producing verified signatures accepted by repository
rulesets that require signed commits.

This replaces 'git push' for workflows that need signed commits
(e.g., agent post-scripts operating on repos with required_signatures
rulesets).

Each local commit between --base and HEAD is replayed on the remote
branch as an individual signed commit, preserving commit messages.
The remote branch is created from --base if it does not already exist.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo == "" {
				return fmt.Errorf("--repo is required (or set GITHUB_REPOSITORY)")
			}
			owner, repoName, ok := splitOwnerRepo(repo)
			if !ok {
				return fmt.Errorf("--repo must be in owner/repo format, got %q", repo)
			}
			if branch == "" {
				return fmt.Errorf("--branch is required")
			}
			if base == "" {
				return fmt.Errorf("--base is required")
			}
			if strings.HasPrefix(base, "-") {
				return fmt.Errorf("--base must be a branch name, not a flag: %q", base)
			}
			if branch == base {
				return fmt.Errorf("--branch and --base must differ (both are %q)", branch)
			}

			dir := repoDir
			if dir == "" {
				var err error
				dir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("determining working directory: %w", err)
				}
			}

			client, err := newAuthenticatedGitHubClient(token, "")
			if err != nil {
				if errors.Is(err, errGitHubTokenMissing) {
					return githubTokenFlagError("--token")
				}
				return err
			}

			return pushSignedCommits(cmd.Context(), client, owner, repoName, branch, base, dir)
		},
	}

	cmd.Flags().StringVar(&repo, "repo", os.Getenv("GITHUB_REPOSITORY"),
		"repository in owner/repo format (default: $GITHUB_REPOSITORY)")
	cmd.Flags().StringVar(&branch, "branch", "", "remote branch to push to (required)")
	cmd.Flags().StringVar(&base, "base", "", "base branch to create from (required)")
	cmd.Flags().StringVar(&repoDir, "repo-dir", os.Getenv("REPO_DIR"),
		"local git repository directory (default: $REPO_DIR or current directory)")
	cmd.Flags().StringVar(&token, "token", "",
		"GitHub token (default: GH_TOKEN, GITHUB_TOKEN, or gh auth token)")

	return cmd
}

// splitOwnerRepo splits "owner/repo" into its two parts.
func splitOwnerRepo(nwo string) (owner, repo string, ok bool) {
	parts := strings.SplitN(nwo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// localCommit represents a single commit in the local git history.
type localCommit struct {
	SHA     string
	Message string
	Author  string // "Name <email>"
}

// localFileChange represents a file changed by a commit.
type localFileChange struct {
	Status string // "A" (added), "M" (modified), "D" (deleted), "T" (type change)
	Mode   string // new file mode (e.g. "100644", "100755")
	Path   string
}

// preparedCommit holds a local commit with its pre-validated tree files.
type preparedCommit struct {
	commit localCommit
	files  []forge.TreeFile
}

// pushSignedCommits reads local commits between base and HEAD and pushes
// them to the remote branch as individually signed commits via the
// GitHub GraphQL API.
func pushSignedCommits(ctx context.Context, client forge.Client, owner, repo, branch, base, repoDir string) error {
	commits, err := listLocalCommits(repoDir, base)
	if err != nil {
		return fmt.Errorf("listing local commits: %w", err)
	}
	if len(commits) == 0 {
		fmt.Fprintf(os.Stderr, "push-signed: nothing to push (no commits between %s and HEAD)\n", base)
		return nil
	}
	fmt.Fprintf(os.Stderr, "push-signed: %d commit(s) to push\n", len(commits))

	// Resolve local base SHA and verify it matches the remote.
	localBaseSHA, err := gitRevParseVerify(repoDir, base)
	if err != nil {
		return fmt.Errorf("resolving local base %q: %w", base, err)
	}
	remotBaseSHA, err := client.GetBranchRef(ctx, owner, repo, base)
	if err != nil {
		return fmt.Errorf("getting remote base branch %q ref: %w", base, err)
	}
	if localBaseSHA != remotBaseSHA {
		return fmt.Errorf("local base %s (%s) does not match remote base (%s); pull or rebase before pushing", base, localBaseSHA[:12], remotBaseSHA[:12])
	}

	// Validate all commits and build tree files before creating the
	// remote branch, so a submodule or mode rejection does not leave a
	// stray empty branch.
	var prepared []preparedCommit
	for _, commit := range commits {
		changes, err := listCommitChanges(repoDir, commit.SHA)
		if err != nil {
			return fmt.Errorf("listing changes for commit %s: %w", commit.SHA[:12], err)
		}
		if len(changes) == 0 {
			continue
		}
		files, err := buildTreeFiles(repoDir, commit.SHA, changes)
		if err != nil {
			return fmt.Errorf("building tree files for commit %s: %w", commit.SHA[:12], err)
		}
		prepared = append(prepared, preparedCommit{commit: commit, files: files})
	}
	if len(prepared) == 0 {
		fmt.Fprintf(os.Stderr, "push-signed: nothing to push (all commits are empty)\n")
		return nil
	}

	// Create the remote branch from the base branch HEAD.
	if createErr := client.CreateBranchFromSHA(ctx, owner, repo, branch, remotBaseSHA); createErr != nil {
		if !forge.IsAlreadyExists(createErr) {
			return fmt.Errorf("creating remote branch %q: %w", branch, createErr)
		}
		branchSHA, err := client.GetBranchRef(ctx, owner, repo, branch)
		if err != nil {
			return fmt.Errorf("getting existing branch %q ref: %w", branch, err)
		}
		if branchSHA != remotBaseSHA {
			return fmt.Errorf("branch %q already exists with commits beyond %s (HEAD %s); delete the remote branch and retry to avoid replaying or duplicating commits", branch, base, branchSHA[:12])
		}
		fmt.Fprintf(os.Stderr, "push-signed: branch %q already exists at base %s, pushing to it\n", branch, base)
	} else {
		fmt.Fprintf(os.Stderr, "push-signed: created remote branch %q from %s@%s\n", branch, base, remotBaseSHA[:12])
	}

	// Push each validated commit as a signed commit via the GraphQL API.
	for i, pc := range prepared {
		message := appendAuthoredByTrailer(pc.commit.Message, pc.commit.Author)
		committed, err := client.CommitFilesToBranch(ctx, owner, repo, branch, message, pc.files)
		if err != nil {
			return fmt.Errorf("pushing commit %s: %w", pc.commit.SHA[:12], err)
		}
		if committed {
			fmt.Fprintf(os.Stderr, "push-signed: [%d/%d] pushed %s\n", i+1, len(prepared), pc.commit.SHA[:12])
		} else {
			fmt.Fprintf(os.Stderr, "push-signed: [%d/%d] skipped %s (no changes)\n", i+1, len(prepared), pc.commit.SHA[:12])
		}
	}

	fmt.Fprintf(os.Stderr, "push-signed: done\n")
	return nil
}

// buildTreeFiles converts local file changes into forge.TreeFile entries
// by reading file content from the local repository at the given commit.
func buildTreeFiles(repoDir, commitSHA string, changes []localFileChange) ([]forge.TreeFile, error) {
	files := make([]forge.TreeFile, 0, len(changes))
	for _, change := range changes {
		if change.Status == "D" {
			files = append(files, forge.TreeFile{
				Path:   change.Path,
				Delete: true,
			})
			continue
		}
		if change.Mode == "160000" {
			return nil, fmt.Errorf("submodule change at %s cannot be replayed via the GraphQL API", change.Path)
		}
		if change.Mode != "" && change.Mode != "100644" {
			return nil, fmt.Errorf("file %s has mode %s: the GraphQL signed-commit path only supports mode 100644; non-default modes route through the REST API which does not produce signed commits with user tokens", change.Path, change.Mode)
		}
		content, err := readFileAtCommit(repoDir, commitSHA, change.Path)
		if err != nil {
			return nil, fmt.Errorf("reading %s at %s: %w", change.Path, commitSHA[:12], err)
		}
		files = append(files, forge.TreeFile{
			Path:    change.Path,
			Content: content,
			Mode:    "100644",
		})
	}
	return files, nil
}

// listLocalCommits returns commits between base and HEAD in chronological
// order (oldest first). The base argument is the local ref name (e.g.
// "main"), not a remote ref.
func listLocalCommits(repoDir, base string) ([]localCommit, error) {
	cmd := exec.Command("git", "log", "--format=%H%x00%aN <%aE>%x00%B%x00", "--reverse", "--first-parent", "--end-of-options", base+"..HEAD")
	cmd.Dir = repoDir
	out, err := cmdOutputWithStderr(cmd)
	if err != nil {
		return nil, fmt.Errorf("git log %s..HEAD: %w", base, err)
	}
	return parseLocalCommits(out)
}

// parseLocalCommits parses the NUL-delimited output of git log.
func parseLocalCommits(out []byte) ([]localCommit, error) {
	// Each record is: SHA \0 author \0 message \0
	parts := bytes.Split(out, []byte{0})
	var commits []localCommit
	for i := 0; i+2 < len(parts); i += 3 {
		sha := strings.TrimSpace(string(parts[i]))
		author := strings.TrimSpace(string(parts[i+1]))
		msg := strings.TrimRight(string(parts[i+2]), "\n")
		if sha == "" {
			continue
		}
		commits = append(commits, localCommit{SHA: sha, Message: msg, Author: author})
	}
	return commits, nil
}

// listCommitChanges returns the files changed by a single commit,
// including file modes and rename/copy handling. For merge commits,
// diffs against the first parent so conflict resolutions are captured.
func listCommitChanges(repoDir, sha string) ([]localFileChange, error) {
	cmd := exec.Command("git", "diff-tree", "-r", "--root", "--no-commit-id", "-z", "--diff-merges=first-parent", sha)
	cmd.Dir = repoDir
	out, err := cmdOutputWithStderr(cmd)
	if err != nil {
		return nil, fmt.Errorf("git diff-tree %s: %w", sha, err)
	}
	return parseDiffTreeNUL(out)
}

// parseDiffTreeNUL parses the NUL-delimited output of git diff-tree -z.
//
// With -z, git outputs each record as:
//
//	:old-mode new-mode old-sha new-sha status\0path\0
//
// For renames/copies (R/C), a second path follows:
//
//	:old-mode new-mode old-sha new-sha Rnnnn\0old-path\0new-path\0
func parseDiffTreeNUL(out []byte) ([]localFileChange, error) {
	fields := bytes.Split(out, []byte{0})
	var changes []localFileChange
	i := 0
	for i < len(fields) {
		meta := string(fields[i])
		meta = strings.TrimLeft(meta, "\n")
		if meta == "" {
			i++
			continue
		}
		if !strings.HasPrefix(meta, ":") {
			i++
			continue
		}
		parts := strings.Fields(meta[1:])
		if len(parts) < 5 {
			return nil, fmt.Errorf("malformed diff-tree metadata: %q", meta)
		}
		newMode := parts[1]
		status := parts[4]
		statusLetter := status[:1]

		switch statusLetter {
		case "R":
			if i+2 >= len(fields) {
				return nil, fmt.Errorf("rename record missing paths at offset %d", i)
			}
			oldPath := string(fields[i+1])
			newPath := string(fields[i+2])
			changes = append(changes,
				localFileChange{Status: "D", Path: oldPath},
				localFileChange{Status: "A", Mode: newMode, Path: newPath},
			)
			i += 3
		case "C":
			if i+2 >= len(fields) {
				return nil, fmt.Errorf("copy record missing paths at offset %d", i)
			}
			newPath := string(fields[i+2])
			changes = append(changes,
				localFileChange{Status: "A", Mode: newMode, Path: newPath},
			)
			i += 3
		default:
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("diff-tree record missing path at offset %d", i)
			}
			path := string(fields[i+1])
			changes = append(changes,
				localFileChange{Status: statusLetter, Mode: newMode, Path: path},
			)
			i += 2
		}
	}
	return changes, nil
}

// readFileAtCommit reads a file's content from the local git repository
// at a specific commit SHA.
func readFileAtCommit(repoDir, sha, path string) ([]byte, error) {
	cmd := exec.Command("git", "show", sha+":"+path)
	cmd.Dir = repoDir
	out, err := cmdOutputWithStderr(cmd)
	if err != nil {
		return nil, fmt.Errorf("reading %s at %s: %w", path, sha[:12], err)
	}
	return out, nil
}

// gitRevParseVerify resolves a ref to its full SHA in the local repo.
func gitRevParseVerify(repoDir, ref string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--verify", "--end-of-options", ref)
	cmd.Dir = repoDir
	out, err := cmdOutputWithStderr(cmd)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// cmdOutputWithStderr runs a command and returns stdout. On failure, the
// error includes git's stderr so diagnostics are not lost.
func cmdOutputWithStderr(cmd *exec.Cmd) ([]byte, error) {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}

// appendAuthoredByTrailer appends an "Authored-by: Name <email>" trailer
// to the commit message so the original sandbox author identity is preserved
// when the commit is replayed through the GraphQL API (which rewrites the
// author to the authenticated account). The trailer is placed in the last
// paragraph per git trailer conventions. If the message already contains
// an Authored-by trailer, it is left unchanged.
func appendAuthoredByTrailer(message, author string) string {
	if author == "" {
		return message
	}
	trailer := "Authored-by: " + author
	if strings.Contains(message, trailer) {
		return message
	}
	message = strings.TrimRight(message, "\n")
	return message + "\n\nAuthored-by: " + author
}
