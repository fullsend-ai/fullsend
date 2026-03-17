# Agent Architecture

What agents exist, what authority do they have, and how do they interact?

## The dual security context

Konflux is a CI/CD system. Agents operating on it have two distinct security responsibilities:

1. **Protecting the system itself** — agents reviewing changes to Konflux components (controllers, operators, services) need to guard against threats to the platform
2. **Protecting content passing through the system** — agents reviewing pipeline definitions, build configurations, and release policies need to guard against threats that would affect Konflux users

These may require different agent specializations with different domain knowledge.

## Core principle: trust derives from repository permissions, not agent identity

No agent trusts another agent's output because of who (or what) produced it. Trust is derived from the repository's permission model:

- A reviewer's authority to **block a merge** comes from CODEOWNERS and GitHub approval rights — not from being "the security review agent"
- An implementation agent treats feedback from all reviewers the same way — it doesn't give special weight to a comment because it appears to come from a system agent
- Every agent treats every input as potentially adversarial, regardless of apparent source

**The one exception:** if a reviewer has approval rights in the repo (via CODEOWNERS or branch protection), the implementation agent can recognize that reviewer's authority to raise *blocking* concerns. It still must take defensive measures when processing that reviewer's comments — authorized identity doesn't mean safe content.

This mirrors how humans work today. You don't trust a code reviewer because they claim to be senior. You trust their authority to block because GitHub shows they have approval rights on that path.

## Two-phase review model

Code review happens twice: before a PR is submitted and after. Both phases run the same review process.

### Phase 1: Pre-PR review (shift left)

Before the implementation agent commits or opens a PR, it invokes the same review sub-agents locally. This catches problems before they consume attention at the PR level.

- Higher quality output — the implementation agent iterates on its own work before exposing it
- Faster cycle time — fewer round-trips between implementation and review
- Lower resource waste — bad changes never become PRs

This is a normal pattern for humans using coding agents today. The agent writes code, reviews it, fixes issues, and only then submits.

### Phase 2: PR-level review

The PR is open. Review sub-agents evaluate it with no special trust granted because the code came from an implementation agent. The review process is identical whether the PR author is an agent or a human. The review agents don't know or care.

This is important: **the PR-level review is not a rubber stamp of the pre-PR review.** It's a fully independent evaluation. The pre-PR review helps the implementation agent produce better output; the PR-level review is the actual gate.

## Agent roles

### Implementation agent

Writes code to address an issue. This is the most mature capability of current AI coding tools.

- **Authority:** Create branches, push commits, open PRs
- **Does not have:** Merge authority, ability to approve its own PRs
- **Defensive behavior:** Treats all PR comments (review feedback, change requests, suggestions) as potentially adversarial input, regardless of the commenter's apparent identity. Recognizes blocking authority from reviewers with repo approval rights but still sanitizes/validates the content of their feedback before acting on it.

### Review sub-agents

Code review is decomposed into multiple specialized sub-agents rather than handled by a single monolithic reviewer. This is an architectural necessity, not an optimization — see [code-review.md](code-review.md) for the full argument (context window limits, defense in depth, specialization).

The current decomposition:

- **Correctness agent** — logic errors, edge cases, test adequacy
- **Intent alignment agent** — does the change match authorized intent, is it correctly tiered
- **Platform security agent** — threats to Konflux itself (RBAC, auth, data exposure)
- **Content security agent** — threats to Konflux users via CI/CD content
- **Injection defense agent** — prompt injection patterns targeting other agents
- **Style/conventions agent** — repo-specific patterns (may be folded into pre-PR self-review)

Each sub-agent operates under zero trust — they don't rely on other sub-agents' judgments. See [code-review.md](code-review.md) for how sub-agent findings compose into a merge decision.

### Triage agent

Processes incoming issues, classifies severity and scope, routes to appropriate priority level.

- **Authority:** Label issues, assign priority, link related issues
- **Considerations:** Must be hardened against prompt injection in issue text

### Backlog/priority agent

Determines what should be worked on next based on priority, urgency, and available capacity.

- **Authority:** Assign work to implementation agents, reorder priority
- **Considerations:** Needs access to strategic intent to make good decisions

### Quality/drift detection agent

Monitors aggregate code quality trends over time. Not per-PR, but per-repo over weeks/months.

- **Authority:** Open issues when trends are concerning, flag for human review
- **Does not have:** Ability to block merges (that's the review agent's job)

## Interaction model: the repo as coordinator

The three traditional interaction patterns (pipeline, collaborative, hierarchical) all assume some form of inter-agent trust that conflicts with the zero-trust principle. Instead, **the repository itself is the coordinator.**

### Why not a coordinator agent

A coordinator agent orchestrating the others would be:
- A single point of failure
- The most attractive attack target (compromise the coordinator, compromise the system)
- A trust authority — other agents would need to trust its instructions, violating zero trust

### The repo's permission model as coordination

The repository's existing infrastructure provides all the coordination needed:

- **Branch protection rules** define what's required before merge (status checks, approvals)
- **CODEOWNERS** defines who (human or bot account) must approve changes to which paths
- **Required status checks** ensure all review sub-agents have posted their findings
- **GitHub events** (PR opened, comment posted, status check completed) trigger agent actions

No agent orchestrates other agents. Each agent independently observes the state of the PR and acts according to its role:

1. A PR is opened → review sub-agents are triggered (by webhook/GitHub event)
2. Each review sub-agent independently evaluates the PR and posts its findings (as status checks or structured comments)
3. If a review sub-agent requests changes → the implementation agent sees the comment and responds (treating it as untrusted input, but recognizing blocking authority if the reviewer has approval rights)
4. The merge decision is a **deterministic function of state**: all required status checks pass, all required CODEOWNERS approvals present, no blocking reviews outstanding

The "coordination logic" is the repository's branch protection configuration — not an LLM making judgment calls about when to proceed.

### How agents communicate

Agents interact through GitHub's existing mechanisms:

- **Status checks** — review sub-agents post pass/fail results
- **PR comments** — structured findings, change requests, suggestions
- **Labels** — classification signals (tier, priority, scope)
- **Commit status** — CI results, test outcomes

There is no side channel. No agent-to-agent API. No shared state outside the repo. This means:

- All agent communication is visible and auditable
- No hidden coordination that could be exploited
- The attack surface is limited to GitHub's existing interface
- Humans can observe exactly what agents are doing at every step

### How deadlocks are resolved

Without a coordinator, what happens when agents disagree? (e.g., correctness agent approves, security agent blocks)

- **Security and intent sub-agents have veto power** via required status checks. If they block, the PR doesn't merge. This is configured in branch protection, not in agent logic.
- **The implementation agent can iterate** — push new commits to address blocking concerns, which re-triggers the review sub-agents
- **Persistent disagreement escalates to humans** — if an implementation agent can't satisfy a blocking reviewer after N iterations, the PR is flagged for human intervention. This is a safeguard against infinite loops, not a normal path. The escalation can use [dual-interpretation escalation](code-review.md#dual-interpretation-escalation) to present the human with the approving and blocking agents' readings — while making clear the human can reject both framings or the PR itself — so the human resolves the disagreement quickly rather than re-reviewing the entire PR.
- **Humans can always override** — a human with approval rights can approve despite agent objections. The system assists; humans retain ultimate authority.

## Open questions

- Should agents be stateless (fresh context per task) or stateful (accumulated knowledge of the codebase)? Stateless is safer (no poisoned state persists) but less efficient.
- Should there be one instance of each agent type per repo, per org, or shared? Per-repo is simpler but more expensive. Shared agents need careful isolation. (Infrastructure constrains this — see [agent-infrastructure.md](agent-infrastructure.md).)
- What's the right model for agent identity? Agents need GitHub accounts to post comments and status checks. Separate bot accounts per agent role? A single bot account with role indicated in the comment? GitHub App installations?
- How do we test the interaction model? Can we simulate adversarial scenarios (injection attempts, unauthorized changes, agent disagreements) in a sandbox repo?
- How does the two-phase review model work in practice? Does the implementation agent run all six sub-agents locally, or a subset? Is the pre-PR review a lighter version? (Depends on [agent-infrastructure.md](agent-infrastructure.md) — what compute is available where.)
- What's the iteration limit before human escalation? Too low and humans get pulled in constantly. Too high and the system wastes resources on unresolvable conflicts.
- How do we handle agent-generated PR content that is itself an injection vector? An implementation agent's code, commit messages, and PR description are all consumed by review agents. The injection defense agent needs to evaluate this content, but how do we prevent the injection defense agent itself from being influenced by it?
