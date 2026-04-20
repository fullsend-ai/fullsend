---
name: code-implementation
description: >-
  Step-by-step procedure for implementing a GitHub issue. Gathers context,
  discovers repo conventions, plans the change, implements, verifies with
  tests and linters, and commits to a feature branch.
---

# Code Implementation

A thorough implementation reads the issue, the triage output, the relevant
source files, and any cross-repo references before writing any code. Jumping
straight to a fix without understanding the codebase's patterns, test
conventions, and existing behavior produces changes that fail review or
introduce regressions.

## Tools reminder

You have the `Bash` tool for all CLI operations. **You must use it** for
verification (step 9) and committing (step 10) — do not skip these steps.

Commands you will need during this procedure:

- `git checkout`, `git add <file>`, `git diff`, `git commit` — branching and committing
- `gh issue view` — reading issues (read-only, no edits or comments)
- `gh pr view`, `gh pr list`, `gh pr diff` — reading PR context
- `make test`, `go test ./...`, `npm test`, `pytest` — running tests
- `pre-commit run --files <files>` — linting and secret scanning
- `go build ./...`, `go vet ./...` — compilation checks

Use `Read`/`Write`/`Grep`/`Glob` for file operations.

### Secret scanning

The `scan-secrets` helper is pre-installed in the sandbox image at
`/usr/local/bin/scan-secrets`. Before starting step 9, verify it exists:

```bash
command -v scan-secrets
```

If missing, **STOP**. Do not improvise a replacement or skip scanning.

Two modes:

- `scan-secrets <files>` — scan named files. Use in step 9a.
- `scan-secrets --staged` — scan the git index. Use in step 10b.

## Progress markers

At the start of each major step, emit a progress marker so the runner
logs show where you are even if the session times out:

```bash
echo "::notice::STEP <N>: <title>"
```

This uses GitHub Actions annotation syntax so it surfaces in the run
summary. **Do this at steps 1, 3, 5, 9a, 9b, 9c, and 10.**

## Time budget

The sandbox may have a hard timeout enforced by the harness. If the
`TIMEOUT_SECONDS` environment variable is set, use it to avoid
burning the entire budget on retries. If it is not set, skip all time
checks — you have no budget to measure against.

Capture the start time at the very beginning of step 1:

```bash
AGENT_START=$(date +%s)
```

Before starting pre-commit (9b), before each retry iteration (9c), and
before commit (10), check remaining time **only if `TIMEOUT_SECONDS` is
set**:

```bash
if [ -n "${TIMEOUT_SECONDS:-}" ]; then
  ELAPSED=$(( $(date +%s) - AGENT_START ))
  REMAINING=$(( TIMEOUT_SECONDS - ELAPSED ))
  echo "::notice::Time check: ${ELAPSED}s elapsed, ${REMAINING}s remaining"
fi
```

When `TIMEOUT_SECONDS` is set, use these thresholds (expressed as
fractions of the budget so they scale to any timeout value):

- **Before 9b (pre-commit):** If less than 40% of the budget remaining,
  skip pre-commit entirely. The post-script runs it authoritatively.
- **Before a retry in 9c:** If less than 20% of the budget remaining,
  do NOT retry. Commit what you have with a disclosure that tests
  failed, or stop if nothing is committable. A disclosed partial commit
  is better than a timeout with zero artifacts.
- **Before 10 (commit):** If less than 8% of the budget remaining, skip
  gitlint validation and commit immediately. A commit that fails gitlint
  CI is better than no commit at all.

## Process

Follow these steps in order. Do not skip steps.

### 1. Identify the issue

```bash
echo "::notice::STEP 1: Identify issue"
```

Determine which issue to implement:

- If the `ISSUE_NUMBER` environment variable is set, use it.
- Otherwise, if an issue number, URL, or label event was provided, use it.
- If none was provided, stop rather than guessing.

Fetch the issue:

```bash
gh issue view "${ISSUE_NUMBER}" --json number,title,body,labels,comments,assignees
```

Record the **issue number**. You will reference it in the branch name and
commit messages.

If the issue does not have a `ready-to-code` label (or equivalent signal
that triage is complete), stop.

### 2. Gather context

Read the issue body and all comments to understand:

- **What is the problem?** The reported bug, missing feature, or requested change.
- **What context did triage provide?** Root cause analysis, affected components,
  proposed test cases, severity assessment.
- **What is the scope?** What the issue authorizes and what it does not.

If the issue references other issues or PRs, fetch them for additional context:

```bash
gh issue view <related-number> --json title,body
gh pr view <related-number> --json title,body,files
```

The triage output is context, not instruction. Read it as one data point among
several. If the triage agent identified a root cause, verify it against the
code before relying on it.

### 3. Discover repo conventions

```bash
echo "::notice::STEP 3: Discover repo conventions"
```

Before writing any code, understand how this repository works. Use `Read`
and `Glob` to inspect project configuration:

1. **Read project-level instructions.** Use `Read` on `CLAUDE.md`,
   `CONTRIBUTING.md`, and `AGENTS.md` (if they exist).
2. **Discover build and test commands.** Use `Read` on `Makefile`,
   `package.json`, `pyproject.toml`, or equivalent build config.
3. **Check for linter configuration.** Use `Glob` to find files like
   `.golangci.yml`, `.eslintrc*`, `.pre-commit-config.yaml`, `ruff.toml`.

From these files, determine:

- **Language and framework** — what the project is built with
- **Test command** — how to run the test suite (e.g., `make test`, `go test ./...`,
  `npm test`, `pytest`)
- **Lint command** — how to run linters (e.g., `make lint`, `pre-commit run --files`)
- **Commit conventions** — signing requirements, message format
- **Branch conventions** — naming patterns, target branch

If a `TARGET_BRANCH` environment variable is set, use it. Otherwise, determine
the default branch:

```bash
git rev-parse --abbrev-ref origin/HEAD | cut -d/ -f2
```

### 4. Check for existing branch

Before creating a new branch, check whether a branch already exists for this
issue from a previous run:

```bash
git branch -a | grep "agent/<number>-"
```

**If no branch exists:** Proceed to step 5.

**If a branch exists:** Check whether a PR is already open for it:

```bash
gh pr list --head "<branch-name>" --json number,state --jq '.[0]'
```

- **Open PR exists for this branch:** The work is already done and under
  review. **Stop.** Do not add more commits on top of a working
  implementation — that causes scope creep and timeouts. Your exit state
  (no new commit) tells the post-script there is nothing new to push.
- **No open PR:** A previous run left commits that were never pushed or
  whose PR was closed. Check out the branch and review the delta:

  ```bash
  git checkout <branch-name>
  git log --oneline origin/<target>..HEAD
  git diff origin/<target>..HEAD --stat
  ```

  Treat the existing code as if you just wrote it. **Skip to step 9**
  (verification) — run secret scan, tests, and pre-commit on the changed
  files. If everything passes, the post-script will push the branch and
  create the PR. If tests or pre-commit fail, fix only the failing issues
  in a new commit on the same branch — do not rewrite or redo the
  existing work.

**Scope guardrail:** When working on top of an existing branch, your
changes must be strictly limited to fixing verification failures or
completing incomplete work. Do not "improve" a working implementation by
adding RBAC configs, extra test cases, documentation, or config files
the issue does not mention.

### 5. Create branch

```bash
echo "::notice::STEP 5: Create branch"
```

If the `BRANCH_NAME` environment variable is set, use it:

```bash
git fetch origin
git checkout -b "${BRANCH_NAME}" origin/<target-branch>
```

Otherwise, create a feature branch from the target branch:

```bash
git fetch origin
git checkout -b agent/<number>-<short-description> origin/<target-branch>
```

The branch name must follow the `agent/<issue-number>-<short-description>`
convention. Keep the description to 2-4 lowercase hyphenated words derived
from the issue title.

### 6. Identify the task type

Before planning, determine what kind of work this issue requires:

- **Bug fix** — the standard path. Reproduce, plan, implement, test, commit.
- **Feature / enhancement** — new behavior. Plan, implement, test, commit.
- **Test-only** — the issue asks for tests, not production code changes. Write
  tests that cover the described behavior. Do not modify production code unless
  tests require it (e.g., exporting a function for testability).
- **Already-fixed** — if step 7 reveals the bug no longer exists, stop cleanly.
  Do not implement a fix for a resolved issue.
- **Label-gated** — if the issue has a label like `do-not-implement` or a gate
  label that signals no work should be done, respect it. Stop cleanly.

### 7. Verify the problem exists

Before implementing, confirm the reported behavior is still present:

1. Read the code paths the issue describes. Does the bug still exist in the
   current codebase?
2. If there is a quick way to verify — run a targeted test, check a return
   value, trace the logic — do it.
3. If the bug has already been fixed (by a recent commit, a dependency update,
   or another PR), **stop**. Do not implement a fix for a resolved issue. Your
   exit state (no commit) tells the post-script to report accordingly.

For feature requests and test-only tasks, skip this step — there is no bug to
reproduce.

### 8. Plan the implementation

Before writing code, form a concrete plan:

1. **Read affected files in full** — not just the lines mentioned in the issue.
   Understand the surrounding context, imports, types, and call sites.
2. **Read test files** that cover the affected code. Understand how the existing
   tests are structured, what patterns they follow, what helpers exist.
3. **Read related files** — if the change touches an API handler, read the
   router, middleware, and model files. If it touches a controller, read the
   reconciler pattern and RBAC config.
4. **Follow cross-repo references** — if the issue, docs, or triage comments
   link to other repos (e.g., an e2e test suite, a dependent service, a
   related PR in another repo), read those references to understand the full
   picture. Use `gh issue view`, `gh pr view`, or `gh pr diff` to fetch
   what you need. For files in other repos that are not part of an issue
   or PR, use `Read` on a local clone if available, or note the gap in
   your plan and proceed with the context you have.
   Do not chase every import — focus on references that the issue context
   points you toward.
5. **Identify what to change** — list the specific files and functions you will
   modify or create.
6. **Identify what tests to write or update** — new behavior needs new tests;
   changed behavior needs updated tests.
7. **Assess risk** — will this change affect other callers? Does it change a
   public interface? Could it break downstream consumers?

When requirements are ambiguous, distinguish between "vague but actionable"
(you can make a reasonable conservative interpretation) and "genuinely
uninterpretable" (no viable path forward). For vague-but-actionable issues,
implement the most conservative interpretation and note your assumptions in
the commit message.

Do not start writing code until you can articulate: what you will change, why,
and how you will verify it works.

### 9. Implement and verify

Write the code change, then verify it.

**Implementation:**

- **Follow existing patterns.** If the repo uses a specific error handling idiom,
  use it. If controllers follow a specific reconciliation pattern, follow it. If
  test files use a specific helper library, use it.
- **Do not introduce new dependencies without justification.** If the change can
  be made with the existing dependency set, prefer that.
- **Write or update tests.** Every behavioral change must have a corresponding
  test change. If the issue includes a proposed test case from triage, evaluate
  it critically — use it if it's good, improve it if it's not, replace it if
  it's wrong.

**9a. Secret scan — MANDATORY FIRST STEP**

```bash
echo "::notice::STEP 9a: Secret scan"
```

Run the secret scan against your changed files before anything else:

```bash
scan-secrets <files-you-modified>
```

If secrets are detected: hard stop. Remove them, re-scan. Only proceed after
the scan passes.

**9b. Pre-commit hooks — best-effort optimization**

```bash
echo "::notice::STEP 9b: Pre-commit hooks"
```

Pre-commit is a **best-effort optimization**, not a hard gate. The
post-script (`post-code.sh`) runs an authoritative pre-commit check on
the GitHub Actions runner before pushing — that is the real security gate.
Running pre-commit here catches formatting and lint issues early so the
post-script doesn't reject your commit, but burning excessive time on
in-sandbox retries is worse than committing with a disclosed failure.

```bash
test -f .pre-commit-config.yaml && echo "pre-commit config found"
```

If no `.pre-commit-config.yaml`, skip to 9c.

**Setup:**

```bash
if ! command -v pre-commit &>/dev/null; then
  pip install pre-commit 2>/dev/null || pip3 install pre-commit 2>/dev/null
fi
```

Do NOT run `pip install pre-commit` if pre-commit is already on the PATH.
The sandbox image ships a pinned version with network policies tuned to it.
Do NOT run `pre-commit install --install-hooks` — it registers a git hook
that can block `git commit`.

**STEP A — Pre-format your code before running pre-commit.** Many hooks
auto-fix files (formatters, trailing-whitespace, end-of-file-fixer). Doing
this yourself first eliminates an entire re-run cycle. Check the repo's
`.pre-commit-config.yaml` for which formatters are configured, then run
them manually on your changed files. For example:

```bash
# Run the repo's formatter directly — language varies:
#   Go: gofmt -w / goimports -w
#   Python: black / ruff format
#   JS/TS: prettier --write
#   Rust: rustfmt
# Check what is available on PATH and what the repo uses.
```

For config files (YAML, JSON, TOML) you create or modify: read 1-2
existing files in the same directory to match indentation, quoting,
and line length. Most linter failures on config files come from
mismatched style.

**STEP B — Run pre-commit once on all changed files:**

```bash
pre-commit run --files <all-your-changed-files>
```

Never run per-file. Many linter hooks analyze the entire project per
invocation — running per-file multiplies that cost.

The first run may be slow (installs hook environments). This is normal.

**STEP C — React to the result:**

- **Exit 0** — all hooks passed. Stage and proceed to 9c.
- **Exit 1 with auto-fix only** (hooks say "Fixed" / "Fixing"): files
  are already corrected. Stage them and re-run once to confirm:

  ```bash
  git add <fixed-files>
  pre-commit run --files <all-your-changed-files>
  ```

- **Exit 1 with linter errors**: fix only what the linter reports — do
  not refactor, do not rewrite. Re-run once:

  ```bash
  pre-commit run --files <all-your-changed-files>
  ```

- **Any other failure** (exit 3, network error, infrastructure error) —
  log the error and move on to step 9c.

**STEP D — After the retry, STOP regardless of the result.**

If the second pre-commit run passes, great. If it fails again, **you are
done with pre-commit for the entire session**. Log the exact hook name,
file, and error in your commit message and move on to 9c. Do NOT attempt
a third run. Do NOT try a different fix. The post-script runs an
authoritative pre-commit check on the runner before pushing.

**RULES:**

1. **Maximum 2 pre-commit runs total across the entire session.** One
   initial run, one retry. No more — not even if step 9c sends you back
   to fix your code. Once you have used your 2 runs, pre-commit is done.
   Do not re-run it during retries.
2. **Always disclose.** If pre-commit did not pass, say so in the commit
   message with the exact error. Never claim hooks passed when they did
   not.
3. **Pre-existing failures on files you did not touch are not your
   responsibility.** Only run hooks on **your** changed files.
4. **Do not refactor to satisfy a linter.** Fix the specific reported
   error — nothing more.

**9c. Tests and linters — MANDATORY**

```bash
echo "::notice::STEP 9c: Tests and linters"
```

You MUST run the test suite that covers the code you changed. Determine
which test command to use by reading the Makefile, CONTRIBUTING.md, or
existing CI workflows.

```bash
# Use the repo's actual test command — check Makefile or CI config
make test        # or: go test ./..., npm test, pytest, etc.
make lint        # or: golangci-lint run, eslint, ruff, etc.
```

**If tests fail due to missing tools or infrastructure** (not due to your
code): try the Makefile's setup targets first (`make deps`, `make setup`,
etc.). If the tool genuinely cannot be installed in the sandbox, note
this in your commit message body so reviewers know what was not verified:

> Note: <suite-name> tests could not run (<reason>). <other-suite>
> tests passed. Manual verification of <suite-name> is required.

**Do NOT silently skip tests and commit as if everything passed.** If you
cannot run the relevant test suite, you must disclose that.

**If tests fail due to your code:**

1. Read the failure output carefully. Understand the root cause.
2. Fix the issue in your implementation. Do not weaken or skip tests.
3. Re-run secret scan (9a) and then tests (9c). This consumes one retry
   iteration. **Do NOT re-run pre-commit (9b) during retries** — you
   already used your 2 pre-commit runs. The post-script handles
   pre-commit authoritatively on the runner.
4. Repeat until tests pass or the retry limit is reached.

The retry limit is read from the `MAX_RETRIES` environment variable
(default: 1 if unset). The harness may also enforce a hard timeout
independently — if the harness kills the session, your retry count is
irrelevant. Prefer committing with a disclosed issue over burning time
on additional retry iterations.

If the retry limit is reached and tests still fail, do not commit. Stop.

**9d. Self-review**

Before staging, review your own changes:

```bash
git diff
```

Read every line. Check for:

- Changes that don't serve the issue (scope creep, unrelated formatting)
- Accidental artifacts: debug prints, commented-out code, TODO comments
- Secret material: `.env`, `*.pem`, `*.key`, `credentials.json`
- Protected-path files (see agent definition for the authoritative list)

If you added more than necessary, revert the extras before staging.

### 10. Commit

```bash
echo "::notice::STEP 10: Commit"
```

Stage **only the files you modified or created** and commit.

**10a. Stage files**

```bash
git add path/to/file1 path/to/file2
```

Only include files you deliberately created or modified.

**10b. Review and scan what you are committing**

```bash
git diff --cached --stat
```

Confirm only your intended files are present. Unstage anything unexpected:

```bash
git reset HEAD <file-you-did-not-intend-to-stage>
```

Then run the secret scan against the staged content:

```bash
scan-secrets --staged
```

This is not a repeat of 9a — it scans what you *actually staged*, which may
differ from what you named. If the scan fails, do not commit.

**10c. Commit**

The commit message must:

- **Use the repo's commit convention as discovered in step 3.** If
  `CONTRIBUTING.md`, `CLAUDE.md`, `.gitlint`, or the existing commit history
  uses a specific format (e.g., Conventional Commits, Angular-style, ticket
  prefixes), follow it.
- **Fall back to `<type>: <description>` only if no convention was found.**
- Reference the issue number with `Closes #<number>` in the body.

**Title length — check `.gitlint` if it exists:**

```bash
test -f .gitlint && cat .gitlint
```

Most repos enforce a title length limit (commonly 72 characters). If
`.gitlint` has `[title-max-length] line-length=72`, keep the title
(first line) under that limit. Use a concise `<type>: <description>`
that fits.

**Body line length — comply with the repo's gitlint config:**

If `.gitlint` has a `[body-max-line-length]` rule (e.g. `line-length=72`),
you **MUST** hard-wrap body text at that limit. This is enforced by CI.
The post-script will unwrap the body when building the PR description,
so your hard-wrapped commit body will still render as nice prose on
GitHub.

Hard-wrap guidelines when a limit is configured:
- Break lines at word boundaries before hitting the limit
- List items that exceed the limit: start the continuation on the next
  line, indented by 2 spaces
- URLs that exceed the limit may remain on one line (gitlint usually
  allows this via `ignore-body-lines`)
- `Closes #N` and similar trailers: keep on one line
- **`Signed-off-by:`** — `git commit -s` auto-generates this from
  `GIT_COMMITTER_NAME` and `GIT_COMMITTER_EMAIL`. If the resulting line
  exceeds the body-max-line-length, gitlint CI will reject the commit.
  Before committing, check: if the `Signed-off-by` trailer would exceed
  the limit, omit the `-s` flag and write a shorter trailer manually, or
  omit it entirely if the repo does not require DCO sign-off

The commit body should:
- Explain **what** changed and **why** (not just "fix bug")
- Describe the root cause or motivation
- Summarize which files/functions were modified and the approach
- Note any trade-offs, assumptions, or edge cases

```bash
git commit -s -m "<type>: <short-description>

<What changed and why. Hard-wrap at the limit from
.gitlint if one is configured. Write substantive
content for human reviewers.>

Closes #<number>"
```

**After committing, validate the commit message if gitlint is available:**

```bash
which gitlint &>/dev/null && gitlint --commit HEAD
```

If gitlint fails, **amend immediately** to fix:

```bash
git commit --amend -m "<fixed title>

<fixed body — respect ALL line-length rules>"
gitlint --commit HEAD
```

Common gitlint failures:
- **B1 body-max-line-length** on `Signed-off-by:` — the auto-generated
  trailer is too long. Re-amend without `-s` and either add a shorter
  sign-off manually or omit it if the repo doesn't require DCO.
- **T1 title-max-length** — shorten the title.
- **B1 body-max-line-length** on prose — re-wrap the offending line.

Repeat until gitlint passes. Do not leave a commit that you know will
fail CI. If gitlint is not available, manually verify that no line in
the title or body exceeds the configured limits.

If a git hook fires during `git commit` and fails (e.g., the repo shipped
a `.git/hooks/pre-commit`), do NOT enter a fix-and-retry loop. You already
ran pre-commit in step 9b (which is the same check). Commit with
`--no-verify` to bypass the git hook and disclose the failure in the commit
message. The post-script runs an authoritative pre-commit on the runner.

**Do not push the branch.** The post-script handles pushing, PR creation,
and failure reporting.

## Partial work

If you hit a token limit or context window boundary before completing the
implementation, and the tests pass on the partial work: commit what you have.
The review agent downstream will evaluate completeness — incomplete-but-passing
code is caught at the review stage, not the implementation stage. The commit
message should note that the work is partial (e.g., "partial implementation"
in the description) so the review agent and post-script can act accordingly.

## Constraints

The agent definition (`agents/code.md`) is the authoritative list of
prohibitions. This skill does not restate them. If a step in this skill
appears to conflict with the agent definition, the agent definition wins.
