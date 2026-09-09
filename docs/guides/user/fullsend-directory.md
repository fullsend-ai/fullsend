# Working with the .fullsend directory

After setup, fullsend writes a small set of files into your repository.
Most of them are installer-managed plumbing: editing them in place does
nothing lasting, because the next setup or sync overwrites the copy.
This guide lists what lands in a per-repo install, how to tell managed
files from yours, and where to put customizations.

For the install itself, see [Configuring GitHub](../getting-started/configuring-github.md).
For field-by-field `config.yaml` documentation, see the
[Config Reference](../../reference/config-reference.md).

## Prerequisites

- A repository with fullsend already installed (`fullsend github setup` or
  `fullsend repos install`).
- This page describes **per-repo** installations — the supported model.

## How to tell managed files from yours

1. Open the file.
2. If it starts with a managed-by header, do not edit it:

   ```yaml
   # This file is managed by fullsend. Do not edit it directly.
   # Upstream: https://github.com/fullsend-ai/fullsend/blob/main/internal/scaffold/...
   ```

3. If it does not have that header, it is yours (or a vendor preset you
   treat as read-through — see [config.base.yaml](#configbaseyaml-vendor-preset) below).

The `Upstream:` URL points at the scaffold inside the fullsend project.
Adopters customize through `config.yaml` and harness overlays, not by
patching the deployed copy. Fullsend maintainers change the scaffold in
[fullsend-ai/fullsend](https://github.com/fullsend-ai/fullsend) and let
install refresh the managed files. See
[ADR 0043](../../ADRs/0043-managed-file-headers.md).

GitLab CI templates under `.gitlab/ci/fullsend-*.yml` are installer-managed
even when a file omits the header. Re-running `repos install` refreshes them.

## What setup writes (GitHub)

A first-time `fullsend github setup <owner/repo>` commits roughly this
tree. Default agents do **not** copy their harness, prompt, policy, or
skill files into your repo — those load from upstream at runtime.

```
your-repo/
  .github/workflows/
    fullsend.yaml       # managed — event shim
    prioritize.yml      # managed — prioritize scheduler entry
  .fullsend/
    config.yaml         # yours — overlay
    config.base.yaml    # optional vendor preset — read-through
```

| Path | Role | Edit? |
|------|------|-------|
| `.github/workflows/fullsend.yaml` | Event shim. Forwards GitHub events to the upstream reusable dispatch workflow. You do not add a workflow per agent. | No. Re-running setup refreshes it. |
| `.github/workflows/prioritize.yml` | Thin caller so an org-level prioritize scheduler can `workflow_dispatch` this repo. | No. |
| `.fullsend/config.yaml` | Overlay. Runtime, roles, registered agents, allowlists, inference, mint URL. Omitted keys fall through to `config.base.yaml` then code defaults. | **Yes.** Re-running setup keeps this file unless you pass a flag that targets a key (`--runtime`, `--agents`, `--mint-url`, `--inference-*`, `--openai-*`). |
| `.fullsend/config.base.yaml` | Present only with `--config` (a vendor preset). Shared baseline. | Treat as read-through. Refresh it by re-running setup with `--config`. Put repo-specific values in `config.yaml`. |

Repository **variables** and **secrets** (`FULLSEND_GCP_REGION`,
`FULLSEND_REVIEW_CLIENT_ID`, `FULLSEND_PER_REPO_INSTALL`,
`FULLSEND_GCP_PROJECT_ID`, `FULLSEND_GCP_WIF_PROVIDER`) are not files.
Change them with `fullsend github set` — see
[Operations](../getting-started/operations.md). The mint URL is not a
`github set` key: it is the `mint_url` field in `.fullsend/config.yaml`,
set via `fullsend github setup --mint-url`.

### config.yaml

This is the file to edit for almost every repo-level setting:

```yaml
# fullsend per-repo configuration
version: "1"
runtime: claude
roles:
  - triage
  - coder
  - review
```

A first install often stores only the keys you actually set. Empty or
omitted fields inherit defaults; an explicit empty list (for example
`roles: []`) means "none", not "use the default".

Common edits:

| Goal | Where |
|------|-------|
| Change the default runtime or model | `runtime:` here, or per-agent fields — [Choose a Runtime](../getting-started/choosing-a-runtime.md) |
| Enable a subset of built-in agents | `roles:` — must match the GitHub Apps you installed |
| Register a custom or derived agent | `agents:` — [Bring Your Own Agent](bring-your-own-agent.md) |
| Stop all dispatch | `kill_switch: true` |
| Tune comments and reactions | `status_notifications:` — [Configuring Agent Behavior](customizing-agents.md#status-notifications) |

Every supported key is in the [Config Reference](../../reference/config-reference.md).
Layering rules (overlay → base → code defaults) are in the
[Layered Config Reference](../infrastructure/layered-config-reference.md).

### config.base.yaml (vendor preset)

Skip this file unless your operator gave you a `--config` preset. The
overlay (`config.yaml`) is the writable layer; the base is a shared
baseline that setup can replace when you refresh the preset.

## What you add later (not generated)

Nothing under `.fullsend/` besides `config.yaml` (and optional
`config.base.yaml`) is required for default agents. Add files here only
when you are customizing or building a new agent.

| Path | When you create it | Guide |
|------|--------------------|-------|
| Repo-root `AGENTS.md` | Teach every agent (and human) your test commands, style, and architecture. This is **not** inside `.fullsend/`. | [Configuring with AGENTS.md](customizing-with-agents-md.md) |
| `.agents/skills/<name>/SKILL.md` | Domain knowledge or extra capabilities. Prefer `.agents/skills/` over a copy under `.fullsend/`. | [Configuring with Skills](customizing-with-skills.md) |
| `.fullsend/harness/<agent>.yaml` | Thin `base:` overlay: change model, timeout, skills, env, or image for an existing agent. | [Configuring Agent Behavior](customizing-agents.md) |
| `.fullsend/agents/<agent>.md` | Prompt and tools for a **new** agent. | [Bring Your Own Agent](bring-your-own-agent.md) |
| `.fullsend/providers/`, `profiles/`, `scripts/` | Supporting files for a custom agent. Copy from the [scaffold](https://github.com/fullsend-ai/fullsend/tree/main/internal/scaffold/fullsend-repo) or reference trusted URLs — setup does **not** install these. | [Bring Your Own Agent](bring-your-own-agent.md#minimum-viable-agent) |
| `.fullsend/policies/` | Sandbox policy for a custom agent. Not in the fullsend scaffold — copy `policies/base.yaml` from [fullsend-ai/agents](https://github.com/fullsend-ai/agents) or write your own. | [Bring Your Own Agent](bring-your-own-agent.md#minimum-viable-agent) |

Register local harnesses in `config.yaml`:

```yaml
agents:
  - name: code
    source: harness/code.yaml
```

Do not add a GitHub Actions workflow for a custom agent. The managed
shim discovers registered harnesses and routes matching events.

The old `customized/` overlay directory is **removed**. Do not recreate
it. Use `base:` composition, `agents:` registration, and skills instead
— see [Customizing Agents](customizing-overview.md) and
[ADR 0064](../../ADRs/0064-deprecate-customized-directory-overlay.md).

## What setup writes (GitLab)

`fullsend repos install` for GitLab writes CI templates and a config
overlay. It merges an include into your existing root `.gitlab-ci.yml`
instead of replacing that file.

```
your-repo/
  .gitlab-ci.yml                         # yours — installer merges an include
  .gitlab/ci/
    fullsend-pipeline.yml                # managed
    fullsend-dispatch.yml                # managed
    fullsend-poll.yml                    # managed
    fullsend-agent.yml                   # managed
  .fullsend/
    config.yaml                          # yours
```

| Path | Role | Edit? |
|------|------|-------|
| `.gitlab/ci/fullsend-*.yml` | Pipeline include, MR dispatch, cron poll, agent job template. | No. `repos install` refreshes them. |
| `.gitlab-ci.yml` | Your pipeline. Install adds `include: .gitlab/ci/fullsend-pipeline.yml` plus fullsend stages and workflow rules when missing. | Yes, for your own jobs. Do not remove the fullsend include if you want agents to run. |
| `.fullsend/config.yaml` | Same overlay as GitHub. | **Yes.** |

Uninstall and day-2 operations: [Operations](../getting-started/operations.md).

## What re-running setup does

1. Refresh managed workflow / CI templates to the CLI version you are
   running.
2. Leave `.fullsend/config.yaml` unchanged, unless you pass a
   config-targeting flag (`--runtime`, `--agents`, `--mint-url`,
   `--inference-*`, `--openai-*`). Those flags change only the keys you named.
3. Rewrite `.fullsend/config.base.yaml` when you pass `--config`.

A flag-less re-run prints `Keeping existing .fullsend/config.yaml` and
still updates managed workflows. That is how you pick up scaffold fixes
without losing hand-written overlay comments or `agents:` entries.

Fleet installs (`fullsend repos install`) also repair drifted managed
files. See [Repo Management](../getting-started/repo-management.md).

## Optional vendored files

`fullsend github setup --vendor` (and the same flag on `repos install`)
adds extra managed assets so CI can run a pinned CLI instead of a
GitHub release:

- `.fullsend/bin/fullsend` — vendored binary
- `.defaults/` — mirrored upstream content used at runtime
- Reusable workflow copies under `.github/workflows/`

Do not edit these. They refresh on the next vendored install. Without
`--vendor`, a later setup removes every safe path recorded in the vendor
manifest — the vendored binary, `.defaults/`, the reusable workflow copies,
and the vendor manifest itself — not just the binary.

## Files that are not source

| Path | What it is |
|------|------------|
| `.fullsend-cache/` | Local fetch cache for remote harness resources. Gitignored. Do not commit it. |

## Common mistakes

| You want | Don't | Do |
|----------|-------|----|
| Change how dispatch works | Edit `.github/workflows/fullsend.yaml` or `.gitlab/ci/fullsend-*.yml` | Re-run setup / `repos install` after upgrading the CLI. Open an issue upstream if the scaffold itself is wrong. |
| Change model, timeout, or skills on a default agent | Copy the whole upstream harness into `.fullsend/` | Write a thin `.fullsend/harness/<name>.yaml` with `base:` and register it — [Configuring Agent Behavior](customizing-agents.md) |
| Teach coding conventions | Fork the code agent | Put them in repo-root `AGENTS.md` |
| Add domain knowledge | Edit a managed workflow | Add a skill under `.agents/skills/` |
| Turn an agent off | Delete its workflow file | Set `enabled: false` on its `agents:` entry, or drop the role from `roles:` |
| Recreate `customized/` | Overlay files by path | Use `base:` composition and `agents:` registration |

## See also

- [Customizing Agents](customizing-overview.md) — pick the lightest customization
- [Config Reference](../../reference/config-reference.md) — every `config.yaml` field
- [Layered Config Reference](../infrastructure/layered-config-reference.md) — overlay / base / defaults
- [Operations](../getting-started/operations.md) — sync, status, uninstall
- [Running agents locally](running-agents-locally.md) — `fullsend run --fullsend-dir .fullsend`
