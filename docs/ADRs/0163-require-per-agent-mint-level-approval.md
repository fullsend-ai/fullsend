---
title: "163. Require per-agent approval for mint roles and privilege levels"
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

# 163. Require per-agent approval for mint roles and privilege levels

Date: 2026-11-10

## Status

Accepted

## Context

[ADR 0058](0058-agent-registration.md) registers agent sources in per-repository
configuration. [ADR 0073](0073-named-mint-privilege-levels.md) lets an agent's
harness request a named mint level for each run-stage; when the harness omits
`privilege_levels`, it requests `write` for every stage. The `agents[]` entry
does not currently record which mint roles or levels the repository owner
approved for that agent. A registered source can therefore request the
authority available to its mint role without a separate per-agent grant.

An agent can have multiple effective harness variants, including inherited
configuration and overlays. Those variants may request different roles and
levels for the same run-stage, so grants must cover their union. [ADR 0112](0112-overlays-may-set-any-harness-field.md)
allows overlays to set harness fields, but the current overlay schema does not
carry `privilege_levels`; adding guarded overlay-level requests is a
prerequisite for implementing this decision. The related concern about
explicit permissions before granting action rights is tracked in
[#7548](https://github.com/fullsend-ai/fullsend/issues/7548).

This ADR aims to require explicit approval for every mint role an agent may
assume, and to define the privilege-level defaults that apply within approved
roles.

## Options

- Treat registering an agent source as approval of every level its role can
  request. This is concise but makes source registration an implicit grant.
- Store one flat ceiling per agent. This cannot express different grants for
  different run-stages or account for overlays that select different levels.
- Store an approved role list and approved level sets per agent and run-stage.
  This separates approval to assume a role from the authority requested within
  that role, while representing all effective harness variants. A missing role
  list denies all roles; approved roles use the built-in level profile unless
  an explicit level map overrides it.

## Decision

We require per-agent mint-role approval and define per-stage level approvals in
repository config so an agent can use only approved roles and levels.

Each `agents[]` entry may specify an `approved_roles` list. Only listed roles
are approved; a missing or empty list approves no role, so the agent cannot
launch. The list must cover every role requested by any effective harness
variant.

An entry may also have an `approved_privilege_levels` map whose values are
lists of levels and whose run-stage keys correspond to the harness's
`privilege_levels` map. This map applies to each approved role. If the map is
absent, the built-in profile applies: `[read]` for `pre_script` and `runtime`,
and `[write]` for `post_script`; `validation_loop` uses the `runtime` grant.
This is Ralph's compatibility default, scoped to roles that the repository
owner explicitly approved.

When `approved_privilege_levels` is present, its values take precedence over
the built-in profile. A stage-specific entry wins; `default` supplies the
grant for other stages when present. If there is no explicit `default`,
unlisted stages use `[read]`. An empty stage list denies every level for that
stage; `default: []` denies every level for stages without an explicit entry.
Removing the whole level map restores Ralph's built-in profile for the
explicitly approved roles.

For example:

```yaml
agents:
  - name: triage
    source: https://example.com/triage.yaml#sha256=...
    approved_roles: [triage]
    approved_privilege_levels:
      default: [read]
      post_script: [write] # shorthand for [read, write]
```

The `[write]` shorthand expands to `[read, write]`. Other custom levels must
be listed explicitly. An empty list denies every level for that stage. Unknown
roles, stage names, malformed maps or lists, and a requested level unavailable
to its requested role are validation errors and fail closed.

Overlay entries may set `role` and `privilege_levels` as guarded fields. Their
values compose in overlay order. For each effective harness variant, the
approval must include its requested role and every level it can request at
each run-stage. The validator and management commands use the union across all
possible overlay outcomes, even when a particular run would match only one
overlay. Level availability is checked for each variant's requested
role-and-level pair; a level required by one role does not have to be defined
by unrelated approved roles. The per-stage level map is a shared set of
approved level names, not a role-to-level map.

The agent add/install and update/upgrade flows resolve the agent's effective
harness variants, show the complete required role and level sets, and ask the
operator to approve all of them. Approval writes the roles to `approved_roles`
and writes an `approved_privilege_levels` map when needed to represent the
approved levels; if those levels match the built-in profile, the map may be
omitted. Declining any requested role or level aborts the install or upgrade
without changing the config. Partial approval is not supported. A standalone
`fullsend agent approve <name>` command performs the same review for an
existing agent.

Before launch, fullsend validates the selected agent's required role and
entire required level set against its config grant. If the role is not listed,
any required level is missing, or the agent's effective requirements cannot
be resolved, the agent is ineligible and does not launch; validation returns
an error naming the missing role or stage and levels. Manually adding an agent
without an approved role has the same result. Harness requests continue to
select the actual level sent to the mint; this approval check does not expand
the harness request or change the mint role's permissions.

The config values are the grants. A harness's `role` and `privilege_levels`
declarations are requests, not approvals. For unmanaged repositories, grants
live in `.fullsend/config.yaml`. For repositories managed through `repos.yaml`,
the manifest's `config.agents[]` entry is authoritative and the generated
`.fullsend/config.yaml` carries those values ([ADR 0122](0122-declarative-repo-configuration.md)).
`fullsend agent approve <name>` must persist managed grants in the owning
manifest, not only in the generated file. The CLI prompt is the supported way
to create grants, but it is not a separate signed approval record; a config
change that adds roles or levels is itself an authorization change and must
remain under the repository's existing policy for protecting agent
configuration. This ADR does not add an out-of-band or cryptographic approval
record.

## Forge scope

Forge-neutral for GitHub, GitLab, and Forgejo: the same per-agent config check
applies before token acquisition. Each forge's mint integration remains
responsible for enforcing the requested role and level; a role that does not
serve a requested level fails closed at minting. No forge receives an approval
bypass.

## Consequences

- Missing `approved_roles` denies every role; an approved role with no level
  map uses Ralph's profile, including `post_script` write, so deleting the
  entire map deliberately restores that profile.
- In an explicit level map, omitted stages default to `[read]`; `{}` is
  equivalent to `default: [read]`, and removing a stage-specific empty list
  grants that default, while `default: []` denies unlisted stages.
- Agent add/install, update/upgrade, and `fullsend agent approve <name>` show
  the full role and level request and require all-or-nothing approval; managed
  repositories keep these grants in their `repos.yaml` source of truth.
- Harnesses that omit `privilege_levels` still request `write` at every stage
  per ADR 0073, and custom-role owners remain responsible for making `read`
  mean the access they intend.
- The config entry is the authority record and must remain protected by
  repository policy; implementation of the management commands, overlay
  composition, validation, managed `repos.yaml` support, and config reference
  is tracked separately from this ADR.
