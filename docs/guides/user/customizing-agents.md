# Configuring Agent Behavior

This guide explains how to configure existing fullsend agents for your
organization and repositories. It covers harness-based configuration with
`base:` composition, environment variables, status notifications, and
disabling agents.

For a quick overview of all customization options, see
[Customizing Agents](customizing-overview.md). For the complete harness field
reference, see [Harness Field Reference](../../reference/harness-reference.md).
To create a new agent rather than configure an existing one, start with
[`fullsend agent new`](../../cli/agent.md#agent-new).

## What you can configure

You can customize any existing agent without building one from scratch.
Common configuration goals:

| Goal | How |
|------|-----|
| Change model, timeout, or image | Override scalar fields via `base:` composition |
| Add org-specific skills | Add entries to the `skills:` list |
| Add environment variables | Add entries under `env.runner` or `env.sandbox` |
| Install GitHub Packages npm dependencies | Add a repo-level GitHub Packages provider (see [Private registries and GitHub Packages](#private-registries-and-github-packages)) |
| Extend the sandbox with packages | Build a custom image and set `image:` |
| Add executables to the sandbox | Use `host_files` to copy scripts to `/sandbox/workspace/bin` |
| Disable a built-in agent | Set `enabled: false` in `config.yaml` |
| Control status comments | Configure `status_notifications` in `config.yaml` |
| Teach agents your conventions | Use [AGENTS.md](customizing-with-agents-md.md) (no harness change needed) |
| Give an agent domain knowledge | Use [skills](customizing-with-skills.md) (no harness change needed) |
| Limit sandbox token privilege | Set `privilege_levels.runtime: read` so write tokens stay on pre/post scripts |

## Configuration with `base:` composition

The primary mechanism for customizing agents. Register agents in
`config.yaml` with a `base:` URL pointing to the upstream harness, and
override only the fields that differ.

### Example: add a skill to the code agent

Create a thin harness that inherits from the upstream code agent and adds your skill:

**`harness/code.yaml`:**
```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/code.yaml#sha256=abc...

skills:
  - skills/my-custom-linting        # Merged with base skills (child overrides by basename)

timeout_minutes: 45                 # Override timeout (scalar: child wins)
```

**`skills/my-custom-linting/SKILL.md`:**
```markdown
---
name: my-custom-linting
description: Org-specific linting rules and conventions.
---

# My Custom Linting

[Your skill content...]
```

Register the agent in `config.yaml`:

```yaml
agents:
  - name: code
    source: harness/code.yaml
```

Because config-registered agents take precedence over built-in agents on name collision, your `code` agent replaces the default — with all of the base agent's scripts, policies, host_files, and plugins still inherited.

Test it locally first:
```bash
fullsend run code --fullsend-dir .fullsend --target-repo ./my-repo --env-file .env.local
```

See [Running agents locally](running-agents-locally.md) for prerequisites and troubleshooting.

### Example: swap the model for review

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/review.yaml#sha256=abc...

model: sonnet
```

### Example: add org-specific environment variables

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/code.yaml#sha256=abc...

env:
  runner:
    JIRA_TOKEN: "${JIRA_TOKEN}"     # Merged with base env; child keys win
  sandbox:
    JIRA_PROJECT: "MYPROJ"
```

### What you can override

Any harness field can be overridden. See the [field merge rules](../../reference/harness-reference.md#field-merge-rules-for-base-and-overlays) for how each field type combines with the base:

- **Change model, timeout, image, scripts** — scalars replace the base value.
- **Add skills** — your entries are merged with the base's by basename; same-named skills override the base entry. **Add plugins or host_files** — your entries are concatenated with the base's, base first.
- **Add or override env vars** — maps are merged; your keys win on collision.
- **Replace validation or security config** — child replaces the entire block.

Base chains support up to 5 levels. Circular references are detected and rejected. Resolution order: base chain, child overrides, overlay resolution.

> **Note:** `allowed_remote_resources`, `allow_runtime_fetch`, and `max_runtime_fetches` are NOT inherited from base harnesses — the child must declare its own. This prevents a base harness from injecting arbitrary URL prefixes or enabling runtime fetching in the child.

> **Org-level fallback:** Separately from base-harness inheritance, the org-level `allowed_remote_resources` from `config.yaml` acts as a fallback for all URL resolution. URLs trusted at the org level are accepted even when the child harness omits the field. This is a distinct trust layer from base composition — the org-level list is set by organization administrators, not by base harness authors.

### Remote providers and profiles

Providers and openshell profiles can be referenced from remote URLs, enabling fully portable harnesses that bundle everything an agent needs.

**`providers`** accepts local provider names, local file paths, and HTTPS URLs with integrity hashes:

```yaml
providers:
  - vertex                       # Local name: loaded from providers/vertex.yaml
  - providers/custom.yaml        # Local path: resolved relative to harness
  - "https://github.com/org/repo/tree/main/providers/claude.yaml#sha256=abc..."  # Remote
```

**`openshell.profiles`** accepts local paths and HTTPS URLs:

```yaml
openshell:
  profiles:
    - profiles/claude-code.yaml    # Local path (resolved relative to harness)
    - "https://github.com/org/profiles/tree/main/claude-code.yaml#sha256=abc..."
```

When using `base:` composition, the base harness can declare its own providers and profiles. Child harnesses inherit and can extend them:

- **Profiles:** base + child lists are concatenated; deduplicated by profile `id` (child wins)
- **Providers:** base + child lists are concatenated; local names shadow URL-resolved names of the same `name`

Only profiles listed in `openshell.profiles` are imported. A YAML file that merely exists under the `profiles/` directory is **not** imported unless the harness names it. To use a local profile as a per-repo override, list it explicitly (e.g., `profiles/fullsend-vertex-ai.yaml`); the child-wins dedup rule applies by `id`.

Remote URLs must include a `#sha256=...` integrity hash and match an `allowed_remote_resources` prefix in the same config. The integrity hash is checked on every resolution to ensure the content hasn't been tampered with since it was pinned.

### Private registries and GitHub Packages

Use this when a code or fix agent must install a **public** npm package on
GitHub Packages that **another organization** owns. The minted App token
cannot read it: GitHub scopes installation tokens to the packages of the org
the App is installed in, and `npm.pkg.github.com` answers 403 across orgs.
Same-org packages already work with `${GH_TOKEN}`, and the shipped code and
fix workflows grant `packages: read`. Nothing in this section ships by
default.

**What fullsend provides.** On GitHub Actions the runner keeps the job's own
workflow token as `GH_WORKFLOW_TOKEN`, a *provider-only* credential:

- Only provider credential expansion can read it. `${GH_WORKFLOW_TOKEN}` is
  refused in `runner_env`, `env.runner`, `env.sandbox`, `host_files` and
  `validation_loop.schema`, and pre/post/validation scripts never see it.
- `gh` and the post-script keep using the minted token (`GH_TOKEN`,
  `PUSH_TOKEN`).
- Outside GitHub Actions the variable is left alone; a local PAT is never
  copied into it. Set it yourself only for a local run against GitHub Packages.
- Any provider definition in `.fullsend` may reference it, the same way one
  can already reference `${GH_TOKEN}` or `${PUSH_TOKEN}`. `.fullsend` is read
  from the trusted ref, so a pull request cannot point a provider at it; if
  your repository ships other providers, review their `${}` uses with that in
  mind.

**Setup.** Add four files to your repository (or config repo) and overlay them
onto `code` and `fix`.

1. **`.fullsend/providers/github-packages.yaml`**

   ```yaml
   ---
   name: github-packages
   type: fullsend-github-packages
   credentials:
     GITHUB_TOKEN: "${GH_WORKFLOW_TOKEN}"
   ```

2. **`.fullsend/profiles/fullsend-github-packages.yaml`**

   ```yaml
   ---
   id: fullsend-github-packages
   display_name: Fullsend GitHub Packages
   description: GitHub Packages npm registry for cross-org public packages
   category: data
   credentials:
     - name: github_token
       description: Actions workflow token with packages:read
       env_vars: [GITHUB_TOKEN]
       required: true
       auth_style: bearer
       header_name: authorization
   endpoints:
     - host: npm.pkg.github.com
       port: 443
       protocol: rest
       access: read-only
       enforcement: enforce
       allow_encoded_slash: true
     - host: pkg-npm.githubusercontent.com
       port: 443
       protocol: rest
       access: read-only
       enforcement: enforce
   binaries:
     - "**/node"
     - "**/npm"
     - "**/pnpm"
     - "**/corepack"
     - "**/yarn"
   ```

3. **`.fullsend/env/npmrc-github-packages`**

   ```
   //npm.pkg.github.com/:_authToken=${GITHUB_TOKEN}
   ```

4. **`.fullsend/harness/code.yaml`** (and the same overlay for `fix.yaml`)

   ```yaml
   base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/code.yaml#sha256=abc...

   providers:
     - providers/github-packages.yaml
   openshell:
     profiles:
       - profiles/fullsend-github-packages.yaml
   host_files:
     - src: env/npmrc-github-packages
       dest: /sandbox/workspace/.npmrc
       expand: false
   env:
     sandbox:
       NPM_CONFIG_USERCONFIG: /sandbox/workspace/.npmrc
   ```

Register the overlay in `.fullsend/config.yaml` as usual (`name: code` /
`name: fix` with `source: harness/code.yaml` / `harness/fix.yaml`). Pin the
`base:` URL to a SHA as shown in [Configuration with `base:` composition](#configuration-with-base-composition).

**How the credential is scoped.**

- *Host-bound.* OpenShell binds every static credential in a profile to all
  of the profile's endpoints, so the token resolves at `npm.pkg.github.com`
  and at the tarball CDN `pkg-npm.githubusercontent.com` (which serves
  pre-signed URLs and ignores it). Anywhere else the proxy answers
  `credential_endpoint_mismatch`.
- *Separate from the forge credential.* OpenShell matches hosts label by
  label: a plain `github.com` endpoint matches only `github.com`, never
  `npm.pkg.github.com`, unless a profile writes an explicit wildcard label.
  The code and fix GitHub profile uses exact hosts, so `GH_TOKEN` never
  resolves at the registry and this profile's token never resolves at
  `api.github.com` or `github.com`. The two providers also use different
  sandbox keys (`GITHUB_TOKEN` here, `GH_TOKEN` for the forge), so both attach
  to one sandbox without a duplicate-key rejection.
- *Read-only.* Both registry hosts are `access: read-only`.
- *Readable by the agent.* This is the standard exposure of any static
  OpenShell credential
  ([ADR 0025](../../ADRs/0025-provider-credential-delivery-for-sandboxed-agents.md)):
  a placeholder resolves wherever it appears in a request to a bound host,
  and the registry echoes unknown package names in its 404 body. A value read
  back that way is the job's own workflow token until the job ends, and
  through the proxy it can only reach the hosts the role's profiles allow; see
  [ADR 0114](../../ADRs/0114-github-packages-via-host-bound-workflow-token-provider.md).

**Notes.**

- `allow_encoded_slash: true` is required: scoped package names (`@org/pkg`)
  put a `%2F`-encoded slash in the metadata request, and without the field the
  proxy closes the connection (verified on OpenShell 0.0.116, whose policy
  schema documents the field for npm scoped packages).
- The npmrc lives at `/sandbox/workspace/.npmrc` with `NPM_CONFIG_USERCONFIG`
  pointing at it, because `/home` is outside the base filesystem policy's
  `read_write` set (`/sandbox`, `/tmp`, `/dev/null`). Keep `expand: false`:
  the file holds no secret, pnpm expands `${GITHUB_TOKEN}` when it reads the
  file as user config, and inside the sandbox that value is the placeholder.
- pnpm ≥ 10.34.2 no longer expands `${VAR}` in project `.npmrc` files
  ([GHSA-3qhv-2rgh-x77r](https://github.com/advisories/GHSA-3qhv-2rgh-x77r));
  the user-config route above is unaffected.

### Tuning agents with augmentation skills

Before you fork a whole agent or replace a built-in skill, decide what you
are actually changing:

| Goal | Prefer |
|------|--------|
| Domain rules, linting, or constraints that sit *alongside* defaults | A **unique-named** augmentation skill (append via harness `skills:`) |
| Shorter or reformatted human-facing output (comments, summaries) | Augmentation skill with **field ownership** and hard limits — not soft "be concise" |
| New review dimension under an orchestrator (for example `pr-review`) | A **sub-agent** file under that skill's `sub-agents/`, plus whatever registration the current platform requires |
| Replace most of a skill's procedure | Whole-skill override / derived harness — heavier; you stop inheriting upstream edits |

**What to think about when authoring:**

1. **Discover first** — read the target agent's harness, agent definition,
   schema, post-script, and any shipped skills under
   [fullsend-ai/agents](https://github.com/fullsend-ai/agents). Do not guess
   field names or roster lists from memory.
2. **Unique skill names** — a repo skill with the same directory name as a
   built-in is ignored and produces a warning (see
   [skill precedence](customizing-with-skills.md#skill-precedence)).
3. **Specificity wins** — vague augmentations lose to hard default
   instructions. Own exact fields; use word limits and templates.
4. **Sub-agents are not wrapper skills** — if you need a new review dimension,
   ship `sub-agents/<name>.md` (and parent dispatch updates when the
   orchestrator uses a fixed roster). Do not invent a parallel
   `*/SKILL.md` that embeds the same content.
5. **Prefer the lightest shipping path** the current docs support —
   upstream contribution, file-level skill override when available, or
   whole-skill fork only when that is still required.

> **Planned:** File-level overrides inside a pinned skill directory (add or
> replace a single `sub-agents/<name>.md` without vendoring the whole tree)
> are tracked in [#6158](https://github.com/fullsend-ai/fullsend/issues/6158)
> / [#6157](https://github.com/fullsend-ai/fullsend/issues/6157). Until that
> lands, fixed-roster sub-agent changes usually need an upstream PR or a
> whole-skill pin.

**Authoring help:** the contributor skill
[`author-fullsend-augmentations`](../../../skills/author-fullsend-augmentations/SKILL.md)
walks this discovery and conflict analysis. Use it when writing or reviewing
augmentation skills and sub-agents. Details also live in
[Configuring with skills](customizing-with-skills.md#authoring-skills-that-augment-defaults).

## Configuration examples

### Extending the sandbox image

When `host_files` injection is not enough and you need additional packages or
toolchains in the sandbox, build an image that extends the published base and
point your harness `image:` field at it:

```dockerfile
FROM ghcr.io/fullsend-ai/fullsend-sandbox:latest
RUN apt-get update && apt-get install -y --no-install-recommends rustc \
  && rm -rf /var/lib/apt/lists/*
```

Use `ghcr.io/fullsend-ai/fullsend-code:latest` as the parent instead when you
also need the Go toolchain. Pin the parent tag to a digest before CI use.

### Adding executables

The sandbox already has `/sandbox/workspace/bin` on its `PATH`. To make a
script available as a command, drop it there:

1. Create your script (e.g. `scripts/my-tool.sh`):

   ```bash
   #!/bin/bash
   echo "Hello from $0"
   ```

2. Make it executable with `chmod +x scripts/my-tool.sh`.
3. Map it into the sandbox via `host_files`:

   ```yaml
   host_files:
     - src: scripts/my-tool.sh
       dest: /sandbox/workspace/bin/my-tool.sh
   ```

4. The agent should be able to run `my-tool.sh` directly.

#### Modifying the PATH for external toolchains

When you need a directory outside `/sandbox/workspace/bin` on the `PATH`
(e.g. an external toolchain), use a `.env.d` file:

1. Create an env file (e.g. `env/extra-path.env`):

   ```bash
   PATH=/opt/my-toolchain/bin:$PATH
   ```

2. Map it into the sandbox:

   ```yaml
   host_files:
     - src: env/extra-path.env
       dest: /sandbox/workspace/.env.d/extra-path.env
   ```

3. At startup the sandbox sources every `*.env` file under
   `/sandbox/workspace/.env.d/`, picking up your PATH addition.

**Note**: `env.sandbox` cannot modify `PATH`, the harness ignores special
variables to protect sandbox operation.

### Adding a skill

Create `skills/my-skill/SKILL.md` in your `.fullsend` config repo or agents repo:

```markdown
# My Custom Skill

Custom domain knowledge for this organization.

## Examples

...
```

Reference the skill in your harness's `skills:` list. The skill is available to all agents that include it in their harness configuration. See [Configuring with Skills](customizing-with-skills.md) for details on skill authoring and precedence.

## Agent roles

On the hosted mint, agents run as one of a **fixed** set of built-in roles.
Each role is a GitHub App identity with a fixed permission ceiling. An agent's
name is separate from its role — the `code` and `fix` agents both run as the
`coder` role. To pick a role for a custom agent, or to use your own identity or
a custom role, see [Custom Agent Identity](custom-agent-identity.md).

| Role | GitHub App | Purpose |
|------|------------|---------|
| `fullsend` | `{org}-fullsend[bot]` | Dispatch/control |
| `triage` | `{org}-triage[bot]` | Issue triage |
| `coder` | `{org}-coder[bot]` | Code generation |
| `review` | `{org}-review[bot]` | PR review |
| `fix` | (reuses coder app) | Fix failures |
| `retro` | `{org}-retro[bot]` | Retrospectives |
| `prioritize` | `{org}-prioritize[bot]` | Backlog priority |

**Naming conventions:**
- App naming: `{org}-{role}`
- Bot naming: `{org}-{role}[bot]`
- PEM storage: GCP Secret Manager or filesystem (standalone)
- Secret name: `fullsend-{role}-app-pem`

> **Note:** The "fix" role reuses the "coder" app and PEM — no separate GitHub App or secret is created for it.
>
> **Note:** The default deployment uses a shared vendor App (`fullsend-ai-review[bot]`). Code that gates on a review bot's identity must match both the org-specific and shared vendor forms — see [Bot Identities](../../contributing/bot-identities.md) for details.

> **Note:** Mint-only dogfood roles such as `scribe` can be registered with
> `fullsend mint add-role` (and used via remote harness registration) but are
> **not** valid values for the `.fullsend` `roles:` field until scaffold /
> workflow wiring lands. Adding them under `roles:` fails config validation
> rather than silently no-oping.

## Status notifications

Agent workflows post status comments on issues and PRs when they start and complete. Control this with `status_notifications` in `.fullsend/config.yaml`:

```yaml
status_notifications:
  comment:
    start: enabled
    completion: enabled
```

When `status_notifications` is omitted entirely, both start and completion comments default to enabled.

### Completion modes

| Value | Behavior |
|-------|----------|
| `enabled` | Always post a completion comment (default) |
| `on_failure` | Post when the agent fails, is cancelled, or is skipped by a pre-script; the start comment is automatically suppressed to avoid notification noise |
| `disabled` | Never post a completion comment; silently remove the start comment |

`on_failure` is useful when you want to reduce notification noise — successful runs leave no trace, but failures still surface. When `completion` is set to `on_failure`, the start comment is automatically suppressed regardless of the `start` setting, because posting and then deleting a start comment would still trigger a GitHub notification pointing to a deleted comment.

In `enabled` mode (the default), a hard crash or cancellation that happens before the agent could post anything at all is also surfaced after the fact: a post-job cleanup step synthesizes an "Interrupted" comment so the run doesn't silently vanish.

### Reactions

As an alternative (or supplement) to comments, agents can signal status with emoji reactions. Reactions don't generate a GitHub notification, so they're a lower-noise way to show that an agent is working on something and how it turned out.

```yaml
status_notifications:
  reaction:
    start: enabled       # "enabled" | "disabled" (default)
    completion: enabled  # "enabled" | "on_failure" | "disabled" (default)
```

Unlike comments, reactions default to `disabled` — they're an opt-in addition, not a default-on behavior. When `start` is enabled, a 👀 reaction is added when the agent begins.

At completion, the start reaction (if any) is removed, and — depending on `completion` — replaced with an outcome reaction:

| Value | Behavior |
|-------|----------|
| `enabled` | Always add a completion reaction: 👍 on success, 😕 on failure/cancelled/skipped |
| `on_failure` | Add a 😕 reaction only on failure/cancelled/skipped; leave no reaction on success |
| `disabled` | Never add a completion reaction (default) |

👎 is deliberately avoided for failures — it overloads GitHub's native up/down-vote convention, so a routine agent failure could be misread as the bot disliking the issue.

Because reactions carry no notification cost, `on_failure` here simply means "leave no reaction on success," with no start-suppression workaround needed.

**Where the reaction lands:** for runs triggered by an issue or PR event, the reaction is added to the issue/PR itself. For runs triggered by a slash command comment (e.g. `/fs-fix`), the reaction is added to the triggering comment instead, so it's clear which command the reaction is responding to.

**Known limitations:**

- Reactions are currently GitHub-only. Enabling `reaction.*` on a GitLab-backed repo is silently a no-op today ([#5998](https://github.com/fullsend-ai/fullsend/issues/5998)).
- If a run is hard-killed before it can post its completion reaction, the start reaction (👀) can be left behind indefinitely — unlike status comments, there's no out-of-process reconciler for orphaned reactions yet.

## Disabling agents

To disable an agent (including built-in scaffold agents) without removing
its role, add an entry with `enabled: false` in your config:

```yaml
agents:
  - name: retro
    enabled: false
```

This prevents the agent from dispatching and from resolving via
`fullsend run`. The role can stay in `defaults.roles` — only the agent
is suppressed. Omitting `enabled` (or setting it to `true`) keeps the
agent active (backward compatible).

When multiple entries share a name, the last writer wins. This allows a
disable-then-enable pattern to replace a default agent with a custom one:

```yaml
agents:
  - name: retro
    enabled: false
  - name: retro
    source: harness/custom-retro.yaml
    enabled: true
```

**Important:** The `name` must match the **agent/harness name**, not the
role name. The built-in agent names are: `code`, `triage`, `review`,
`fix`, `retro`, `prioritize`. Note that the role `coder` maps to the
agent named `code` — writing `name: coder` passes validation but
disables nothing because no agent has that harness name.

## See also

- [Customizing Agents](customizing-overview.md) — overview of all customization approaches
- [Harness Field Reference](../../reference/harness-reference.md) — complete harness YAML field reference, merge rules, and resource referencing
- [Bring Your Own Agent](bring-your-own-agent.md) — building and registering custom agents from scratch
- [Configuring with AGENTS.md](customizing-with-agents-md.md) — repo-level instructions for all agents
- [Configuring with Skills](customizing-with-skills.md) — extending agents with skills
- [Default, derived, and custom agents](../../agents/topics/default-vs-custom.md) — when does configuration cross into derived or custom agent territory?
- [Escalation ladder](../../agents/topics/escalation-ladder.md) — prove-it path before deriving or replacing a core agent
- [Getting Started](../getting-started/) — initial setup
- [Bugfix Workflow](bugfix-workflow.md) — how agents work together
- [Standalone Mint](../infrastructure/standalone-mint.md) — running your own mint with custom agent roles
