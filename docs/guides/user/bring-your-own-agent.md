# Bring Your Own Agent

Add a custom agent to fullsend — from `agent new` to a running pipeline. This
guide gets you from nothing to a running agent, then documents every piece
you might want to change by hand.

To configure an existing agent (model, timeout, skills, env vars) without
building from scratch, see
[Configuring Agent Behavior](customizing-agents.md). For a quick overview of
all customization options, see
[Customizing Agents](customizing-overview.md).

## Quick start

[`fullsend agent new`](../../cli/agent.md#agent-new) writes every file an
agent needs and registers it, so you edit prose rather than plumbing:

```bash
fullsend agent new lint-docs \
  --role triage --description "Check docs changes for broken links"
```

```
  ✓ Created agent "lint-docs" in .fullsend
  harness/lint-docs.yaml
  agents/lint-docs.md
  schemas/lint-docs-result.schema.json
  scripts/post-lint-docs.sh
  policies/base.yaml
  ✓ Added agent "lint-docs"

Next:
  ...
```

1. **Generate.** The command needs the fullsend CLI on your `PATH` and a
   repository already scaffolded with
   [`fullsend github setup`](../getting-started/configuring-github.md) — see
   [Before you begin](#before-you-begin) for the full prerequisite list.
   The generated agent works on GitHub: its harness, prompt and post-script
   all use GitHub. On GitLab, scaffold with `fullsend repos install` (see
   [Configuring GitLab](../getting-started/configuring-gitlab.md#installing-fullsend))
   and adapt those three files; for the
   harness `role:` and its credentials, see
   [Custom roles](../getting-started/configuring-gitlab.md#custom-roles) and
   the [role-credential contract](../../contributing/gitlab-role-credentials.md).
   The command above generates an
   agent on the default route, Claude on Vertex (when the repository's default
   runtime is `claude` or unset) — for OpenAI, or a mix of the
   two, see [Pick a route](#pick-a-route).
2. **Fill in the prompt.** `agents/lint-docs.md` is the only file with
   sections left for you to fill — it ships with marked sections because
   deciding what the agent actually does is the one thing the generator
   cannot do for you. Everything else is complete.
3. **Dry-run locally.**

   ```bash
   POST_LINT_DOCS_DRY_RUN=1 fullsend run lint-docs \
     --target-repo . --env-file .env.local
   ```

   This runs the real sandbox and prints the comment instead of posting it.
   A run that finds nothing to report ends like this:

   ```text
     ✓ Agent exited with code 0 (39.2s)
     ...
     • Running post-script: .fullsend/scripts/post-lint-docs.sh
   post-lint-docs: status=ok, nothing to post (dry run: did not check for an earlier findings comment to replace)
     ✓ Post-script completed (0.3s)
   ```

   `.env.local` needs `FULLSEND_WORK_ITEM_URL`, `ISSUE_NUMBER`,
   `REPO_FULL_NAME`, `GH_TOKEN`, and whatever the agent's route needs besides
   those — see
   [Pick a route](#pick-a-route). `GH_TOKEN` must be a real token: a
   connectivity check runs before the agent does. See
   [Running agents locally](running-agents-locally.md).
4. **Commit and comment.** Commit `.fullsend/`, then comment `/fs-lint-docs`
   on an issue or pull request to run it in CI.

## Pick a route

The route — which model, on which credential path — decides what a local
run needs in `.env.local` beyond the GitHub variables every route uses
(`FULLSEND_WORK_ITEM_URL`, `ISSUE_NUMBER`, `REPO_FULL_NAME`, `GH_TOKEN`).
Pick the row that matches the agent you want; CI is covered below the table.

| Route | `agent new` flags | harness `providers:` | Env beyond the GitHub variables |
|-------|-------------------|-----------------------|----------------------------------|
| Claude on Vertex | `--runtime claude`, or none when the repository's default runtime is `claude` or unset | `vertex-ai`, `github-ro` (`github` for `--role coder`), `openai` | `ANTHROPIC_VERTEX_PROJECT_ID`, `CLOUD_ML_REGION`, `GOOGLE_APPLICATION_CREDENTIALS` |
| codex on OpenAI | `--runtime codex --model openai/<id>` | `github-ro` (`github` for `--role coder`), `openai` | `OPENAI_API_KEY` |
| pi on Vertex | `--runtime pi` | `vertex-ai`, `github-ro` (`github` for `--role coder`), `openai` | `ANTHROPIC_VERTEX_PROJECT_ID`, `CLOUD_ML_REGION`, `GOOGLE_APPLICATION_CREDENTIALS` |
| pi on OpenAI | `--runtime pi --model openai/<id>` | `github-ro` (`github` for `--role coder`), `openai` | `OPENAI_API_KEY` |
| pi, OpenAI parent + Vertex sub-agents | `--runtime pi --model openai/<id>`, then uncomment the generated `overlays:` block | `github-ro`/`github`, `openai`, plus `vertex-ai` once uncommented | `OPENAI_API_KEY`, plus the three Vertex variables once uncommented |
| pi, Vertex parent + sub-agents on OpenAI | `--runtime pi`, then `fullsend agent set <name> --subagent default=openai/<id>` (or `<persona>=openai/<id>` for one persona) | `vertex-ai`, `github-ro`/`github`, `openai` | The three Vertex variables above, plus `OPENAI_API_KEY` |

Notes on the last two rows:

- **OpenAI parent, Vertex sub-agents.** `agent new --model openai/<id>` ends
  the harness with the Vertex settings commented out. Remove the `# ` from
  every line under the first one, add `Agent` to the `tools:` list in
  `agents/<name>.md`, and add the three Vertex variables. Without the
  credentials file the run stops before the sandbox starts, rather than the
  sub-agents failing part-way through. Codex gets no such block — its
  sub-agents take OpenAI model ids only.
- **Vertex parent, sub-agents on OpenAI.** The harness must declare the
  `openai` provider, which `agent new` already does for every route;
  `agent set --subagent` points sub-agents at it, and you add `Agent` to the
  `tools:` list in `agents/<name>.md` so the agent can dispatch them; without
  it, no sub-agent is found and no OpenAI credential is created. `default`
  routes an `Agent` call that names no persona and omits `model`, and any
  persona with no model of its own; a persona
  name, such as `challenger` from a review skill, covers that persona only.
  An explicit `model` argument on an anonymous call (for example,
  `model: "sonnet"`) still wins and is not rerouted to OpenAI — omit `model`
  when you want the configured default:

  ```bash
  fullsend agent set lint-docs --runtime pi \
    --subagent default=openai/gpt-5.6-luna
  ```

  ```text
    ✓ Set agent "lint-docs": runtime="pi" model="" effort="" (empty = inherit) subagents: default=openai/gpt-5.6-luna
  ```

  The run then creates the OpenAI credential before the sandbox and logs
  `subagents: default → openai/gpt-5.6-luna (children that name no persona)`.
  See [pi § Route a persona to OpenAI](../../runtimes/pi.md#route-a-persona-to-openai).

In CI you set none of these variables yourself. The dispatch workflow and
`fullsend run` take them from what the setup commands record:

- **Vertex route:** the `FULLSEND_GCP_PROJECT_ID` and
  `FULLSEND_GCP_WIF_PROVIDER` secrets and the `FULLSEND_GCP_REGION` variable,
  which [`fullsend github setup`](../getting-started/configuring-github.md)
  writes. The Workload Identity Federation provider it records comes from
  [`fullsend inference provision`](../../cli/inference.md).
- **OpenAI route:** the Workload Identity Federation identifiers that
  [`fullsend inference openai request` and `import`](../../cli/inference.md)
  record. `request` writes the document an OpenAI administrator answers;
  `import` writes their reply into `.fullsend/config.yaml`, which you commit
  (or, with `--variables`, into three `FULLSEND_OPENAI_*` repository
  variables). A `FULLSEND_OPENAI_API_KEY` secret works instead — see
  [`fullsend run` § OpenAI credentials on pi and codex](../../cli/run.md#openai-credentials-on-pi-and-codex).

## How a run works

One run goes from a slash-command comment to a posted reply:

```mermaid
sequenceDiagram
  autonumber
  actor User as GitHub comment
  participant Dispatch as Dispatch workflow
  participant Runner as fullsend run
  participant Mint
  participant Gateway as OpenShell gateway
  participant Sandbox as Sandbox (agent)
  participant Model
  participant Post as Post-script

  User->>Dispatch: /fs-<name>
  Dispatch->>Dispatch: authorize, match each harness trigger
  Dispatch->>Runner: start a runner job for the agent
  Runner->>Mint: exchange the job's OIDC token
  Mint-->>Runner: short-lived token for the harness role
  Runner->>Gateway: register providers, create sandbox
  loop until the agent writes its result
    Sandbox->>Gateway: model or GitHub request
    Gateway->>Model: same request (GitHub and OpenAI placeholders swapped for the real credential)
    Model-->>Sandbox: response
  end
  Sandbox-->>Runner: agent-result.json
  Runner->>Mint: re-mint for the post-script
  Runner->>Post: run with the result and the token
  Post->>User: reply posted on the issue or pull request
```

The dispatch workflow matches the comment against the `trigger:` of every
registered harness and starts `fullsend run` for each match. `fullsend run`
gets a short-lived GitHub token for the harness's `role:` from the mint (a
local run with no mint URL from `--mint-url`, `config.yaml`/`config.base.yaml`,
or `FULLSEND_MINT_URL` — or where GitHub Actions OIDC isn't available —
skips this and uses your `GH_TOKEN`), sets up the providers on the OpenShell
gateway, and starts the sandbox. For GitHub and OpenAI the agent never holds
a real credential:
the gateway gives it a placeholder and swaps in the real value on each
request a declared provider covers. On the Vertex route the GCP credentials
file is copied into the sandbox, and the model calls use it directly. After
the sandbox exits, `fullsend run` re-mints a token (again only with a mint)
and hands it to the post-script, which posts the reply. Scripts cannot mint
their own.

**What lives where.** Your repository holds everything that is specific to
the agent: the harness, the agent's prompt, the result schema, the sandbox
policy, the post-script, and any pre-script you add. The `fullsend` binary
holds the rest: the dispatch logic, `fullsend run`, and the definitions of the built-in
providers and their profiles (`vertex-ai`, `github-ro`, `github`, `openai`,
and others), which is why a generated harness names providers without any
files under `providers/`. The mint is a hosted service.

## What gets generated

For each field below: whether `agent new` writes it, what sets it, and when
you would add or change it by hand.

| File | Field | Written by `agent new`? | Set by | Add or change by hand when |
|------|-------|--------------------------|--------|------------------------------|
| `harness/<name>.yaml` | `agent:` | always | `agents/<name>.md` | — |
| `harness/<name>.yaml` | `policy:` | always, `policies/base.yaml` | — | point it at another policy file, or a pinned URL |
| `harness/<name>.yaml` | `readonly_repo:` | for `--role review` | `--role` | — |
| `harness/<name>.yaml` | `description:` | always | `--description` / spec `description` | — |
| `harness/<name>.yaml` | `role:` | always | `--role` / spec `role` | — |
| `harness/<name>.yaml` | `slug:` | always | `--slug` / spec `slug` | the GitHub App is installed under a different slug |
| `harness/<name>.yaml` | `image:` | always; the default image is pinned to a digest | `--image` / spec `image` | run a different image |
| `harness/<name>.yaml` | `providers:` | always | `--runtime` / `--model` (see [Pick a route](#pick-a-route)) | a provider the route doesn't cover — see [Remote providers and profiles](customizing-agents.md#remote-providers-and-profiles) |
| `harness/<name>.yaml` | `model:`, `effort:` | always | `--model` / `--effort` or spec keys | — |
| `harness/<name>.yaml` | `post_script:` | always, `scripts/post-<name>.sh` | — | a `pre_script:` — never generated, see [Scripts](#scripts) |
| `harness/<name>.yaml` | `host_files:` | Vertex routes only: the GCP credentials file, marked `optional: true` | `--runtime` / `--model` | a file your prompt reads, such as pre-script output |
| `harness/<name>.yaml` | `env.runner`, `env.sandbox` | always: the GitHub variables, plus the Vertex variables on a Vertex route | `--runtime` / `--model` | a variable your prompt reads beyond those |
| `harness/<name>.yaml` | `timeout_minutes:` | always | `--timeout-minutes` / spec `timeout_minutes` | — |
| `harness/<name>.yaml` | `trigger:` | always | `--on` / `--trigger` or spec `on`/`trigger` | — |
| `harness/<name>.yaml` | `validation_loop:` | only when asked for | `--validation-loop` / spec `validation_loop` | — |
| `harness/<name>.yaml` | `overlays:` (commented) | only for pi on an `openai/` model | — | uncomment for Vertex sub-agents ([Pick a route](#pick-a-route)); write your own for other conditional fields |
| `harness/<name>.yaml` | `skills:`, `base:` | never | — | see [Skills](#skills), [Harness composition with `base`](#harness-composition-with-base) |
| `agents/<name>.md` | `name:`, `model:` | always | `<name>` / resolved model | — |
| `agents/<name>.md` | `description:` | always | `--description` / spec `description` | — |
| `agents/<name>.md` | `tools:` | always, a role/runtime default | — | tighten the Bash allowlist, or add `Agent` for any sub-agents |
| `agents/<name>.md` | `skills:`, `disallowedTools:` | never | — | see [Skills](#skills) |
| `schemas/<name>-result.schema.json` | `status`, `summary`, `comment` | always | — | add a field for a richer result; update `post-<name>.sh` to read it |
| `config.yaml` `agents:` entry | `name:`, `source:` | always, unless `--no-register` | `<name>` / `harness/<name>.yaml` | — |
| `config.yaml` `agents:` entry | `runtime:` | only when a runtime is given | `--runtime` / spec `runtime` | — |
| `config.yaml` `agents:` entry | `model:`, `effort:`, `subagents:` | never — `agent set` writes these, not `agent new` | `fullsend agent set` | per-agent tuning after generation |

## Troubleshooting

Rows are keyed on the exact error text where one exists. Longer commands sit
in the code block below the table, not in a cell.

| Error | Cause | Fix |
|-------|-------|-----|
| `API Error: Error code policy_denied` on the first model call (0 tokens, ~2s) | The gateway denied the agent's *binary*, not the model. Built-in profiles already allow the runtime binaries; this comes from a custom profile | Check that profile's `binaries:` list (for Claude, both `**/claude` and `**/claude.exe`) — see the grep command below |
| Agent crashes at 0s, sandbox can't reach the model | `vertex-ai` is missing from `providers:`, or `ANTHROPIC_VERTEX_PROJECT_ID` or `CLOUD_ML_REGION` is set but empty | Add `vertex-ai` to `providers:`. Locally, give the variables real values in `--env-file`; in CI, check the `FULLSEND_GCP_PROJECT_ID` secret and the `FULLSEND_GCP_REGION` variable — re-run `fullsend github setup`, or set one with `fullsend github set <owner/repo> <key> <value>` |
| `Provider "..." declared in harness but no definition found in ...`, then the agent crashes at 0s | A `providers:` entry names neither a built-in bare name nor a file that exists | Use a [built-in name](#pick-a-route), or commit `providers/<name>.yaml` under your own name |
| Agent never fires, no error anywhere | The harness has no `trigger:` | `agent new` always writes one; a hand-written harness needs one too — see [Triggers](../../cli/agent.md#triggers) |
| `"role field is required"` | `role:` is missing from the harness | Add `role:` |
| `403` / "role not allowed" from the mint | `role:` is not one the hosted mint serves | Use one of the roles in the [role table](../../cli/agent.md#roles); for a custom role see [Custom Agent Identity](custom-agent-identity.md) |
| `unknown role "..."` from `agent new` | Same as above, caught at generation time | See the role table in [`agent new`](../../cli/agent.md#roles) |
| `validating files: policy field is required` | The composed harness has no `policy:`, and fullsend ships no default | Commit a copy of the fleet policy from [fullsend-ai/agents](https://github.com/fullsend-ai/agents), or set `policy:` to its URL with a `#sha256=` hash under `allowed_remote_resources`. A child that inherits `policy:` from its `base:` already has one |
| `validating files: policy: ... declares no fields` | The policy file is empty or comment-only | Replace it with a copy of the fleet policy from [fullsend-ai/agents](https://github.com/fullsend-ai/agents) |
| `validating files: policy: stat .../policies/base.yaml: no such file or directory` | The harness points at a policy file that isn't committed next to it | Commit `policies/base.yaml` with the agent, or point `policy:` at a committed or pinned URL |
| `host_files[0]: GOOGLE_APPLICATION_CREDENTIALS is empty; mark the mount optional or provide a credential file` | pi on `openai/`, Vertex sub-agent block uncommented, no credentials file set | Set `GOOGLE_APPLICATION_CREDENTIALS` — see [Pick a route](#pick-a-route). Don't mark the mount optional |
| `validating env:` followed by unresolved host variables at `fullsend run` | A `${VAR}` in the harness is unset | Supply it via `--env-file` locally. In CI, the variables a generated harness uses come from the setup commands in [Pick a route](#pick-a-route) |
| `provider credentials are not declared by profile '<profile-id>'` | The provider supplies credential keys the profile does not declare; stale copies under built-in names or IDs may still use `_NOOP_*` placeholder credentials | For stale built-in overrides, finish [Upgrading agents generated before built-in providers](#upgrading-agents-generated-before-built-in-providers). For custom profiles, align the provider's credential keys with the profile's declarations |
| The same error on your machine, with bare names and no `fullsend-github*` profile left | The OpenShell gateway still holds an older profile with that id; `fullsend run` cannot replace a profile while a provider uses it | Delete and re-create the providers — see the command below |
| `provider profile 'fullsend-github-ro' requires static credentials: GH_TOKEN` at `fullsend run` | `GH_TOKEN` is unset or empty on the machine running `fullsend run` | Put a real token in your env file, e.g. `echo "GH_TOKEN=$(gh auth token)" >> .env.local` |
| `Vertex inference requires GOOGLE_APPLICATION_CREDENTIALS to point to an existing file` (or `a regular/non-empty file`) | The variable is unset or points at a missing path, directory, or empty file | Point it at a real file — [Get GCP credentials](running-agents-locally.md#get-google-cloud-platform-credentials) |
| `reading behaviour script .../behaviour/current-scenario.yaml: ... no such file or directory` | `--runtime dummy` with no scripted result | Write `.fullsend/behaviour/current-scenario.yaml` — see [Testing locally](#testing-locally) |
| `sub-agent model resolves to Vertex, but GOOGLE_APPLICATION_CREDENTIALS is not set` at `fullsend run`, or `provider "anthropic-vertex" is not available in this run (Vertex sub-agents need GOOGLE_APPLICATION_CREDENTIALS ...` in a sub-agent's result | pi on `openai/` dispatched a Vertex sub-agent with no credentials file in the sandbox: the variable is unset, or the harness has no `host_files` mount for it | Set `GOOGLE_APPLICATION_CREDENTIALS` and use the overlay from [Pick a route](#pick-a-route), or keep sub-agents on `openai/` models; see [pi › Vertex sub-agents under an OpenAI parent](../../runtimes/pi.md#vertex-sub-agents-under-an-openai-parent) |
| Agent can't find input files | Pre-script output paths don't match `host_files` entries | Align the two |
| Provider blocks requests | A second profile with the same `fullsend-<name>` id as a built-in provider, or a custom provider whose profile or definition is missing | For a built-in name, remove the other profile with that id. For a custom provider, list its profile in `openshell.profiles:` and commit its definition under `providers/` |
| A request to an endpoint fails inside the sandbox | No provider profile or inline `network_policies` rule allows the endpoint for that binary | Find the `DENIED` entry with the grep command below, then add a provider or an inline rule — see [Sandbox policy](#sandbox-policy) |
| Schema validation fails | Compare the sandbox output against the schema in `validation_loop` / `FULLSEND_OUTPUT_SCHEMA` | Re-run with `--keep-sandbox` to inspect |
| Agent not found | Verify registration | `fullsend agent list` |
| Agent not triggered by events | Verify the `trigger` expression | See [Verifying your trigger](cel-triggers-reference.md#verifying-your-trigger) |
| `allowed_remote_resources` error | URL agents need a matching prefix | `fullsend agent add` sets this automatically |
| `fullsend run` fails locally | Missing route credentials (GCP on the Vertex route, `OPENAI_API_KEY` on the OpenAI route) or sandbox image | See [Running agents locally](running-agents-locally.md) |
| Integrity hash mismatch | Remote content changed | `fullsend agent update <name>` |

```bash
# API Error: Error code policy_denied — see which binary was denied:
grep DENIED <run-dir>/logs/openshell-sandbox.log

# OpenShell gateway still holds a stale GitHub profile:
openshell provider list | awk '$2=="fullsend-github-ro" || $2=="fullsend-github"{print $1}' \
  | xargs -r openshell provider delete
# Each run re-creates the providers it needs.
```

See [Debugging network policies locally](running-agents-locally.md#debugging-network-policies-locally)
for more on the first command.

## Upgrading agents generated before built-in providers

`agent new` on v0.44.0 and earlier wrote `providers/*.yaml` and
`profiles/fullsend-*.yaml`, and the harness referenced them by path. Those
copies no longer receive fixes, and `fullsend run` warns about them:

```text
  ! provider "vertex-ai": the name is reserved for the definition built into fullsend, and a future release rejects the copy at ".../.fullsend/providers/vertex-ai.yaml". It is still used for now. Declare the bare name "vertex-ai" and delete the copy, or rename it to a name fullsend does not ship
  ! provider "github-ro": the name is reserved for the definition built into fullsend, and a future release rejects the copy at ".../.fullsend/providers/github-ro.yaml". It is still used for now. Declare the bare name "github-ro" and delete the copy, or rename it to a name fullsend does not ship
  ! provider profile "fullsend-vertex-ai" will be rejected in a future release: the id is reserved for the copy built into fullsend. Your copy is still used for now. Remove it from openshell.profiles and declare the bare provider name "vertex-ai" instead
  ! provider profile "fullsend-github-ro" will be rejected in a future release: the id is reserved for the copy built into fullsend. Your copy is still used for now. Remove it from openshell.profiles and declare the bare provider name "github-ro" instead
```

Make all four changes below in one commit. A harness with only some of them
mixes a copied GitHub provider or profile with a built-in one, and the two
may not agree on the `GH_TOKEN` credential (see the stale-profile rows in
[Troubleshooting](#troubleshooting)).

1. In `harness/<name>.yaml`, replace each built-in `providers:` path with
   its bare name (`providers/vertex-ai.yaml` becomes `vertex-ai`).
2. Remove the `profiles/fullsend-*.yaml` entries from `openshell.profiles:`.
   Keep entries for your own profiles, and delete the block if it is empty.
3. Under `env.sandbox`, delete the `GH_TOKEN: ${GH_TOKEN}` line. Keep the
   one under `env.runner`. The built-in GitHub providers give the sandbox a
   placeholder token; that line would put the real token in the sandbox.
4. Delete `providers/<built-in name>.yaml` and `profiles/fullsend-*.yaml`.
   Do steps 1–3 in every harness in the directory first: while a copy sits
   in `providers/`, even a bare name uses the copy, and the run keeps
   warning that it does.

The next `fullsend run` prints none of the warnings and reports each
provider as built in:

```text
    Provider "vertex-ai": using the definition shipped with fullsend (no providers/vertex-ai.yaml in the workspace)
    Provider "github-ro": using the definition shipped with fullsend (no providers/github-ro.yaml in the workspace)
```

## Building an agent by hand

`agent new` does steps 1, 2, and 4 of the sequence below for you — read on
when you need to go beyond what it generates, or want to write an agent from
scratch.

1. **Create the harness and agent definition** — write a harness YAML file
   that defines _how_ the agent runs and a Markdown file that defines _what_
   it does. See [Minimum viable agent](#minimum-viable-agent).
2. **Add a CEL trigger** — write a trigger expression so dispatch knows when
   to launch your agent. See
   [CEL Triggers Reference](cel-triggers-reference.md).
3. **Test locally** — run your agent with `fullsend run` before deploying to
   CI. See [Testing locally](#testing-locally).
4. **Register and deploy** — add your agent to `config.yaml` so dispatch
   discovers it. See [Registering your agent](#registering-your-agent).

The sections after [Minimum viable agent](#minimum-viable-agent) document
each piece you may write or change: the
[agent definition](#agent-definitions), [sandbox policy](#sandbox-policy),
[output schema](#output-schema), [skills](#skills),
[pre- and post-scripts](#scripts), [dispatch inputs](#dispatch-inputs), and
[secrets for host-side scripts](#pass-secrets-to-host-side-scripts-github).

### Before you begin

- **fullsend CLI** installed and available on your PATH.
- **Repository scaffolded.** Run [`fullsend github setup`](../getting-started/configuring-github.md) first — it creates `.fullsend/config.yaml` and the dispatch workflow. It does **not** create `policies/`; [`fullsend agent new`](#quick-start) writes that one, and you commit it with the agent. Writing the harness by hand instead? [Minimum viable agent](#minimum-viable-agent) lists what to supply. GitLab repositories are scaffolded instead by declaring the project in `repos.yaml` and running `fullsend repos install`, and use role-specific credentials (project service accounts, or fallback project access tokens) rather than GitHub Apps — see [Configuring GitLab](../getting-started/configuring-gitlab.md).
- **Inference set up for CI (CI only).** For agents running in GitHub Actions, on the Vertex route run [`fullsend inference provision`](../../cli/inference.md) to set up Workload Identity Federation, then give the provider it prints to [`fullsend github setup`](../getting-started/configuring-github.md), which stores it with the project and region. On the OpenAI route run [`fullsend inference openai request` and `import`](../../cli/inference.md), or set a `FULLSEND_OPENAI_API_KEY` secret.
- **GitHub Apps installed (CI only).** Your org needs the fullsend GitHub Apps — see [Configuring GitHub](../getting-started/configuring-github.md).

### How agents work

A fullsend agent has two parts:

1. **Harness file** (YAML) — _how_ the agent runs: sandbox image, policy, scripts, skills, credentials, timeouts.
2. **Agent definition** (Markdown) — _what_ the agent does: prompt, tools, model, skills.

Once registered, your agent runs automatically when a matching GitHub event arrives — an issue is opened, a label is applied, a comment is posted, or a PR is submitted. The harness `trigger` field contains a [CEL expression](cel-triggers-reference.md) that fullsend evaluates against incoming events to decide whether your agent should run. See [How a run works](#how-a-run-works) for the full pipeline from comment to posted reply.

You do not need to write a GitHub Actions workflow file for each custom agent. The dispatch workflow that `fullsend github setup` installs handles discovery and routing for all registered agents.

For local development and debugging, you can also run an agent directly with `fullsend run my-agent` — see [Testing locally](#testing-locally).

**Security model:** agents run inside a sandboxed environment. The sandbox policy enforces filesystem access, landlock, and process identity. Network access is typically managed via **provider profiles** referenced by name in the harness `providers:` list — the shared `policies/base.yaml` that `agent new` writes contains no network rules, since built-in agents use providers. Custom agents can also use inline `network_policies` in a per-agent policy file if providers don't cover their needs. Pre-scripts run on the trusted runner _before_ the sandbox starts; post-scripts run _after_ it exits.

### Minimum viable agent

You need a harness, an agent definition, and one supporting file. [`fullsend agent new`](#quick-start) writes all of them; the layout below is what it produces, and what you create yourself if you are building from scratch:

```
.fullsend/
+-- harness/my-agent.yaml                  # Execution config (you write)
+-- agents/my-agent.md                     # Agent prompt (you write)
+-- policies/base.yaml                     # Sandbox policy — commit it; CI never supplies one
```

Commit all three. `fullsend github setup` creates none of them.

**`harness/my-agent.yaml`:**
```yaml
agent: agents/my-agent.md
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest  # Pin to a digest before CI use
policy: policies/base.yaml
providers:
  - vertex-ai
role: triage                        # a role your mint SERVES — not the agent's name (see note below)
slug: my-org-my-agent               # install-time App discovery only; the mint never reads it
trigger: |
  event.entity.kind == "work_item"
    && event.transition.kind == "label_changed"
    && event.transition.label.name == "ready-for-my-agent"
    && event.transition.label.action == "added"
timeout_minutes: 15
# privilege_levels:            # optional; omitted = write for every stage
#   runtime: read              # LLM sandbox token; pre/post scripts stay write
```

> **`role` is not the agent's name.** The agent's name is `name:` in its `.md`;
> `role:` selects which GitHub App and permissions the mint issues. On the
> default (hosted) mint, `role:` must be one of the built-in roles it serves —
> `triage`, `coder`, `review`, `retro`, `prioritize`, `fullsend`. Pick the one
> whose permissions fit what your agent does (a code-writing agent uses
> `role: coder`). A made-up role like `role: my-agent` returns `403` from the
> mint. To use a *new* role or your *own* identity, you need your own mint —
> see [Custom Agent Identity](custom-agent-identity.md).

**`vertex-ai` above is a bare provider name**, not a path. Every provider
name fullsend ships —
`vertex-ai`, `github`, `github-ro`, `github-artifacts`, `gitleaks`,
`package-registries`, `atlassian-cloud`, `openai` — resolves to the
definition and profile built into the `fullsend` binary: no `providers/`
file, no `profiles/` file, and no `openshell.profiles` entry to add, so
nothing to keep in sync. These names are reserved for the built-in copies.
If a harness still uses its own copy under one (a `providers/<name>.yaml`
file, a path or URL entry, or an `openshell.profiles` entry with the
`fullsend-<name>` id), `fullsend run` uses that copy and prints a warning; a
later release rejects it. To customise a provider, define it under your own
name — see [Remote providers and profiles](customizing-agents.md#remote-providers-and-profiles).

> **Note (CI only):** the embedded `vertex-ai` profile controls network access only; real credentials are delivered via `host_files` (see [real-world example](#real-world-example-the-triage-agent)). Make sure you've completed the GCP prerequisites in [Before you begin](#before-you-begin).

**`agents/my-agent.md`:**
````markdown
---
name: my-agent
description: One-line description of what this agent does.
tools: Bash(gh,jq)
model: opus
---

You are my-agent. Your job is to [task description].

## Steps
1. Fetch input from environment variables
2. Analyze and process
3. Write JSON result to `$FULLSEND_OUTPUT_DIR/agent-result.json`

Do NOT push code, create issues, or modify anything directly.
Your only output is the JSON result file.
````

The agent's environment also carries its budget: `FULLSEND_TIMEOUT_MINUTES` (the harness's
`timeout_minutes`) and `FULLSEND_ITERATION_DEADLINE` (Unix seconds at which the iteration is killed),
and `TRACEPARENT` (this iteration's agent-span W3C trace context, so runtime telemetry joins the
Fullsend trace; empty when telemetry produced no valid span context). Write the result before the
deadline — see [`fullsend run` § Budget and deadline](../../cli/run.md#budget-and-deadline).

Network access (which APIs the agent can reach) is controlled by provider profiles or inline `network_policies`. The eight builtin provider names (`vertex-ai`, `github`, `github-ro`, `github-artifacts`, `gitleaks`, `package-registries`, `atlassian-cloud`, `openai`) resolve against the definition and profile built into the `fullsend` binary with no `openshell.profiles` entry at all. A fully custom provider type is different: it needs a provider definition (local or remote) together with a matching `openshell.profiles` entry (or one inherited via `base:` composition) to be imported — see [Remote providers and profiles](customizing-agents.md#remote-providers-and-profiles). For endpoints not covered by providers, inline `network_policies` in the policy YAML also work. Providers are the pattern used by fullsend's built-in agents, but custom agents can use whichever approach fits.

**Next steps:** [Register your agent](#registering-your-agent) so dispatch discovers it, then [write a CEL trigger](cel-triggers-reference.md#writing-cel-triggers) to control when it runs. To iterate on your agent locally before registering, see [Testing locally](#testing-locally).

### Real-world example: the triage agent

The [fullsend-ai/agents](https://github.com/fullsend-ai/agents) triage agent is a full production agent. The harness below is adapted from the current [`harness/triage.yaml`](https://github.com/fullsend-ai/agents/blob/main/harness/triage.yaml) (field order adjusted for readability):

```yaml
agent: agents/triage.md
doc: docs/triage.md
model: opus
image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
policy: policies/triage.yaml

role: triage
slug: fullsend-ai-triage

host_files:
  - src: common/env/gcp-vertex.env
    dest: /sandbox/workspace/.env.d/gcp-vertex.env
    expand: true
  - src: ${GOOGLE_APPLICATION_CREDENTIALS}
    dest: /tmp/.gcp-credentials.json
  - src: ${GCP_OIDC_TOKEN_FILE}
    dest: /sandbox/workspace/.gcp-oidc-token
    optional: true
  - src: env/triage.env
    dest: /sandbox/workspace/.env.d/triage.env
    expand: true

skills:
  - skills/issue-labels

pre_script: scripts/pre-triage.sh
post_script: scripts/post-triage.sh

validation_loop:
  script: scripts/validate-output-schema.sh
  schema: schemas/triage-result.schema.json
  max_iterations: 2

timeout_minutes: 10

overlays:
- when: 'runtime.forge == "github"'
  pre_script: scripts/pre-triage.sh
  post_script: scripts/post-triage.sh
  env:
    runner:
      ISSUE_URL: ${GITHUB_ISSUE_URL}
      GH_TOKEN: ${GH_TOKEN}
      FULLSEND_FORGE: github
    sandbox:
      ISSUE_URL: "${GITHUB_ISSUE_URL}"
      FULLSEND_FORGE: github
```

New agents should map `ISSUE_URL` from `FULLSEND_WORK_ITEM_URL`; the example
above shows the current built-in GitHub overlay before that migration.

Key patterns to note:

- **`policy: policies/triage.yaml`** is a per-agent policy that includes filesystem, landlock, process, and network rules (via inline `network_policies`). This agent predates the provider-based pattern — new agents can use `providers:` instead (see [Minimum viable agent](#minimum-viable-agent)).
- **`host_files`** copy credentials from the trusted runner into the sandbox. `expand: true` resolves `${VAR}` references before copying.
- **`validation_loop.schema`** references the JSON schema file directly — the validation script checks agent output against it.
- **`overlays`** uses CEL `when` expressions to conditionally apply scripts, skills, providers, openshell, host_files, and env vars. Resolution merges all matching entries in order: every entry whose `when` evaluates to true is applied, with later matches taking precedence over earlier ones. The CEL environment exposes `event` (the triggering event), `runtime.forge` (the effective forge platform), and `config` (per-repo config from config.yaml). When running without an event context (e.g., `fullsend run` or `fullsend lock`), `event` is an empty map — use `has(event.source)` to guard event field access: `has(event.source) && event.source.system == "jira"` instead of just `event.source.system == "jira"` to avoid "no such key" errors.
- **`common/env/gcp-vertex.env`** is referenced by relative path because both files live in the same repo. If your agent lives in a different repo, reference it by URL (see [Harness Field Reference — Referencing resources](../../reference/harness-reference.md#referencing-resources-local-vs-remote)) or copy it locally.

For the complete list of harness fields, see the [Harness Field Reference](../../reference/harness-reference.md).

### Agent definitions

The agent definition is Markdown with YAML frontmatter:

| Field | Purpose |
|-------|---------|
| `name` | Must match the filename (sans `.md`) |
| `description` | One-line summary |
| `tools` | The tools the agent may use, in Claude Code's names (`Read`, `Grep`, `Glob`, `LS`, `Bash(gh,jq)`, ...). Restrict to what's needed. On pi the names are translated, and two of them surprise people — see [What to write in `tools:`](../../runtimes/pi.md#what-to-write-in-tools) |
| `model` | LLM model (`opus`, `sonnet`, etc.) |
| `skills` | [Skill](../../glossary.md#skill) names to load, as uploaded under `skills/`: a skill's declared `SKILL.md` `name:`, or its directory basename when none is declared |
| `disallowedTools` | Bash patterns the agent is forbidden from running, e.g. `Bash(git push *), Bash(gh issue edit *)` |

When writing the agent body:
- **The agent writes JSON, scripts do actions.** The agent's only output is a
  structured JSON file matching the [output schema](#output-schema). All side
  effects (creating issues, posting comments, calling APIs) happen in
  [post-scripts](#post-script).
- **Name specific things.** Don't say "add caching." Say "use `casbin`
  v2.82.0 from `go.mod` with the RBAC model adapter in
  `pkg/api/middleware/`." Define scoring dimensions, thresholds, and output
  fields.
- **Include decision points.** Have the agent assess its own confidence and
  branch: act when confident, ask when uncertain.
- **Name every input.** List each environment variable and file the agent
  reads. A variable must be passed through `env.sandbox` and a file through
  `host_files`, or the agent reads an empty value at run time.

### Sandbox policy

The sandbox policy controls filesystem, process, and landlock restrictions.
Network access is handled separately, through providers (see below). Most
custom agents can reuse the `policies/base.yaml` that
[`fullsend agent new`](../../cli/agent.md#agent-new) writes — the same policy
the fleet agents run under. Write your own only when the agent needs
different filesystem or process rules, for example
`.fullsend/policies/my-agent.yaml`:

```yaml
version: 1
filesystem_policy:
  include_workdir: true
  read_only: [/usr, /lib, /proc, /dev/urandom, /app, /etc, /var/log]
  read_write: [/sandbox, /tmp, /dev/null]
landlock:
  compatibility: best_effort
process:
  run_as_user: sandbox
  run_as_group: sandbox
```

Point the harness `policy:` field at it. Either way, commit the policy: CI
does not supply one.

**Network access through providers (recommended).** List providers in the
harness `providers:` field. The built-in names
[above](#minimum-viable-agent) need no other file. For a service fullsend does
not ship, define a provider under your own name and list its profile under
`openshell.profiles`. The gateway composes only the profiles named there (or
inherited through `base:`) into the network policy, not every file under
`profiles/`:

```yaml
providers:
  - vertex-ai
  - github-ro
  - my-service

openshell:
  profiles:
    - profiles/my-service.yaml
```

See [Remote providers and profiles](customizing-agents.md#remote-providers-and-profiles)
for the provider and profile file formats.

**Network access through inline policies (alternative).** For a one-off
endpoint specific to a single agent, add `network_policies` to the policy
YAML:

```yaml
network_policies:
  my_service:
    name: my-service
    endpoints:
      - host: "api.example.com"
        port: 443
        protocol: rest
        enforcement: enforce
        access: read-only
    binaries:
      - path: "**/curl"
```

Inline rules and provider-composed rules coexist — composition is additive.
If both define the same endpoint, the duplicate is harmless.

Policy design principles:

- **The agent needs network access to whichever LLM provider it runs
  inference through.** Use the `vertex-ai` provider on the Vertex route and
  `openai` on the OpenAI route (see [Pick a route](#pick-a-route)). On the
  OpenAI route, the GCP credentials mount (the
  `${GOOGLE_APPLICATION_CREDENTIALS}` entry under `host_files:`) must be
  `optional: true` or removed; with `GOOGLE_APPLICATION_CREDENTIALS` unset, a
  required mount stops the run with
  `GOOGLE_APPLICATION_CREDENTIALS is empty; mark the mount optional or provide a credential file`.
  A `pi` agent on an OpenAI model whose sub-agents run on Vertex needs both
  providers and the mount — see
  [pi › Vertex sub-agents under an OpenAI parent](../../runtimes/pi.md#vertex-sub-agents-under-an-openai-parent).
- **Add network access only for what the agent needs.** If the agent doesn't
  need web search, don't allow it.
- **Use `binaries` to restrict which programs can access each endpoint.**
  This prevents the agent from using unexpected tools to exfiltrate data.
- **Prefer providers for shared services.** Use inline policies only for
  agent-specific endpoints.
- **Keep writes out of the sandbox.** Where the agent must read from an API,
  give it read-only access (such as the `github-ro` provider), and perform
  every write in a post-script.

To test a policy change, see
[Debugging network policies locally](running-agents-locally.md#debugging-network-policies-locally).

### Output schema

The agent's result file is checked against a JSON Schema, for example
`.fullsend/schemas/my-agent-result.schema.json`:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "My Agent Result",
  "type": "object",
  "additionalProperties": false,
  "required": ["status", "summary"],
  "properties": {
    "status": {
      "type": "string",
      "enum": ["ok", "findings", "error"]
    },
    "summary": {
      "type": "string",
      "maxLength": 280
    }
  }
}
```

Enforce it with `validation_loop` in the harness (`agent new
--validation-loop` writes this for you):

```yaml
validation_loop:
  script: scripts/validate-output-schema.sh
  schema: schemas/my-agent-result.schema.json
  max_iterations: 2
  feedback_mode: append
```

When the agent's output does not match, the agent is re-invoked with the
validation error and asked to fix it. This happens only when `feedback_mode`
is `append`; without it, the retry runs without the error text.
`max_iterations` is the total number of attempts, including the initial
invocation, so `max_iterations: 2` allows one re-invocation. Set
`maxLength` on every string the post-script passes to an external API, and
enumerate every value the post-script branches on, so the schema rejects
output the post-script cannot handle.

### Skills

A skill is a directory with a `SKILL.md` file that teaches the agent domain knowledge:

```
skills/issue-labels/
  SKILL.md            # Required: frontmatter + instructions
  scripts/            # Optional: helper scripts
  references/         # Optional: reference data
```

Reference in the agent frontmatter by name (`skills: [issue-labels]`) and in the harness by path (`skills: [skills/issue-labels]`). Skills can also be URLs with integrity hashes. See [Configuring with Skills](customizing-with-skills.md) for details on creating and managing skills.

For details on skill authoring, precedence, and extension points, see
[Configuring with Skills](customizing-with-skills.md).

### Scripts

Pre and post scripts run on the trusted runner outside the sandbox.

- **Pre-scripts** prepare the environment — fetch data, reset state, write files for `host_files` to copy in.
- **Post-scripts** act on agent output — apply labels, post comments, create PRs.

Post-scripts act with the role's minted token. Actions that need permissions no role grants (re-running CI jobs, dispatching workflows, deploying) belong in a follow-up workflow you own, chained on the run's artifact — see [Chaining Follow-up Workflows](chaining-follow-up-workflows.md).

Declare both scripts in the harness:

```yaml
pre_script: scripts/pre-my-agent.sh
post_script: scripts/post-my-agent.sh

host_files:
  # Written by the pre-script, copied into the sandbox
  - src: /tmp/workspace/my-input.json
    dest: /sandbox/workspace/my-input.json
    optional: true
```

Data reaches the sandbox through `host_files`:

1. The **pre-script** runs on the runner and writes files to
   `/tmp/workspace/`.
2. The **harness** copies those files into the sandbox through `host_files`.
3. The **agent** reads them inside the sandbox.

The pre-script uses credentials to fetch data and writes it to a file; the
harness copies the file, not the credentials, into the sandbox.

#### Pre-script

`.fullsend/scripts/pre-my-agent.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

WORKSPACE="/tmp/workspace"
mkdir -p "$WORKSPACE"

gh issue view "$ISSUE_NUMBER" --repo "$REPO_FULL_NAME" \
  --json number,state,labels \
  > "$WORKSPACE/my-input.json"

echo "Pre-script complete."
```

`ISSUE_NUMBER` and `REPO_FULL_NAME` are [dispatch inputs](#dispatch-inputs).
The example stages only structured fields: do not stage issue bodies or
comments where the agent reads them (see the snapshot note below).

**Skipping the run.** A pre-script can tell `fullsend run` not to start the
agent at all — useful when an open PR already addresses the issue, or when
the work is otherwise redundant. `fullsend run` exports
`FULLSEND_PRESCRIPT_OUTPUT` (a file path); the script appends `skipped=true`
to it, and the run reports a ⏭️ skipped status and exits 0 **before the
sandbox is created**:

```bash
if [[ -n "${FULLSEND_PRESCRIPT_OUTPUT:-}" ]]; then
  {
    echo "skipped=true"
    echo "reason=PR #${EXISTING_PR} already addresses this issue"
  } >> "${FULLSEND_PRESCRIPT_OUTPUT}"
  exit 0
fi
```

Always guard on the variable being set — older `fullsend` versions do not
export it, and appending to an empty path would fail the script. Malformed
content is a hard error rather than a silent proceed, so a mistyped skip
cannot turn into a duplicate agent run. See the
[pre-script output v1 contract](../../normative/prescript-output/v1/README.md)
for the full grammar, error semantics, and CI relay behavior.

**Exit code 78 — neutral skip.** For simple cases, a pre-script can skip the
run by exiting with code 78 instead of writing to the output file. The last
non-empty line of stdout becomes the skip reason:

```bash
# Scheduled agent: check if there's work to do
PENDING=$(gh api ... --jq 'length')
if [[ "${PENDING}" -eq 0 ]]; then
  echo "No issues need scoring"
  exit 78
fi
```

Exit 78 and the output-file protocol can be combined — the output file is
still parsed for `reason` and other outputs when exit 78 is used, but a parse
error does not block the skip. See the
[normative spec](../../normative/prescript-output/v1/README.md#exit-code-78--neutral-skip)
for full details.

**Hard failures.** A non-zero exit other than 78 fails the run. Print a
GitHub Actions error annotation so the message appears in the PR status
comment instead of a bare `exit status 1`:

```bash
echo "::error::Fix iteration ${ITERATION} exceeds bot cap of ${CAP}. Escalating to human."
exit 1
```

Without an annotation, the last non-empty stderr line (then stdout) is used.
See the [normative spec](../../normative/prescript-output/v1/README.md#hard-failure-diagnostics)
for the full preference order and sanitization rules.

**Pre-script context is a snapshot.** The pre-script sees the work item as it
was when the run started. Changes made after that (a new push, an edited title
or body) arrive only through a later run.
When a script decides something from the work item, read the head SHA, title
and body from the forge API (for example
`gh pr view "$PR_NUMBER" --repo "$REPO_FULL_NAME" --json headRefOid,title,body`),
not from the event payload, which can be older still. Do not stage that content
where the agent reads it: unfiltered source content must never reach the
model. Forge context for the agent will come from a filtered snapshot that the
runner stages (not yet implemented).

#### Post-script

`.fullsend/scripts/post-my-agent.sh`, for the schema in
[Output schema](#output-schema):

```bash
#!/usr/bin/env bash
set -euo pipefail

# Prefer the validated iteration directory set by the harness
# (FULLSEND_VALIDATED_ITERATION_DIR, always an absolute path) — without
# it, scanning for the last iteration can pick up output that failed
# validation. Fall back to scanning for the last iteration for
# harnesses with no validation_loop.
if [[ -n "${FULLSEND_VALIDATED_ITERATION_DIR:-}" ]]; then
  RESULT_FILE="${FULLSEND_VALIDATED_ITERATION_DIR}/agent-result.json"
else
  RESULT_FILE=""
  for dir in iteration-*/output; do
    if [[ -f "${dir}/agent-result.json" ]]; then
      RESULT_FILE="${dir}/agent-result.json"
    fi
  done
fi

if [[ -z "${RESULT_FILE}" ]] || [[ ! -f "${RESULT_FILE}" ]]; then
  echo "ERROR: agent-result.json not found"
  exit 1
fi

# Validate JSON structure before extracting fields.
# The agent runs in an untrusted sandbox — treat its output as untrusted input.
if ! jq empty "${RESULT_FILE}" 2>/dev/null; then
  echo "ERROR: agent-result.json is not valid JSON"
  exit 1
fi

STATUS=$(jq -r '.status // ""' "${RESULT_FILE}")
SUMMARY=$(jq -r '.summary // ""' "${RESULT_FILE}")

# Validate status against known values before acting on it.
case "${STATUS}" in
  ok)
    echo "Nothing to report"
    exit 0
    ;;
  findings | error)
    ;;
  *)
    # STATUS is model output: flatten CR/LF and cap it before logging, and
    # use printf, which never expands backslash escapes, so a crafted value
    # cannot start a new log line (a "::" workflow command).
    shown="${STATUS//[$'\r\n']/ }"
    printf '%s\n' "ERROR: Unknown or missing status '${shown:0:40}'"
    exit 1
    ;;
esac

if [[ -n "${SUMMARY}" ]]; then
  gh issue comment "${ISSUE_NUMBER}" --repo "${REPO_FULL_NAME}" --body "${SUMMARY}"
fi
```

**Behavioral exits.** When the agent hits a behavioral limit (turn limit or
cost limit), the harness sets `FULLSEND_AGENT_EXIT_REASON` to the error
subtype before running the post-script. Known subtypes: `error_max_turns`,
`error_max_budget_usd`. Check this variable to distinguish "agent chose not
to change anything" from "agent was interrupted mid-work":

```bash
if [[ -n "${FULLSEND_AGENT_EXIT_REASON:-}" ]]; then
  echo "Agent was interrupted: ${FULLSEND_AGENT_EXIT_REASON}"
  # Handle partial work — e.g., post a comment explaining the interruption
fi
```

Without a `validation_loop`, a behavioral exit runs the post-script and the
run succeeds. Any other agent failure fails the run, but the post-script
still runs when the agent left its result file (`FULLSEND_OUTPUT_FILE`,
default `agent-result.json`) and it parses as a JSON object;
`FULLSEND_AGENT_EXIT_CODE` is then set. With a loop, validation decides. See
[How a run ends](../../cli/run.md#how-a-run-ends).

**Security.** The post-script runs on the trusted runner with credentials,
but reads output produced by the untrusted sandbox. Treat agent output as
untrusted input:

- **Validate JSON structure** before extracting fields (`jq empty` catches
  malformed output).
- **Validate field values against an allowlist** (the `case` statement
  above) rather than passing them to shell commands or APIs unchecked.
- **Never interpolate agent output into shell commands** without quoting.
  Use `jq -r` to extract values into variables, then use `"${VAR}"`
  (double-quoted) everywhere.
- **Limit string lengths** in the JSON schema (`maxLength`) to prevent
  resource exhaustion when posting to external APIs.

### Dispatch inputs

When an event triggers an agent, the dispatch workflow exports these
variables to the runner:

| Variable | Description |
|----------|-------------|
| `FULLSEND_WORK_ITEM_URL` | Canonical, tracker-agnostic work item URL (`https://...`) |
| `FULLSEND_WORK_ITEM_KEY` | Canonical key (`42` on GitHub, `PROJ-123` on Jira) |
| `FULLSEND_WORK_ITEM_SOURCE` | Tracker the work item came from (`github`, `gitlab`, `jira`); set by `fullsend run` from `_normalized_event.source.system`, or the detected forge when there is no normalized event |
| `ISSUE_NUMBER` | Numeric GitHub issue/PR number; empty for Jira work items, where `fullsend run` clears it (use `FULLSEND_WORK_ITEM_KEY` in tracker-independent scripts) |
| `REPO_FULL_NAME` | Repository slug (`owner/repo`) |
| `GITHUB_ISSUE_URL` | Deprecated, use `FULLSEND_WORK_ITEM_URL` instead. |

The full event payload is also written to
`.fullsend/dispatch/event-payload.json`. Detect the tracker from
`_normalized_event.source.system`, not variable presence.

- **Pre/post scripts** inherit these variables directly from the host
  environment (`"$FULLSEND_WORK_ITEM_URL"`).
- **The sandbox** does not inherit runner variables by default. Forward them
  via `env.sandbox` in your harness, as `agent new` does:
  ```yaml
  env:
    sandbox:
      FULLSEND_WORK_ITEM_URL: "${FULLSEND_WORK_ITEM_URL}"
      ISSUE_NUMBER: "${ISSUE_NUMBER}"
  ```
  Alternatively, have your `pre_script` fetch details and write them into
  the workspace or `agent_input` directory.

Every `${VAR}` that `env.runner` references must be set on the runner. For a
secret, use the stored `FULLSEND_GITHUB_RUNNER_SECRETS` object (next
section).

### Pass secrets to host-side scripts (GitHub)

A pre-script that calls Jira, CodeRabbit or another service needs a token. This
channel is an interim one for your own tools on GitHub: external secret manager
support, read with the job's OIDC token, is the expected successor (long-term
design in [#6359](https://github.com/fullsend-ai/fullsend/issues/6359)), and
this secret stays as the fallback when your workflow cannot use GitHub OIDC or a
WIF provider with your secret manager. Integrations fullsend owns, such as the
inference gateway, keep their own named secrets. If your Jira poller workflow
already passes `JIRA_TOKEN` and `JIRA_USER_EMAIL` to the reusable workflow
([Jira integration](jira-integration.md)), declare them in `env.runner`
(`JIRA_TOKEN: "${JIRA_TOKEN}"`) and your `harness-run` scripts receive them;
this channel is for every other secret and every other stage. On GitHub, store
all such tokens in one secret, `FULLSEND_GITHUB_RUNNER_SECRETS`, as a JSON
object of name → value. The installed shim forwards it, and the reusable
workflow hands it to each stage's composite action. The action writes it to a
file that only the job user can read and passes `fullsend run` the path, never
the value. `fullsend run` deletes the file as soon as it has read it, so the
object is never in the environment of `fullsend run` or of any process it
starts. A host-side script receives only the names its harness references in
`env.runner`, as ordinary environment variables. The values never enter the
sandbox.

The example below is a review harness whose pre-script fetches the Jira issue
named in the pull request title.

1. Store the object on the repository, or on the organization for every repository the agent serves:

   ```bash
   gh secret set FULLSEND_GITHUB_RUNNER_SECRETS --repo OWNER/REPO \
     --body '{"JIRA_API_TOKEN":"...","JIRA_API_EMAIL":"..."}'

   gh secret set FULLSEND_GITHUB_RUNNER_SECRETS --org OWNER --visibility selected \
     --repos REPO_A,REPO_B --body '{"JIRA_API_TOKEN":"...","JIRA_API_EMAIL":"..."}'
   ```

   The object holds literal values: GitHub never expands a reference such as
   `${{ secrets.JIRA_TOKEN }}` inside a stored secret, so a token you already
   keep as its own secret must be copied into the object too. A secret holds one
   value, so adding or rotating a key means setting the whole object again. Keep
   the JSON in a file outside the repository and run
   `gh secret set FULLSEND_GITHUB_RUNNER_SECRETS --repo OWNER/REPO < runner-secrets.json`.
   Each value must be a string of at least 8 characters (the redactor's current
   minimum), so that `fullsend run` can redact it; keys may not repeat. Every
   agent run that receives the secret validates the whole object, so one invalid
   key fails every agent in every repository that gets it, not only your custom
   agent.

2. Reference each name from `env.runner` in `.fullsend/harness/my-agent.yaml`:

   ```yaml
   env:
     runner:
       JIRA_API_TOKEN: "${JIRA_API_TOKEN}"
       JIRA_API_EMAIL: "${JIRA_API_EMAIL}"
       JIRA_BASE_URL: "https://example.atlassian.net"
   ```

   A name the harness does not reference reaches no script. The name may not be
   runner-owned (`GH_TOKEN`, `GITHUB_*`, `FULLSEND_*`, `OTEL_*`, `PATH`, ...) or
   one the reusable workflow already sets, such as `JIRA_TOKEN` or
   `JIRA_USER_EMAIL`, so pick your own names, and it may appear only in
   `env.runner`. A reference from `env.sandbox`, a provider credential, an
   expanded `host_files` entry, or an overlay guarded by anything other than
   `runtime.forge` or `config` fails the run. The full rules are in the
   [harness reference](../../reference/harness-reference.md#field-details).

3. Read the variables in the pre-script as usual. `JIRA_API_TOKEN`,
   `JIRA_API_EMAIL` and `JIRA_BASE_URL` come from `env.runner`.
   `REPO_FULL_NAME` and `PR_NUMBER` are set by the review stage of the reusable
   workflow, and `GH_TOKEN` is the token `fullsend run` mints for the agent.
   This example uses the token for a decision: it skips the review when the
   Jira issue named in the pull request title (for example
   `PROJ-123: fix login`) is already done. It hands the agent nothing, because
   raw forge or Jira content must not reach the model:

   ```bash
   #!/usr/bin/env bash
   set -euo pipefail

   TITLE=$(gh pr view "$PR_NUMBER" --repo "$REPO_FULL_NAME" --json title --jq .title)
   JIRA_KEY=$(grep -oE '[A-Z][A-Z0-9]+-[0-9]+' <<<"$TITLE" | head -n 1 || true)
   [[ -n "$JIRA_KEY" ]] || exit 0

   STATUS=$(curl --fail-with-body --silent \
     --user "${JIRA_API_EMAIL}:${JIRA_API_TOKEN}" \
     "${JIRA_BASE_URL}/rest/api/3/issue/${JIRA_KEY}?fields=status" |
     jq -r '.fields.status.statusCategory.key')
   if [[ "$STATUS" == "done" ]]; then
     echo "reason=${JIRA_KEY} is already done" >> "$FULLSEND_PRESCRIPT_OUTPUT"
     echo "skipped=true" >> "$FULLSEND_PRESCRIPT_OUTPUT"
   fi
   ```

4. Trigger the agent and check the run log. The `Run fullsend` step prints
   `Runner secrets` followed by the names the harness references, and GitHub
   shows each value as `***`:

   ```bash
   gh run list --repo OWNER/REPO --workflow fullsend.yaml --limit 1
   gh run view RUN_ID --repo OWNER/REPO --log | grep -E 'Runner secrets|Pre-script'
   ```

`fullsend run` masks each value, and each line of a multi-line value, in the
log, and redacts them from pre-script failure detail and validation feedback.
Masking matches exact strings: a value your script transforms (base64,
URL-encoded, a substring) is printed as is, so never echo a secret or anything
derived from it.

The shim always passes `FULLSEND_GITHUB_RUNNER_SECRETS`, and GitHub rejects a
call that passes a secret the called workflow does not declare. If you set up a
repository with `--fullsend-ref` pointing at a release older than this channel,
render the shim with the CLI from that same release.

If you run `fullsend run` from your own workflow instead of the installed shim,
use the composite action and pass
`with: runner_secrets: ${{ secrets.FULLSEND_GITHUB_RUNNER_SECRETS }}`; the
action stages the file for you. The `FULLSEND_GITHUB_RUNNER_SECRETS`
environment variable also works, but only for local runs: in CI the value would
stay readable in `/proc/<pid>/environ` for the whole run. See
[`fullsend run` § Runner secrets](../../cli/run.md#runner-secrets).

On GitLab, skip the object. Add each secret as its own masked CI/CD variable
(for example `JIRA_API_TOKEN`) and reference it from `env.runner` the same way.
The rules above (refused names, host side only, overlays) apply to names in the
GitHub object only; GitLab behaves as before, and every CI/CD variable in the
job except `JIRA_TOKEN`, `JIRA_USER_EMAIL` and the OTLP headers still reaches
host-side scripts.

### Harness composition with `base`

Inherit from an existing harness and override only what differs:

```yaml
base: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/triage.yaml#sha256=abc...

model: sonnet
slug: my-org-triage
skills:
  - skills/my-enhancement
timeout_minutes: 15
```

Base chains support up to 5 levels (`MaxBaseDepth` in `internal/harness/compose.go`). Circular references are detected and rejected. Resolution order: base chain, child overrides, overlay resolution. See the [Harness Field Reference](../../reference/harness-reference.md#field-merge-rules-for-base-and-overlays) for how each field type combines.

> **Overlay precedence with `base:`:** Overlays are concatenated base-first, child-appended — the same ordering as `plugins`, `providers`, and `api_servers`. Because `ResolveOverlays` merges all matching entries in order (later matches take precedence), child overlay entries override base overlay entries with the same condition. This follows the child-overrides-base convention used by scalar and map merges.

> **Note:** `allowed_remote_resources`, `allow_runtime_fetch`, and `max_runtime_fetches` are NOT inherited from base harnesses — the child must declare its own. This prevents a base harness from injecting arbitrary URL prefixes or enabling runtime fetching in the child.

> **Config-level fallback:** Separately from base-harness inheritance, the `allowed_remote_resources` from the repository-local configuration acts as a fallback for all URL resolution. URLs trusted at the config level are accepted even when the child harness omits the field. This is a distinct trust layer from base composition — the config-level list is set by repository administrators, not by base harness authors.

To configure an existing agent without building from scratch, see [Configuring Agent Behavior](customizing-agents.md#configuration-with-base-composition).

### Testing locally

Before registering, verify your agent works locally. Use `fullsend run` as a development and debugging tool — it runs your agent directly without going through dispatch:

```bash
fullsend run my-agent \
  --target-repo ./my-repo \
  --env-file .env.local
```

The `--env-file` supplies variables your harness references: `GH_TOKEN` and the GitHub variables on every route, plus `ANTHROPIC_VERTEX_PROJECT_ID`, `CLOUD_ML_REGION` and `GOOGLE_APPLICATION_CREDENTIALS` on the Vertex route or `OPENAI_API_KEY` on the OpenAI route (see [Pick a route](#pick-a-route)). See [Running agents locally](running-agents-locally.md) for prerequisites and troubleshooting.

Most agents need additional flags for credentials and target repo — see [Running agents locally](running-agents-locally.md) for the full list.

**Testing without a model.** The `dummy` runtime runs the real sandbox and the real post-script but replaces the model with a scripted result, so you can exercise the whole pipeline without spending any inference. It needs no model credentials: no `OPENAI_API_KEY`, and no `GOOGLE_APPLICATION_CREDENTIALS` unless the harness requires the file (a `pi` harness with the Vertex block uncommented does). A harness generated for the Vertex route, or a `pi` harness with the Vertex block uncommented, passes `ANTHROPIC_VERTEX_PROJECT_ID` and `CLOUD_ML_REGION` into the sandbox, so set those two to any value.

Write the script at `.fullsend/behaviour/current-scenario.yaml`. This one writes a result matching the `lint-docs` schema (`status`, `summary`, `comment`):

```yaml
ops:
  - description: Write the agent result
    op: write_fixture
    args: output/agent-result.json, fixtures/agent-result.json
    content: |
      {
        "status": "findings",
        "summary": "2 broken links added in docs/",
        "comment": "### Broken links\n\n- `docs/a.md:14` -> `../missing.md`\n- `docs/b.md:3` -> `/docs/gone.md`"
      }
```

`write_fixture` writes `content` to the path before the comma; the path after the comma is used only by the behaviour-test suite. Other ops are listed in [Behaviour testing](../dev/behaviour-testing.md#dummy-agent-tables). Run it:

```bash
POST_LINT_DOCS_DRY_RUN=1 \
  FULLSEND_WORK_ITEM_URL="https://github.com/OWNER/REPO/pull/99" \
  ISSUE_NUMBER=99 \
  REPO_FULL_NAME=OWNER/REPO \
  GH_TOKEN="$(gh auth token)" \
  ANTHROPIC_VERTEX_PROJECT_ID=unused CLOUD_ML_REGION=unused \
  fullsend run lint-docs \
    --runtime dummy --target-repo .
```

Drop `--runtime dummy` and supply the route's real variables to run it with a model instead — the sandbox, post-script, and validation (when the harness has one) are exactly the same either way; only the agent's own reasoning differs.

### Registering your agent

Register agents in `.fullsend/config.yaml` so fullsend discovers them. Registration is what makes your agent visible to dispatch — without it, the agent can only be invoked via `fullsend run`.

Authentication for CLI commands uses `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token` (in that order). For URL agents, the CLI resolves GitHub blob URLs to `raw.githubusercontent.com` URLs automatically.

Harness agents route via CEL triggers on arbitrary labels — there is no prefix constraint.

#### CLI

```bash
# Add (auto-pins URL with SHA256):
fullsend agent add \
  https://github.com/fullsend-ai/agents/blob/main/harness/triage.yaml

# Add local:
fullsend agent add harness/my-agent.yaml --name my-agent

# List / update / remove:
fullsend agent list
fullsend agent update triage <sha>
fullsend agent update code   # re-pins base: in a local harness
fullsend agent remove triage
```

#### Config file (`.fullsend/config.yaml`)

```yaml
version: "1"
agents:
  - https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/triage.yaml#sha256=abc...
  - name: my-cool-agent
    source: harness/my-cool-agent.yaml
allowed_remote_resources:
  - https://raw.githubusercontent.com/fullsend-ai/fullsend/
  - https://raw.githubusercontent.com/fullsend-ai/agents/
```

**Notes:**
- URL entries are automatically pinned with `#sha256=...` by `fullsend agent add`.
- URLs must be covered by `allowed_remote_resources` in the same config.
- On name collision, config-registered agents take precedence over built-in agents.
- Individual agents can be disabled with `enabled: false` — see [Disabling Agents](customizing-agents.md#disabling-agents).
- Per-repo config is read from the **base branch**, not from PR branches.

## See also

- [Customizing Agents](customizing-overview.md) — overview of all customization approaches
- [fullsend-ai/agents](https://github.com/fullsend-ai/agents) — reference implementation used throughout this guide
- [Harness Field Reference](../../reference/harness-reference.md) — complete harness YAML field reference, merge rules, and resource referencing
- [Custom Agent Identity](custom-agent-identity.md) — using a standalone mint for custom GitHub App identity
- [Chaining Follow-up Workflows](chaining-follow-up-workflows.md) — act on an agent's result from your own workflow with the job token
- [CEL Triggers Reference](cel-triggers-reference.md) — dispatch flow, NormalizedEvent fields, transition kinds, and trigger patterns
- [Configuring with Skills](customizing-with-skills.md) — creating and managing skills; [authoring augmentations](customizing-with-skills.md#authoring-skills-that-augment-defaults)
- [`author-fullsend-augmentations` skill](../../../skills/author-fullsend-augmentations/SKILL.md) — discovery-driven guide for writing skills and sub-agents that complement shipped defaults
- [Configuring with AGENTS.md](customizing-with-agents-md.md) — repo-level instructions for all agents
- [Configuring Agent Behavior](customizing-agents.md) — harness configuration and `base:` composition
- [Default, derived, and custom agents](default-vs-custom.md) — when configuration crosses into custom agent territory
- [Escalation ladder](escalation-ladder.md) — prove-it path before deriving or replacing a core agent
- [Standalone mint](../infrastructure/standalone-mint.md) — custom agent roles and identity
