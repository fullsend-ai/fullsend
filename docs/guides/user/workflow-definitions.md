# Run a workflow definition repository

A **workflow definition** is a pipeline packaged for an agent runtime: a Claude
Code plugin that ships a workflow script (`workflows/<name>.js`) that fixes the
order of the steps, or a pi extension whose session hook drives the same loop.
This guide shows you how to run one under fullsend: you pin it in an agent's
[harness](../../reference/harness-reference.md) with one `workflow:` field, and
on Claude the runner starts the workflow for you. To make your own pipeline
repository loadable, see
[Make your pipeline repository loadable by fullsend](workflow-definition-authors.md).
The design is recorded in
[ADR 0130](../../ADRs/0130-workflow-definition-repos-are-harness-resources.md).

The examples use a sample Claude Code definition, `sample-pipeline`, kept at
`pipelines/sample-pipeline/` in a repository checked out at `~/src/sample-repo`.
Its workflow `triage-fanout` reads an issue, checks each unchecked checklist
item in parallel, and writes a result. The same files ship in fullsend at
[`e2e/behaviour/fixtures/workflow/sample-pipeline/`](https://github.com/fullsend-ai/fullsend/tree/main/e2e/behaviour/fixtures/workflow/sample-pipeline).

## Pick a route and a source

You choose two things: where the run happens, and where the definition comes
from. Every pairing works.

| Route | Who starts the run | How to tell it applies |
|---|---|---|
| **Local run** | You, with `fullsend run` on your machine, against an OpenShell gateway | You are testing before you commit, or debugging a run |
| **CI run** | A slash command on an issue or pull request, through the per-repo install's dispatch workflow | The repository has `.github/workflows/fullsend.yaml` from `fullsend github setup` |

| Source | Use it when | What you write in `workflow.source` |
|---|---|---|
| **Path** | The definition lives in the repository that holds the harness | A path from the top of that repository, such as `pipelines/sample-pipeline` (`.` is its root) |
| **Remote** | The definition lives in another GitHub repository | A github.com tree URL at a full commit sha, with `#sha256=<tree hash>` |

Steps 1 to 3 are the same for both routes. Step 4 is the local route, step 6
the CI route.

## Prerequisites

- **A fullsend binary that knows `workflow:`.** Check it:

  ```console
  $ fullsend agent new --help | grep -- --workflow
        --workflow string          the workflow to start from --workflow-source, workflows/<name>.js (claude runtime; omit for a pi extension)
        --workflow-source string   pin a workflow-definition repository in the harness workflow: field: a GitHub tree URL at a commit sha (#sha256= optional here) or a path in this repository
  ```

  No output means your binary predates workflow definitions. An older binary
  **silently ignores** the `workflow:` key and runs the agent's default
  prompt, so check this before you trust a run. For the CI route the same
  rule applies to the fullsend release your `.github/workflows/fullsend.yaml`
  pins (the tag after `reusable-dispatch.yml@<sha> #`).
- **The `claude` runtime for a Claude Code definition, `pi` for a pi
  extension.** The definition's kind must match the agent's runtime, and
  every other runtime refuses `workflow:` before the definition is fetched.
  This guide uses Claude; [pi extension definitions](#pi-extension-definitions)
  covers the difference.
- **For the local route:** OpenShell with a running gateway, Google Cloud
  credentials and a GitHub token. Follow
  [Running agents locally](running-agents-locally.md#prerequisites) to set
  them up.
- **For the CI route:** a per-repo install from
  [Configuring GitHub](../getting-started/configuring-github.md).
- **For a remote source:** the definition pushed to github.com. Tree URLs on
  other forges are refused today; a path source works on any forge.

## Step 1: Scaffold the agent with the definition pinned

`fullsend agent new` writes the harness `workflow:` field from
`--workflow-source` and `--workflow`, the workflow's `meta.name`.

### Path source

```console
$ fullsend agent new sample-triage --workflow-source pipelines/sample-pipeline --workflow triage-fanout --description "Triage an issue with the sample pipeline"
  ✓ Created agent "sample-triage" in .fullsend
  harness/sample-triage.yaml
  agents/sample-triage.md
  schemas/sample-triage-result.schema.json
  scripts/post-sample-triage.sh
  policies/base.yaml
  ✓ Added agent "sample-triage"

Next:
  1. Fill in the marked sections of agents/sample-triage.md — the runner starts the workflow; that file sets the main loop's instructions and tools.
  2. Test locally, printing the result instead of commenting:
       POST_SAMPLE_TRIAGE_DRY_RUN=1 fullsend run sample-triage \
         --target-repo . --env-file .env.local
     .env.local needs GITHUB_ISSUE_URL, ISSUE_NUMBER, REPO_FULL_NAME,
     GH_TOKEN, ANTHROPIC_VERTEX_PROJECT_ID, CLOUD_ML_REGION, and
     GOOGLE_APPLICATION_CREDENTIALS pointing at a GCP credentials file;
     the run stops before it starts without them.
     GH_TOKEN must be a real token: a connectivity check runs before the
     agent does. See docs/guides/user/running-agents-locally.md.
  3. Commit .fullsend, then comment `/fs-sample-triage` on an issue or pull request to run it in CI.
```

It writes two things that matter here:

```console
$ grep -A2 "^workflow:" .fullsend/harness/sample-triage.yaml
workflow:
  source: pipelines/sample-pipeline
  name: triage-fanout
$ grep "^tools:" .fullsend/agents/sample-triage.md
tools: Bash(gh,jq), Read, Grep, Glob, Write, Workflow
```

Keep `Workflow` in `tools:`: the runner refuses an agent whose `tools:` list
leaves it out, because the main loop must call the Workflow tool to start the
run. Flag details:
[`agent new` § Starting a workflow](../../cli/agent.md#starting-a-workflow).
Fill in the marked sections of `agents/sample-triage.md`. The runner passes the
workflow command instead of a prompt, and the agent file still sets the main
loop's instructions and tools.

The runner reads a path source from the git index of the repository that
holds the harness: only files git tracks are delivered. Check what it will
deliver:

```console
$ git ls-files pipelines/sample-pipeline
pipelines/sample-pipeline/.claude-plugin/plugin.json
pipelines/sample-pipeline/scripts/checklist.sh
pipelines/sample-pipeline/skills/read-issue/SKILL.md
pipelines/sample-pipeline/workflows/triage-fanout.js
```

Rules for a path source, each with its reason:

- **Commit or `git add` every file.** An untracked file is left out without an
  error, so nothing untracked beside the checkout (a credentials file, a
  nested repository) can reach the sandbox.
- **Keep it outside `.fullsend/`.** The configuration directory is never part
  of a definition, so a source inside it is refused. When `.fullsend/` lies
  under the source (for example `source: .`), it is left out.
- **Run from a git checkout.** fullsend reads the index of the repository
  that holds the harness.

A path source has no pin and no lock entry.

### Remote source

Pin another repository's definition to a full commit sha and its tree hash,
and allow the URL prefix. The example pins the sample from the public
fullsend repository, where it lives at
`e2e/behaviour/fixtures/workflow/sample-pipeline`.

1. **Get the commit sha.** In a clone of the definition repository, at the
   commit you pushed, `git rev-parse HEAD` prints it. Without a clone,
   `git ls-remote <repository URL> <branch>` prints the sha of a pushed
   branch head. Use the full 40-character sha. A branch or tag is refused,
   so the definition cannot change under the pin; with `main` in place of the
   sha, `fullsend lock` stops with:

   ```text
   Error: loading harness for forge "": invalid harness: workflow.source ref "main" is not a commit sha; pin the commit sha (40 hex characters) so the definition cannot move under the pin
   ```

2. **Scaffold the agent with the tree URL.** Leave out `#sha256=`: no command
   prints the tree hash on its own, so `agent new` pins 64 zeros and says how
   to get the real one (`...` marks cut lines):

   ```console
   $ fullsend agent new sample-triage --workflow-source https://github.com/fullsend-ai/fullsend/tree/261a813893445bbbba070a59d40c94d53499d059/e2e/behaviour/fixtures/workflow/sample-pipeline --workflow triage-fanout --description "Triage an issue with the sample pipeline"
     ✓ Created agent "sample-triage" in .fullsend
     harness/sample-triage.yaml
     agents/sample-triage.md
     schemas/sample-triage-result.schema.json
     scripts/post-sample-triage.sh
     policies/base.yaml
     ✓ Added agent "sample-triage"
     ! workflow.source has no tree hash yet, so harness/sample-triage.yaml pins #sha256=0000000000000000000000000000000000000000000000000000000000000000 (64 zeros) as a placeholder
     Run 'fullsend lock sample-triage': it fails with "the fetched tree hashes to sha256=<hash>"; put that hash after #sha256= and run it again.
   ...
   ```

3. **Allow the URL prefix.** The default `allowed_remote_resources` covers
   only raw.githubusercontent.com prefixes for fullsend's own repositories,
   not tree URLs on github.com, so the first lock stops and names the prefix
   to add:

   ```console
   $ fullsend lock sample-triage
   ⚡ fullsend dev
     Autonomous agentic development for Git-hosted organizations
   → Locking dependencies: sample-triage

     ✗ Workflow definition resolution failed
   Error: resolving workflow definition: workflow.source URL "https://github.com/fullsend-ai/fullsend/tree/261a813893445bbbba070a59d40c94d53499d059/e2e/behaviour/fixtures/workflow/sample-pipeline" is not covered by allowed_remote_resources; add a prefix that covers it, such as https://github.com/fullsend-ai/fullsend/, to allowed_remote_resources in config.yaml
   ```

   Add it to `.fullsend/config.yaml`:

   ```yaml
   allowed_remote_resources:
     - https://github.com/fullsend-ai/fullsend/
   ```

4. **Read the tree hash from the mismatch.** Lock again. The error names the
   real hash after `the fetched tree hashes to sha256=`:

   ```console
   $ fullsend lock sample-triage
   ⚡ fullsend dev
     Autonomous agentic development for Git-hosted organizations
   → Locking dependencies: sample-triage

     ✗ Workflow definition resolution failed
   Error: resolving workflow definition: workflow.source: tree hash mismatch for https://github.com/fullsend-ai/fullsend/tree/261a813893445bbbba070a59d40c94d53499d059/e2e/behaviour/fixtures/workflow/sample-pipeline: the harness pins sha256=0000000000000000000000000000000000000000000000000000000000000000 but the fetched tree hashes to sha256=e645c8d64206ff44d00ead5cd3f1ac5002b300771bc9f07a494873fdcf864cb7; if the commit is the one you meant, update the #sha256= fragment
   ```

5. **Copy the hash into the fragment and lock.**

   ```console
   $ grep "source: https" .fullsend/harness/sample-triage.yaml
     source: https://github.com/fullsend-ai/fullsend/tree/261a813893445bbbba070a59d40c94d53499d059/e2e/behaviour/fixtures/workflow/sample-pipeline#sha256=e645c8d64206ff44d00ead5cd3f1ac5002b300771bc9f07a494873fdcf864cb7
   $ fullsend lock sample-triage
   ⚡ fullsend dev
     Autonomous agentic development for Git-hosted organizations
   → Locking dependencies: sample-triage

     • Resolving dependencies
     ✓ Resolved 0 dependencies
     • Writing lock file
     ✓ Locked 1 dependencies for sample-triage -> ~/src/sample-repo/.fullsend/lock.yaml
         workflow: https://github.com/fullsend-ai/fullsend/tree/261a813893445bbbba070a59d40c94d53499d059/e2e/behaviour/fixtures/workflow/sample-pipeline (fetched)
   ```

   `.fullsend/lock.yaml` (format `version: 2`) now records the definition as a
   `workflow` dependency, with the hash of each file (`...` marks cut lines):

   ```console
   $ cat .fullsend/lock.yaml
   # Generated by fullsend lock — DO NOT EDIT
   version: 2
   ...
   harnesses:
       sample-triage:
           source: harness/sample-triage.yaml
   ...
           dependencies:
               - field: workflow
                 url: https://github.com/fullsend-ai/fullsend/tree/261a813893445bbbba070a59d40c94d53499d059/e2e/behaviour/fixtures/workflow/sample-pipeline
                 sha256: e645c8d64206ff44d00ead5cd3f1ac5002b300771bc9f07a494873fdcf864cb7
                 type: directory
                 files:
                   - path: .claude-plugin/plugin.json
   ...
                   - path: workflows/triage-fanout.js
   ...
   ```

   Commit `lock.yaml` with the harness. The pin is part of the harness file,
   so changing it makes the lock entry out of date until you lock again.

With the remote pin, the run's `Workflow:` line names the commit instead of a
path:

```text
    Workflow: fullsend-ai/fullsend@261a81389344/e2e/behaviour/fixtures/workflow/sample-pipeline (sha256:e645c8d64206) → /sample-pipeline:triage-fanout
```

The later steps use the path source.

## Step 2: Pass args to the workflow

Add `args:` under `workflow:` in the harness. `${VAR}` references expand from
the runner environment, so the harness can name the work item:

```console
$ grep -A3 "^workflow:" .fullsend/harness/sample-triage.yaml
workflow:
  source: pipelines/sample-pipeline
  name: triage-fanout
  args: issue ${ISSUE_NUMBER}
```

The script receives the expanded text as one string in `args`, with no
splitting or quoting. Every variable it names must be set in the runner
environment (an empty value is allowed), and it must stay on one line: an
unset variable, or one that holds a newline, carriage return or NUL, fails
the run before the sandbox starts.

Name work-item identifiers only. Args reach the model prompt, the run plan
and `metrics.json`, so a variable whose name looks like a credential (ending
in `_TOKEN`, `_API_KEY`, `_PRIVATE_KEY`, `_ACCESS_KEY`, `_SECRET_KEY`, `_PROXY`, `_PASSWORD` or `_CREDENTIALS`,
containing `_SECRET`, or starting with `OTEL_`) is refused when the harness
loads. A bare `_KEY`, such as `ISSUE_KEY` for a Jira work item, is allowed.
Args text that looks like a credential is refused when the harness loads, and
an expanded value that looks like one fails the run.

## Step 3: Check before you run

Commit `.fullsend/` and the definition, then check the definition with
fullsend's own resolver. `fullsend lock` runs the same definition checks as
`fullsend run` without a gateway:

```console
$ fullsend lock --all
⚡ fullsend dev
  Autonomous agentic development for Git-hosted organizations
→ Locking all harnesses

→ Locking dependencies: sample-triage

  ✓ Harness has no remote dependencies — nothing to lock
  ✓ No harnesses have remote dependencies — nothing to lock
```

A path source has nothing to lock, so success is the line
`✓ Harness has no remote dependencies — nothing to lock`. With the remote
pin locked, `fullsend lock sample-triage` prints
`✓ Lock entry for sample-triage is up to date (1 dependencies)`. Any definition
problem prints `✗ Workflow definition resolution failed` and an `Error:` line
from the [troubleshooting table](#troubleshooting). `fullsend lock` also
refuses a credential-shaped variable in `args`. Four checks need
`fullsend run`: the runtime, the definition's kind against it, the agent's
`tools:`, and the expanded `args`. `fullsend run` runs them before it checks
for OpenShell, so a failure costs no sandbox.

## Step 4: Run locally

```bash
POST_SAMPLE_TRIAGE_DRY_RUN=1 fullsend run sample-triage --target-repo . --env-file .env.local --output-dir ../out
```

`.env.local` needs `GITHUB_ISSUE_URL`, `ISSUE_NUMBER`, `REPO_FULL_NAME`,
`GH_TOKEN`, `ANTHROPIC_VERTEX_PROJECT_ID`, `CLOUD_ML_REGION` and
`GOOGLE_APPLICATION_CREDENTIALS`. This run pointed them at the public issue
[fullsend-ai/fullsend#8204](https://github.com/fullsend-ai/fullsend/issues/8204).
`POST_SAMPLE_TRIAGE_DRY_RUN=1` makes the post-script print the comment instead
of posting it. Output, trimmed to the lines that matter (`...` marks cut lines):

```text
→ Running agent: sample-triage

  ✓ Agent sample-triage resolved from config (local path)
...
  ✓ Harness loaded (0.0s)
...
    Runtime: claude (from ~/src/sample-repo/.fullsend/config.yaml)
...
    Workflow: pipelines/sample-pipeline (sha256:9d2717dc50ed) → /sample-pipeline:triage-fanout issue 8204
...
  • Creating sandbox: fs-sam-a5f10d8db64b
  ✓ Sandbox created (5.6s)
...
  • Running agent

...
→ Agent: claude-opus-4-6 (v2.1.258)
  🧠 The user wants me to run the "sample-pipeline:triage-fanout" workflow with the args "issue 8204". Let me invoke it.
  ⚙ Workflow
  💬 The **sample-pipeline:triage-fanout** workflow has been launched (Task ID: `w12kc1301`, Run ID: `wf_644d534e-b74`). It will:
...
  💬 The result file has been written to `$FULLSEND_OUTPUT_DIR/agent-result.json` with status **`findings`**. The post-script will pick it up and post the comment on issue #8204.
...
  ✓ Agent exited with code 0 (110.3s)
...
    Run directory: ~/src/out/fs-sam-a5f10d8db64b
...
post-sample-triage: dry run, not posting
  ✓ Post-script completed (0.1s)
```

For the harness above, with `ISSUE_NUMBER=8204`, the plan block prints:

```text
    Workflow: pipelines/sample-pipeline (sha256:9d2717dc50ed) → /sample-pipeline:triage-fanout issue 8204
```

What success looks like:

- **Before the sandbox:** the plan block prints a `Workflow:` line. It names
  the source (a path as written, or `<owner>/<repo>@<first 12 characters of
  the commit>[/<path>]` for a remote pin), the first 12 characters of the tree
  hash, and the command the runner starts, `/<plugin name>:<workflow> <args>`.
  No `Workflow:` line means the harness has no `workflow:` or the binary
  ignores it (see [Prerequisites](#prerequisites)). Line format:
  [`fullsend run` § Workflow line](../../cli/run.md#workflow-line).
- **In the agent stream:** a `⚙ Workflow` tool call.
- **At the end:** the agent exits with code 0 and the post-script runs.

The run directory is under `--output-dir`, here `../out`, next to the
repository (without the flag: `fullsend` in your system's temporary
directory).

## Step 5: Read the run metrics

The runner records the definition and what it started in the run directory's
`metrics.json`:

```console
$ jq .workflow ~/src/out/fs-sam-a5f10d8db64b/metrics.json
{
  "source": "pipelines/sample-pipeline",
  "pin_sha256": "9d2717dc50ed46a59bb200b86053b14fa4cc3fb648b45d2672faf5b5aeb56705",
  "kind": "claude-plugin",
  "command": "/sample-pipeline:triage-fanout issue 8204"
}
```

| Field | Meaning |
|---|---|
| `source` | The path as written, or the tree URL without `#sha256=` |
| `pin_sha256` | The full tree hash of what was uploaded |
| `kind` | `claude-plugin` or `pi-extension` |
| `command` | The command the runner started, with `args` expanded; absent for a pi extension |

Token and cost totals in the same file include the workflow's agents. Field
reference: [`fullsend run` § metrics.json fields](../../cli/run.md#metricsjson-fields).

## Step 6: Run in CI

1. Commit `.fullsend/` (and a path-source definition) and merge it to the
   default branch.
2. Comment `/fs-sample-triage` on an issue.

Not run here: needs a pushed commit and a per-repo install.

CI reads a path source from the commit the dispatch workflow checks out: the
pull request's base commit for pull request events, otherwise the commit that
triggered the run (the default branch for an issue comment). Merge a
definition change before you expect CI to use it. A remote source runs
exactly the pinned commit on both routes. The run's `metrics.json` is in the
workflow run's `fullsend-sample-triage` artifact, with the same `workflow`
object as step 5.

## Share one pin between harnesses

Several agents of one repository that run the same definition share the pin
through [`base:`](../../reference/harness-reference.md#field-merge-rules-for-base-and-overlays):
put `workflow:` in a base harness, and give each agent a thin harness whose
`base:` is that file. A child that sets its own `workflow:` replaces the
base's whole block, so a child that needs other `args` repeats `source` and
`name`.

## Install a harness someone else ships

A pipeline repository may ship its own harness with `workflow:`. How it
installs depends on its `source`:

- **A harness that pins `source` as a URL** installs by
  `fullsend agent add <url>`, like any harness.
- **A harness with a relative `source`** needs a one-line `base:` harness in
  your repository. Its relative source resolves in the base's repository at
  the base's pinned commit, which must be a full commit sha; see
  [Let others install your harness](workflow-definition-authors.md#let-others-install-your-harness).
- **Adding a harness with a relative `source` by URL** is refused, because a
  harness added by URL resolves relative paths in your repository, not its
  own. `fullsend lock` prints:

  ```text
    ✗ Workflow definition resolution failed
  Error: resolving workflow definition: workflow.source "pipelines/sample-pipeline" is relative, but this harness was added by URL, so relative paths resolve in your repository, not the harness's; pin source as a tree URL with #sha256=, or install the harness through a one-line base: harness
  ```

## pi extension definitions

A definition whose tree is a pi extension runs on the `pi` runtime: fullsend
loads it with `-e`, like a `plugins:` entry, and the extension's own session
hook drives the sequence under the default prompt. The runner starts no
command, so `workflow:` takes only `source`, the agent needs no `Workflow`
tool, and `metrics.json` records `kind: pi-extension` without a `command`.
With `--runtime pi`, `fullsend agent new --workflow-source <source>` writes
such a harness. The plan line says how the definition is delivered:

```text
    Workflow: pipelines/sample-extension (sha256:016c0867516f) delivered as pi extension
```

How a script-led pi parent reports completion is not settled yet; it is a
follow-up to [ADR 0130](../../ADRs/0130-workflow-definition-repos-are-harness-resources.md).

## Troubleshooting

`fullsend lock` and `fullsend run` print each error after `Error: `. Errors
from the definition checks also start with
`resolving workflow definition: `, after the step line
`✗ Workflow definition resolution failed`. Errors in the harness's own
`workflow:` field start with `invalid harness: ` after
`✗ Failed to load harness`. The runtime, kind and `tools:` errors are also
the text of their `✗` step line. "Seen with" names the command that printed
the text for this page. `fullsend run` prints every `run` row before it
checks for OpenShell or creates a sandbox.

| Error text | Cause and fix | Seen with |
|---|---|---|
| `workflow: is not supported by the <runtime> runtime; agent "<agent>" resolves to "<runtime>", but a workflow definition runs on claude (a Claude Code plugin) or pi (a pi extension); set runtime: claude or runtime: pi for this agent, or remove workflow: from its harness` | The agent resolves to codex or another runtime. Set `runtime: claude` (or `pi` for a pi extension) on the agent's `agents:` entry, or drop the `--runtime`/`FULLSEND_RUNTIME` override. Printed for `fullsend run sample-triage --runtime codex`, before the definition is fetched. | `run` |
| `workflow.source <source> is a Claude Code plugin, but agent "<agent>" resolves to the pi runtime, which runs a pi extension; set runtime: claude for this agent, or point workflow.source at a pi extension` | The definition's kind does not match the runtime (the reverse case names a pi extension on `claude`). Printed for `fullsend run sample-triage --runtime pi`. | `run` |
| `workflow.source <source> is a pi extension, which starts its own sequence from its session hook, so workflow.name and workflow.args do not apply; remove them` | `name` or `args` is set for a pi extension definition. Remove them. | `run` |
| `workflow: agent "<agent>" lists tools: without Workflow, so it cannot start /<plugin>:<workflow>; add Workflow to its tools:` | The agent file's `tools:` lacks `Workflow`. Add it. | `run` |
| `workflow: agent "<agent>" lists Workflow in disallowedTools:, so it cannot start /<plugin>:<workflow>; remove Workflow from its disallowedTools:` | The agent file blocks the tool. Remove it from `disallowedTools:`. | `run` |
| `workflow: security.sandbox_hooks.tool_allowlist_pretool is enabled and env.sandbox FULLSEND_TOOL_ALLOWLIST does not name Workflow, so the hook would block /<plugin>:<workflow>; add Workflow to FULLSEND_TOOL_ALLOWLIST` | The harness turns on the tool allowlist hook and its `env.sandbox` list leaves out `Workflow`. Add it to the list. A list set only through a `host_files` env file is not checked here; the hook then blocks the first `Workflow` call. | `run` |
| `workflow.args references ${<NAME>}, which is not set in the runner environment; set it, or remove it from args` | `args` names a variable the runner environment lacks. Set it, for example in `--env-file`, or drop it from `args`. Printed after `✗ Environment validation failed`. | `run` |
| `workflow.args references ${<NAME>}, whose value holds NUL, carriage return or newline characters, so args would no longer be one line; set it to a single-line value` | A variable in `args` holds a line break. Name a single-line variable, such as an issue number. Printed after `✗ Environment validation failed`; neither `args` nor the value is printed. | `run` |
| `workflow.args references ${<NAME>}, which names a credential (<rule>); pass work-item identifiers such as ${ISSUE_NUMBER} instead` | `args` names a credential-shaped variable, such as `${GH_TOKEN}`. Name the work item instead. Printed after `✗ Failed to load harness`. | `lock` |
| `workflow.args looks like it holds a credential (<rules>); args reach the model prompt, the run plan and metrics.json, so remove it and pass work-item identifiers such as ${ISSUE_NUMBER} only` | The `args` text itself holds a token-like value, such as a GitHub token. Remove it. `<rules>` names the matched patterns, such as `github_pat`; the value is not printed. Printed after `✗ Failed to load harness`. | `lock`, `run` |
| `workflow.args expands to a value that looks like a credential (<rules>); pass work-item identifiers only` | A variable in `args` holds a token-like value. Name a variable that holds the work item. Printed after `✗ Environment validation failed`. | `run` |
| `workflow.source <source>: workflows/<workflow>.js declares meta.name "<meta-name>"; Claude Code runs it as /<plugin>:<meta-name>, so rename the file or set meta.name to "<workflow>"` | File name and `meta.name` differ. Make them identical. | `lock` |
| `` workflow.source <source>: workflows/<workflow>.js: the script does not start with `export const meta = {`; Claude Code lists a workflow only when `export const meta = { ... }` is the script's first statement and a plain object literal with a quoted name, and fullsend needs `;` right after its closing brace `` | Code comes before the meta object. Move `export const meta = { ... };` to the top; comments may precede it. | `lock` |
| `` workflow.source <source>: workflows/<workflow>.js: end the meta statement with `;` directly after its closing brace; Claude Code lists a workflow only when `export const meta = { ... }` is the script's first statement and a plain object literal with a quoted name, and fullsend needs `;` right after its closing brace `` | The meta object ends in `}` without `;`. Write `};`. | `lock` |
| `workflow.source <source> has no workflows/<workflow>.js; the definition ships: <names>` | The harness `workflow.name` matches no tracked script. Fix the name, or `git add` the script: an untracked file is not delivered. | `lock` |
| `workflow.source <source> is neither a Claude Code plugin nor a pi extension (<details>): add .claude-plugin/plugin.json at the definition root for a Claude workflow, or a pi extension entry point` | The source has no plugin manifest or pi entry point at its root. Point `source` at the definition root, or add the manifest. | `lock` |
| `workflow.source <source>: .claude-plugin/plugin.json sets "workflows", a custom workflow path, which fullsend does not support yet; keep the scripts in workflows/ at the definition root and remove the "workflows" key` | The manifest moves the workflows directory. Keep the scripts in `workflows/` and drop the key. | `lock` |
| `workflow.source "<source>": <path> is a symlink to <target>, which leaves the source directory; point the link inside the repository or commit the file` | A link points outside the definition. Link inside it, or commit a copy. | `lock` |
| `workflow.source "<source>" is inside fullsend's configuration directory .fullsend/, which is never part of a definition; put the definition in another directory and set source to its path` | Move the definition out of `.fullsend/`. | `lock` |
| `workflow.source: a path source needs the harness to be in a git checkout, and "<dir>" is not inside one (<git error>); run from a git checkout or pin a tree URL` | Run from a clone, not an exported copy. | `lock` |
| `` workflow.source "<source>": "<source>" holds no files git tracks; commit or `git add` the definition's files `` | Nothing under the source is tracked, for example a new directory you have not added yet. Commit the definition. | `lock` |
| `plugins[<i>]: "<path>" loads as plugin "workflow-definition", a sandbox directory name reserved for the workflow: definition this harness declares (compared without case); rename the plugin directory` | The definition is uploaded under the fixed sandbox directory `workflow-definition`, and a `plugins:` entry uses that name. Rename the plugin's directory. Printed after `✗ Failed to load harness`. | `lock` |
| `plugins[<i>] "<path>" has the Claude Code plugin name "<name>", the same as the workflow: definition's plugin name "<name>"; rename one of them (the "name" in .claude-plugin/plugin.json, or the directory when there is no manifest)` | Two plugins would answer to the same `/<name>:` commands. Rename one. | `lock` |
| `workflow.source URL "<url>" is not covered by allowed_remote_resources; add a prefix that covers it, such as https://github.com/<owner>/<repo>/, to allowed_remote_resources in config.yaml` | The default allowlist does not cover github.com tree URLs. Add the suggested prefix, as in [Remote source](#remote-source). | `lock` |
| `workflow.source ref "<ref>" is not a commit sha; pin the commit sha (40 hex characters) so the definition cannot move under the pin` | The tree URL names a branch or tag. Pin the full commit sha. Printed after `✗ Failed to load harness`. | `lock` |
| `workflow.source: tree hash mismatch for <url>: the harness pins sha256=<pinned> but the fetched tree hashes to sha256=<actual>; if the commit is the one you meant, update the #sha256= fragment` | The pin does not match the commit's tree. If the commit is right, copy `<actual>` into the fragment. | `lock` |
| `workflow.source "<source>" is relative, but this harness was added by URL, so relative paths resolve in your repository, not the harness's; pin source as a tree URL with #sha256=, or install the harness through a one-line base: harness` | The harness was registered by URL and pins a relative source. Install it through a `base:` harness, as in [Install a harness someone else ships](#install-a-harness-someone-else-ships). | `lock` |
| `base workflow.source "<source>" is a relative path, so it resolves in the base harness's repository at the base's commit, but the base URL <url> pins ref "<ref>", not a commit sha; pin base: at the full 40-character commit sha, or set workflow.source in the base to a tree URL with #sha256=` | A `base:` URL at a branch or tag holds a relative `workflow.source`. Pin `base:` at the commit sha. Printed after `✗ Failed to load harness`, after `loading base chain: resolving base workflow from <url>: `. | `lock` |

Full field rules:
[harness `workflow`](../../reference/harness-reference.md#field-details).

## See also

- [Make your pipeline repository loadable by fullsend](workflow-definition-authors.md) — the author's side
- [Running agents locally](running-agents-locally.md) — local run setup and lock behaviour
- [Bring Your Own Agent](bring-your-own-agent.md) — what `fullsend agent new` generates
- [ADR 0130](../../ADRs/0130-workflow-definition-repos-are-harness-resources.md) — why a definition is a harness resource
