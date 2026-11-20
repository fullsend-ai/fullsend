---
sidebar_label: fullsend push-signed
---

# fullsend push-signed

Push local git commits to a remote GitHub branch as signed commits via
the `createCommitOnBranch` GraphQL mutation. GitHub signs these commits
server-side, producing verified signatures accepted by repository
rulesets that require signed commits.

This replaces `git push` for workflows that need signed commits (e.g.,
agent post-scripts operating on repos with `required_signatures`
rulesets). See [#9062](https://github.com/fullsend-ai/fullsend/issues/9062)
for background.

## Usage

```bash
fullsend push-signed --repo owner/repo --branch feature-branch --base main [flags]
```

Each local commit between `--base` and `HEAD` is replayed on the remote
branch as an individual signed commit, preserving commit messages. The
remote branch is created from `--base` if it does not already exist.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--repo` | `$GITHUB_REPOSITORY` | Repository in `owner/repo` format (required) |
| `--branch` | — | Remote branch to push to (required). Must differ from `--base`. |
| `--base` | — | Base branch name that exists both locally and on the remote (required). The local and remote SHAs must match; pull or rebase first if they diverge. |
| `--repo-dir` | `$REPO_DIR` or current directory | Local git repository directory |
| `--token` | `$GH_TOKEN`, `$GITHUB_TOKEN`, or `gh auth token` | GitHub token with repo write access |

## Limitations

- **Submodule changes** (mode `160000`) cannot be replayed via the API
  and are rejected with an error.
- **Executable files** (mode `100755`) route through the Git Data REST
  API instead of GraphQL. The REST path produces signed commits only
  with **App installation tokens** (the kind agent post-scripts use).
  User PATs produce unsigned REST commits, which rulesets will reject.
- **Symlinks and other non-standard modes** (e.g. `120000`) are
  rejected. Only regular (`100644`) and executable (`100755`) files are
  supported.
- **Existing branches with commits** are rejected to prevent replaying
  or duplicating commits. Delete the remote branch and retry.
- **Local branch divergence**: after pushing, the local branch still
  references the original unsigned commits. Run
  `git fetch origin <branch> && git reset --hard origin/<branch>` to
  sync.

## Examples

```bash
# Push agent commits from inside a sandbox post-script
fullsend push-signed \
  --repo "$REPO_FULL_NAME" \
  --branch "$BRANCH_NAME" \
  --base main \
  --repo-dir "$TARGET_REPO_DIR"

# Push with an explicit token
fullsend push-signed \
  --repo org/repo \
  --branch fix/issue-123 \
  --base main \
  --token "$PUSH_TOKEN"
```
