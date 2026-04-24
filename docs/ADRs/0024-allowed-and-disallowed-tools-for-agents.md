---
title: "0024. Allowed and Disallowed Tools for Agents"
status: Proposed
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - tools
  - permissions
  - sandbox
---

# 0024. Allowed and Disallowed Tools for Agents

Date: 2026-04-21

## Status

Proposed

## Context

Agents are launched with `claude --agent <agent> --print
--dangerously-skip-permissions`, which bypasses all permission checks. Every
tool call succeeds regardless of intent. Tool restrictions exist only in
agent prose instructions — prompt-level compliance with no hard enforcement.

Each agent runs in its own openshell sandbox
([ADR 0020](0020-composable-single-responsibility-agents-with-individual-sandboxes.md))
with default-deny network and filesystem policies. The sandbox is the
primary security boundary: it enforces restrictions at the OS level
regardless of what the agent runtime does. Tool-level restrictions are
always bypassable — every sandbox requires writable paths (`/tmp`,
`/sandbox`) for Claude Code to function, so an agent can always write and
execute a script that performs a denied action. The sandbox cannot be
circumvented this way because it operates below the tool layer.

Because each agent gets its own sandbox, all mechanisms — frontmatter,
`.claude/settings.json`, CLI flags — are scoped to exactly one agent.
Restricting tool access serves **steering** (focusing the agent on
intended tools, saving tokens) not **security** (the sandbox handles that).

Claude Code provides several mechanisms for restricting tool calls. Their
behavior differs between `--agent` sessions and subagents:

**Effective in `--agent` sessions:**

| Mechanism | Parameterized? |
|-----------|---------------|
| `--dangerously-skip-permissions` CLI flag | N/A — auto-approves all tools |
| `permissionMode: bypassPermissions` | N/A — partial bypass; auto-approves some tools (e.g. Bash), denies others (e.g. Write) |
| `permissionMode: dontAsk` | N/A — auto-denies everything not in `permissions.allow` |
| `permissionMode: auto` | N/A — classifier model decides per-call |
| `permissions.allow` in settings.json | Yes — supplements permission mode |
| `permissions.deny` in settings.json | Yes — hard-blocks; enforced even with `--dangerously-skip-permissions` |

**Inert in `--agent` sessions — subagents only:**

| Mechanism | `--agent` session | Subagent |
|-----------|-------------------|----------|
| `tools` in frontmatter | No effect | Restricts to listed tools |
| `disallowedTools` in frontmatter | No effect | Removes listed tools |

`tools` and `disallowedTools` only take effect when the agent runs as a
subagent spawned via the Agent tool. In `--agent` sessions, `tools` neither
restricts nor grants tool access regardless of permission mode
([experiment](https://github.com/fullsend-ai/experiments/tree/main/tool-scoping)).

## Options

### Option A: `--dangerously-skip-permissions` + `permissions.deny`

All tools auto-approved. Deny specific patterns for steering.

| Concern | Mechanism | Behavior |
|---------|-----------|----------|
| Allow | `--dangerously-skip-permissions` | All tool calls auto-approved |
| Deny | `permissions.deny` | Matching patterns hard-blocked |

Simple to adopt — only requires adding deny rules. For example, denying
`Bash(gh *)` redirects the agent to the credential-isolated REST API
([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)). No
tool-level scoping — all tools are available.

### Option B: `bypassPermissions` + `permissions.allow` + `permissions.deny`

Drop `--dangerously-skip-permissions`. `bypassPermissions` auto-approves
some tools but not others — `permissions.allow` supplements it for tools
that require explicit approval (e.g. `Write(*)`).

| Concern | Mechanism | Behavior |
|---------|-----------|----------|
| Allow | `bypassPermissions` + `permissions.allow` | Some tools auto-approved; others need allow entries |
| Deny | `permissions.deny` | Matching patterns hard-blocked |

More control than Option A — tools that `bypassPermissions` doesn't
auto-approve are denied unless explicitly allowed. But which tools
`bypassPermissions` auto-approves is undocumented and may change across
Claude Code versions. Requires testing after upgrades.

### Option C: `dontAsk` + `permissions.allow` + `permissions.deny`

Drop `--dangerously-skip-permissions`. Only `permissions.allow` patterns
run; everything else is auto-denied.

| Concern | Mechanism | Behavior |
|---------|-----------|----------|
| Allow | `dontAsk` + `permissions.allow` | Only matching patterns auto-approved |
| Deny | `permissions.deny` | Matching patterns hard-blocked |

Most explicit — every allowed pattern is enumerated. But highest
maintenance burden: the allow list must be kept in sync per agent role.
A missing pattern causes silent failure — the agent simply stops.

### Option D: `auto` + `permissions.deny`

Drop `--dangerously-skip-permissions`. A classifier model decides per-call
whether the action is safe.

| Concern | Mechanism | Behavior |
|---------|-----------|----------|
| Allow | `auto` (classifier) | Safe actions auto-approved, unsafe denied |
| Deny | `permissions.deny` | Matching patterns hard-blocked |

No allow enumeration needed. But the classifier is probabilistic: the
same tool call may be approved or denied across runs, making agent
behavior non-reproducible.

### Option E: `--dangerously-skip-permissions` + sandbox only

All tools auto-approved. No tool-level deny rules. Rely entirely on
openshell sandbox policies.

| Concern | Mechanism | Behavior |
|---------|-----------|----------|
| Allow | `--dangerously-skip-permissions` | All tool calls auto-approved |
| Deny | Sandbox (openshell) | OS-level default-deny policies |

No maintenance burden. Security enforcement doesn't depend on Claude Code.
But no tool-level steering — failures only surface at the sandbox level,
where the agent must interpret OS-level errors.

## Decision

Use Option A: `--dangerously-skip-permissions` + `permissions.deny`.

**Security:** the sandbox is the sole enforcement layer. Tool-level
restrictions are always bypassable.

**Steering:** `permissions.deny` blocks specific command patterns to
redirect the agent toward intended alternatives — e.g. denying
`Bash(gh *)` forces use of the credential-isolated REST API
([ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)). This is
not a security control — it improves agent focus and reduces wasted tokens.

`tools` and `disallowedTools` in agent frontmatter have no effect in
`--agent` sessions.

## Consequences

- Security enforcement stays in the sandbox — the only layer the agent
  cannot bypass.
- `permissions.deny` serves as a steering tool, redirecting specific
  patterns to intended alternatives. Not a security control.
- Missing deny patterns do not cause silent failures — the agent uses the
  unintended tool path and the sandbox handles security.
- Sandbox policies must cover each agent's security requirements as the
  sole hard enforcement layer.
- `tools` and `disallowedTools` have no effect in `--agent` sessions;
  they only apply to subagents spawned via the Agent tool.
- Runtime-agnostic: the security model does not depend on Claude Code.
