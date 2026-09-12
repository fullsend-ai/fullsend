# Governance

The rules about the rules. Who has the authority to define, modify, and enforce the policies that the agentic system operates under?

Governance is distinct from [intent representation](intent-representation.md). Intent answers "what changes are authorized?" Governance answers "who decides the authorization rules themselves?" Intent is the game; governance is who writes the rules of the game.

## Three concerns

### 1. System policy

The decisions that shape how the agentic system behaves across the org:

- **Intent authorization tier definitions** — what change types exist, what authorization each intent authorization tier requires, and who can approve at each level. (The intent authorization tiers themselves are defined in [intent-representation.md](intent-representation.md); governance decides who has the authority to create or modify those definitions.)
- **Autonomy levels** — which repos are agent-autonomous, which are in shadow mode, which require full human review. What are the graduation criteria, and who evaluates them?
- **Agent permissions** — what authority each agent role has (merge, approve, comment, label). What are the boundaries, and who draws them?
- **Org-wide guardrails** — minimum standards that apply to all repos regardless of individual repo policy. Examples: all repos must have CODEOWNERS, all security-sensitive paths require human approval, all agent config changes require human approval.
- **Model and tool egress policy** — which model providers and tool protocols (e.g. MCP servers) agent runtimes may use, how spend and quotas are enforced, and who may approve changes to those allowlists. Central **protocol gateways** are one enforcement point; they should fall under the same change-control rigor as other agent infrastructure (see [landscape.md](../landscape.md#agent-gateway)).
- **Manual bypass policy** — whether and how humans can temporarily bypass failed agent or CI gates. [Forge-sdlc/forge](../landscape.md#forge-sdlcforge) exposes `/forge skip-gate` for infrastructure-related CI failures, with PR confirmation and Jira audit comments. Fullsend needs the same UX category only if it is governed from day one: named checks, permission checks, expiry or revalidation, and immutable audit trails.

### 2. Configuration security

Agent configuration is itself a security-critical attack surface. If someone can modify what agents are allowed to do, they can bypass all other controls.

**Hard rules:**
- Agent configuration must not be modifiable through the same channels agents operate on (PRs, issues, comments in target repos)
- Changes to agent policy require a higher level of approval than changes to code
- CODEOWNERS files are always human-owned (established in [autonomy-spectrum.md](autonomy-spectrum.md))
- No agent self-modification — agents cannot change their own configuration, permissions, or system prompts

**Open design questions:**
- Where does agent policy live? In the repos it governs (as CLAUDE.md, agent config files)? In a separate policy repo? In a central configuration system?
- If policy lives in a separate repo, how does it get applied to target repos? Push-based (policy repo pushes to targets) or pull-based (agents read from policy repo at runtime)? (Push-based as Renovate pull requests, from a preset hosted in any repo — decided in [ADR 0103](../ADRs/0103-shared-config-presets-converged-by-fullsend-update.md); agents still read the merged layers at runtime, and enforcing a policy floor remains open.)
- How do we audit changes to agent configuration? Git history helps if policy is in git, but we also need to detect unauthorized runtime changes.
- How do we handle the bootstrap problem — who sets up the initial agent configuration for a new repo, and how is that initial setup secured? (Preset-based install and `config.base.yaml` / `config.yaml` layering decided in [ADR 0069](../ADRs/0069-ready-made-configuration-presets.md); workflow pinning and backend policy remain open.)

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
- How do we trace an agent action back to the policy that authorized it? Every merge should be traceable: this PR was merged because the review sub-agents approved, operating under policy version X, with the change classified as intent authorization tier N, authorized by intent record Y.
- What's the escalation path when something goes wrong? Who gets paged? Who has authority to revoke agent autonomy in an emergency?
- Can autonomy be automatically revoked? If a bad merge is detected (e.g., production incident traced to an agent-merged PR), should the system automatically downgrade the repo to human-required review?

## Cost governance

Token consumption and compute costs are scattered across multiple problem documents as secondary concerns, but at scale they become a governance problem in their own right.

Every agent operation has a cost: triage costs tokens, implementation costs tokens, review costs tokens (multiplied by the number of sub-agents), salvaging external contributions costs tokens, and even rejecting a PR costs tokens if the rejection is well-reasoned. At 50+ PRs/day (as in the [contribution volume](contribution-volume.md) scenario), or across dozens of agent-autonomous repos, these costs compound.

Cost governance intersects with several existing concerns:

- **The [security threat model](security-threat-model.md)** identifies DoS via token exhaustion as a threat, with cost budgets as the defense. But who sets those budgets, who reviews them, and what happens when a legitimate surge looks like an attack?
- **The salvage model** described in [code review](code-review.md) and [contribution volume](contribution-volume.md) trades tokens for community throughput. Without cost governance, a well-intentioned project could spend more on salvaging contributions than the contributions are worth.
- **Agent testing** (see [testing-agents.md](testing-agents.md)) requires running LLM evaluations, which themselves cost tokens. Testing the agents that test the code that agents wrote — the cost multiplies at each layer.
- **Production feedback loops** (see [production-feedback.md](production-feedback.md)) can generate runaway token spending if remediation loops don't have explicit cost budgets.

The governance question isn't just "how much should we spend?" but "who decides how much to spend on what?" A project might reasonably decide that 80% of its token budget goes to internal implementation and review, 15% to external contribution triage and salvage, and 5% to agent testing — but those are strategic allocation decisions that belong to governance, not to individual agents or repos.

## Adoption anti-patterns

Deploying autonomous agents creates organizational dynamics that governance should anticipate. Oxide's [RFD 576](https://rfd.shared.oxide.computer/rfd/0576) identifies three anti-patterns in LLM adoption that map directly to governance concerns for agent autonomy:

### Agent mandates

Requiring repositories or teams to adopt agent autonomy undermines the voluntary participation that makes open-source collaboration work. If a project mandates agent use — or makes non-agent workflows so inconvenient that they are effectively required — contributors who prefer manual workflows are penalized.

Governance should explicitly address whether repos can decline agent autonomy without penalty. The [autonomy spectrum](autonomy-spectrum.md) defines graduation criteria for increasing autonomy, but whether it should also support an opt-out path is an open question. A repo maintainer who judges that their codebase is better served by human-driven development may need a way to make that choice — but what "without organizational friction" looks like in practice, and whether it conflicts with org-wide consistency goals, is unresolved.

### Agent shaming

The informal counterpart of mandates. In organizations deploying fullsend, contributors who prefer manual workflows should not be treated as obstacles to productivity. This is particularly relevant when agents demonstrably increase throughput — the pressure to adopt becomes implicit even without explicit mandates.

This intersects with the [human factors](human-factors.md#contributor-motivation-in-open-source) concern about contributor motivation: if the culture shifts to treat non-agent contributors as slower or less valuable, the community loses contributors who bring exactly the deep expertise that [guarded-path approval](human-factors.md#is-the-two-point-model-enough) depends on.

### Agent anthropomorphization

Creating personas for agents — naming them, giving them personalities, treating their output as "opinions" — risks obscuring the mechanical nature of their operation. Trust in fullsend's model derives from repository permissions, structured evidence (see [trustworthiness-evidence.md](trustworthiness-evidence.md)), and audit trails — not from agent identity or personality.

However, agents do participate in communication channels: they author review comments, issue responses, and PR descriptions. Even without explicit personas, these communications risk implicit anthropomorphization — readers may attribute judgment, intent, or understanding to generated text. This is the flip side of the [trust through voice](human-factors.md#trust-erosion-from-agent-generated-voice) concern: anthropomorphization inflates trust in agent communication, while voice erosion deflates trust in community communication.

Governance should consider whether agent-authored communications require attribution (making the mechanical origin transparent) and whether certain communication contexts (design discussions, architectural RFCs) should remain human-authored.

## Relationship to other problem areas

- **Intent representation** defines the intent authorization tiers and authorization mechanisms. Governance defines who has authority to change those definitions.
- **Security threat model** identifies the threats. Governance defines the policies that mitigate them and who can modify those policies.
- **Autonomy spectrum** describes the graduation model. Governance defines who evaluates readiness and makes the graduation decision.
- **Agent architecture** defines the agent roles and permissions. Governance defines who assigns those permissions and under what constraints.
- **Human factors** explores what happens to the people alongside the system. The adoption anti-patterns above have direct consequences for contributor motivation and community trust.

## Open questions

- Should governance itself be subject to the agentic system, or is it always human-operated? (Strong argument for always-human: agents governing themselves is a security risk.)
- How do we handle disagreements between repo maintainers and org-wide policy? What's the appeal process?
- What's the relationship between governance of the agentic system and governance of the target organization itself? Are they the same body, or separate?
- How do we prevent governance from becoming a bottleneck? If every policy change requires broad consensus, experimentation slows down.
- What's the minimum viable governance for getting started? We don't need the full model on day one — what's the smallest governance structure that lets us begin experimenting safely?
