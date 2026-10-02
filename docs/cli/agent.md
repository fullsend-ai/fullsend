---
sidebar_label: fullsend agent
---

# fullsend agent

Manage agents in fullsend config. Generate a new agent, add, list, set (runtime, model, effort), update, and remove agents.

`agent add` and `agent update` fetch remote content and resolve GitHub URLs. Authentication is via `GH_TOKEN`, `GITHUB_TOKEN`, or `gh auth token`.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend agent new <name>` | Generate a complete custom agent and register it |
| `fullsend agent add <url-or-path>` | Register an agent in config |
| `fullsend agent list` | List registered agents |
| `fullsend agent update <name> [sha]` | Update a URL agent or a local harness `base:` URL to a new commit SHA |
| `fullsend agent set <name>` | Set an agent's runtime, model or effort |
| `fullsend agent remove <name>` | Remove an agent from config |

## `agent new`

Generate a complete, valid, runnable custom agent and register it. Every file
an agent needs is written for you; the only one you have to edit is the
instructions the agent follows.

```bash
fullsend agent new lint-docs --fullsend-dir .fullsend \
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
  1. Fill in the marked sections of agents/lint-docs.md — that file is the agent's prompt.
  2. Test locally, printing the result instead of commenting:
       POST_LINT_DOCS_DRY_RUN=1 fullsend run lint-docs --fullsend-dir .fullsend \
         --target-repo . --env-file .env.local
     .env.local needs GITHUB_ISSUE_URL, ISSUE_NUMBER, REPO_FULL_NAME,
     GH_TOKEN, ANTHROPIC_VERTEX_PROJECT_ID, CLOUD_ML_REGION, and
     GOOGLE_APPLICATION_CREDENTIALS pointing at a GCP credentials file;
     the run stops before it starts without them.
     GH_TOKEN must be a real token: a connectivity check runs before the
     agent does. See docs/guides/user/running-agents-locally.md.
  3. Commit .fullsend, then comment `/fs-lint-docs` on an issue or pull request to run it in CI.
```

Step 2 lists the Vertex route's variables. For an agent on an OpenAI model,
see [Agents on OpenAI models](#agents-on-openai-models); to try the pipeline
without a model, see [Try it without a model](#try-it-without-a-model).

The generated tree:

```bash
find .fullsend -type f | sort
```

```
.fullsend/agents/lint-docs.md
.fullsend/config.yaml
.fullsend/harness/lint-docs.yaml
.fullsend/policies/base.yaml
.fullsend/schemas/lint-docs-result.schema.json
.fullsend/scripts/post-lint-docs.sh
```

Only `agents/lint-docs.md` needs your attention — it is the agent's prompt and
it ships with marked sections to fill in. Everything else is complete.

### What gets written

| File | Written | Overwritten by `--force` |
|------|---------|--------------------------|
| `harness/<name>.yaml` | always | yes |
| `agents/<name>.md` | always | yes |
| `schemas/<name>-result.schema.json` | always | yes |
| `scripts/post-<name>.sh` (mode 0755) | always | yes |
| `policies/base.yaml` | when absent | **no** |
| `scripts/validate-output-schema.sh` | with `--validation-loop`, when absent | **no** |
| `config.yaml` `agents:` entry | unless `--no-register` | n/a |

The generated harness's `providers:` entries are bare names (`vertex-ai`,
`github-ro`, ...), not paths, and no `providers/` or `profiles/` files are
written. `fullsend run` resolves each built-in name to the provider
definition and profile in the `fullsend` binary, so a fix to one reaches
you with the next `fullsend` release. `policies/base.yaml` is the one shared
file: there is no built-in policy, so `agent new` writes one copy for every
agent in the directory and never overwrites it (not even with `--force`).
Commit it with the agent.

| What | In CI and locally | To customize |
|------|-------------------|--------------|
| `policies/` | Your committed copy is used as-is | Edit `policies/base.yaml`, or add another policy file and point the harness `policy:` at it |
| Built-in providers and their profiles | Resolved from the `fullsend` binary | Copy the provider and profile under your own name (for example `providers/myorg-github-ro.yaml` with `name: myorg-github-ro` and `type: myorg-github-ro`, and `profiles/myorg-github-ro.yaml` with `id: myorg-github-ro`), list the profile under `openshell.profiles`, and declare `myorg-github-ro` instead of the built-in name |

The built-in names (`vertex-ai`, `github`, `github-ro`, `github-artifacts`,
`gitleaks`, `package-registries`, `atlassian-cloud`, `openai`) and their
`fullsend-<name>` profile ids are reserved. A harness that still uses its own
copy under one of them keeps working for now: `fullsend run` uses the copy
and prints a warning naming the migration below. A later release rejects
it. `fullsend-openai` is already rejected.

Bare names need the same `fullsend` release in CI as the one that
generated the agent, or a newer one. The scaffolded workflows run the
binary that matches the workflow commit, so this holds unless your workflow
passes an older `fullsend_version`.

### Migrating a generated agent to built-in providers

`agent new` on v0.44.0 and earlier wrote `providers/*.yaml` and
`profiles/fullsend-*.yaml`, and the harness referenced them by path. Those
copies no longer receive fixes, and `fullsend run` warns about them:

```text
! provider "github-ro": the name is reserved for the definition built into fullsend, and a future release rejects the copy at "/path/to/repo/.fullsend/providers/github-ro.yaml". It is still used for now. Declare the bare name "github-ro" and delete the copy, or rename it to a name fullsend does not ship
! provider profile "fullsend-github-ro" will be rejected in a future release: the id is reserved for the copy built into fullsend. Your copy is still used for now. Remove it from openshell.profiles and declare the bare provider name "github-ro" instead
```

Make all of these changes in one commit. A harness with only some of them
mixes a copied GitHub provider or profile with a built-in one, and the two
may not agree on the `GH_TOKEN` credential (see `provider credentials are
not declared` under Troubleshooting).

1. In `harness/<name>.yaml`, replace each built-in `providers:` path with
   its bare name (`providers/vertex-ai.yaml` becomes `vertex-ai`).
2. Remove the `profiles/fullsend-*.yaml` entries from `openshell.profiles:`.
   Keep entries for your own profiles, and delete the block if it is empty.
3. Under `env.sandbox`, delete the `GH_TOKEN: ${GH_TOKEN}` line. Keep the
   one under `env.runner`. The built-in GitHub providers give the sandbox a
   placeholder token; that line would put the real token in the sandbox.
4. Delete `providers/<built-in name>.yaml` and `profiles/fullsend-*.yaml`
   once no harness in the directory references them.

The next `fullsend run` prints neither warning, and the agent's GitHub
calls go through the built-in provider.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | | Path to the `.fullsend` configuration directory (required) |
| `-f`, `--file` | | Read the agent definition from a spec YAML file |
| `--role` | `triage` | Mint role the agent runs as (see the table below) |
| `--description` | `Custom <name> agent.` | One-line description; written to both the harness and the agent definition |
| `--on` | `command:/fs-<name>` | Trigger preset; mutually exclusive with `--trigger` |
| `--trigger` | | A trigger written by hand, in CEL (the expression language dispatch evaluates); mutually exclusive with `--on` |
| `--model` | `opus` | Model for the agent. With `--runtime codex` there is no default: pass an OpenAI id such as `openai/<id>`, or the command refuses |
| `--effort` | `high` | Effort level (`low`, `medium`, `high`, `xhigh`, `max`) |
| `--runtime` | | Agent runtime recorded in `config.yaml` (`claude`, `pi` or `codex`); when omitted, the repo's `runtime:` default applies. The runtime and model decide which credentials the harness asks for: see [Agents on OpenAI models](#agents-on-openai-models) |
| `--slug` | `<owner>-<name>` | Names the GitHub App to look for when the agent is installed; `<owner>` comes from the `origin` remote |
| `--image` | per-role pin | Container image the agent runs inside |
| `--timeout-minutes` | `15` | Agent timeout in minutes |
| `--validation-loop` | `false` | Add a `validation_loop` checking output against the schema |
| `--no-register` | `false` | Write the files but do not touch `config.yaml` |
| `--force` | `false` | Overwrite generated files (never shared assets) |
| `--dry-run` | `false` | Validate and print what would be written, writing nothing |

### Roles

`--role` is not the agent's name. It decides which GitHub identity the agent
acts as and what that identity may do.

Agents do not carry long-lived credentials. At run time they ask a service
called the **mint** for a short-lived GitHub token, and `role:` is what they
ask for. The mint only issues tokens for roles it knows, so a role it does not
serve fails at the first run rather than at generation time — which is why this
command refuses an unknown one up front. The hosted mint serves these:

| `--role` | Permissions | Providers |
|----------|-------------|-----------|
| `triage` (default) | `contents:read`, `issues:write`, `metadata:read` | vertex-ai, github-ro, openai |
| `review` | `contents:read`, `pull_requests:write`, `issues:write`, `checks:read`, `metadata:read` | vertex-ai, github-ro, openai |
| `coder` | `contents:write`, `packages:read`, `pull_requests:write`, `issues:write`, `checks:read`, `metadata:read` | vertex-ai, github, openai |
| `retro` | `actions:read`, `contents:read`, `pull_requests:write`, `issues:write`, `metadata:read` | vertex-ai, github-ro, github-artifacts, openai |
| `prioritize` | `contents:read`, `issues:write`, `organization_projects:write`, `metadata:read` | vertex-ai, github-ro, openai |

Every provider is a bare name: the runner resolves its definition and
profile from the binary, and nothing is written under `.fullsend/providers/`
or `.fullsend/profiles/`. A codex agent declares no Vertex provider; a pi
agent on an `openai/` model declares none but keeps a commented-out overlay
that adds it (see [Agents on OpenAI models](#agents-on-openai-models)).

A `review` agent also gets `readonly_repo: true`: the checked-out repository is made read-only in the sandbox, so a reviewer cannot modify the code it reviews. That matches `harness/review.yaml` in fullsend-ai/agents.

Pick the role whose permissions fit what the agent does. An unknown role fails
immediately with this table, rather than returning `403` from the mint the
first time the agent runs. To use a role the hosted mint does not serve, you
need to run your own — see
[Custom Agent Identity](../guides/user/custom-agent-identity.md).

### Agents on OpenAI models

The runtime and the model decide which credentials the generated harness asks
for. Without `--runtime`, the repo's `config.yaml` `runtime:` applies. Pick the
row that matches the agent:

| You pass | The agent calls | `.env.local` needs, besides the GitHub variables |
|----------|-----------------|--------------------------------------------------|
| `--runtime claude`, or no `--runtime` in a repo whose `config.yaml` sets no `runtime:` | Claude on Vertex | `ANTHROPIC_VERTEX_PROJECT_ID`, `CLOUD_ML_REGION`, `GOOGLE_APPLICATION_CREDENTIALS` |
| `--runtime pi` with a Claude model (default `opus`) | Claude on Vertex | The same three |
| `--runtime pi --model openai/<id>` | OpenAI only | `OPENAI_API_KEY` |
| `--runtime codex --model openai/<id>` | OpenAI only | `OPENAI_API_KEY` |

Every harness declares the `openai` provider; a run whose model is not on
OpenAI skips it. An agent that calls only OpenAI gets no Vertex provider, no
GCP `host_files` and no Vertex variables, so it runs with no GCP credentials
at all.

`--runtime codex` has no default model, because codex takes OpenAI model ids
only and the default `opus` is a Claude alias. Name one:

```bash
fullsend agent new summarize-issue --fullsend-dir .fullsend \
  --runtime codex --model openai/gpt-5.6-luna
```

```
  ✓ Created agent "summarize-issue" in .fullsend
  harness/summarize-issue.yaml
  agents/summarize-issue.md
  schemas/summarize-issue-result.schema.json
  scripts/post-summarize-issue.sh
  policies/base.yaml
  ✓ Added agent "summarize-issue"
  ✓ Set agent "summarize-issue": runtime="codex" model="" effort="" (empty = inherit)

Next:
  1. Fill in the marked sections of agents/summarize-issue.md — that file is the agent's prompt.
  2. Test locally, printing the result instead of commenting:
       POST_SUMMARIZE_ISSUE_DRY_RUN=1 fullsend run summarize-issue --fullsend-dir .fullsend \
         --target-repo . --env-file .env.local
     .env.local needs GITHUB_ISSUE_URL, ISSUE_NUMBER, REPO_FULL_NAME,
     GH_TOKEN, and OPENAI_API_KEY. No GCP variables are needed: this
     agent calls only OpenAI, and the runner keeps the key out of the
     sandbox.
     GH_TOKEN must be a real token: a connectivity check runs before the
     agent does. See docs/guides/user/running-agents-locally.md.
  3. Commit .fullsend, then comment `/fs-summarize-issue` on an issue or pull request to run it in CI.
```

A pi agent on an `openai/` model can still dispatch sub-agents on Vertex
models, for example `model: "sonnet"` in an `Agent` call. Its harness ends with
the Vertex settings commented out:

```yaml
# To dispatch Vertex sub-agents (e.g. sonnet), uncomment this overlay, which is merged on top of the settings above; GOOGLE_APPLICATION_CREDENTIALS is then required.
# overlays:
#   - when: "true"
#     providers:
#       - vertex-ai
#     ...
```

To use it, remove the `# ` from every line under the first one, add `Agent` to
the `tools:` list in `agents/<name>.md`, and add the three Vertex variables to
`.env.local`. The credentials file is then required: without it the run stops
before the sandbox is created, rather than the sub-agents failing part-way
through. Codex gets no such block, because its sub-agents take OpenAI model ids
only.

On codex the generated `tools:` list is `Bash(gh,jq), Write`. Codex has no
Read, Grep or Glob tool and does that work through its shell. The Bash
allowlist stays in the file so it still applies if the agent moves to pi; on
codex the run prints that it is recorded but not enforced.

### Triggers

A trigger is the rule that decides which GitHub events start the agent —
a comment, a label, a new issue, a pull request. Every generated agent gets
one, because an agent without a trigger is accepted everywhere and then simply
never runs, with nothing reported anywhere to tell you why. `agent new`
therefore refuses to write one without a trigger. `--on` takes a preset:

| `--on` | Fires when |
|--------|-----------|
| `command:/<command>` (default `/fs-<name>`) | Someone comments the slash command on an issue, or on a pull request that is not from a fork |
| `label:<label>` (default `<name>`) | The label is added |
| `issue-opened` | A new issue is opened |
| `pr-opened` | A non-fork pull request is opened, updated, or marked ready |

The expressions these presets produce are exactly the ones written out in the
[CEL Triggers Reference](../guides/user/cel-triggers-reference.md#common-trigger-patterns),
and a test keeps the two identical. For anything the presets do not cover, pass
`--trigger` with your own expression — it is compiled and checked before any
file is written, so a mistake fails here rather than at the first event.

Both `command:` and `pr-opened` refuse comments and pull requests from forks.
That matters most for `--role coder`, which can write to the repository.

### Spec files

`-f` reads the same settings from a YAML document, so a local coding agent can
produce one:

```yaml
version: "1"
name: link-check
role: review
description: Check that links in changed docs resolve
on: label:needs-link-check
model: opus
timeout_minutes: 20
```

```bash
fullsend agent new -f link-check.agent.yaml --fullsend-dir .fullsend
```

```
  ✓ Created agent "link-check" in .fullsend
  harness/link-check.yaml
  agents/link-check.md
  schemas/link-check-result.schema.json
  scripts/post-link-check.sh
  policies/base.yaml  (already present, left unchanged)
  ✓ Added agent "link-check"
```

Unknown keys are rejected rather than ignored, so a typo does not silently
produce a different agent. Command-line flags override spec keys.

### Checking the result

`agent new` validates what it generates before it writes anything. To re-check
later — after you have edited the harness by hand, for example — load it with
the same loader dispatch uses:

```bash
fullsend lock lint-docs --fullsend-dir .fullsend --offline
```

```
⚡ fullsend dev
  Autonomous agentic development for Git-hosted organizations
→ Locking dependencies: lint-docs

  ✓ Harness has no remote dependencies — nothing to lock
```

`--offline` proves the agent needs no network. Note that `fullsend agent list`
shows **registrations** and does not open harness files, so it is not a
validity check:

```bash
fullsend agent list --fullsend-dir .fullsend
```

```
NAME       SOURCE
lint-docs  harness/lint-docs.yaml
```

Per-agent overrides compose on top of the generated harness:

```bash
fullsend agent set lint-docs --fullsend-dir .fullsend --model sonnet
```

```
  ✓ Set agent "lint-docs": runtime="" model="sonnet" effort="" (empty = inherit)
```

To see what would be generated without writing anything, use `--dry-run`. It
prints the file list and every rendered body:

```bash
fullsend agent new report --fullsend-dir .fullsend --dry-run
```

```
    Dry run: would create agent "report" in .fullsend
  harness/report.yaml
  agents/report.md
  schemas/report-result.schema.json
  scripts/post-report.sh
  policies/base.yaml  (already present, would be left unchanged)
  ...
    Nothing was written and no agent was registered
```

And to undo a generated registration:

```bash
fullsend agent remove link-check --fullsend-dir .fullsend
```

```
  ✓ Removed agent "link-check"
```

`agent remove` unregisters the agent; the generated files stay on disk for you
to delete or keep.

### Running it

Generation is step one. Fill in the marked sections of `agents/<name>.md`,
then run it with `fullsend run`. What the run needs depends on where the model
runs, which the runtime and the model decide together:

- **Runtime:** `--runtime` on `fullsend run`, else the agent's `config.yaml`
  entry (`agent new --runtime` writes it), else the repository's `runtime:`,
  else `claude`.
- **Model:** `--model` on `fullsend run`, else the agent's `config.yaml`
  entry, else the `model:` line in `harness/<name>.yaml`, else the one in
  `agents/<name>.md`.

Environment variables such as `FULLSEND_RUNTIME` and `FULLSEND_MODEL` sit
between the flag and the config; the full order is in
[Selecting a runtime and model](../runtimes.md#selecting-a-runtime-and-model).

Those two decide the route — Claude on Vertex, or OpenAI — and so the
credentials the run needs. The table in
[Agents on OpenAI models](#agents-on-openai-models) maps them. On `pi`, the
route follows the provider the model resolves to: a bare id takes
`FULLSEND_PI_PROVIDER` (default `anthropic-vertex`), so
`FULLSEND_PI_PROVIDER=openai` sends a bare id like `gpt-5.6-luna` over the
OpenAI route. `agent new` ignores that variable, so a harness it generated
with a bare id still passes the two Vertex variables into the sandbox; set
them to any value — see [pi: Models and providers](../runtimes/pi.md#models-and-providers).

Every route needs these variables:

| Variable | Value |
|----------|-------|
| `GITHUB_ISSUE_URL` | The issue or pull request the agent works on, e.g. `https://github.com/OWNER/REPO/pull/99` |
| `ISSUE_NUMBER` | Its number |
| `REPO_FULL_NAME` | `OWNER/REPO` |
| `GH_TOKEN` | A real token, e.g. `"$(gh auth token)"`. A GitHub connectivity check runs before the agent, and a placeholder fails it with `Bad credentials (HTTP 401)` |

Also set `POST_<NAME>_DRY_RUN=1` while you test, so the post-script prints its
comment instead of posting it. `<NAME>` is the agent name in upper case with
`-` turned into `_`: `POST_LINT_DOCS_DRY_RUN` for `lint-docs`. Leave it unset
only when you mean to post.

Then the route's own:

- **Vertex:** `ANTHROPIC_VERTEX_PROJECT_ID`, `CLOUD_ML_REGION`, and
  `GOOGLE_APPLICATION_CREDENTIALS` pointing at a GCP credentials file. The
  credentials file is checked before the sandbox is created, so the run stops
  there without it:

  ```
    ✗ Inference credential validation failed
  Error: host_files[0]: Vertex inference requires GOOGLE_APPLICATION_CREDENTIALS to point to an existing file
  ```

  See [Get Google Cloud Platform credentials](../guides/user/running-agents-locally.md#get-google-cloud-platform-credentials).
- **OpenAI:** `OPENAI_API_KEY` — see
  [Get an OpenAI key](../guides/user/running-agents-locally.md#get-an-openai-key-gpt-on-pi-or-codex).
  No GCP variables are needed. How to generate an agent for this route, and
  how a `pi` agent on it adds Vertex sub-agents, is in
  [Agents on OpenAI models](#agents-on-openai-models). A harness that
  carries the Vertex settings with the credentials mount marked
  `optional: true` instead — for example one generated for Vertex and then
  edited by hand to an `openai/` model — is not checked: a Vertex sub-agent
  fails mid-run (see [Troubleshooting](#troubleshooting)). In CI, `fullsend run` prepares Vertex
  credentials for such sub-agents when `FULLSEND_GCP_PROJECT_ID` and
  `FULLSEND_GCP_WIF_PROVIDER` are both set.

`--forge github` is not needed for a generated agent: its harness already
sets `FULLSEND_FORGE: github` and `ISSUE_URL` in `env.runner`. The flag
matters for the default agents in
[Running agents locally](../guides/user/running-agents-locally.md#run-default-agents),
whose harnesses take the forge from it and stop with `ISSUE_URL must be set`
without one.

#### Try it without a model

The `dummy` runtime runs the real sandbox and the real post-script but
replaces the model with a scripted result, so you can exercise the whole
pipeline without spending any inference. It needs no model credentials: no
`GOOGLE_APPLICATION_CREDENTIALS` and no `OPENAI_API_KEY`.

The script lives at `.fullsend/behaviour/current-scenario.yaml`. This one
writes a result that matches the `lint-docs` schema
(`schemas/lint-docs-result.schema.json`: `status`, `summary`, `comment`):

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

`write_fixture` writes `content` to the path before the comma. The path after
the comma is used only by the behaviour-test suite, so any value works for a
local run. The other ops are listed in
[Behaviour testing](../guides/dev/behaviour-testing.md#dummy-agent-tables).

Run it. `lint-docs` was generated for the Vertex route, so its harness passes
`ANTHROPIC_VERTEX_PROJECT_ID` and `CLOUD_ML_REGION` into the sandbox; `dummy`
never uses them, but they must be set, so give them any value. An agent
generated for the OpenAI route has no Vertex variables and needs neither:

```bash
POST_LINT_DOCS_DRY_RUN=1 \
  GITHUB_ISSUE_URL="https://github.com/OWNER/REPO/pull/99" \
  ISSUE_NUMBER=99 \
  REPO_FULL_NAME=OWNER/REPO \
  GH_TOKEN="$(gh auth token)" \
  ANTHROPIC_VERTEX_PROJECT_ID=unused CLOUD_ML_REGION=unused \
  fullsend run lint-docs --fullsend-dir .fullsend \
    --runtime dummy --target-repo .
```

The tail of a successful run:

```
    Agent exit code: 0
    Agent runs: 1
    Trace ID: d3ef0291-57de-4889-be45-c944abd6239e

  • Collecting OpenShell logs
  ✓ Collected 2 OpenShell log source(s) to .../logs
  • Cleaning up sandbox
  ✓ Sandbox deleted (0.7s)
  • Running post-script: .fullsend/scripts/post-lint-docs.sh
::stop-commands::691d4dd37ed96a3461f8e2f26c1995f1
**2 broken links added in docs/**

### Broken links

- `docs/a.md:14` -> `../missing.md`
- `docs/b.md:3` -> `/docs/gone.md`
::691d4dd37ed96a3461f8e2f26c1995f1::
post-lint-docs: dry run, not posting
  ✓ Post-script completed (0.0s)
  ✓ Download directory removed: .../fs-lin-e65af9bc482b
```

That block is the whole contract working: the sandbox started, the agent
wrote its result, and the post-script found it and rendered the comment. The
`::stop-commands::` lines around the preview stop GitHub Actions from reading
anything the model wrote as a workflow command. The token is random on every
run, so the model cannot predict the line that closes the block.

#### Run it with a model

Drop `--runtime dummy` and supply the route's variables. The run then uses
the runtime resolved as described at the top of [Running it](#running-it). With
`--runtime claude`, the same agent against a real pull request — the model
does the work, the schema gate runs, and the post-script still only prints:

```
  ✓ Extracted 1 output file(s) (0.4s)
  ...
  • Running validation: .fullsend/scripts/validate-output-schema.sh
  ✓ Validation passed: Validating: output/agent-result.json against .fullsend/schemas/link-check-result.schema.json
PASS: output validated against schema (0.4s)
  ...
    Agent exit code: 0
    Agent runs: 1
    Validation: passed
  ...
  • Running post-script: .fullsend/scripts/post-link-check.sh
post-link-check: status=ok, nothing to post (dry run: did not check for an earlier findings comment to replace)
  ✓ Post-script completed (0.2s)
```

That run is the reference agent from
[fullsend-ai/agents](https://github.com/fullsend-ai/agents) `examples/link-check/`,
not the generated stub — the stub has sections still to fill in, so it has no
work to do. Every runtime goes through the same harness, sandbox and post-script,
and the validation loop when the harness has one (`agent new
--validation-loop`); only the agent's own reasoning differs.

In CI, commit `.fullsend/` and fire the agent with whatever its trigger
describes — for the default preset, comment `/fs-<name>` on an issue or pull
request.

### Troubleshooting

| Error | Cause | Fix |
|-------|-------|-----|
| `unknown role "scribe"` followed by the role table | The role is not one the hosted mint serves | Use one of the five listed; for a custom role see [Custom Agent Identity](../guides/user/custom-agent-identity.md) |
| `agent name "..." contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)` | The name would not be safe to interpolate into a shell script | Rename. Nothing is written when this fires |
| `these files already exist:` followed by a list | An agent of that name was already generated | Pick another name, or pass `--force`. `--force` never overwrites `policies/` |
| `agent "..." already exists in config` | The name is registered in `config.yaml` | `fullsend agent remove <name>` first. `--force` deliberately does not override this |
| `trigger does not compile: ERROR: <input>:1:5: Syntax error: ...` | A `--trigger` expression is not valid CEL, or does not return a boolean | Compare against the `--on` presets above |
| `unknown --on preset "..."` followed by the preset list | `--on` is not one of the four presets | Use a listed preset, or pass raw CEL with `--trigger` |
| `a trigger is required: pass --on with a preset, or --trigger` | `--trigger ""` was passed explicitly | Give a real trigger. A trigger-less agent is silently never dispatched |
| `fullsend dir ... does not exist; run ` + "`fullsend github setup`" + ` first` | `--fullsend-dir` points at nothing | Scaffold the repo first |
| `validating files: policy: stat .../policies/base.yaml: no such file or directory` | The harness points at a policy file that is not committed next to it. Neither `fullsend github setup` nor CI creates one | Commit a copy of the fleet policy in [fullsend-ai/agents](https://github.com/fullsend-ai/agents) at the path the error shows, or set `policy:` to its URL with a `#sha256=` hash, under a prefix listed in `allowed_remote_resources`. Re-running `agent new` on this agent name does not help — it refuses (registered name or existing files, see the rows above) rather than adding the missing policy |
| Agent crashes at 0s in CI | A `providers:` entry names neither a built-in bare name nor a file that exists | Use a built-in bare name (`vertex-ai`, `github`, `github-ro`, `github-artifacts`, `gitleaks`, `package-registries`, `atlassian-cloud`, `openai`), or commit a `providers/<name>.yaml` (and, if needed, a profile) under your own name |
| `runtime codex takes OpenAI model ids only, and ...: use --model openai/gpt-5.6-luna ...` | `--runtime codex`, or a repo whose `config.yaml` sets `runtime: codex`, with no `--model` or with a model that is not an OpenAI id, such as `opus` | Use `--model openai/<id>` on the same command. Nothing is written when this fires |
| `host_files[0]: GOOGLE_APPLICATION_CREDENTIALS is empty; mark the mount optional or provide a credential file` | A pi agent on an `openai/` model has its Vertex sub-agent block uncommented, and no credentials file is set | Set `GOOGLE_APPLICATION_CREDENTIALS` to the file in `.env.local`. Do not mark the mount optional: the Vertex sub-agents need it |
| `validating env:` followed by unresolved host variables at `fullsend run` | A `${VAR}` in an environment-aware harness field is unset | `agent new` does not check host variables at generation time; supply them via `--env-file` locally or the workflow `env:` block in CI |
| `provider credentials are not declared by profile 'fullsend-github-ro': GH_TOKEN` (or `'fullsend-github'`) | The harness declares the bare name `github-ro` (or `github`), whose built-in definition passes `GH_TOKEN`, but still lists a `profiles/fullsend-github-ro.yaml` (or `fullsend-github.yaml`) written by fullsend v0.44.0 or earlier, which declares no credential. During the warning release your listed copy is used instead of the built-in one | Remove the `profiles/fullsend-github*.yaml` entries from `openshell.profiles:` and delete the files (see [Migrating a generated agent to built-in providers](#migrating-a-generated-agent-to-built-in-providers)). While there, delete any `GH_TOKEN` line under `env.sandbox` in your harnesses (keep the `env.runner` one): the provider hands the sandbox a placeholder, and that line would give it the real token |
| The same error from `fullsend run` on your machine, with bare names and no listed `fullsend-github*` profile | The OpenShell gateway still holds an older profile with that id. `fullsend run` cannot replace a profile while a provider uses it ([#7973](https://github.com/fullsend-ai/fullsend/issues/7973)) | Delete the providers that use either GitHub profile, then re-run: `openshell provider list \| awk '$2=="fullsend-github-ro" \|\| $2=="fullsend-github"{print $1}' \| xargs -r openshell provider delete`. Each run re-creates the providers it needs |
| `provider profile 'fullsend-github-ro' requires static credentials: GH_TOKEN` at `fullsend run` | `GH_TOKEN` is unset or empty on the machine running `fullsend run`. The `github-ro` and `github` providers pass it to the sandbox as a placeholder | Put a real token in the env file you pass to `fullsend run`, for example `echo "GH_TOKEN=$(gh auth token)" >> .env.local`. The file is read literally, so a `$(...)` written inside it is not run |
| `Vertex inference requires GOOGLE_APPLICATION_CREDENTIALS to point to an existing file` (or `a regular file`, `a non-empty file`) at `fullsend run` on your machine | A Vertex-route run (see [Running it](#running-it)) with `GOOGLE_APPLICATION_CREDENTIALS` unset, or pointing at a missing path, a directory or an empty file | Point it at a GCP credentials file — [Get Google Cloud Platform credentials](../guides/user/running-agents-locally.md#get-google-cloud-platform-credentials) |
| `reading behaviour script .../behaviour/current-scenario.yaml: ... no such file or directory` | `--runtime dummy` with no scripted result. The sandbox has already started when this fires | Write the file — [Try it without a model](#try-it-without-a-model) has one |
| `The file at /tmp/.gcp-credentials.json does not exist, or it is not a file` in a sub-agent's output, while the run exits 0 | A `pi` agent on an `openai/` model whose harness carries the Vertex settings with the credentials mount marked `optional: true` (not the Vertex block that [Agents on OpenAI models](#agents-on-openai-models) describes) dispatched a Vertex sub-agent (such as `sonnet`), with `GOOGLE_APPLICATION_CREDENTIALS` unset. Nothing checks for it before the run starts ([#7980](https://github.com/fullsend-ai/fullsend/issues/7980)) | Add the Vertex block from [Agents on OpenAI models](#agents-on-openai-models) and set the Vertex route's variables, or keep the sub-agents on `openai/` models |

## `agent add`

Register an agent in config by URL or local path. URL sources are automatically pinned to a specific commit SHA and annotated with a `#sha256=...` integrity hash. When a URL references a branch or tag (rather than a commit SHA), the original ref is stored in the config entry's `ref` field so that subsequent `agent update` calls re-resolve against the same branch. The URL prefix is added to `allowed_remote_resources` if not already present.

```bash
fullsend agent add https://github.com/my-org/agents/blob/main/harness/lint.yaml --fullsend-dir .fullsend
fullsend agent add harness/custom-review.yaml --name my-review --fullsend-dir .fullsend
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | | Path to the `.fullsend` configuration directory (required) |
| `--name` | derived from filename | Explicit agent name |

GitHub blob URLs are resolved to pinned `raw.githubusercontent.com` URLs. Non-GitHub URLs must already contain a commit SHA in the path. Local paths must be relative, must not contain path traversal (`..`), and the file must exist. If an agent with the same name already exists, the command fails.

## `agent list`

List all agents registered in config, showing each agent's name and source.

```bash
fullsend agent list --fullsend-dir .fullsend
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | | Path to the `.fullsend` configuration directory (required) |

Read-only. Displays a table with `NAME` and `SOURCE` columns. For URL agents, the `#sha256=...` integrity hash suffix is stripped from the displayed source for readability. Disabled agents (`enabled: false`) are included in the listing.

Example output:
```
NAME     SOURCE
triage   https://raw.githubusercontent.com/fullsend-ai/agents/abc123/harness/triage.yaml
my-lint  harness/my-lint.yaml
```

## `agent update`

Update a URL-based agent, or a local-path agent's `base:` URL, to a new commit SHA and recompute the `#sha256=...` integrity hash. If no SHA is provided, the branch ref stored at adoption time is re-resolved; if no ref was stored, the default branch HEAD is used. `agent add` never stores a ref for local-path sources, so an `agent update` on a local-path agent's `base:` URL without an explicit SHA always resolves the base repo's default branch — pass an explicit SHA if the `base:` URL was originally pinned to a different branch.

```bash
fullsend agent update triage --fullsend-dir .fullsend
fullsend agent update triage a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2 --fullsend-dir .fullsend
fullsend agent update code --fullsend-dir .fullsend
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | | Path to the `.fullsend` configuration directory (required) |

URL agents are re-pinned in `config.yaml`. Local-path agents whose harness YAML has a `base:` URL are re-pinned in that YAML file; `config.yaml` is left unchanged. Local-path agents without a `base:` URL have nothing to pin. Non-GitHub URLs require an explicit SHA argument. The integrity hash is recomputed by fetching the content at the new SHA.

## `agent set`

Sets `runtime`, `model` and/or `effort` for one agent in `.fullsend/config.yaml` (per-repo
configs). A built-in agent (`triage`, `code`, `review`, `fix`, `retro`, `prioritize`) without an
entry gets a name-only entry; a custom agent's settings land on its `source:` entry (or, for an
agent registered in `config.base.yaml`, on a name-only overlay entry that merges onto it). Only the
flags given change; pass an empty value (`--model ""`) to clear a setting. The result is validated
before it is written.

```bash
fullsend agent set code --fullsend-dir .fullsend --runtime claude --model sonnet --effort high
fullsend agent set triage --fullsend-dir .fullsend --model xai-vertex/xai/grok-4.6
fullsend agent set review --fullsend-dir .fullsend --subagent correctness=opus --subagent default=haiku
```

### Flags

| Flag | Description |
|------|-------------|
| `--fullsend-dir` | Path to the `.fullsend` configuration directory (required) |
| `--runtime` | Agent runtime for this agent (`claude`, `pi`, `codex` or `opencode`) |
| `--model` | Model for this agent — an alias, a model id, or `provider/id` on pi and codex (codex takes OpenAI ids only) |
| `--effort` | Effort level for this agent (`low`, `medium`, `high`, `xhigh`, `max`) |
| `--subagent` | Per-persona model override as `key=value` (repeatable). Key is a persona name or `default`; value is a model reference. Pass an empty value (`--subagent key=`) to clear an inherited entry — that writes `key: ~` in the config, after which the persona resolves the way an unmentioned one does (its frontmatter model, then `subagents.default`). On pi, a value that resolves to `openai/` prints a warning when the agent's local harness declares no `openai` provider; see [pi § Route a persona to OpenAI](../runtimes/pi.md#route-a-persona-to-openai) |

See [Runtimes — per-agent settings](../runtimes.md#per-agent-runtime-model-and-effort) for precedence.
See [pi § Per-persona model configuration](../runtimes/pi.md#per-persona-model-configuration) for
how `subagents` map to persona dispatch.

## `agent remove`

Remove an agent from config. If the removed agent was the last one using a given `allowed_remote_resources` prefix, that prefix is also cleaned up.

```bash
fullsend agent remove triage --fullsend-dir .fullsend
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | | Path to the `.fullsend` configuration directory (required) |

## See also

- [Bring Your Own Agent](../guides/user/bring-your-own-agent.md) — building custom agents and configuring existing ones
- [Default, derived, and custom agents](../agents/topics/default-vs-custom.md) — terminology and classification
- [Configuring with skills](../guides/user/customizing-with-skills.md) — extending agents with skills
