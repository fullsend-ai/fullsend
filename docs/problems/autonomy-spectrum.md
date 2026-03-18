# Autonomy Spectrum

When should agents auto-merge, and when should they escalate to humans?

## The model: binary with CODEOWNERS

The autonomy model is **binary per-repo** with **CODEOWNERS as the escape hatch**:

- A repo is either "agent-autonomous" or it isn't
- Within an autonomous repo, specific file paths can require human approval via CODEOWNERS
- Repos graduate to autonomous mode once they meet readiness criteria

This avoids the complexity of fine-grained per-change-type rules while still protecting critical paths.

## CODEOWNERS as the control mechanism

CODEOWNERS is already a well-understood GitHub mechanism. In the agentic context, it means:

- **Agent-owned paths** — agents can review and merge without human approval
- **Human-owned paths** — changes here always require human approval, regardless of what the agent thinks

### Likely candidates for human-owned paths

- Security policies and RBAC configuration
- API surface (CRD schemas, REST endpoints, protobuf definitions)
- Deployment manifests and release configuration
- Agent configuration and system prompts (critical — agents must not modify their own guardrails)
- **CODEOWNERS files themselves** — always human-owned, never agent-modifiable. This is a hard rule, not a suggestion. If agents could modify CODEOWNERS, they could remove their own guardrails.
- Cross-repo interface contracts
- UX-facing components

### How CODEOWNERS interacts with agents

When a PR touches both agent-owned and human-owned paths:
- The human-owned paths block auto-merge
- The agent can still review the entire PR and provide feedback
- The human approves the guarded paths; the agent's review covers the rest

## Graduation criteria

What does a repo need before agents can be trusted to merge autonomously?

Possible criteria (all TBD — this needs experimentation):

- Minimum test coverage threshold (what number? per-package or overall?)
- CI pipeline includes integration/e2e tests, not just unit tests
- Linting and formatting enforced in CI
- CODEOWNERS file covers all security-sensitive paths
- History of successful agent-reviewed PRs (agents review but don't merge, humans validate the agent's judgment)
- No recent security incidents attributable to missed review

These criteria are all properties of the repo and the agents. But graduation also changes the role of the humans responsible for guarded paths — from active participants to approvers of agent output. Whether those humans can remain effective in that reduced role is an open question explored in [human-factors.md](human-factors.md).

## The probationary period

Before flipping a repo to full autonomy, run agents in "shadow mode":

1. Agents review PRs and produce recommendations
2. Humans still approve and merge
3. Compare agent decisions to human decisions over time
4. When confidence in alignment is high, graduate to autonomous mode

This builds trust incrementally and provides data on agent reliability.

## Alternative: per-decision escalation dimensions

The binary per-repo model is simple but coarse. An alternative (or supplement) is to evaluate each decision against a set of escalation dimensions at runtime. Instead of asking "is this repo autonomous?" the agent asks "is this particular action safe to proceed with?"

Example dimensions:

| Dimension | Low (agent proceeds) | High (escalate) |
|---|---|---|
| **Reversibility** | Undo in minutes, no data loss | Hours/days to roll back, or irreversible |
| **Blast radius** | One component, one agent | Multiple services, teams, or agents |
| **Visibility** | Internal only | Visible to users, customers, or third parties |

The rule is simple: if any dimension is high, escalate. No special cases for "strategic" vs "operational" — the dimensions apply uniformly.

### How this could supplement the binary model

Per-decision evaluation doesn't have to replace the per-repo binary model. It could layer on top of it:

- **Non-autonomous repos** stay non-autonomous — humans review everything regardless.
- **Autonomous repos** use dimensional checks as a runtime safety net. An agent operating in an autonomous repo would still escalate if it recognizes that a change is irreversible, cross-cutting, or user-visible — even if the files involved aren't in CODEOWNERS.

This addresses the gap where the binary model can miss risky changes that don't happen to touch a guarded path. CODEOWNERS catches known-sensitive files; dimensional checks catch emergent risk in the change itself.

### Trade-offs

- Requires agents to accurately self-assess dimensions in real time — a judgment call the binary model avoids entirely.
- The dimensions listed above are examples, not necessarily exhaustive. Different organizations might weight or define them differently.
- Could produce false escalations (agent is uncertain, so it escalates conservatively) or false confidence (agent misjudges blast radius). Shadow mode data would help calibrate.

## Open questions

- Who decides when a repo is ready for autonomy? (See [governance.md](governance.md))
- Can autonomy be revoked? Under what circumstances? Automatically if a bad merge is detected?
- How do we handle repos with poor test coverage today? Do agents help improve coverage as a prerequisite to their own autonomy?
- Is per-repo binary too coarse? Should there be sub-repo zones of autonomy beyond what CODEOWNERS provides?
- What about cross-repo changes? If a change spans an autonomous repo and a non-autonomous one, which rules apply?
- How do we handle the CODEOWNERS bootstrap — who decides the initial set of guarded paths?
- Should graduation criteria include human factors — not just "can agents be trusted here?" but "can the humans responsible for guarded paths remain effective once they stop implementing?" (See [human-factors.md](human-factors.md))
- Automation research suggests that [intermediate levels of automation preserve better human oversight than full automation](human-factors.md#the-out-of-the-loop-performance-problem). Does this argue for a third autonomy level between "human-approved" and "fully autonomous" — one where agents do most of the work but humans retain some implementation role?
- If autonomy can be revoked, should human engagement metrics (approval times, review depth, domain expert confidence) be among the triggers, alongside code quality metrics?
