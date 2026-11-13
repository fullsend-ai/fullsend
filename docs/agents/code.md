---
description: How the fullsend code agent implements triaged GitHub issues following repository conventions, runs tests and linters, and commits to a feature branch.
---

# Code Agent

![Code agent icon](icons/coder.png)

Implementation specialist that reads triaged GitHub issues, implements fixes or features following repository conventions, runs tests and linters, and commits to a local feature branch.

## How the agent works

Triggered when the `ready-to-code` label is applied to an issue or via `/fs-code`.

The code agent follows a three-phase pipeline: pre-script, sandbox execution, post-script.

1. **Pre-script** validates inputs on the runner before sandbox creation. It also checks for open PRs that close the issue (via `Fixes`/`Closes`/`Resolves` keywords).
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

Requires write-level repository permission (admin, maintain, or write).

The `/fs-code` command accepts an optional `--force` flag. It can only be used
on issues (not PRs). The code agent is also triggered automatically when the
`ready-to-code` label is applied to an issue.

## Control labels

| Label | Meaning |
|-------|---------|
| `ready-to-code` | Triggers the code agent. Applied by the [triage](triage.md) post-script for low-risk categories (bug, documentation, performance), or manually by a human for feature work after prioritization. |
| `ready-for-review` | Applied by the code agent's post-script after pushing a PR. In per-repo installs, triggers review when applied to a PR; also marks workflow state for humans and the retro agent. |

## Configuration and extension

See [Configuring with AGENTS.md](../guides/user/customizing-with-agents-md.md) and
[Configuring with Skills](../guides/user/customizing-with-skills.md).

### Image and network policy synchronization

By default, the code and [fix agent](fix.md) use the same upstream container
image and overlapping sandbox policies (`policies/code.yaml` and
`policies/fix.yaml` in [fullsend-ai/agents](https://github.com/fullsend-ai/agents)).
They are separate harnesses, though — you can override each independently when
their needs diverge. For example, you might keep Jira endpoints out of the code
agent's policy while allowing them on the fix agent when reviewers ask you to
verify something against a ticket during PR feedback.

> **Warning:** If you customize image, `policy:`, or `providers:` on only one
> agent by mistake, the other may fail with no obvious reason (for example, a
> package manager or registry endpoint allowed in code but not fix).

**Recommended configuration**

If you want both agents to share one behavior set — one place to edit image,
policy, providers, and runner scripts — put the same overrides in both harness
files in your repo's `.fullsend/` directory:

```yaml
# .fullsend/harness/code.yaml  (register as source: harness/code.yaml in config.yaml)
base: https://raw.githubusercontent.com/fullsend-ai/agents/<tag>/harness/code.yaml#sha256=…
image: ghcr.io/your-org/your-fullsend-image@sha256:…
policy: policies/base.yaml
providers:
  - vertex-ai
  - github
  - package-registries

# .fullsend/harness/fix.yaml  (register as source: harness/fix.yaml in config.yaml)
base: https://raw.githubusercontent.com/fullsend-ai/agents/<tag>/harness/fix.yaml#sha256=…
image: ghcr.io/your-org/your-fullsend-image@sha256:…
policy: policies/base.yaml   # same file — edit once, both agents use it
providers:
  - vertex-ai
  - github
  - package-registries
```

Keep the shared policy at `.fullsend/policies/base.yaml`. The policy file
defines non-network sandbox restrictions only — filesystem access, landlock, and
process identity. Network access is controlled through `providers:` profiles
listed in the harness (see [Architecture](../architecture.md#agent-sandbox) for details on
provider-backed policy composition) — when
unifying configuration, keep the `providers:` list the same on both harnesses
too. The same pattern applies to `pre_script` and `post_script` when you want a
single place to maintain runner-side behavior.

See [Customizing Agents](../guides/user/customizing-agents.md) for harness
composition.

### Variables

None.

## Source

[`fullsend-ai/agents` — `harness/code.yaml`](https://github.com/fullsend-ai/agents/blob/main/harness/code.yaml)
