---
title: "156. Require a jira authorization entry when the jira-poll driver runs"
status: Accepted
relates_to:
  - security-threat-model
  - agent-architecture
topics:
  - authorization
  - jira
  - deprecation
---

# 156. Require a jira authorization entry when the jira-poll driver runs

Date: 2026-11-02

## Status

Accepted

Supersedes the opt-in rule of
[ADR 0153](0153-authorize-jira-poll-actors-without-project-administration.md);
the rest of that ADR stands. See Context.

## Context

Teams are starting to run Fullsend agents from Jira: someone comments
`/fs-code` on an epic, or adds a label, and an agent goes to work in the
repository. For that to happen, a Fullsend bot account has to watch the Jira
project and decide, for each comment or label, whether the person behind it
is allowed to start an agent. Two things have to be true for a team to adopt
this. The bot must not need more Jira permission than a Jira administrator
is willing to give a bot, and the team must know, and have chosen, how the
bot decides who is trusted. Until recently the first was false: the bot had
to be a project administrator just to look up who people were. Fullsend now
offers a way around that, but it is optional and nothing tells a team whether
they are using it. This ADR closes that gap by making the choice explicit,
so that no team ends up in the admin-only mode by accident, and no team's
trust rules change without them editing a file.

In Fullsend terms, the bot is the `jira-poll` input driver
([ADR 0063](0063-polling-based-work-discovery.md)), and its trust decision
is the authorization contract every dispatch path uses
([ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)).
The least-privilege principle of the
[security threat model](../problems/security-threat-model.md) says the
driver's Jira account should hold only the permission that decision needs.
Until recently it needed far more: the only way to learn who an actor was in
the project was the project role roster, which Jira Cloud serves only to
accounts with Administer Projects, so a bot that merely watched a project had
to be one of its administrators
([#8401](https://github.com/fullsend-ai/fullsend/issues/8401)).

[ADR 0153](0153-authorize-jira-poll-actors-without-project-administration.md)
fixed that for repositories that ask for it. A `jira` entry under
`authorization:` in `.fullsend/config.yaml` names the Jira groups and
accounts allowed to run agents, and lets Jira's own Edit Issues permission
vouch for anyone who adds a label, so the poller never needs the roster. To
avoid changing any existing setup, the entry is opt-in: a repository with no
entry keeps the old behaviour, every event resolved through the admin-only
roster. That leaves a gap. The absence of an entry is a silent choice. A
repository that sets up the poller and skips the authorization step runs in
the admin-only mode with nothing saying so, and learns of it from a `401` in
the poll log, by granting the poller a project role it should not hold, or
by never seeing a dispatch. "Not configured" and "project roles for every
event" are the same config file. The review of
[#8892](https://github.com/fullsend-ai/fullsend/pull/8892) raised this
([comment](https://github.com/fullsend-ai/fullsend/pull/8892#issuecomment-6496078895)),
and [#9004](https://github.com/fullsend-ai/fullsend/issues/9004) proposed
the decision recorded here.

In code, at `d7394b881b4e68aa78976ee82ffb226cfbcb42c6`,
`jiraPollAuthorization` (`internal/cli/poll.go`) leaves
`Authorization.Configured` false when config.yaml has no `jira` provider,
and `roleSourceFor` (`internal/jirapoll/auth.go`) then selects the project
role roster (`GET /rest/api/3/project/<KEY>/role`) as the role source for
every event.

This ADR aims to make a repository's Jira authorization source an explicit
choice whenever the `jira-poll` driver runs, without changing who may
trigger agents under any existing configuration.

## Options

- **Keep the provider opt-in.** No change. The admin-only default stays
  invisible, and the least-privilege gain of [ADR 0153](0153-authorize-jira-poll-actors-without-project-administration.md) reaches only
  repositories that happen to read the guide's authorization step.
- **Flip the default.** With no entry, label-added events would be
  authorized by Jira's Edit Issues permission (`labels: jira_editors`), as
  they are once an entry exists. No repository needs a config change, but in
  a project where Edit Issues is granted more widely than the Developers role
  this silently widens who can trigger agents, under a repository that did
  nothing.
- **Require the entry** (chosen). A missing entry fails loudly at poller
  start, and the fix is a two-line entry that reproduces today's behaviour
  exactly. Behaviour changes only for a repository that edits its config.

Within the chosen option, the deprecation warning is logged by the poller,
not reported by `fullsend lint`. At the SHA above, `runLint`
(`internal/cli/lint.go`) reads `config.yaml` and composes the harnesses; it
never sees the scheduled workflow or pipeline that starts the driver, so it
cannot tell whether a repository polls Jira. A lint finding would fire for
every repository with no `jira` entry, most of which never poll Jira, or
would have to guess from workflow file names. The poller knows for certain,
so its warning is right every time it appears.

## Decision

We require a `jira` entry under `authorization:` whenever the `jira-poll`
input driver runs, so that project-role authorization, and the Administer
Projects permission it needs, is a choice a repository writes down rather
than a default it falls into.

The requirement arrives in two steps, each in its own release:

1. **Warning**
   ([#9005](https://github.com/fullsend-ai/fullsend/issues/9005)). When the
   driver starts with no `jira` entry, the poller logs one warning before the
   first poll cycle. It says that running without an entry is deprecated and
   will become an error, that the current mode authorizes every event through
   project roles, and it prints the two-line entry below. The release notes
   announce the deprecation. Nothing else changes.
2. **Removal**
   ([#9006](https://github.com/fullsend-ai/fullsend/issues/9006)). The poller
   refuses to start without a `jira` entry, before any Jira call, with a
   message that names the requirement and prints the same two-line entry.
   This is a breaking change and is marked as one.

The migration for a repository that wants today's behaviour is the entry
[ADR 0153](0153-authorize-jira-poll-actors-without-project-administration.md) already defines for it:

```yaml
authorization:
  - provider: jira
    labels: project_roles
```

The meaning of an entry does not change. Lists, `labels:` values and roster
loading stay as [ADR 0153](0153-authorize-jira-poll-actors-without-project-administration.md) decided them, and the two-line entry resolves every
event through the project role roster exactly as no entry does today.

## Forge scope

This decision applies to the `jira-poll` input driver under both of its
targets, GitHub Actions (`--target-forge github`) and GitLab CI
(`--target-forge gitlab`), which build the poller's options through the same
function. GitHub, GitLab and Forgejo event authorization is unchanged:
`authorization:` stays optional for a repository that does not run the
driver, and the `owners_file` provider is untouched.

## Consequences

- Every Jira poller configuration states where its actor roles come from, so
  the admin-only path is visible in config review and in the guide's setup
  steps, instead of being the mode a repository is in by omission.
- A repository that keeps project roles adds two lines; one that never added
  an entry sees a warning for at least one release, then a start failure
  whose message contains the fix.
- The removal should follow a deprecation period. The proposal is at least
  three months after the first release whose notes announce the deprecation;
  the interval is settled on #9006 before the removal is scheduled.
- Jira poller setups that Fullsend generates, such as the `repos install`
  option proposed in
  [#8906](https://github.com/fullsend-ai/fullsend/issues/8906), must write a
  `jira` entry.
- The user guide, the config references, the authorization normative specs
  and `docs/architecture.md` describe the entry as deprecated-when-missing
  from the warning release and as required from the removal release. The
  deprecation is listed on the deprecations tracker
  [#7155](https://github.com/fullsend-ai/fullsend/issues/7155) until it is
  removed.
