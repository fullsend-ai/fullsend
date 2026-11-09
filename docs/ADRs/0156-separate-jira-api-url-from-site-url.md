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
accepts their credentials at different hosts. The
[Jira integration guide](../guides/user/jira-integration.md) covers setting
up either kind.

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
(or OAuth 2.0 client credentials), and
Atlassian accepts it only through its API gateway
(`https://api.atlassian.com/ex/jira/<cloudId>/...`), never at the site URL.
Its strengths are that it is designed for integrations: no human login to
protect, least-privilege scopes, and administration separate from people.
Its weaknesses are that it requires organization-administrator provisioning
and that a site must have the feature enabled.

The two tokens look alike. A bot account's token is sent with the
account's email, and a service account's token is sent on its own as a
Bearer token. The bigger difference is the host. The gateway accepts
both kinds: Atlassian documents it for scoped and service-account tokens,
and a classic bot token has been observed to work there as well, though
Atlassian's documentation does not promise it. The site URL accepts only the
bot token.

Before this decision (verified at `6f45fa15b`), Fullsend had one URL
setting, the site URL, and used it for three jobs: every API call, the links
to issues that it writes for humans, and the sandbox egress rule for Jira.
It therefore supported only bot accounts
([#9007](https://github.com/fullsend-ai/fullsend/issues/9007) recorded the
gap, with the file-level evidence in this ADR's pull request). A team that
had a service account could use it only by pointing the site URL setting at
the gateway, which worked for API calls but produced wrong links and left
the sandbox without Jira access.

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
cloud ID. API calls go through the Atlassian API gateway. A separate API URL
setting names the gateway host; it defaults to `https://api.atlassian.com`
and can be overridden. Fullsend always calls
`{API URL}/ex/jira/{cloudId}/...`, so an override must serve the same path
layout. Fullsend does not ask which kind of account it holds: a token with
the account's email is sent with Basic auth, as a bot account's is, and a
token without an email is sent as a Bearer token, as a service account's is.

Fullsend always discovers the cloud ID from the site URL. If discovery
fails, the run stops and reports the failure rather than falling back to the
site URL, because a silent fallback would turn a service-account rejection
into an unexplained failure. Fullsend resolves the gateway address once and
hands only the resolved `{API URL}/ex/jira/{cloudId}` to scripts it runs on
behalf of agents, so that nothing downstream repeats the discovery.

Every deployment, bot accounts included, talks to the gateway, and there is
no setting that sends API calls to the site URL: one would keep unscoped
classic tokens on the site host. Upgrading therefore requires a credential
that works through the gateway. A deployment pinned to an earlier Fullsend
version keeps its current behaviour until it switches. A classic bot token
working through the gateway rests on observed behaviour that Atlassian does
not promise, and we accept that risk.

A service account can also authenticate with OAuth 2.0 client credentials
instead of a token. Fullsend exchanges them at Atlassian's token endpoint for
a short-lived Bearer access token, refreshes it before it expires, and sends
it to the same API URL. This is another way of authenticating, not another
host, so it needs no further URL or account-kind setting. A deployment
supplies either a token or client credentials, never both
([#9176](https://github.com/fullsend-ai/fullsend/issues/9176)). How a
deployment obtains and rotates the credential, including the longer-term
goal of short-lived credentials minted centrally
([#2269](https://github.com/fullsend-ai/fullsend/issues/2269),
[#4885](https://github.com/fullsend-ai/fullsend/issues/4885)), is a separate
question.

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
- Existing deployments switch to the gateway host on upgrade and need a
  credential that works there. A bot token there rests on observed
  behaviour, and there is no supported way back to the site URL if that
  ever changes.
- The sandbox egress profile for Jira must admit the gateway host as well as
  the site, within the credential-isolation rules of
  [ADR 0017](0017-credential-isolation-for-sandboxed-agents.md).
- Fullsend passes the resolved gateway address to agent runs
  ([#9218](https://github.com/fullsend-ai/fullsend/issues/9218)). Every
  component that calls Jira, including built-in agents' scripts in
  `fullsend-ai/agents`, must call it rather than the site URL before service
  accounts work end to end.
- Atlassian's government and isolated cloud variants may be reachable
  through the API URL override, if their gateways serve the same path
  layout, but remain untested and unsupported until someone can try them.
