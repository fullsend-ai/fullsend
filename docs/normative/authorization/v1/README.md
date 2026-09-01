# Authorization Contract v1

Normative rules governing which actors may trigger agent dispatch.

This document is the single living contract for authorization policy.
The historical decision and rationale are recorded in
[ADR 0054](../../../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md).
The [NormalizedEvent v1](../../normalized-event/v1/) specification defines
the `actor.role` field consumed by this contract.

## Role ordering

Fullsend uses a forge-neutral role hierarchy. Roles are listed from most
to least privileged:

```
admin > maintain > write > triage > read > none > external
```

| Role | Meaning |
|------|---------|
| `admin` | Full repository control |
| `maintain` | Repository settings without destructive admin |
| `write` | Push, label, comment, merge |
| `triage` | Label and moderate without push access |
| `read` | Read-only collaborator |
| `none` | Authenticated user without explicit repository permission |
| `external` | Actor outside the repository (fork PR author, drive-by commenter) |

### Forge-native permission mapping

| Forge | Native level | Fullsend role |
|-------|-------------|---------------|
| **GitHub** | `admin` | `admin` |
| | `maintain` | `maintain` |
| | `write` | `write` |
| | `triage` | `triage` |
| | `read` | `read` |
| | _(no collaborator entry)_ | `none` |
| | _(fork/external actor)_ | `external` |
| **GitLab** | Owner | `admin` |
| | Maintainer | `maintain` |
| | Developer | `write` |
| | Reporter | `triage` |
| | Guest | `read` |

On GitHub the mapping source is the collaborator permission API
(`GET /repos/{owner}/{repo}/collaborators/{username}/permission`), which
returns the user's **effective** role including inherited org grants
regardless of membership visibility. The `author_association` field is
**not** used because it does not correctly reflect private org membership
(see [Excluded fields](#excluded-fields)).

## Default thresholds

| Category | Minimum role | Rationale |
|----------|-------------|-----------|
| **Observation** (triage, review) | `triage` | Read-only analysis; lower barrier to reduce maintainer toil |
| **Mutation** (code, fix, retro slash command, prioritize) | `write` | Agents that push commits or alter state require push access |

A role satisfies a threshold when it is **at or above** the minimum in
the role ordering. For example, `admin` satisfies both `triage` and
`write` thresholds.

The dispatch implementation uses a parameterized
`has_repo_permission(username, min)` helper that encodes this comparison
(see [Enforcement point](#enforcement-point)).

## Fail-closed behavior

The authorization gate is **fail-closed**: when a role cannot be
determined, the actor is denied.

| Condition | Outcome |
|-----------|---------|
| Collaborator API returns an unrecognized `role_name` | Mapped to `none`; denied |
| Collaborator API returns an error or times out | Denied (function returns failure) |
| Custom repository roles (GitHub) | Mapped to `none`; denied until custom roles are handled platform-wide |
| `actor.role` is empty or missing | Event fails `NormalizedEvent` validation; never reaches dispatch |
| Username is empty | Denied |

## Exceptions

Certain transitions are authorized without requiring a `write` or
`triage` role from the acting user. Each exception is documented with its
rationale.

### Label application (GitHub)

When `transition.kind` is `label_changed` and `label.action` is `added`,
the event is authorized regardless of `actor.role`. GitHub's own
permission model requires at least `triage` access to apply a label, so
label application is an **implicit authorization gate**. Bot accounts
that apply labels as part of agent-to-agent handoff (e.g., adding
`ready-to-code` after triage completes) rely on this path because the
collaborator API often returns 404 for `[bot]` accounts even when the
GitHub App has write access via its installation token.

### Bot-submitted reviews (GitHub)

When `transition.kind` is `review_submitted` and `actor.kind` is `bot`,
the event is authorized regardless of `actor.role`. This allows the
review agent's bot identity to trigger downstream stages (e.g., the fix
agent) without a collaborator role lookup. The downstream harness CEL
trigger constrains which bot and review state are accepted.

### Lifecycle close (pull\_request\_target.closed)

The `pull_request_target.closed` event that triggers the retro stage is
intentionally ungated: any closer may trigger read-only lifecycle
accounting. The retro agent performs only observation work and does not
mutate repository state.

### Schedule and manual dispatch

When `source.system` is `schedule` or `manual`, the actor is the
configured service identity (GitHub App bot or workflow `GITHUB_ACTOR`).
Adapters set `actor.kind` to `bot` and `actor.role` to the effective
permission of that identity on the target repository (typically `write`
for installed apps). The standard authorization gate applies; the
platform does not default schedule or manual actors to `role: none`.

## Excluded fields

The following fields are **not** authorization evidence and must not be
used for dispatch gating:

| Field | Why excluded |
|-------|-------------|
| `author_association` | Does not reflect private org membership; an org admin with private membership gets `CONTRIBUTOR` instead of `MEMBER` ([github/gh-aw-mcpg#2862](https://github.com/github/gh-aw-mcpg/issues/2862)). Also reflects contribution history, not current authority. |
| Contribution history | Past contributions do not confer current repository permissions. A former maintainer whose access was revoked should not pass authorization. |
| `actor.is_entity_author` | Being the author of an issue or PR does not grant repository permissions. This field supports routing decisions in CEL triggers, not authorization. |

**Principle:** relationship and contribution-history fields are not
evidence of current authority. Authorization must be derived from the
forge's permission model at event time, not from cached or inferred
relationships.

## Enforcement point

Authorization is enforced as a **platform-level gate** inside
`fullsend dispatch`, after `NormalizedEvent` normalization and **before**
CEL trigger evaluation.

```
Forge event
  --> NormalizedEvent (adapter)
  --> Authorization gate (this contract)    <-- enforced here
  --> CEL trigger evaluation (harness routing)
  --> Execution
```

### CEL triggers: routing only

Harness `trigger` expressions express **routing**, not permission policy.
A CEL expression may **tighten** dispatch conditions (e.g., require a
specific label, restrict to non-fork PRs, filter by bot identity) but
may **never weaken** the platform authorization gate. An event that fails
authorization never reaches CEL evaluation.

This separation is enforced architecturally: `IsAuthorized()` runs
before `MatchHarnesses()` in the dispatch core. There is no mechanism
for a CEL expression to override or relax an authorization denial.

### Per-repo configurability

The authorization gate is a platform-level security boundary. Individual
repositories cannot disable it. Per-repo configuration (which stages are
enabled, which labels trigger automation) operates **within** the
authorization boundary. A repository can disable a stage entirely but
cannot make it available to unauthorized users.

A future per-repo configuration system that needs to customize
authorization rules (e.g., allowing `triage` for a mutation stage)
should extend the `has_repo_permission` helper's allowed permission
list, not bypass the gate.

## Versioning

This is a living normative document under
[ADR 0015](../../../ADRs/0015-normative-specifications-directory.md).
Breaking changes require `docs/normative/authorization/v2/`.

| Change | v1 impact |
|--------|-----------|
| **Breaking** (requires v2): remove a role from the hierarchy, raise a default threshold, remove a documented exception, change fail-closed to fail-open | Dispatch implementations must migrate |
| **Non-breaking** (allowed in v1): add a role, lower a default threshold, add a new exception, add forge mappings, clarify documentation | Existing dispatch behavior is preserved or relaxed |
