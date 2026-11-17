---
title: "169. Assess vulnerabilities with a read-only agent before the code agent"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - agents
  - vulnerability-management
  - coordination
---

# 169. Assess vulnerabilities with a read-only agent before the code agent

Date: 2026-11-11

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

Teams want fullsend to handle vulnerability work: a CVE arrives as a tracker,
an alert or an issue, someone decides whether it affects a repository, and if
it does, someone fixes it. Many findings need no code change, because the
component is absent, the fix is already carried or the vulnerable code cannot
be reached. The result is then a justified "not affected" on the tracker, not
a pull request. No built-in agent produces that result: triage labels and
routes an issue, and code writes a change.

The [CVE workflow experiment](https://github.com/fullsend-playground/cve-workflow-experiment)
showed the flow can work. It composes the triage harness with its own agent,
result schema and scripts, registers that under the `triage` name, and keeps
a controller in the repository. A second repository would have to copy that
code, and a repository that adopts it no longer has regular triage. Dispatch
to several targets, recovery and Pi tool selection for this work are being
decided separately ([#9266](https://github.com/fullsend-ai/fullsend/pull/9266),
[#9264](https://github.com/fullsend-ai/fullsend/pull/9264),
[#9265](https://github.com/fullsend-ai/fullsend/pull/9265)).

Each step gets its own agent, harness and sandbox
([ADR 0020](0020-composable-single-responsibility-agents-with-individual-sandboxes.md)),
any harness may name any existing role
([ADR 0124](0124-criteria-for-adding-a-built-in-agent-role.md)), and runs are
serialized per harness and work item
([ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md)).
This ADR aims to give any repository a trustworthy verdict on one public
vulnerability for one branch, and a fix where one is needed, without
orchestration code of its own.

## Options

### Where assessment runs

- **A skill on the code agent.** The smallest change. The code agent's output
  is a pull request and its role can write code, so every assessment,
  including the many that end in "not affected", would run with write access
  and have nowhere to put its answer.
- **The triage agent, extended or composed.** Triage has the right role.
  Extending it adds a verdict to a result every adopter shares, on a time
  budget sized for labelling an issue. Composing over it, as the experiment
  does, replaces regular triage for the repository.
- **A Pi extension that holds the whole workflow.** It works on one runtime,
  which the adopter chooses per agent
  ([ADR 0091](0091-per-agent-runtime-model-effort.md)), and the checks that
  gate publishing and coding would run inside the sandbox.
- **A read-only assessment agent, then the code agent.** Chosen.

### How a work item reaches the agent

- **Triage first.** Triage runs on the work item, recognises a
  vulnerability and adds the label that starts the `cve` agent. This works
  on any tracker and catches a vulnerability reported as an ordinary bug.
  But triage reads the whole work item into a model before any check that
  the finding is public, every tracker costs a triage run before its
  assessment, and triage's outcomes do not fit a tracker
  ([agents#1967](https://github.com/fullsend-ai/agents/issues/1967)).
- **A label or a command from a person.** This works on every forge and
  keeps a person on the choice to assess. Triage has usually run by the
  time anyone adds the label.
- **The issue type.** A work item whose type marks it as a vulnerability
  skips triage and starts the `cve` agent, so no model reads it before the
  host checks. Today only Jira events carry an issue type, a type's id and
  name belong to the Jira site, and changing only the type of an open work
  item raises no event. Chosen where the tracker has such a type, with the
  label and the command for everything else.

## Decision

We add a first-party `cve` agent that assesses one public vulnerability
against one branch and changes nothing, and we hand any fix to the existing
code agent, so that assessment needs no write access to code and a fix
always follows a verdict.

1. **Role.** The agent uses the existing `triage` role with a read-only
   checkout. It adds no role and no scope, so ADR 0124 is not engaged. It
   lives in `fullsend-ai/agents`, a repository registers it with an
   `agents:` entry ([ADR 0058](0058-agent-registration.md)), and its harness
   trigger dispatches it ([ADR 0061](0061-harness-cel-dispatch.md)).
2. **Start.** Where events carry an issue type, a work item of a
   vulnerability type starts the agent when it is opened, and triage does
   not start on it. The `cve` and triage triggers match the type by its
   id, because a site can rename a type and the id stays the same. The ids
   belong to the tracker's site, so an adopter sets them by changing those
   triggers on its `agents:` entries
   ([ADR 0162](0162-agent-entries-may-carry-harness-overrides.md)).
   Everywhere else, and for a work item that is already open, the agent
   starts on a `/fs-cve-triage` comment from a person or when the
   `fullsend-cve` label is added, and triage does not start on a work item
   with that label. Whether a work item of a vulnerability type should
   instead wait for a person's comment before its first assessment, so that
   nothing is assessed by default, is not decided here.
3. **Verdict.** The result is affected, not affected, fixed or under
   investigation, in the terms of CSAF VEX, and each piece of evidence says
   whether a tool observed it or the model judged it. Host scripts publish it
   as one comment on the work item, which a later run updates. It is a
   proposal for a person to review; fullsend does not publish VEX. A run
   that cannot gather its evidence, for example because the sandbox lacks
   the toolchain the branch declares, says so and gives no verdict.
4. **Handoff.** An affected verdict records on the work item the
   vulnerability, the branch, the commit and the files its plan may change.
   The code agent works on a vulnerability only while that record is current.
   It opens its change against the assessed branch
   ([ADR 0053](0053-agent-driven-branch-targeting.md)), and a host check
   rejects a change outside those files. On a vulnerability work item the
   code agent starts only on a person's `/fs-code` comment; the
   `ready-to-code` label does not start it there. The fix agent is held to
   the same record and the same files, and a change that needs other files
   needs a new assessment.
5. **One target per run.** Host scripts resolve one repository and branch
   for the work item. They use a structured intake record on the work item
   (the vulnerability, product, release and component) and a release
   mapping the adopter keeps in its repository, not the work item's free
   text. Who writes the intake record is not decided here. Several
   maintained branches are several work items. How those are created is
   not decided here; #9266 proposes one.
6. **Scope.** Embargoed vulnerabilities are out of scope. Host scripts stop
   a run that cannot show its finding is public, before a sandbox exists.
   A work item that carries a tracker security level counts as not public
   unless the adopter lists that level as readable. Whether a private
   tracker's text may be used for a public repository is also not decided
   here (see
   [private context in public agent workflows](../problems/private-context-public-workflows.md));
   until it is, the agent by default does not read that text in that case.
7. **Adopter facts.** The release mapping with its maintained branches,
   vendor VEX sources, readable tracker security levels and label
   conventions live in files in the adopter's repository. A harness setting
   names those files
   ([ADR 0162](0162-agent-entries-may-carry-harness-overrides.md)). They are
   not new `config.yaml` fields.
8. **Tools.** Tools the agent calls during a run, MCP servers included,
   execute inside its sandbox, under its network policy and an allow-list
   of tools. The sandbox is given no credential that can change code, and
   publishing stays in host scripts. A tool server that has to run outside
   the sandbox with its own access to the checkout is a platform decision
   this ADR does not make.

## Forge scope

- **GitHub:** issues are the work item, including one opened from a
  Dependabot alert.
- **GitLab:** covered as the repository, with Jira as the tracker. GitLab
  issues as the work item are not covered yet, because no adopter files
  vulnerability trackers there; the agent skips them with a reason.
- **Jira:** Cloud trackers reach the agent through the Jira poller
  ([ADR 0063](0063-polling-based-work-discovery.md)). Its events are the
  only ones that carry an issue type today.
- **Agent job on one forge, repository on another:** not covered. A GitLab
  job has assessed a GitHub repository, and the code agent then changed it,
  but only with adopter-side scripts and a credential kept as a CI/CD
  variable ([#9577](https://github.com/fullsend-ai/fullsend/issues/9577),
  [#402](https://github.com/fullsend-ai/fullsend/issues/402)). A pull
  request opened that way raises no event where the job ran, so it is
  reviewed only if fullsend is also installed on the repository.
- **Forgejo:** fullsend has no Forgejo support yet; nothing to change.

## Consequences

- A repository gets vulnerability assessment by registering one agent, and a
  justified "not affected" needs no write access to code.
- The triage and code harnesses each gain a condition, so a vulnerability
  work item reaches a verdict before either acts on it.
- A vulnerability reported as an ordinary issue, with no vulnerability type
  and no label, still goes to triage. Having triage hand it to the `cve`
  agent is not decided here.
- A fix takes two runs with a person between them by default, which is slower
  than one run and keeps a person on the verdict.
- The check that holds a change to the verdict's plan is built for the code
  agent only. The fix agent, which acts on review findings, needs the same
  check before it is enabled on these changes. When the change is on
  another forge than the work item, the fix agent's run also needs a way
  to read the record.
- The adopter keeps the release mapping, and something has to write the
  intake record. Opening one work item per maintained branch also stays
  with the adopter until a later decision.
- Where a site puts a security level on every vulnerability tracker, the
  agent does not run until the adopter lists a level as readable.
- One vulnerability can have many trackers, and several can map to the same
  branch and commit. Each is assessed on its own. Reusing a verdict for the
  same vulnerability, repository, branch and commit is left to a later
  decision.
- An assessment needs the toolchain its branch declares, and the maintained
  branches of one repository can declare different versions. The agent
  image carries one version of each toolchain, so a run on a branch that
  needs another can end without a verdict.
  [#9395](https://github.com/fullsend-ai/fullsend/pull/9395) proposes
  composing images into a sandbox.
