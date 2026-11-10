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
the same run-stage, so a grant must cover their union. The related concern
about explicit permissions before granting action rights is tracked in
[#7548](https://github.com/fullsend-ai/fullsend/issues/7548).

This ADR aims to make each config-registered agent's maximum mint authority
explicit and fail closed when any effective harness variant requests a level
the repository has not approved.

## Options

- Treat registering an agent source as approval of every level its role can
  request. This is concise but makes source registration an implicit grant.
- Store one flat ceiling per agent. This cannot express different grants for
  different run-stages or account for overlays that select different levels.
- Store approved level sets per agent and run-stage. This makes the grant
  explicit while representing all effective harness variants; we choose this
  option.

## Decision

We adopt per-agent, per-stage mint-level approvals in repository config so
agent installation records the maximum authority each harness may request.

Each `agents[]` entry may have an `approved_privilege_levels` map with the same
run-stage keys as the harness's `privilege_levels` map. Values are lists of
levels. The `default` key supplies the grant for stages without an explicit
entry; a stage-specific entry replaces that default. If
`approved_privilege_levels` is omitted, it behaves as `default: [read]`. If
the map is present without a `default`, unlisted stages still default to
`[read]`. `validation_loop` uses the `runtime` grant, as it does for harness
level selection.

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
be listed explicitly. For each run-stage, the approved set must contain every
level requested by any effective harness variant registered for the agent,
including levels selected through inheritance or overlays. The check uses the
union across variants, even when a particular run would match only one
overlay.

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
request, not an approval. The CLI prompt is the supported way to create the
grant, but it is not a separate signed approval record; a config change that
adds levels is itself an authorization change and must remain under the
repository's existing policy for protecting agent configuration. This ADR
does not add an out-of-band or cryptographic approval record.

## Forge scope

Forge-neutral for GitHub, GitLab, and Forgejo: the same per-agent config check
applies before token acquisition. Each forge's mint integration remains
responsible for enforcing the requested role and level; a role that does not
serve a requested level fails closed at minting. No forge receives an approval
bypass.

## Consequences

- Operators see the full privilege request before adding or updating an agent,
  and the repository config records the grant by stage.
- Existing entries without an approval map authorize only `read`; agents that
  require higher levels must be approved with `fullsend agent approve` before
  they can launch.
- Overlay variants can require approval of a higher level even when that
  variant is not selected for a particular run.
- Repository protections remain essential because the config entry is the
  authority record; the CLI does not provide a separate approval signature.
- Implementation requires coordinated changes to agent management commands,
  harness/config validation, and the config reference; those changes are
  tracked separately from this ADR.
