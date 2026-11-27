---
sidebar_label: Overview
---

# Fullsend CLI

The `fullsend` CLI manages the complete fullsend lifecycle: provisioning GCP infrastructure, configuring GitHub, enrolling repositories, and running agents locally.

## Installation

Download the latest binary from [GitHub Releases](https://github.com/fullsend-ai/fullsend/releases). For detailed setup instructions, see [Getting Started](../guides/getting-started/).

To update an installed binary later, run [`fullsend self-update`](self-update.md).

## Command groups

| Command group | Description |
|--------------|-------------|
| [`fullsend agent`](agent.md) | Generate a custom agent and manage agent registrations — new, add, list, set, update, remove |
| [`fullsend github`](github.md) | Configure GitHub repos — setup and single-value config updates |
| [`fullsend inference`](inference.md) | Manage inference credentials — GCP Workload Identity Federation for Agent Platform, OpenAI WIF enrolment for GPT on pi or codex, and `gateway status` to check an inference gateway block and its authentication |
| [`fullsend mint`](mint.md) | Deploy and manage the OIDC token mint service |
| [`fullsend repos`](repos.md) | Manage per-repo installations at scale via declarative manifest |

## Additional commands

| Command | Description |
|---------|-------------|
| [`fullsend run`](run.md) | Execute an agent locally in a sandbox. See [running agents locally](../guides/user/running-agents-locally.md). |
| `fullsend review-dedup` | Decide whether an unchanged synchronize event can skip review, publish the skip note, or supersede an earlier skip note. |
| [`fullsend prepare`](prepare.md) | Resolve an agent's sandbox image as `fullsend run` does, pre-pull a private image with the CI job's registry credential (`ghcr.io` on GitHub Actions, `CI_REGISTRY` on GitLab CI) as `run` does, and create and delete a restrictive preflight sandbox, which makes the gateway pull the image. Run it once before many parallel runs. `--json` prints the resolved image, architecture, and result. |
| [`fullsend poll`](poll.md) | Discover forge events and dispatch agent pipelines — GitLab cron polling, Jira polling, and the GitLab webhook fast path. |
| [`fullsend dispatch`](dispatch.md) | Evaluate harness CEL triggers for one forge event and print the dispatch matrix, or decide routing for one built-in agent with `--route`. |
| [`fullsend lock`](lock.md) | Pin remote dependencies to `lock.yaml`. |
| [`fullsend lint`](lint.md) | Lint `config.yaml` and agent harness definitions — deprecations, misconfigurations, and file checks, with diagnostics for each enabled agent, or for one agent with `fullsend lint <agent-name>`. See [Checking a trigger before you merge](../guides/user/cel-triggers-reference.md#checking-a-trigger-before-you-merge) for a walkthrough. |
| `fullsend scan` | Run security scanners on agent input/output |
| [`fullsend self-update`](self-update.md) | Update the local `fullsend` binary to the latest release, or to `--version`, after verifying the release's Sigstore signature and checksum. |
| `fullsend eval-measure` | Score wild-run traces into `eval-measurements.jsonl`. See [Eval measurements](../guides/infrastructure/eval-measurements.md). |
| `fullsend prepare-artifacts` | Build, in `--artifact-dir`, the filtered copy of a run's `--output-dir` that CI uploads as the run artifact. The level (`all`, `metadata` or `none`) comes from `config.yaml`'s [`artifacts`](../reference/config-reference.md#artifacts) setting and the run's source; a run whose source is unknown gets the strictest configured level. Prints the level on stdout and the rule it applied on stderr. If the level cannot be resolved, it leaves `--artifact-dir` empty and exits non-zero. `action.yml` and the GitLab agent job run it after `eval-measure`. See [Run artifacts and private data](../guides/user/run-artifacts-and-private-data.md). |
| `fullsend issues transition` | Transition an issue: exactly one of `--state closed`, `--transition NAME` (Jira), or `--status NAME` (Jira). Accepts `--resolution ID_OR_NAME` (Jira) and `--comment TEXT`, plus the existing `--tracker`, `--project`, `--number`, `--fullsend-dir`, and `--token` flags. See [Issue transitions](../guides/user/issues-commands.md#fullsend-issues-transition) for permissions, ambiguity handling, and partial failures. |

## Global flags

All commands that interact with GitHub resolve authentication via `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token` (in that order). Explicit token flags such as `--token` and `--forge-token` override the chain. For GitLab, set `GITLAB_TOKEN` or pass `--gitlab-token` to `repos` subcommands. The CLI runs preflight checks and tells you exactly which OAuth scopes are missing before making any changes.

For the complete command tree with implementation details, see [CLI internals](../guides/dev/cli-internals.md).
