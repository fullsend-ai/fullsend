# Make your pipeline repository loadable by fullsend

This guide is for authors of a Claude Code pipeline: a plugin with skills,
helper scripts and a workflow script that fixes the order of the steps. (A pi
extension definition follows pi's own extension layout; see
[pi extension definitions](workflow-definitions.md#pi-extension-definitions).) It
shows the layout fullsend accepts, the rules fullsend checks, and how others
run your pipeline. To run a definition someone else wrote, see
[Run a workflow definition repository](workflow-definitions.md). The design is
recorded in
[ADR 0130](../../ADRs/0130-workflow-definition-repos-are-harness-resources.md).

The worked example is the sample definition that ships in fullsend at
[`e2e/behaviour/fixtures/workflow/sample-pipeline/`](https://github.com/fullsend-ai/fullsend/tree/main/e2e/behaviour/fixtures/workflow/sample-pipeline).
The commands below run it from `pipelines/sample-pipeline/` in a repository
checked out at `~/src/sample-repo`, which also holds `.fullsend/`.

## Prerequisites

- The Claude Code CLI, for `claude plugin validate`.
- A fullsend binary that knows workflow definitions. `fullsend agent new --help`
  lists `--workflow-source` and `--workflow` flags when it does; an older
  binary silently ignores the harness `workflow:` key.
- The definition committed to git. fullsend reads a local definition from the
  git index and a remote one from a pinned commit, so untracked files never
  reach the sandbox.

## The layout

```console
$ git ls-files pipelines/sample-pipeline
pipelines/sample-pipeline/.claude-plugin/plugin.json
pipelines/sample-pipeline/scripts/checklist.sh
pipelines/sample-pipeline/skills/read-issue/SKILL.md
pipelines/sample-pipeline/workflows/triage-fanout.js
```

| Path | What it is | Rule |
|---|---|---|
| `.claude-plugin/plugin.json` | The plugin manifest | At the definition root, with a usable `name` |
| `workflows/<name>.js` | One workflow script per workflow | File stem equals `meta.name` |
| `skills/<skill>/SKILL.md` | Skills the workflow's agents use | Standard Claude Code layout |
| `scripts/` | Helper scripts | Reached through `${CLAUDE_PLUGIN_ROOT}` |

The definition root is the directory a consumer's `source:` points at: the
repository root or a sub-directory. A definition may hold at most 1000 files
and 50 MiB in total.

## Step 1: Validate the plugin with Claude Code

Run Claude Code's own check first. It catches manifest problems that fullsend
does not look for:

```console
$ claude plugin validate pipelines/sample-pipeline
Validating plugin manifest: ~/src/sample-repo/pipelines/sample-pipeline/.claude-plugin/plugin.json

⚠ Found 1 warning:

  ❯ author: No author information provided. Consider adding author details for plugin attribution

✔ Validation passed with warnings
```

`Validation passed` or `Validation passed with warnings` is success. Message
reference: Claude Code's
[plugin manifest reference](https://code.claude.com/docs/en/plugins-reference#validate-the-manifest).

## Step 2: Name the plugin

```json
{"name":"sample-pipeline","version":"0.1.0","description":"Sample issue pipeline"}
```

- **Use only letters, digits, `_` and `-` in `name`.** It becomes the
  `/<name>:` prefix of the workflow command, and fullsend refuses other
  characters, which is stricter than Claude Code.
- **Leave out the `workflows` key.** Claude Code accepts a custom workflow
  path there; fullsend does not yet, and refuses the manifest. Keep the
  scripts in `workflows/`.
- **Pick a name no other plugin of the consumer's
  [harness](../../reference/harness-reference.md) uses.** Two plugins
  with one name would answer to the same `/<name>:` commands, so fullsend
  refuses the pair. Without a manifest `name` the namespace is
  `workflow-definition`, the fixed sandbox directory the definition is
  uploaded under, so set the name.

Keep the manifest. A directory that is neither a Claude Code plugin nor a pi
extension is refused with `is neither a Claude Code plugin nor a pi extension
(...): add .claude-plugin/plugin.json at the definition root for a Claude
workflow, or a pi extension entry point`.

## Step 3: Write the workflow script

The sample's script starts like this:

```javascript
export const meta = {
  name: 'triage-fanout',
  description: 'Read an issue, check each checklist item in parallel, then report',
  phases: [
    { title: 'Read', detail: 'read the issue named in args' },
    { title: 'Check', detail: 'one agent per checklist item' },
    { title: 'Report', detail: 'write the agent result' },
  ],
};
```

fullsend reads this object as text, without running the script. Its rules:

- **Make `export const meta = {` the first statement.** Comments may precede
  it. Claude Code lists a workflow only when its meta comes first.
- **End it with `};`.** fullsend reads nothing after the `;`, and needs it to
  find the end of the object.
- **Set `meta.name` to the file stem**, here `triage-fanout` for
  `workflows/triage-fanout.js`. Claude Code names the workflow by
  `meta.name`; fullsend finds the script by file name. Use letters, digits,
  `_` and `-`, in single or double quotes, without escapes.
- **Use plain literal values only:** strings, numbers, `true`, `false`,
  `null`, and arrays and objects of these. A variable, call or spread is not
  a plain literal, and Claude Code then drops the workflow from its list.
- **Read input from `args`.** The consumer's harness sets one string, such as
  `issue 8204`, and the script receives it unsplit. The sample uses it in
  its first prompt: `` `Use the read-issue skill to read ${args}. ...` ``.
  Expect work-item identifiers only: fullsend refuses args that name or
  expand to a credential.

The full script is in the
[fixture](https://github.com/fullsend-ai/fullsend/blob/main/e2e/behaviour/fixtures/workflow/sample-pipeline/workflows/triage-fanout.js).
Writing the body is covered by Claude Code's
[workflow documentation](https://code.claude.com/docs/en/workflows#what-the-saved-script-looks-like).

## Step 4: Reach helper scripts through a skill

Claude Code substitutes `${CLAUDE_PLUGIN_ROOT}` with the plugin's directory in
skill, command and agent Markdown, but does not set it in the environment of
commands the Bash tool runs
([Claude Code: Environment variables](https://code.claude.com/docs/en/plugins-reference#environment-variables)).
So name helper scripts in a skill body, and have the workflow's agents use the
skill. The sample's `skills/read-issue/SKILL.md`:

````markdown
---
name: read-issue
description: Read the issue named in the workflow args and list its unchecked checklist items.
---

# Read an issue

The workflow args name the issue as `issue <number>`. Run the plugin's helper
with that number; it reads the issue from the repository the run works on
(`REPO_FULL_NAME`) over the GitHub REST API:

```bash
"${CLAUDE_PLUGIN_ROOT}/scripts/checklist.sh" <number>
```

It prints `{"title": ..., "items": [...]}`: the issue title and every
unchecked Markdown checklist item (`- [ ] ...`) in the order they appear.
Return exactly that object.
````

## Step 5: Keep symlinks inside the definition

fullsend replaces a symlink whose target is inside the definition with a copy
of the target, so the uploaded tree has no links. A link that leaves the
definition, dangles or loops is refused, because its target would not be part
of what was pinned and scanned.

A skill that links to the shared helper is delivered as a copy. For this
check, the sample got one extra link that the fixture does not have,
`skills/read-issue/checklist.sh -> ../../scripts/checklist.sh`:

```console
$ git ls-files -s pipelines/sample-pipeline/skills
100644 e8c20be916eedbe161573f0c3825daf1ac8de811 0	pipelines/sample-pipeline/skills/read-issue/SKILL.md
120000 5f7d8a438ba0b476b1a5d467d6d28d46d71ee771 0	pipelines/sample-pipeline/skills/read-issue/checklist.sh
$ fullsend lock sample-triage
→ Locking dependencies: sample-triage
  ✓ Harness has no remote dependencies — nothing to lock
```

A link to a file outside the definition stops the run:

```console
$ fullsend lock sample-triage
→ Locking dependencies: sample-triage
  ✗ Workflow definition resolution failed
Error: resolving workflow definition: workflow.source "pipelines/sample-pipeline": scripts/readme.md is a symlink to ../../../README.md, which leaves the source directory; point the link inside the repository or commit the file
```

Both outputs come from a harness that pins the definition as a path source
(see [Step 7](#step-7-check-with-fullsend-lock)); the banner lines are left
out.

## Step 6: Design the pipeline for unattended runs

Each rule comes from how Claude Code workflows run inside a fullsend sandbox:

- **Launch every sub-agent from the script.** A workflow agent has no Agent
  tool (checked on Claude Code 2.1.260, 2.1.292 and 2.1.295), so an agent
  cannot start its own sub-agents. Call `agent()`, `parallel()` or
  `pipeline()` in the script for each one instead.
- **End the run at each human gate.** A workflow takes no mid-run user input
  ([Claude Code: Behavior and limits](https://code.claude.com/docs/en/workflows#behavior-and-limits)).
  Write the result, let the run end, and start the next stage from a later
  event, such as another slash command.
- **Keep the sandbox read-only and publish from the post-script.** Write the
  result to `$FULLSEND_OUTPUT_DIR/agent-result.json`, as the sample's
  `Report` phase does. The harness post-script posts it from the host after
  the sandbox is gone. The model-driven part of the run then needs only read
  access (the sample harness gives it the `github-ro` provider), and the
  write happens in a script you can review.
- **Treat work-item text as data in prompts.** Issue titles, bodies and
  checklist items are written by whoever files the issue. Pass them to a
  sub-agent inside explicit delimiters, labelled as untrusted content to
  evaluate and never instructions to follow, as the sample's `untrusted()`
  helper does with `JSON.stringify` for the Check and Report prompts.
- **Make a rerun skip finished work.** A validation-loop retry starts the same
  command again, and a Claude Code workflow resumes only in the session that
  started it. Read progress from files in the repository or
  the workspace, not from the earlier run.

## Step 7: Check with fullsend lock

Scaffold an agent in the repository's own `.fullsend/` whose harness pins the
definition as a path source, and lock. Both are walked through in
[Run a workflow definition repository](workflow-definitions.md#step-1-scaffold-the-agent-with-the-definition-pinned).
`fullsend lock` runs the same definition checks as `fullsend run`, without an
OpenShell gateway:

```console
$ fullsend lock --all
⚡ fullsend dev
  Autonomous agentic development for Git-hosted organizations
→ Locking all harnesses

→ Locking dependencies: sample-triage

  ✓ Harness has no remote dependencies — nothing to lock
  ✓ No harnesses have remote dependencies — nothing to lock
```

Typical author mistakes and what they print (banner lines left out):

```console
$ fullsend lock sample-triage
→ Locking dependencies: sample-triage
  ✗ Workflow definition resolution failed
Error: resolving workflow definition: workflow.source pipelines/sample-pipeline: workflows/triage-fanout.js declares meta.name "triage-fan-out"; Claude Code runs it as /sample-pipeline:triage-fan-out, so rename the file or set meta.name to "triage-fanout"
```

```console
$ fullsend lock sample-triage
→ Locking dependencies: sample-triage
  ✗ Workflow definition resolution failed
Error: resolving workflow definition: workflow.source pipelines/sample-pipeline: workflows/triage-fanout.js: end the meta statement with `;` directly after its closing brace; Claude Code lists a workflow only when `export const meta = { ... }` is the script's first statement and a plain object literal with a quoted name, and fullsend needs `;` right after its closing brace
```

```console
$ fullsend lock sample-triage
→ Locking dependencies: sample-triage
  ✗ Workflow definition resolution failed
Error: resolving workflow definition: workflow.source pipelines/sample-pipeline: .claude-plugin/plugin.json sets "workflows", a custom workflow path, which fullsend does not support yet; keep the scripts in workflows/ at the definition root and remove the "workflows" key
```

The consumer guide's [troubleshooting table](workflow-definitions.md#troubleshooting)
lists every message.

## Run your own pipeline from the same repository

A definition repository can hold its own `.fullsend/` and run its pipeline
with a path source, without pinning itself:

```yaml
# .fullsend/harness/sample-triage.yaml
workflow:
  source: pipelines/sample-pipeline
  name: triage-fanout
  args: issue ${ISSUE_NUMBER}
```

- **Point `source` at the plugin root.** Use `source: .` only when the
  repository root is the plugin, with `.claude-plugin/plugin.json` at the top.
- **Keep the definition outside `.fullsend/`.** The configuration directory is
  never part of a definition. When it lies under the source, as with
  `source: .`, a local run leaves it out.
- **Expect a different hash from a remote pin of the root.** A remote tree URL
  at the same path includes `.fullsend/`, so its tree hash differs from the
  local one. A sub-directory source without `.fullsend/` under it hashes the
  same both ways when its tracked files match the commit, so the
  `pin_sha256` of a local run is then the `#sha256=` value for a remote pin
  of that commit.

## Let others install your harness

Ship a harness whose `workflow.source` is relative, such as the one above. A
consumer installs it with a thin harness whose `base:` is your harness at a
commit: the relative source then resolves in **your** repository at that
commit, the path taken from your repository root, so the consumer pins one
commit for the harness and the definition together. The base must be pinned
at a full commit sha; a branch or tag is refused with
`base workflow.source "pipelines/sample-pipeline" is a relative path, so it resolves in the base harness's repository at the base's commit, but the base URL ... pins ref "main", not a commit sha; pin base: at the full 40-character commit sha, or set workflow.source in the base to a tree URL with #sha256=`.

A consumer cannot add such a harness with `fullsend agent add <url>`: a
harness added by URL resolves relative paths in the consumer's repository,
so its relative `workflow.source` is refused with advice to use a `base:`
harness. A harness whose `workflow.source` is a tree URL with `#sha256=`
installs by `fullsend agent add <url>` like any other.

Not run here: needs your definition repository's harness at a pushed
commit; fullsend publishes no harness with `workflow:` that could stand in
for yours as `base:`. The consumer's steps:

1. **Allow your repository and register the agent.** Add to
   `.fullsend/config.yaml`:

   ```yaml
   # .fullsend/config.yaml (consumer)
   agents:
     - name: sample-triage
       source: harness/sample-triage.yaml
   allowed_remote_resources:
     - https://raw.githubusercontent.com/example-org/sample-pipeline/
   ```

   The raw-content prefix covers both your harness and the definition: an
   inherited relative source is checked against
   `https://raw.githubusercontent.com/<owner>/<repo>/<commit-sha>/<path>/`,
   as a base plugin is.

2. **Write the thin harness**, with 64 zeros as `<file-hash>` for now:

   ```yaml
   # .fullsend/harness/sample-triage.yaml (consumer)
   base: https://raw.githubusercontent.com/example-org/sample-pipeline/<commit-sha>/.fullsend/harness/sample-triage.yaml#sha256=<file-hash>
   ```

3. **Fill in the harness hash.** Run
   `fullsend agent update sample-triage <commit-sha>`. It fetches your harness
   at that commit and rewrites the `base:` line with the real hash.

4. **Lock.** Run `fullsend lock sample-triage`. The inherited source needs no
   `#sha256=`: the base's pin covers it, and the lock file records the
   definition's tree hash.

How your harness's other relative paths resolve for the consumer: from the
parent of your harness's directory, at the pinned commit. For the `base:`
above, `agent: agents/sample-triage.md` is fetched from
`https://raw.githubusercontent.com/example-org/sample-pipeline/<commit-sha>/.fullsend/agents/sample-triage.md`,
and `post_script`, `policy` and `validation_loop.schema` resolve the same way;
only `workflow.source` is taken from the repository root. Merge rules for a
child harness: [Harness field reference § Field merge rules](../../reference/harness-reference.md#field-merge-rules-for-base-and-overlays);
which fields are fetched from a URL base:
[Harness fields § semantic types](../../contributing/harness-fields.md#semantic-types-adr-0127).

## See also

- [Run a workflow definition repository](workflow-definitions.md) — the consumer's side, with troubleshooting
- [Harness `workflow`](../../reference/harness-reference.md#field-details) — the harness field, with every source and pin rule
- [Configuring agent behavior](customizing-agents.md) — `base:` composition
- [ADR 0130](../../ADRs/0130-workflow-definition-repos-are-harness-resources.md) — the decision
