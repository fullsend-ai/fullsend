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
2. **Start.** It starts on a `/fs-cve` comment from a person, or when the
   `cve` label is added. Triage does not start on a work item labelled
   `cve`.
3. **Verdict.** The result is affected, not affected, fixed or under
   investigation, in the terms of CSAF VEX, and each piece of evidence says
   whether a tool observed it or the model judged it. Host scripts publish it
   as one comment on the work item, which a later run updates. It is a
   proposal for a person to review; fullsend does not publish VEX.
4. **Handoff.** An affected verdict records on the work item the
   vulnerability, the branch, the commit and the files its plan may change.
   The code agent works on a vulnerability only while that record is current.
   It opens its change against the assessed branch
   ([ADR 0053](0053-agent-driven-branch-targeting.md)), and a host check
   rejects a change outside those files. By default a person adds
   `ready-to-code`.
5. **One target per run.** A run assesses the branch its work item names,
   from a list the repository gives. Several maintained branches are several
   work items. How those are created is not decided here; #9266 proposes
   one.
6. **Scope.** Embargoed vulnerabilities are out of scope. Host scripts stop
   a run that cannot show its finding is public, before a sandbox exists.
   Whether a private tracker's text may be used for a public repository is
   also not decided here
   ([#2513](https://github.com/fullsend-ai/fullsend/issues/2513)); until it
   is, the agent by default does not read that text in that case.
7. **Adopter facts.** Maintained branches, vendor VEX sources, readable
   tracker security levels and label conventions are harness settings
   ([ADR 0162](0162-agent-entries-may-carry-harness-overrides.md)), not new
   `config.yaml` fields.

## Forge scope

- **GitHub:** issues are the work item, including one opened from a
  Dependabot alert.
- **GitLab:** covered as the repository, with Jira as the tracker. GitLab
  issues as the work item are not covered yet, because no adopter files
  vulnerability trackers there; the agent skips them with a reason.
- **Jira:** Cloud trackers reach the agent through the Jira poller
  ([ADR 0063](0063-polling-based-work-discovery.md)).
- **Forgejo:** fullsend has no Forgejo support yet; nothing to change.

## Consequences

- A repository gets vulnerability assessment by registering one agent, and a
  justified "not affected" needs no write access to code.
- The triage and code harnesses each gain a condition, so a labelled
  vulnerability reaches a verdict before either acts on it.
- A fix takes two runs with a person between them by default, which is slower
  than one run and keeps a person on the verdict.
- The check that holds a change to the verdict's plan covers the code agent
  only; the fix agent, which acts on review findings, needs the same check.
- Finding a tracker's repository and branch, and opening one work item per
  maintained branch, stay with the adopter until a later decision.
