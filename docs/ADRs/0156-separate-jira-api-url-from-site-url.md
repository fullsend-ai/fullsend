---
title: "156. Separate the Jira API URL from the site URL"
status: Accepted
relates_to:
  - agent-infrastructure
topics:
  - jira
  - credentials
  - authentication
---

# 156. Separate the Jira API URL from the site URL

Date: 2026-11-02

## Status

Accepted

## Context

Fullsend's Jira integration (the `jira-poll` input driver of
[ADR 0063](0063-polling-based-work-discovery.md), status notifications, and
`fullsend issues`) authenticates as a single non-human Jira identity.
Atlassian Cloud offers two kinds of identity for that purpose, and Atlassian
accepts their credentials at different hosts.

A **bot account** is an ordinary Atlassian user account that a team creates
for automation. It has an email address, can log in like a person, holds
project roles like a person, and authenticates with a classic API token.
Atlassian documents that token for use at the site URL
(`https://<site>.atlassian.net`). Its strengths are that any Jira Cloud site
supports it and no organization administrator is involved beyond the usual
user invitation. Its weaknesses are that it occupies a user seat, it depends
on a password and two-factor setup that someone must own, and its token is an
unscoped user credential that organizations may limit in lifetime or forbid
for automation.

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
accounts). It has no password login. Its credential is a scoped API token
(or OAuth 2.0 client credentials, which this ADR does not cover), and
Atlassian accepts it only through its API gateway
(`https://api.atlassian.com/ex/jira/<cloudId>/...`), never at the site URL.
Its strengths are that it is designed for integrations: no human login to
protect, least-privilege scopes, and administration separate from people.
Its weaknesses are that it requires organization-administrator provisioning
and that a site must have the feature enabled.

The two tokens look alike and are used the same way, as a password
alongside the account's email. What differs is the host. The gateway accepts
both kinds: Atlassian documents it for scoped and service-account tokens,
and a classic bot token has been observed to work there as well, though
Atlassian's documentation does not promise it. The site URL accepts only the
bot token.

Today (verified at `6f45fa15b`) Fullsend has one URL setting, the site URL,
and uses it for three jobs: every API call, the links to issues that it
writes for humans, and the sandbox egress rule for Jira. It therefore
supports only bot accounts
([#9007](https://github.com/fullsend-ai/fullsend/issues/9007) records the
gap, with the file-level evidence in this ADR's pull request). A team that
has a service account can use it today only by pointing the site URL setting
at the gateway, which works for API calls but produces wrong links and
leaves the sandbox without Jira access.

Fullsend supports Jira Cloud only; Jira Data Center is tracked as future
work. Atlassian also operates cloud variants with their own hosts (a
government cloud with a different gateway, and an isolated cloud with a
per-customer domain), which Fullsend has not been tested against.

## Options

**Explicit account kind.** Keep the site URL as the only URL and add a
setting that names the kind of account, from which Fullsend picks the host.
Simple for the user, who knows what they were given. Costs a second
authentication code path, requires the user to answer a question that the
credential itself could answer, and hard-codes the gateway host for the
service-account case.

**API URL defaulting to the site URL.** Add an API URL setting beside the
site URL, used for API calls, defaulting to the site URL. Existing
deployments are untouched. A service-account deployment sets the API URL to
the gateway address, which Atlassian's own service-account instructions
hand them. Fullsend never needs to know the account kind. Costs the user
one copied URL, including the cloud ID, and leaves the default on the only
host that rejects service accounts.

**API URL defaulting to the gateway.** The same two settings, but the
default API URL is the gateway address for the site, built from the site's
cloud ID, which Fullsend discovers from the site URL. Both account kinds
work with no extra setting; the override remains for cloud variants with
other hosts, or to pin the site URL. Costs a change of host for every
existing deployment on upgrade, resting on observed rather than documented
behaviour for bot tokens, and a discovery call at startup.

## Decision

We separate the two URLs. The site URL stays the one required setting and
keeps its human-facing jobs: links to issues and discovery of the site's
cloud ID. API calls go to a separate API URL that defaults to the Atlassian
gateway address for that site and can be overridden. The credential is the
account email and an API token in both cases; Fullsend does not need to
know, and does not ask, which kind of account it holds.

When the API URL is not set and cloud-ID discovery fails, Fullsend reports
the failure and tells the operator to set the API URL explicitly, rather
than falling back to the site URL, because a silent fallback would turn a
service-account rejection into an unexplained failure. Fullsend resolves the
API URL once and hands the resolved value to scripts it runs on behalf of
agents, so that nothing downstream repeats the discovery.

This changes the host that existing bot-account deployments talk to. It
lands unless a maintainer objects on the ADR's pull request; the escape
hatch is to set the API URL to the site URL, which restores today's
behaviour exactly.

OAuth 2.0 client credentials for service accounts are a different way of
authenticating, not a different host, and are out of scope. How a
deployment obtains and rotates the credential, including the longer-term
goal of short-lived credentials minted centrally
([#2269](https://github.com/fullsend-ai/fullsend/issues/2269),
[#4885](https://github.com/fullsend-ai/fullsend/issues/4885)), is also a
separate question.

## Forge scope

Forge-neutral. The Jira identity and URLs are the same whether the poller
runs from GitHub Actions ([ADR 0063](0063-polling-based-work-discovery.md))
or GitLab CI ([ADR 0067](0067-gitlab-cron-polling-event-dispatch.md)); only
the mechanism that delivers the secret to the job differs per forge, and
this ADR does not change it. Forgejo has no Jira poll driver today, so
nothing changes there.

## Consequences

- Teams can adopt Fullsend with whichever Jira identity their organization
  provisions, bot account or service account, with the same settings.
- Existing deployments switch to the gateway host on upgrade; bot tokens
  there rest on observed behaviour, and the API URL override is the
  documented way back to the site URL if that ever changes.
- The sandbox egress profile for Jira must admit the gateway host as well as
  the site, within the credential-isolation rules of
  [ADR 0017](0017-credential-isolation-for-sandboxed-agents.md).
- Every Fullsend component that calls Jira, including built-in agents'
  post-scripts in `fullsend-ai/agents`, must call the resolved API URL
  rather than the site URL before service accounts work end to end.
- Atlassian's government and isolated cloud variants are reachable through
  the override but remain untested and unsupported until someone can try
  them.
- Implementation is tracked in
  [#9007](https://github.com/fullsend-ai/fullsend/issues/9007), not in this
  ADR.
