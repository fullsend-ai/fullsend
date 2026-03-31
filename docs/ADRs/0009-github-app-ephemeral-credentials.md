---
title: "9. GitHub App for agent identity and ephemeral credentials"
status: Undecided
relates_to:
  - agent-architecture
  - security-threat-model
  - agent-infrastructure
topics:
  - security
  - credentials
  - identity
---

# 9. GitHub App for agent identity and ephemeral credentials

Date: 2026-03-27

## Status

Undecided — the credential delivery mechanism depends on
[ADR 0008](0008-reusable-workflows-for-credential-isolation.md)'s experimental
validation. The GitHub App approach itself is proven (experiment #67), but how
the private key reaches the entry point is not yet settled.

## Context

The architecture doc defines an [Agent Identity Provider](../architecture.md#agent-identity-provider)
responsible for issuing, scoping, rotating, and revoking the credentials agents
use to interact with the hosting forge. The core design principle is that trust
derives from repository permissions, not agent identity — but agents still need
credentials to authenticate.

[Experiment #67](../../experiments/67-claude-github-app-auth/README.md)
demonstrated that:

1. A GitHub App can be installed on an org and granted permissions (contents,
   pull requests, issues) on specific repos.
2. The App's private key can generate a short-lived JWT.
3. The JWT can generate installation access tokens scoped to individual repos.
4. Passing the scoped token as `GH_TOKEN` to Claude Code is sufficient for the
   agent to operate on that repo.
5. Tokens expire automatically (typically within 1 hour).

The experiment ran locally, not in a GitHub Actions environment. Validating the
full flow on GitHub Actions runners is open work.

Agents need forge credentials to do their work — creating branches, opening
PRs, posting comments, applying labels. The question is what kind of
credentials and how they are scoped.

## Options

### Option 1: GitHub App with ephemeral installation tokens

A fullsend-managed GitHub App is installed on the adopting org. The entry point
uses the App's private key to generate a short-lived installation token scoped
to the target repo. The agent receives only this scoped, ephemeral token.

**Pros:**
- Tokens are short-lived (1 hour) and repo-scoped — minimal blast radius if
  compromised.
- The private key never reaches the agent. The entry point generates the token
  and passes it to the sandbox; the agent sees only the scoped token.
- App permissions are explicit and auditable in the GitHub App settings.
- App identity is distinct from any human user — agent actions are clearly
  attributable.
- Proven in experiment #67.

**Cons:**
- Requires creating and managing a GitHub App per adopting org (or a shared
  public App with per-org installations).
- The private key must be stored somewhere the entry point can access it —
  this is the question ADR 0008 addresses.
- GitHub Apps have rate limits separate from user accounts.
- Not portable to GitLab/Forgejo as-is — those forges have different identity
  mechanisms (project access tokens, deploy tokens, OAuth applications).

### Option 2: Personal access tokens (PATs)

A bot user account with a long-lived PAT stored as a repository or org secret.

**Pros:**
- Simple setup. Well-understood.

**Cons:**
- Long-lived tokens — if compromised, they remain valid until manually revoked.
- Broad scope — PATs are scoped to the user's permissions, not to individual
  repos or operations.
- Tied to a user account that must be managed (seat licensing, credential
  rotation).
- No structural separation between the token and the agent — the agent has
  whatever the PAT allows.

### Option 3: GitHub Actions' automatic GITHUB_TOKEN

Use the `github.token` that GitHub Actions provides to every workflow run.

**Pros:**
- Zero configuration. Automatically scoped to the repo. Short-lived.

**Cons:**
- Scoped to the repo where the workflow runs, which for the reusable workflow
  pattern (ADR 0008) may be the `.fullsend` repo rather than the target repo.
- Permissions are limited to what the workflow declares in its `permissions:`
  block.
- Cannot act across repos — an agent working on repo A cannot open a PR in
  repo B.
- Cannot be further scoped (e.g., read-only for review agents, read-write for
  implementation agents).

### Option 4: Forge-agnostic approach via forgekit

Defer the identity mechanism to the forge abstraction layer
([ADR 0006](0006-forge-abstraction-layer.md)). `forgekit` handles credential
issuance per forge — GitHub App tokens on GitHub, project access tokens on
GitLab, etc.

**Pros:**
- Portable from day one.

**Cons:**
- Adds complexity before we have a second forge to support.
- The GitHub App mechanism is well-understood and proven; abstracting it now is
  premature.
- Can be refactored into `forgekit` later when GitLab/Forgejo support is
  added.

## Decision

_Undecided pending ADR 0008._

The GitHub App with ephemeral installation tokens (Option 1) is the strong
favorite. The token generation mechanism is proven (experiment #67). The open
question is how the App's private key is delivered to the entry point — if
ADR 0008's reusable workflow approach is validated, the private key lives as a
secret in the `.fullsend` repo and the reusable workflow passes the generated
token to the sandbox. If not, an alternative delivery mechanism is needed.

When a decision is reached, the credential flow would be:

1. The entry point (running inside the reusable workflow or equivalent) reads
   the GitHub App private key from secrets.
2. The entry point generates a short-lived JWT from the private key.
3. The entry point uses the JWT to request an installation token scoped to the
   target repo with minimum required permissions.
4. The scoped token is passed to the sandbox as `GH_TOKEN`.
5. The agent uses `GH_TOKEN` for all forge interactions (directly via `gh` or
   indirectly via tools).
6. The token expires automatically. No revocation needed under normal
   operation.

### Per-role permission scoping

Different agent roles need different permissions:

- **Implementation agents** — contents (read/write), pull requests
  (read/write), issues (read/write)
- **Review agents** — contents (read), pull requests (read/write for posting
  reviews), issues (read)
- **Triage agents** — issues (read/write), contents (read)

The GitHub App installation token API supports permission scoping at token
generation time. The entry point can generate tokens with only the permissions
the selected agent role needs, following the principle of least privilege.

### Forge portability

This ADR decides the GitHub-specific mechanism. When GitLab or Forgejo support
is added, equivalent mechanisms (GitLab project access tokens, Forgejo OAuth
applications) will be implemented in `forgekit`
([ADR 0006](0006-forge-abstraction-layer.md)). The entry point's credential
issuance step becomes a `forgekit` call that dispatches to the appropriate
forge backend.

## Consequences

_Consequences depend on ADR 0008's outcome._

If the GitHub App approach is adopted:

- **Agents never see long-lived secrets.** The private key stays in the
  `.fullsend` repo's secrets; agents receive only ephemeral, scoped tokens.
- **Credential blast radius is minimized.** A compromised token is
  repo-scoped and expires within an hour.
- **Agent actions are attributable.** The GitHub App identity is distinct from
  human users — every commit, PR, and comment by an agent is clearly
  identifiable as agent-produced.
- **Adopting orgs must create a GitHub App** (or install a shared one) and
  configure its permissions. This is a one-time setup cost.
- **Per-role scoping is possible but not required initially.** The entry point
  can start by generating tokens with the full App permission set and add
  per-role scoping as the agent architecture matures.
- **The `forgekit` library will eventually own credential issuance** for
  portability across forges, but the initial implementation is
  GitHub-specific.
