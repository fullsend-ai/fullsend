---
title: "175. Allow an opt-in to skip the GitLab no_one_allowed pipeline-variable check"
status: Accepted
relates_to:
  - gitlab-implementation
  - security-threat-model
topics:
  - gitlab
  - forge
  - ci-cd
  - security
---

# 175. Allow an opt-in to skip the GitLab no_one_allowed pipeline-variable check

Date: 2026-11-17

Amends: [ADR 0131](0131-gitlab-dispatch-pipeline-inputs-no-owner-role.md)

## Status

Accepted

## Context

Amends, but does not supersede,
[ADR 0131](0131-gitlab-dispatch-pipeline-inputs-no-owner-role.md):
`no_one_allowed` stays the default and the recommendation.

ADR 0131 requires GitLab's `ci_pipeline_variables_minimum_override_role`
to be `no_one_allowed` before fullsend delivers runnable typed jobs. The
check runs in install, converge, the trigger dispatch gate, and the CLI
activation path. Some adopters cannot apply that setting. Their teams
queue pipelines manually with pipeline variables every day, so the
setting blocks their own work.

Relaxing the setting has a real cost. This is GitLab's problem, not
fullsend's: every job in the project takes pipeline variables from whoever
starts it, whatever the job does. A caller can point `CI_API_V4_URL` at a
host they control. The job identity pin then gives away the job token, the
Poller PAT, and a role credential. A `BASH_ENV` file-type variable can also
run the caller's code inside the job before fullsend's script starts. The
warning must say this plainly.

This ADR aims to let adopters who cannot use `no_one_allowed` install
fullsend anyway, through an explicit opt-in that fullsend saves and warns
about.

## Options

- **Role threshold** (accept `owner`, `maintainer`, or `developer` as
  the minimum acceptable role). Rejected: the risk does not depend on the
  role alone, so a threshold adds settings without adding safety.
- **Companion project** (run fullsend from a separate project that keeps
  `no_one_allowed`). Rejected for now: sub-issues 9632–9635 were closed in
  favor of an opt-in.
- **External credential gate** (the job holds no long-lived credential; a
  service checks GitLab-signed claims and hands one out). Deferred to its
  own ADR; too large to bundle with the opt-in.

## Decision

We add an explicit opt-in to let blocked adopters install fullsend without
weakening the default for anyone else. The exact CLI shape is an
implementation detail tracked in #9597.

The opt-in is saved, so every later check accepts it instead of refusing
again. With the opt-in, fullsend accepts a setting weaker than
`no_one_allowed` and warns, in plain words, what a relaxed setting exposes.
Without the opt-in, nothing changes from ADR 0131. `no_one_allowed` stays
the default and the documented recommendation.

## Forge scope

GitLab only. GitHub needs no equivalent because no one running a workflow
can override `GITHUB_API_URL` or the job token (see ADR 0131's context).
Forgejo has no pipeline-variable override mechanism to restrict.

## Consequences

- Adopters whose workflow depends on pipeline variables can install and
  converge without changing their GitLab setting.
- Anyone at the relaxed role who can run a pipeline on the default branch
  can read the Poller and role credentials. The warning and the docs must
  say so, so an adopter's security team knows what they accept.
- The health check for this setting (#9466) must treat the opt-in as
  expected, not as drift.
- A future ADR may make relaxed mode safe through an external credential
  gate. This ADR does not depend on it.
