---
title: "156. Support both bot accounts and service accounts for Jira access"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - jira
  - credentials
  - authentication
---

# 156. Support both bot accounts and service accounts for Jira access

Date: 2026-11-02

## Status

Accepted

## Context

Fullsend's Jira integration (the `jira-poll` input driver of
[ADR 0063](0063-polling-based-work-discovery.md), status notifications, and
`fullsend issues`) authenticates as a single non-human Jira identity.
Atlassian Cloud offers two kinds of identity for that purpose, and they
authenticate differently.

A **bot account** is an ordinary Atlassian user account that a team creates
for automation. It has an email address, can log in like a person, holds
project roles like a person, and authenticates with a classic API token
presented at the site URL (`https://<site>.atlassian.net`). Its strengths
are that any Jira Cloud site supports it, no organization administrator is
involved beyond the usual user invitation, and existing integrations work
with it unchanged. Its weaknesses are that it occupies a user seat, it
depends on a password and two-factor setup that someone must own, and its
token is an unscoped user credential that organizations may limit in
lifetime or forbid for automation.

A person's own Atlassian account authenticates the same way and is
indistinguishable from a bot account to Fullsend. It is not a supported
choice: the poller treats the account it authenticates as as itself, so
that person's own comments and label changes never dispatch agents (see
the Jira guide's
[Use a dedicated account for the poller](../guides/user/jira-integration.md#use-a-dedicated-account-for-the-poller)).
This ADR uses "bot account" for a regular account created for Fullsend
alone.

A **service account** is an Atlassian-managed identity created by an
organization administrator (Atlassian Administration > Directory > Service
accounts). It has no password login. Its credentials are a scoped API token
or OAuth 2.0 client credentials, and Atlassian accepts them only through its
API gateway (`https://api.atlassian.com/ex/jira/<cloudId>/...`), not at the
site URL. Its strengths are that it is designed for integrations: no human
login to protect, least-privilege scopes, and administration separate from
people. Its weaknesses are that it requires organization-administrator
provisioning, that a site must have the feature enabled, and that callers
must know the site's cloud ID and reach a second host.

Today (verified at `6f45fa15b`) Fullsend supports only the first kind. The
Jira client requires an account email and an API token, sends every request
to the site URL with Basic authentication, and the sandbox profile for Jira
allows egress only to `*.atlassian.net`
([#9007](https://github.com/fullsend-ai/fullsend/issues/9007) records the
gap, with the file-level evidence in this ADR's pull request). A team that
has a service account can use it today only through an unsupported
workaround: pointing the site URL setting at the gateway, which Fullsend
does not detect, at the cost of wrong links to issues and no Jira access
from the sandbox.

This ADR aims to settle which kinds of Jira identity Fullsend commits to
supporting, so that implementation and documentation can proceed against a
stated requirement rather than the historical accident of what was built
first.

## Options

**Bot accounts only.** Keep the current contract. Simplest, and the path
every deployment is documented to use. Leaves teams whose organization
provisions service accounts for integrations, or that do not want a
user-seat identity with a human-style login for a bot, on the unsupported
workaround or out entirely.

**Service accounts only.** Move to the gateway and scoped credentials and
retire the bot-account path. Aligns with Atlassian's intended model for
integrations. Breaks every bot-account deployment (teams on the workaround
would simply drop it), and excludes sites where service accounts are not
enabled or where getting one provisioned is slow or impossible.

**Both.** Keep the bot-account path unchanged and add the service-account
path alongside it, selected per deployment. Costs a second authentication
code path, a second set of settings to document, and an extra sandbox egress
host.

## Decision

We support both kinds of identity, chosen per deployment, so that a team can
use whichever its organization provides. Both are dedicated non-human
identities; a person's account is not an option this ADR supports.

The bot-account path keeps working unchanged: existing settings, secrets,
and the `atlassian-cloud` sandbox credential need no migration. The
service-account path is added beside it: the client can route API calls
through the Atlassian gateway for the site's cloud ID, send a scoped API
token or an OAuth 2.0 access token as a bearer credential, and reach the
gateway and OAuth hosts from the sandbox. Links shown to humans continue to
use the site URL in both paths.

Fullsend does not infer which kind of identity it has been given. The two
token formats are indistinguishable and are accepted at different hosts, so
the deployment states the kind explicitly. The default is the bot-account
path, so an unchanged configuration keeps its current behaviour.

This decision is about which identities Fullsend accepts. How a deployment
obtains and rotates the credential, including the longer-term goal of
short-lived credentials minted centrally
([#2269](https://github.com/fullsend-ai/fullsend/issues/2269),
[#4885](https://github.com/fullsend-ai/fullsend/issues/4885)), is a separate
question that this ADR does not decide.

## Forge scope

Forge-neutral. The Jira identity is the same whether the poller runs from
GitHub Actions ([ADR 0063](0063-polling-based-work-discovery.md)) or GitLab
CI ([ADR 0067](0067-gitlab-cron-polling-event-dispatch.md)); only the
mechanism that delivers the secret to the job differs per forge, and this
ADR does not change it. Forgejo has no Jira poll driver today, so nothing
changes there.

## Consequences

- Teams can adopt Fullsend with whichever Jira identity their organization
  provisions, and documentation describes both as supported choices.
- Existing bot-account deployments are unaffected; the service-account path
  is opt-in per deployment.
- The Jira client gains a second authentication mode and a second host to
  reach, which the sandbox egress profile and the credential-isolation rules
  of [ADR 0017](0017-credential-isolation-for-sandboxed-agents.md) must
  cover.
- Every Fullsend component that calls Jira, including built-in agents'
  post-scripts in `fullsend-ai/agents`, must honour the selected mode before
  the service-account path is complete end to end.
- Implementation is tracked in
  [#9007](https://github.com/fullsend-ai/fullsend/issues/9007), not in this
  ADR.
