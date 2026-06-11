# Code Agent

![Code agent icon](icons/coder.png)

Implementation specialist that reads triaged GitHub issues, implements fixes or features following repository conventions, runs tests and linters, and commits to a local feature branch.

## How the agent works

Triggered when the `ready-to-code` label is applied to an issue or via `/fs-code`.

The code agent follows a three-phase pipeline: pre-script, sandbox execution, post-script.

1. **Pre-script** validates inputs on the runner before sandbox creation. It also checks for open PRs linked to the issue.
2. **Sandbox** — the agent reads the issue, explores the codebase, writes code, runs tests and linters, and commits locally. It has no network access (enforced by OpenShell).
3. **Post-script** runs on the runner: it performs protected path checks, secret scanning, pre-commit checks, pushes the branch, and creates the PR.

This separation ensures the agent never has direct write access to the repository.

## How it helps

- Triaged issues can go from "ready" to "PR open" without human involvement.
- Implementation follows repo conventions because the agent reads existing code, tests, and linter configs before writing.
- The sandboxed execution model means a misbehaving agent cannot push arbitrary code — the post-script gates everything.

## Commands

| Command | Where | Effect |
|---------|-------|--------|
| `/fs-code` | Issue comment | Triggers the code agent on the issue |

The `/fs-code` command accepts an optional `--force` flag. It can only be used
on issues (not PRs). The code agent is also triggered automatically when the
`ready-to-code` label is applied to an issue.

## Control labels

| Label | Meaning |
|-------|---------|
| `ready-to-code` | Triggers the code agent. Applied by the [triage](triage.md) post-script for low-risk categories (bug, documentation, performance), or manually by a human for feature work after prioritization. |
| `ready-for-review` | Applied by the code agent's post-script after pushing a PR. Signals the [review agent](review.md) to evaluate the change. |

## Configuration and extension

See [Customizing with AGENTS.md](../guides/user/customizing-with-agents-md.md) and
[Customizing with Skills](../guides/user/customizing-with-skills.md).

## Source

[`internal/scaffold/fullsend-repo/harness/code.yaml`](https://github.com/fullsend-ai/fullsend/blob/main/internal/scaffold/fullsend-repo/harness/code.yaml)
