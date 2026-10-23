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
| Control status comments | Configure [`status_notifications`](../../reference/config-reference.md#status_notifications) in `config.yaml` |
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
  - skills/my-custom-linting        # Merged with base skills (child overrides by skill identity)

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

To re-pin the `base:` URL to a new upstream commit (and recompute the integrity hash), run `fullsend agent update code`. That writes the new SHA into the local harness file and leaves `config.yaml` unchanged.

Test it locally first (add `--forge github` or `--forge gitlab` only if this
repo's `.fullsend/config.yaml` does not already set `forge:`):
```bash
fullsend run code --target-repo ./my-repo --env-file .env.local
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

### Example: swap the validation schema

Setting a single `validation_loop` field inherits the rest from the base
(`script`, `max_iterations`, `feedback_mode`, `preflight_check`):

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/code.yaml#sha256=abc...

validation_loop:
  schema: schemas/my-custom-schema.json
```

Setting `validation_loop.preflight_check` swaps the dependency probe. It runs in a minimal, credential-safe environment, and an out-of-scope `${VAR}` reference fails the run before the probe executes.

> **Note:** the `preflight_check` in that list is the validation loop's nested
> field. For a host-dependency probe that gates the whole run, set the
> top-level `preflight_check` field: it runs once before `pre_script` and
> sandbox creation and covers `pre_script`, `post_script`, and
> `validation_loop`. See the
> [Harness Field Reference](../../reference/harness-reference.md).

### What you can override

Any harness field can be overridden. See the [field merge rules](../../reference/harness-reference.md#field-merge-rules-for-base-and-overlays) for how each field type combines with the base:

- **Change model, timeout, image, scripts** — scalars replace the base value.
- **Add skills** — your entries are merged with the base's by skill identity (declared `SKILL.md` name when on disk, otherwise directory basename); a matching child skill overrides the base entry. Nested forge skills that share a leaf directory name are kept alongside each other. **Add plugins or host_files** — your entries are concatenated with the base's, base first.
- **Override files inside a skill** — repeat the skill's entry in map form
  with [per-file overrides](customizing-with-skills.md#overriding-single-files-in-a-skill);
  the child's override keys merge with any the base declares, child keys win.
- **Add or override env vars** — maps are merged; your keys win on collision.
- **Override validation_loop fields** — child non-zero values win; omitted fields inherit from the base (for example, set only `schema` to swap the schema without copying `script`). **Replace security config** — child replaces the entire block.

Base chains support up to 5 levels. Circular references are detected and rejected. Resolution order: base chain, child overrides, overlay resolution.

> **Note:** `allowed_remote_resources`, `allow_runtime_fetch`, and `max_runtime_fetches` are NOT inherited from base harnesses — the child must declare its own. This prevents a base harness from injecting arbitrary URL prefixes or enabling runtime fetching in the child.

> **Config-level fallback:** Separately from base-harness inheritance, the config-level `allowed_remote_resources` from the repository's `.fullsend/config.yaml` (layered over `config.base.yaml`) acts as a fallback for all URL resolution. URLs trusted at the config level are accepted even when the child harness omits the field. This is a distinct trust layer from base composition — the config-level list is set by the repository's administrators, not by base harness authors.

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

Only profiles listed in `openshell.profiles` are imported. A YAML file that merely exists under the `profiles/` directory is **not** imported unless the harness names it. To use a local profile as a per-repo override, list it explicitly under its own `id` (e.g., `profiles/myorg-vertex-ai.yaml`); the child-wins dedup rule applies by `id`. The `fullsend-<name>` ids of the built-in profiles are reserved: list a copy under one and `fullsend run` warns and uses it for now, and a later release rejects it.

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
  `GITHUB_TOKEN`, `PUSH_TOKEN`).
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
  OpenShell credential: a placeholder resolves wherever it appears in a
  request to a bound host, and the registry echoes unknown package names in
  its 404 body. A value read back that way is the job's own workflow token
  until the job ends, and through the proxy it can only reach the hosts the
  role's profiles allow.

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
2. **Unique skill names** — a repo skill whose identity (its declared
   `SKILL.md` `name:`, or its directory name when none is declared) matches a
   built-in's is ignored and produces a warning (see
   [skill precedence](customizing-with-skills.md#skill-precedence)).
3. **Specificity wins** — vague augmentations lose to hard default
   instructions. Own exact fields; use word limits and templates.
4. **Sub-agents are not wrapper skills** — if you need a new review dimension,
   ship `sub-agents/<name>.md` (and parent dispatch updates when the
   orchestrator uses a fixed roster). Do not invent a parallel
   `*/SKILL.md` that embeds the same content.
5. **Prefer the lightest shipping path** the current docs support —
   upstream contribution, file-level skill override, or whole-skill fork
   only when that is still required.

To add, replace or remove a single `sub-agents/<name>.md` without vendoring
the whole skill, list
[per-file overrides](customizing-with-skills.md#overriding-single-files-in-a-skill)
on the skill's `skills:` entry. Each run also writes a persona manifest that
pre-scripts, post-scripts and skills read through `FULLSEND_PERSONAS_FILE`,
so a roster change made by an override is visible to them
([#6158](https://github.com/fullsend-ai/fullsend/issues/6158)). An
orchestrator whose dispatch list is hard-coded still needs a parent-skill
change to call an added persona.

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

On Claude and pi runs the sandbox already has `/sandbox/workspace/bin` on its
`PATH`, after the image's own directories. A file there adds a command; it
does not replace a command the image already provides, such as `git`,
`python3` or `node`. Codex and OpenCode runs restore the `PATH` from before
the workspace `.env` is read, so `/sandbox/workspace/bin` is not on the codex
or OpenCode agent's `PATH`; call the script by its absolute path there. To make a script
available as a command on Claude and pi runs, drop it there:

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

`host_files` cannot write to paths the runner manages. Loading the harness
fails with `dest "..." is reserved for the runner` when a `dest` is one of:

- `/sandbox/workspace/.env`
- `/sandbox/workspace/bin/` followed by `claude`, `codex`, `pi`, `opencode`,
  `fullsend` or `fullsend-check-output`
- anything under `/sandbox/claude-config`, `/sandbox/codex-config`,
  `/sandbox/pi-config`, `/sandbox/workspace/.fullsend`,
  `/sandbox/workspace/.security`, `/sandbox/.venv` or `/sandbox/go/bin`

`dest` must also be an absolute path under `/sandbox/` or `/tmp/` with no `..`
path component. Other
names in `/sandbox/workspace/bin` and files in `/sandbox/workspace/.env.d/`
work as shown here.

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

3. At startup the sandbox reads every `*.env` file under
   `/sandbox/workspace/.env.d/`, picking up your PATH addition. Claude, codex,
   pi and OpenCode launches read these files as data, not shell: write plain
   `export NAME=value` lines, one assignment per line and without
   `${NAME:-default}` (see
   [`.env.d` host files](../../reference/harness-reference.md)).

On Claude and pi runs, after the `.env.d` files are read, the runner puts
its own `PATH` back in front, so your directory ends up after the runner's
entries: a command found only in `/opt/my-toolchain/bin` is picked up from
there, and a command the image also provides, such as `git` or `go`, is
picked up from the image. To replace one of those, extend the sandbox image
(see [Extending the sandbox image](#extending-the-sandbox-image)). Codex and
OpenCode runs restore the `PATH` from before `.env`, so `.env.d` additions are
not on the codex or OpenCode agent's `PATH`.

Launches and the runner's probes read `.env.d` files as data, and shell
constructs in them (command substitution, `;`-joined commands, `${NAME:-default}`
and other lines that are not plain assignments) are skipped with a warning.
`.env` itself is still valid shell that an agent may source, so keep treating
these files like `pre_script`. `host_files` can also place configuration that tools load, such
as git config or shell rc files; that is trusted like `pre_script` too.

#### Reserved `env.sandbox` names

`env.sandbox` cannot change runner infrastructure or security settings. The
runner drops reserved names, such as `PATH`, `HOME`, runner-owned `FULLSEND_*`
variables, credentials it mints, and proxy, TLS-trust, Git, interpreter and
shell-behavior variables like `HTTPS_PROXY`, `SSL_CERT_FILE`,
`GIT_SSH_COMMAND` and `NODE_OPTIONS`, instead of exporting them into the
sandbox. Matching is case-insensitive, and `fullsend run` warns about every
key it drops. The gate covers `env.sandbox` only; it is not a reason to move
those settings into `.env.d` files.

For the full list, how to check a harness with `fullsend lock`, and where
proxy and certificate settings go instead, see [Reserved `env.sandbox`
names](../../reference/harness-reference.md#reserved-envsandbox-names) and
[Migrating proxy and certificate
settings](../../reference/harness-reference.md#migrating-proxy-and-certificate-settings)
in the harness reference.

### Adding a skill

Create `.fullsend/skills/my-skill/SKILL.md` in the target repository, or `skills/my-skill/SKILL.md` in a shared agents repo:

```markdown
# My Custom Skill

Custom domain knowledge for this organization.

## Examples

...
```

Reference the skill in your harness's `skills:` list. The skill is available to all agents that include it in their harness configuration. See [Configuring with Skills](customizing-with-skills.md) for details on skill authoring and precedence.

## Agent roles

On the hosted mint, agents run as one of a **fixed** set of built-in roles.
An agent's name is separate from its role — the `code` and `fix` agents both
run as the `coder` role. For the built-in roles and their GitHub Apps, and to
use your own identity or a custom role, see
[Custom Agent Identity](custom-agent-identity.md#built-in-roles). For each
role's permissions, see [`fullsend agent` — Roles](../../cli/agent.md#roles).

## Status notifications

Agent workflows post status comments, and optionally emoji reactions, on
issues and PRs when they start and complete. Control this with
`status_notifications` in `.fullsend/config.yaml`; see
[`status_notifications` in the Config Reference](../../reference/config-reference.md#status_notifications)
for the fields, completion modes, and reaction behavior.

## Disabling agents

To disable an agent (including built-in scaffold agents) without removing
its role, add an entry with `enabled: false` in your config:

```yaml
agents:
  - name: retro
    enabled: false
```

This prevents the agent from dispatching and from resolving via
`fullsend run`. The role can stay in `roles` — only the agent
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

## Choosing when review runs

By default, the dispatch workflow decides when the review agent runs: when a
pull request is opened, pushed to or marked ready, when the
`ready-for-review` label is added, and on a `/fs-review` comment. To decide it
yourself, give the `review` entry in `.fullsend/config.yaml` a `trigger`: a
[CEL expression](cel-triggers-reference.md) over the event. The review still
runs in the same `Review` job; the trigger only decides whether that job
starts.

To stop the automatic review on every pull request but keep `/fs-review`, add
this entry to `.fullsend/config.yaml` on your default branch:

```yaml
agents:
  - name: review
    trigger: >-
      event.transition.kind == "comment_added"
      && has(event.entity.linked_change_proposal)
      && has(event.transition.comment.command)
      && event.transition.comment.command == "/fs-review"
      && event.actor.kind == "human"
```

Opting in changes more than when review runs: until
[#8329](https://github.com/fullsend-ai/fullsend/issues/8329) ships, pushes
from the fix agent no longer start a re-review. An invalid trigger makes every
agent run in the repository fail. Before you opt in, read
[Choosing when review runs](cel-triggers-reference.md#choosing-when-review-runs)
in the CEL Triggers Reference. It covers what opts a repository in, the
differences from the default rules, how to read the decision in the `Route`
job, and how to
[check a trigger before you merge](cel-triggers-reference.md#checking-a-trigger-before-you-merge).

## Choosing when retro runs

By default, the dispatch workflow starts the retro agent when a pull request
is closed, merged or not, and on a `/fs-retro` comment on an issue or a pull
request. To decide it yourself, give the `retro` entry in
`.fullsend/config.yaml` a `trigger`. The retro still runs in the same `Retro`
job; the trigger only decides whether that job starts.

To stop the retro on every closed pull request but keep `/fs-retro`, add this
entry to `.fullsend/config.yaml` on your default branch:

```yaml
agents:
  - name: retro
    trigger: >-
      event.transition.kind == "comment_added"
      && has(event.transition.comment.command)
      && event.transition.comment.command == "/fs-retro"
      && event.actor.kind == "human"
```

For which events the trigger is asked about and who can start a retro, see
[Choosing when retro runs](cel-triggers-reference.md#choosing-when-retro-runs)
in the CEL Triggers Reference.

## Choosing when triage and prioritize run

By default, the dispatch workflow starts triage when an issue is opened or
edited, when the `ready-for-triage` label is added to an issue, and on a
`/fs-triage` comment. It starts prioritize on a `/fs-prioritize` comment. To
decide it yourself, give the `triage` or `prioritize` entry in
`.fullsend/config.yaml` a `trigger`. Each agent opts in on its own: a `triage`
trigger does not change when prioritize runs.

To stop triaging every new or edited issue but keep `/fs-triage`, add this
entry to `.fullsend/config.yaml` on your default branch:

```yaml
agents:
  - name: triage
    trigger: >-
      event.entity.kind == "work_item"
      && event.transition.kind == "comment_added"
      && has(event.transition.comment.command)
      && event.transition.comment.command == "/fs-triage"
      && event.actor.kind == "human"
```

This trigger leaves out the `ready-for-triage` label, which the triage agent
adds to each sub-issue it creates when it splits an issue. For a label arm
that keeps splits triaged, which trigger decides each event, and the built-in
triage rules written as a trigger, see
[Choosing when triage and prioritize run](cel-triggers-reference.md#choosing-when-triage-and-prioritize-run)
in the CEL Triggers Reference.

## Agent dispatch authorization

By default, fullsend checks the forge's permission API to decide who can
trigger agents. Users with `write` or above can trigger all agents; users
with `triage` can trigger the built-in observation agents (`/fs-triage`,
`/fs-review`). This applies to **GitHub webhook dispatch** (slash commands,
label-triggered dispatch, and event-triggered dispatch).

On the CEL dispatch path (GitHub `fullsend dispatch` and the Jira poll), the
threshold follows the harness `role:`:

- `role: triage` or `role: review` — triage-level actors can trigger it,
  including OWNERS reviewers on GitHub when
  [OWNERS authorization](owners-file-authorization.md) is enabled.
- Any other `role:` — the actor needs `write`.

GitLab uses its own stage router, which applies the same triage and write
split to slash commands. The GitLab agent job also requires the event actor
to hold Developer access or higher for every stage except retro and
prioritize, so automatic triage of a newly opened issue runs only when its
author is a Developer, Maintainer, or Owner. Jira work items dispatched to a
GitLab project skip that check, because their actor has no GitLab account; the
Jira poller authorizes them by Jira project role (see
[Jira integration](jira-integration.md#gitlab-runtime)).
See the
[Authorization Contract](../../normative/authorization/v1/README.md) for the
full role hierarchy, exception rules, and implementation notes.

To extend trigger access beyond forge collaborators using Prow-style
OWNERS files, see the
[OWNERS file authorization guide](owners-file-authorization.md).

## See also

- [OWNERS file authorization](owners-file-authorization.md) — using Prow-style OWNERS files to authorize agent dispatch
- [Customizing Agents](customizing-overview.md) — overview of all customization approaches
- [Harness Field Reference](../../reference/harness-reference.md) — complete harness YAML field reference, merge rules, and resource referencing
- [Bring Your Own Agent](bring-your-own-agent.md) — building and registering custom agents from scratch
- [CEL Triggers Reference](cel-triggers-reference.md) — writing triggers, routing the built-in review, retro, triage and prioritize agents, and checking a trigger before you merge
- [Configuring with AGENTS.md](customizing-with-agents-md.md) — repo-level instructions for all agents
- [Configuring with Skills](customizing-with-skills.md) — extending agents with skills
- [Default, derived, and custom agents](default-vs-custom.md) — when does configuration cross into derived or custom agent territory?
- [Escalation ladder](escalation-ladder.md) — prove-it path before deriving or replacing a core agent
- [Authorization Contract](../../normative/authorization/v1/README.md) — role hierarchy, thresholds, and exceptions
- [Getting Started](../getting-started/) — initial setup
- [Bugfix Workflow](bugfix-workflow.md) — how agents work together
- [Standalone Mint](../infrastructure/standalone-mint.md) — running your own mint with custom agent roles
