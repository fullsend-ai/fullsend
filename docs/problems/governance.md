# Governance

The rules about the rules. Who has the authority to define, modify, and enforce the policies that the agentic system operates under?

Governance is distinct from [intent representation](intent-representation.md). Intent answers "what changes are authorized?" Governance answers "who decides the authorization rules themselves?" Intent is the game; governance is who writes the rules of the game.

## Three concerns

### 1. System policy

The decisions that shape how the agentic system behaves across the org:

- **Tier definitions** — what change types exist, what authorization each tier requires, and who can approve at each level. (The tiers themselves are defined in [intent-representation.md](intent-representation.md); governance decides who has the authority to create or modify those tier definitions.)
- **Autonomy levels** — which repos are agent-autonomous, which are in shadow mode, which require full human review. What are the graduation criteria, and who evaluates them?
- **Agent permissions** — what authority each agent role has (merge, approve, comment, label). What are the boundaries, and who draws them?
- **Org-wide guardrails** — minimum standards that apply to all repos regardless of individual repo policy. Examples: all repos must have CODEOWNERS, all security-sensitive paths require human approval, all agent config changes require human approval.
- **Model and tool egress policy** — which model providers and tool protocols (e.g. MCP servers) agent runtimes may use, how spend and quotas are enforced, and who may approve changes to those allowlists. Central **protocol gateways** are one enforcement point; they should fall under the same change-control rigor as other agent infrastructure (see [landscape.md](../landscape.md#agent-gateway)).

### 2. Configuration security

Agent configuration is itself a security-critical attack surface. If someone can modify what agents are allowed to do, they can bypass all other controls.

**Hard rules:**
- Agent configuration must not be modifiable through the same channels agents operate on (PRs, issues, comments in target repos)
- Changes to agent policy require a higher level of approval than changes to code
- CODEOWNERS files are always human-owned (established in [autonomy-spectrum.md](autonomy-spectrum.md))
- No agent self-modification — agents cannot change their own configuration, permissions, or system prompts

**Open design questions:**
- Where does agent policy live? In the repos it governs (as CLAUDE.md, agent config files)? In a separate policy repo? In a central configuration system?
- If policy lives in a separate repo, how does it get applied to target repos? Push-based (policy repo pushes to targets) or pull-based (agents read from policy repo at runtime)?
- How do we audit changes to agent configuration? Git history helps if policy is in git, but we also need to detect unauthorized runtime changes.
- How do we handle the bootstrap problem — who sets up the initial agent configuration for a new repo, and how is that initial setup secured?

### 3. Decision process

How are governance decisions made, and how does the community participate?

**The spectrum of governance models:**

**Centralized** — a small team manages all agent policy. Repos can request autonomy, but the central team decides. Clear authority, but doesn't scale and may not reflect the needs of individual repo maintainers.

**Federated with guardrails** — org-wide minimum standards that repos cannot weaken. Individual repo maintainers set their own autonomy levels, CODEOWNERS boundaries, and agent configurations within those bounds. Scales better, but requires clear definition of what's "org-wide" vs. "repo-level."

**Progressive delegation** — start centralized while the system is new and trust is low. As patterns emerge and the system proves itself, delegate more control to repo maintainers. Pragmatic, but needs clear criteria for when and how delegation happens.

**Process questions:**
- How does someone propose a change to agent policy? A PR to a governance repo? An RFC with a review period?
- How do we balance speed of experimentation with community consensus? Early on, tight control is reasonable. As the system matures, broader participation is needed.
- Can individual repos opt out of agent autonomy entirely? Can they add stricter controls but not loosen org-wide ones?

## Accountability

- When an agent makes a bad decision, who is responsible? The person who configured the agent? The person who authored the policy? The person who approved the repo for autonomy?
- How do we trace an agent action back to the policy that authorized it? Every merge should be traceable: this PR was merged because the review sub-agents approved, operating under policy version X, with the change classified as Tier N, authorized by intent record Y.
- What's the escalation path when something goes wrong? Who gets paged? Who has authority to revoke agent autonomy in an emergency?
- Can autonomy be automatically revoked? If a bad merge is detected (e.g., production incident traced to an agent-merged PR), should the system automatically downgrade the repo to human-required review?

## Relationship to other problem areas

- **Intent representation** defines the tiers and authorization mechanisms. Governance defines who has authority to change those definitions.
- **Security threat model** identifies the threats. Governance defines the policies that mitigate them and who can modify those policies.
- **Autonomy spectrum** describes the graduation model. Governance defines who evaluates readiness and makes the graduation decision.
- **Agent architecture** defines the agent roles and permissions. Governance defines who assigns those permissions and under what constraints.

## Open questions

- Should governance itself be subject to the agentic system, or is it always human-operated? (Strong argument for always-human: agents governing themselves is a security risk.)
- How do we handle disagreements between repo maintainers and org-wide policy? What's the appeal process?
- What's the relationship between governance of the agentic system and governance of the target organization itself? Are they the same body, or separate?
- How do we prevent governance from becoming a bottleneck? If every policy change requires broad consensus, experimentation slows down.
- What's the minimum viable governance for getting started? We don't need the full model on day one — what's the smallest governance structure that lets us begin experimenting safely?
