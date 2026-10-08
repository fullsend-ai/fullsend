---
title: "130. Runtime workflows are harness resources"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - runtime
  - harness
  - security
---

# 130. Runtime workflows are harness resources

Date: 2026-10-08

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

Some teams keep a whole multi-step agent process in one repository: skills,
helper scripts, sub-agents, and a script that fixes the order of the steps and
fans out over work items. This ADR calls that script a *runtime workflow*: a
workflow the agent runtime executes itself, not a GitHub Actions workflow. For
Claude Code it is a workflow (`workflows/<name>.js`) in a repository whose root
is a Claude Code plugin, which Claude Code users install as a marketplace entry
pinned to a commit sha; the `Workflow` tool, `workflows/<name>.js` and
`/<plugin>:<workflow>` are Claude Code's own names. For pi it is an extension
whose session hook drives the same loop. Running a *runtime-workflow
repository* under fullsend should keep the agent-led path, where the runner
starts the runtime itself and so keeps its hooks, metrics, transcripts and
validation loop; a script entrypoint above the runtime
([#7947](https://github.com/fullsend-ai/fullsend/pull/7947)) loses them.

[ADR 0094](0094-pi-extensions-are-harness-resources.md) already delivers a
Claude plugin or a pi extension directory: sourced like a skill
([ADR 0038](0038-universal-harness-access.md): a forge tree URL pinned with
fullsend's tree hash), injection-scanned, and loaded by the runtime whose kind
it is. A workflow that a Claude plugin ships runs headless as
`/<plugin>:<workflow>`, also with fullsend's `--agent <name>` on the same
command line. The result event's `modelUsage` sums the whole tree, workflow
agents included, so the runner's metrics stay complete. Both were checked on
Claude Code 2.1.295.

Three gaps remain. `plugins:` refuses a tree URL at the repository root. The
tree fetcher refuses symlinks, and runtime-workflow repositories link skills
to a shared `scripts/` directory. And nothing starts a Claude workflow: the
agent prompt has to ask the model to call it. This ADR aims to run a
runtime-workflow repository on the agent-led path as a pinned, scanned harness
resource.

## Options

- **A script entrypoint (`entrypoint:`).** Rejected: the runner sees only the
  script's exit code and stdout, so token and cost metrics, the hook chain
  and transcript checks must be rebuilt by each script, and the path is
  Claude-only.
- **Keep the sequence in the agent prompt or a skill-driven state machine.**
  Kept as supported shapes; both run on the agent-led path today and need no
  change. They do not give the pipeline author a fixed, testable order of
  steps, which is what a workflow script is for.
- **Vendor the runtime-workflow repository from a host pre-script.** Works on
  released fullsend and is what consumers do today, but each consumer
  re-implements the fetch, pin check and copy, and the runner never scans
  what is vendored into the target checkout.
- **Pin the runtime workflow once in `config.yaml` and name it from the
  harness.** Rejected: the harness is the unit fullsend installs, locks and
  shares, and a runtime workflow is a harness resource like `plugins:`.
  Harnesses of one repository already share a resource through `base:`, so a
  second sharing mechanism in `config.yaml` adds a place to look without
  adding a capability, and it would make a harness depend on the consumer's
  `config.yaml`.

## Decision

A runtime-workflow repository is a harness resource. A harness pins it in one
field, `runtime_workflow:`. fullsend delivers it through the ADR 0094 path for
its kind, and on Claude the runner starts the workflow itself. Five rules
govern it.

1. **One harness field.** `runtime_workflow:` takes a `source`, and on Claude a
   `name` and optional `args`:

   ```yaml
   runtime_workflow:
     source: https://github.com/example-org/sample-pipeline/tree/<commit-sha>#sha256=<tree-hash>
     name: triage-fanout
     args: issue ${ISSUE_NUMBER}
   ```

   - **Remote source.** A forge tree URL at a full commit sha, pointing at the
     repository root or at a sub-directory, with fullsend's tree hash
     (github.com only, for now). These are the `github` (`repo` +
     `sha`) and `git-subdir` (`url` + `path` + `sha`) source shapes of a
     Claude Code marketplace entry, so a repository that already installs as
     a Claude Code plugin needs no new layout. The URL must be covered by
     `allowed_remote_resources` ([ADR 0058](0058-agent-registration.md)), and
     `fullsend lock` records it with the harness's other dependencies.
   - **Relative source.** A path in the repository that holds the harness,
     `.` being its root. Locally that is the git checkout, and only files git
     tracks are read, so nothing untracked beside the checkout (another
     repository, a credentials file) is delivered. For a harness fetched by
     URL, as a `base:` or through `fullsend agent add <url>`, it is that
     harness's repository at the commit the harness was fetched from, as
     [ADR 0045](0045-forge-portable-harness-schema.md) resolves the harness's
     other relative paths. A runtime-workflow repository can therefore ship
     its own harness with `source: .`.
   - **Sharing and overlays.** Several harnesses of one repository share a pin
     through `base:`. A child's `runtime_workflow:` replaces the base's whole
     mapping, so a Claude `name` never carries over to a pi extension. An
     overlay may set the field as a guarded field
     ([ADR 0112](0112-overlays-may-set-any-harness-field.md)).
   - **Field types** ([ADR 0127](0127-harness-schema-versioning-and-field-types.md)).
     `runtime_workflow` is structural, `source` is a resource reference, and
     `name` and `args` are scalars.
   - **Not accepted.** Zip archives with a digest (the marketplace `archive`
     shape).
2. **Fetched as exactly what is uploaded.** The fetch accepts the repository
   root and stays bounded by the tree fetcher's limits on file count and total
   size. A symlink whose target is a file or directory of the fetched tree
   itself (for a local source, one that git tracks) is *materialized*: the
   target's content is stored under the link's path, and counts toward the
   limits like any other file. A symlink that leaves the tree, dangles or
   loops is refused with its path in the error. The tree that is hashed is
   therefore the tree that is injection-scanned and uploaded, with no links
   left in it. On every runtime the runner checks the uploaded copy against
   that hash before each iteration and fails the run on a mismatch. A source
   path that itself passes through a symlink is refused, and neither `.git/`
   nor fullsend's own configuration directory is ever part of the tree.
3. **The tree's kind decides what runs it.** The fetched tree is classified
   by `pluginformat.Detect`, as a `plugins:` entry is, and must match the
   selected runtime; a mismatch fails at plan time naming both.
   - **Claude plugin.** `name` is the workflow (the script's `meta.name`).
     fullsend requires `workflows/<name>.js` to match, and `name` to use only
     letters, digits, `_` and `-`. The runner uploads the runtime workflow
     like a `plugins:` entry and replaces the default prompt with
     `/<plugin>:<name> <args>`, where `<plugin>` is the `name` in
     `.claude-plugin/plugin.json` or, without that file, the uploaded
     directory's name. Claude Code hands the text after the command to the
     script as one string, unsplit (checked on 2.1.295). `--agent` stays on
     the command line, so the agent file still sets the main loop's tools and
     instructions. A validation-loop retry starts the same command again and
     does not pass validation feedback to the workflow. The workflow skips
     finished work from its own durable state, kept outside the
     runtime-workflow directory, since a Claude Code workflow resumes only
     within the session that started it.
   - **pi extension.** The runtime workflow is loaded with `-e` like a
     `plugins:` entry, and its own session hook starts the sequence under the
     default prompt. `name` and `args` are refused for a pi runtime workflow
     rather than ignored. How a script-led pi parent reports completion is a
     follow-up.
   - **Other runtimes** refuse `runtime_workflow:` at plan time. The run plan
     prints the source, its pin and, on Claude, the command. The dummy runtime
     records the command it would have run, so fullsend's behaviour tests can
     assert on it without a model; a pipeline's external systems, such as
     Jira, stay with the runtime-workflow repository's own integration tests.
4. **Args carry identifiers, never credentials.** `args` expands `${VAR}`
   from the runner environment, as `env:` does, so a harness can name the
   work item. The expanded value reaches the model prompt, the run plan and
   `metrics.json`, so fullsend refuses a credential-shaped or runner-only
   variable, an unset variable, a value with a line break or another control
   character, and a value that looks like a credential. The contract is on
   the harness author: args reference only non-secret identifiers. The name
   denylist and the value check are defense in depth, since a secret held in
   an ordinary-looking variable can pass both.
5. **No new tool or hook surface.** `Workflow` is already a canonical Claude
   tool name. The tool-allowlist, canary and PostToolUse hooks already match
   every tool, and an agent with a `tools:` list must name `Workflow` for the
   main loop to start the run. Workflow agents run inside the same `claude`
   process, under the same hooks, and the script runs inside the same sandbox
   and egress policy.

## Forge scope

- **GitHub:** a remote `source` is a github.com tree URL.
- **GitLab and Forgejo:** a relative `source` and the runner path work
  unchanged; a remote `source` on these forges is out of scope for this
  decision.

## Consequences

- A runtime-workflow repository runs under fullsend with one
  `runtime_workflow:` field in a thin harness per agent, so consumers stop
  vendoring it from a pre-script.
- Symlink materialization and the repository-root mode apply to this resource
  only, so the pin, scan and upload of `skills:` and `plugins:` do not change.
- An older fullsend binary silently ignores the unknown `runtime_workflow:`
  key, runs the default prompt, and cannot read the version 2 lock file that
  records it, so it runs every agent of that repository without lock pins;
  consumers must pin the release that introduced the field, which is additive
  and does not bump `schema_version` (ADR 0127).
- A Claude workflow agent has no Agent tool (Claude Code 2.1.260, 2.1.292,
  2.1.295) and a workflow cannot pause for a human, so a runtime workflow
  lifts nested sub-agent launches into the script and ends the run at each
  human gate.
- Follow-ups, each its own decision: named steps with a `resume_from` input
  started by a later `trigger:` event, the pi completion contract (a
  completion event for a script-led parent and a public child-call API), and
  the marketplace `archive` source shape.
