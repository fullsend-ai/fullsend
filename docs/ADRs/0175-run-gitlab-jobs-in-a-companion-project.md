---
title: "175. Run GitLab Fullsend jobs in a companion project"
status: Accepted
relates_to:
  - gitlab-implementation
  - security-threat-model
  - agent-infrastructure
topics:
  - gitlab
  - ci-cd
  - security
  - installation
---

# 175. Run GitLab Fullsend jobs in a companion project

Date: 2026-11-17

## Status

Accepted

Refines the scope of the `no_one_allowed` requirement in
[ADR 0131](0131-gitlab-dispatch-pipeline-inputs-no-owner-role.md): the
requirement applies to the project where the fullsend jobs run. Resolves
#9597.

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

Today, `fullsend repos install` puts the fullsend CI jobs, the protected
credentials, the schedules, and the poll state into the target project
([ADR 0067](0067-gitlab-cron-polling-event-dispatch.md),
[ADR 0125](0125-gitlab-hybrid-webhook-poller-dispatch.md)).
[ADR 0131](0131-gitlab-dispatch-pipeline-inputs-no-owner-role.md) makes the
target project set "Minimum role to use pipeline variables" to
`no_one_allowed`. Install does not deliver runnable jobs without this
setting.

Some adopters cannot use this setting. The first GitLab adopter project
uses pipeline variables at the Developer role for CI with other projects
(#9597). The [spike](../spikes/2026-11-17-gitlab-pipeline-variable-restriction.md)
tested a relaxed setting on GitLab.com:

- A pipeline caller can change `CI_API_V4_URL`. The fullsend identity pin
  then sends the job token and the Poller PAT to the host of the caller.
- A pipeline caller can set `BASH_ENV` as a file variable. Bash then runs
  the code of the caller in the fullsend job, with access to all job
  variables.

Thus, controls inside the job cannot make a relaxed setting safe. A job
script cannot stop code that bash runs before the first line of the
script.

This ADR aims to let a GitLab project with a relaxed pipeline variable
setting use fullsend, without exposure of the fullsend credentials to
pipeline callers in that project.

## Options

- **Controls in the job.** Pin the host and project from values that
  install writes. The spike shows that `BASH_ENV` and similar variables
  run code before these controls. Rejected.
- **Trusted roles only.** Accept `maintainer` or `owner` when only
  Maintainers can merge or push to `main`. This option is correct for
  some adopters, but not for an adopter where Developers can merge. It
  does not resolve #9597. A different ADR can decide it.
- **Companion project.** Run the fullsend jobs in a different project
  that sets `no_one_allowed`. The target project setting then has no
  effect on the fullsend jobs. Selected.

## Decision

We run the fullsend jobs for a GitLab target project in a companion
project to keep the fullsend credentials out of pipelines that target
project callers can change.

- **One companion project for each target project.** The adopter owns the
  companion project. Install creates it in a namespace that the adopter
  selects. This keeps the per repository model of
  [ADR 0044](0044-deprecate-per-org-installation-mode.md). It is not a
  shared configuration repository for an organization.
- **The companion project holds all fullsend parts.** These parts are the
  fullsend CI templates and scripts, the `.fullsend/` configuration, the
  protected credentials, the schedules, the poll state branches, and the
  webhook trigger token.
- **The companion project must meet the current gates.** It must set
  `no_one_allowed`, and its default branch must be protected. The
  ADR 0131 checks apply to the companion project without change.
- **The target project holds no fullsend jobs or variables.** The target
  project keeps the service account membership that the roles use today.
  Its project webhook sends events to the trigger of the companion
  project. The agents act on the target project through the role
  credentials, not through `CI_JOB_TOKEN`.
- **Inference trust binds the companion project.** The WIF provider
  condition uses the `project_path` of the companion project.
- **The in-project install stays available.** A target project that sets
  `no_one_allowed` can continue to use the current install. Install uses
  the companion project only when the manifest requests it.

Users who can change the companion project (its Maintainers and Owners)
are in the fullsend trust boundary. Target project Developers must not get
a role above Reporter in the companion project.

## Forge scope

- **GitLab:** This ADR applies.
- **GitHub:** Not affected. A workflow caller cannot set environment
  variables of a job, so GitHub has no setting like `no_one_allowed`.
- **Forgejo:** Out of scope. Examine this ADR again if Forgejo dispatch
  accepts caller variables.

## Consequences

- An adopter that needs pipeline variables at the Developer role can use
  fullsend, and the adopter does not change the project setting.
- Install no longer changes the `.gitlab-ci.yml` file of the target
  project in companion mode.
- Install, converge, and teardown must manage a second project, and the
  tests must cover two install modes.
- Agent run pipelines and logs show in the companion project. Target
  project users need Reporter access to the companion project to see them.
- An existing in-project install does not change. Movement to companion
  mode needs new WIF provider bindings for the companion project path.
