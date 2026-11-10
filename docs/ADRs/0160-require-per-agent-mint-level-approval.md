---
title: "160. Require per-agent approval for mint privilege levels"
status: Accepted
relates_to:
  - agent-architecture
  - governance
  - security-threat-model
topics:
  - agent-installation
  - harness
  - least-privilege
  - mint
---

# 160. Require per-agent approval for mint privilege levels

Date: 2026-11-10

## Status

Accepted

## Context

[ADR 0058](0058-agent-registration.md) registers agent sources in per-repository
configuration. [ADR 0073](0073-named-mint-privilege-levels.md) lets an agent's
harness request a named mint level for each run-stage; when the harness omits
`privilege_levels`, it requests `write` for every stage. The `agents[]` entry
does not currently record which levels the repository owner approved for that
agent. A registered source can therefore request the authority available to
its mint role without a separate per-agent grant.

An agent can have multiple effective harness variants, including inherited
configuration and overlays. Those variants may request different levels for
the same run-stage, so a grant must cover their union. [ADR 0112](0112-overlays-may-set-any-harness-field.md)
allows overlays to set harness fields, but the current overlay schema does not
carry `privilege_levels`; adding guarded overlay-level requests is a
prerequisite for implementing this decision. The related concern about
explicit permissions before granting action rights is tracked in
[#7548](https://github.com/fullsend-ai/fullsend/issues/7548).

This ADR aims to make each config-registered agent's maximum mint authority
explicit and fail closed when any effective harness variant requests a level
outside its effective grant. The built-in profile below is a deliberate
compatibility exception: an omitted grant resolves to that profile rather than
being denied.

## Options

- Treat registering an agent source as approval of every level its role can
  request. This is concise but makes source registration an implicit grant.
- Store one flat ceiling per agent. This cannot express different grants for
  different run-stages or account for overlays that select different levels.
- Store approved level sets per agent and run-stage. This makes the grant
  explicit while representing all effective harness variants. Missing stages
  could deny every level, which provides the strongest fail-closed behavior
  but requires existing entries to be migrated. We instead retain a built-in
  compatibility profile for omitted grants, as described below.

## Decision

We adopt per-agent, per-stage mint-level approvals in repository config so
agent installation records the maximum authority each harness may request.

Each `agents[]` entry may have an `approved_privilege_levels` map with the same
run-stage keys as the harness's `privilege_levels` map. Values are lists of
levels. The built-in profile is `[read]` for `pre_script` and `runtime`, and
`[write]` for `post_script`; `validation_loop` uses the `runtime` grant.
Omitting `approved_privilege_levels` applies this profile. In an explicit map,
a stage-specific entry wins; `default` supplies the grant for other stages
when present, otherwise the built-in profile applies to each unlisted stage.
This fallback is an intentional compatibility trade-off, not a record of
operator consent: it implicitly grants `post_script` write. Removing an
explicit stage entry therefore restores that stage's built-in grant; an empty
list is required to deny every level for that stage.

For example:

```yaml
agents:
  - name: triage
    source: https://example.com/triage.yaml#sha256=...
    approved_privilege_levels:
      default: [read]
      post_script: [write] # shorthand for [read, write]
```

The `[write]` shorthand expands to `[read, write]`. Other custom levels must
be listed explicitly. An empty list denies every level for that stage. Unknown
stage names, malformed maps or lists, and levels unavailable to the agent's
role are validation errors and fail closed.

Overlay entries may set `privilege_levels` as a guarded field. Their maps merge
in overlay order: a later overlay replaces earlier values for the stages it
names and inherits values for other stages. For each stage, the approval must
contain every level any effective harness variant can request after
inheritance and overlay composition. The validator and management commands
use the union across all possible overlay outcomes, even when a particular
run would match only one overlay.

The agent add/install and update/upgrade flows resolve the agent's effective
harness variants, show the complete required level set, and ask the operator
to approve all of it. Approval writes the complete grant to
`approved_privilege_levels`; declining aborts the install or upgrade without
changing the config. Partial approval is not supported. A standalone
`fullsend agent approve <name>` command performs the same review and writes
the grant for an existing agent.

Before launch, fullsend validates the selected agent's entire required level
set against its config grant. If any required level is missing or the
agent's effective requirements cannot be resolved, the agent is ineligible
and does not launch; validation returns an error naming the stage and missing
levels. Manually adding an agent without sufficient grants has the same
result. Harness requests continue to select the actual level sent to the
mint; this approval check does not expand the harness request or change the
mint role's permissions.

The config value is the grant. A harness's `privilege_levels` declaration is a
request, not an approval. For unmanaged repositories, the grant lives in
`.fullsend/config.yaml`. For repositories managed through `repos.yaml`, the
manifest's `config.agents[]` entry is authoritative and the generated
`.fullsend/config.yaml` carries that value ([ADR 0122](0122-declarative-repo-configuration.md)).
`fullsend agent approve <name>` must persist managed grants in the owning
manifest, not only in the generated file. The CLI prompt is the supported way
to create the grant, but it is not a separate signed approval record; a config
change that adds levels is itself an authorization change and must remain
under the repository's existing policy for protecting agent configuration.
This ADR does not add an out-of-band or cryptographic approval record.

## Forge scope

Forge-neutral for GitHub, GitLab, and Forgejo: the same per-agent config check
applies before token acquisition. Each forge's mint integration remains
responsible for enforcing the requested role and level; a role that does not
serve a requested level fails closed at minting. No forge receives an approval
bypass.

## Consequences

- Operators see the full privilege request before adding or updating an agent,
  and the repository config records the grant by stage.
- Entries without an approval map use the built-in profile, including its
  implicit `post_script` write grant. This deliberately preserves Ralph's
  proposed default, but does not satisfy #7548's no-write-by-default criterion
  for agents registered by editing config directly. Deleting an explicit
  `post_script: []` also restores the built-in write grant. A repository that
  requires every write grant to be recorded must set `post_script: []` or
  another explicit grant; changing the fallback to deny-by-default would
  require a separate migration decision.
- The supported add/install and update/upgrade commands show the effective
  requested levels and require full approval before writing the grant. That
  supplies deliberate consent for those CLI flows; it does not turn an omitted
  config grant into evidence of approval.
- Harnesses that omit `privilege_levels` still request `write` at every stage
  per ADR 0073, so they need additional grants beyond the built-in profile
  before they can launch.
- The default approves the level named `read`; it does not inspect a custom
  role's underlying permissions. Custom-role owners are responsible for
  defining levels that match their intended access.
- Overlay variants can require approval of a higher level even when that
  variant is not selected for a particular run, and managed repositories keep
  grants in their `repos.yaml` source of truth.
- Repository protections remain essential because the config entry is the
  authority record; the CLI does not provide a separate approval signature.
- Implementation requires coordinated changes to agent management commands,
  overlay composition, harness/config validation, managed `repos.yaml`
  configuration, and the config reference; those changes are tracked
  separately from this ADR.
