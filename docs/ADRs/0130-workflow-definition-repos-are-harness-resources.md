---
title: "130. Workflow-definition repositories are harness resources"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - runtime
  - harness
  - security
---

# 130. Workflow-definition repositories are harness resources

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

Some teams keep a whole pipeline in one repository: skills, helper scripts,
sub-agents, and a script that fixes the order of the steps and fans out over
work items. For Claude Code that script is a workflow (`workflows/<name>.js`)
in a repository whose root is a Claude Code plugin, which Claude Code users
install as a marketplace entry pinned to a commit sha. For pi it is an
extension whose session hook drives the same loop. Running such a
*workflow-definition repository* under fullsend should keep the agent-led
path, so the runner keeps its hooks, metrics, transcripts and validation
loop; a script entrypoint above the runtime
([#7947](https://github.com/fullsend-ai/fullsend/pull/7947)) loses them.

[ADR 0094](0094-pi-extensions-are-harness-resources.md) already delivers a
Claude plugin or a pi extension directory, sourced like a skill
([ADR 0038](0038-universal-harness-access.md): a tree URL with a tree hash),
injection-scanned, and loaded by the runtime whose kind it is. A workflow that
a Claude plugin ships runs headless as `/<plugin>:<workflow>`, also with
fullsend's `--agent <name>` on the same command line, and the result event's
`modelUsage` sums the whole tree, workflow agents included (both checked on
Claude Code 2.1.295), so the runner's metrics stay complete.

Three gaps remain. `plugins:` refuses a tree URL at the repository root. The
tree fetcher refuses symlinks, and definition repositories link skills to a
shared `scripts/` directory. And nothing starts a Claude workflow: the agent
prompt has to ask the model to call it.

## Options

- **A script entrypoint (`entrypoint:`).** Rejected: the runner sees only the
  script's exit code and stdout, so token and cost metrics, the hook chain
  and transcript checks must be rebuilt by each script, and the path is
  Claude-only.
- **Keep the sequence in the agent prompt or a skill-driven state machine.**
  Kept as supported shapes; both run on the agent-led path today and need no
  change. They do not give the pipeline author a fixed, testable order of
  steps, which is what a workflow script is for.
- **Vendor the definition repository from a host pre-script.** Works on
  released fullsend and is what consumers do today, but each consumer
  re-implements the fetch, pin check and copy, and the runner never scans
  what is vendored into the target checkout.
- **Pin the definition once in `config.yaml` and name it from the harness.**
  Rejected: the harness is the unit fullsend installs, locks and shares, and
  a definition is a harness resource like `plugins:`. Harnesses of one
  repository already share a resource through `base:`, so a second sharing
  mechanism in `config.yaml` adds a place to look without adding a capability,
  and it would make a harness depend on the consumer's `config.yaml`.

## Decision

A workflow-definition repository is a harness resource. A harness pins it in
one field, `workflow:`, it is delivered through the ADR 0094 path for its
kind, and on Claude the runner starts the workflow itself. Five rules govern
it.

1. **One harness field.** `workflow:` takes a `source`, and on Claude a
   `name` and optional `args`:

   ```yaml
   workflow:
     source: https://github.com/example-org/sample-pipeline/tree/<commit-sha>#sha256=<tree-hash>
     name: triage-fanout
     args: issue ${ISSUE_NUMBER}
   ```

   A remote `source` is a forge tree URL at a full commit sha, pointing at
   the repository root or at a sub-directory, with fullsend's tree hash
   (github.com today, as for `plugins:`). These are the `github` (`repo` +
   `sha`) and `git-subdir` (`url` + `path` + `sha`) source shapes of a Claude
   Code marketplace entry, so a repository that already installs as a Claude
   Code plugin needs no new layout. The URL must be covered by
   `allowed_remote_resources` ([ADR 0058](0058-agent-registration.md)), and
   `fullsend lock` records it with the harness's other dependencies. A
   relative `source` is a path in the repository that holds the harness, `.`
   being its root: locally the git checkout, and for a harness composed
   through `base:` the base harness's repository at its pinned commit, as for
   a base plugin. Locally only files git tracks are read, so nothing untracked
   beside the checkout (another repository, a credentials file) is delivered.
   Several harnesses of one repository share a pin through `base:`. A harness
   added by URL (`fullsend agent add <url>`) resolves relative paths in the
   consumer's repository, so such a harness pins `source` as a URL; a
   relative `source` there is refused with that advice. Zip archives with a
   digest (the marketplace `archive` shape) are not accepted in this
   decision.
2. **Fetched as exactly what is uploaded.** The fetch accepts the repository
   root and stays bounded by the tree fetcher's limits on file count and total
   size. A symlink whose target is inside the fetched tree is *materialized*:
   the target's content is stored under the link's path, and counts toward
   the limits like any other file. A symlink that leaves the tree, dangles or
   loops is refused with its path in the error. The tree that is hashed is
   therefore the tree that is injection-scanned, uploaded and preflighted,
   with no links left in it. A source path that itself passes through a
   symlink is refused, and fullsend's own configuration directory is never
   part of the tree.
3. **The tree's kind decides what runs it.** The fetched tree is classified
   by `pluginformat.Detect`, as a `plugins:` entry is, and must match the
   selected runtime; a mismatch fails at plan time naming both.
   - **Claude plugin.** `name` is the workflow (the script's `meta.name`;
     fullsend requires `workflows/<name>.js` to match). The runner uploads
     the definition like a `plugins:` entry and replaces the default prompt
     with `/<plugin>:<name> <args>`, where `<plugin>` is the `name` in
     `.claude-plugin/plugin.json` or, without that file, the uploaded
     directory's name. Claude Code hands the text after the command to the
     script as one string, unsplit (checked on 2.1.295). `--agent` stays on
     the command line, so the agent file still sets the main loop's tools and
     instructions. A validation-loop retry starts the same command again; the
     workflow skips finished work from its own durable state, since a Claude
     Code workflow resumes only within the session that started it.
   - **pi extension.** The definition is loaded with `-e` like a `plugins:`
     entry, and its own session hook starts the sequence under the default
     prompt. `name` and `args` are refused for a pi definition rather than
     ignored. How a script-led pi parent reports completion is a follow-up.
   - Other runtimes refuse `workflow:` at plan time. The run plan prints the
     source, its pin and, on Claude, the command; the dummy runtime records
     the command it would have run, so behaviour tests can assert on it
     without a model.
4. **Args carry identifiers, never credentials.** `args` expands `${VAR}`
   from the runner environment, as `env:` does, so a harness can name the
   work item. It reaches the model prompt, the run plan and `metrics.json`,
   so a credential-shaped variable, an unset variable, a value that adds a
   line, and a value that looks like a credential are each refused. The
   contract is on the harness author: args reference only non-secret
   identifiers. The name denylist and the value check are defense in depth,
   since a secret held in an ordinary-looking variable can pass both.
5. **No new tool or hook surface.** `Workflow` is already a canonical Claude
   tool name, the tool-allowlist, canary and PostToolUse hooks already match
   every tool, and an agent with a `tools:` list must name `Workflow` for the
   main loop to start the run. Workflow agents run inside the same `claude`
   process, under the same hooks.

## Consequences

- A pipeline repository runs under fullsend with one `workflow:` field in a
  thin harness per agent, so consumers stop vendoring it from a pre-script.
- Symlink materialization and the repository-root mode apply to this resource
  only, so the pin, scan and upload of `skills:` and `plugins:` do not change.
- An older fullsend binary silently ignores the unknown `workflow:` key and
  runs the harness with the default prompt, so consumers must pin the fullsend
  release that introduced it; the field is additive and does not bump
  `schema_version` ([ADR
  0127](0127-harness-schema-versioning-and-field-types.md)). A lock file that
  records a definition is written as version 2, which older releases do not
  read.
- fullsend's own behaviour tests cover this path against a GitHub test
  repository; coverage of a pipeline's external systems, such as Jira, stays
  with the definition repository's integration tests.
- A Claude workflow agent has no Agent tool (Claude Code 2.1.260, 2.1.292,
  2.1.295) and a workflow cannot pause for a human, so a definition lifts
  nested sub-agent launches into the script and ends the run at each human
  gate.
- Follow-ups, each its own decision: named steps with a `resume_from` input
  started by a later `trigger:` event, the pi completion contract (a
  completion event for a script-led parent and a public child-call API), and
  the marketplace `archive` source shape.
