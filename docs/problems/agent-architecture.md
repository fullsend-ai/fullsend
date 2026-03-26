# Agent Architecture

What agents exist, what authority do they have, and how do they interact?

## The dual security context (platform organizations)

Some target organizations — particularly platform providers (CI/CD systems, cloud services, infrastructure) — have two distinct security responsibilities:

1. **Protecting the system itself** — agents reviewing changes to platform components need to guard against threats to the platform
2. **Protecting content passing through the system** — agents reviewing configurations that affect platform users need to guard against threats that would affect those users

These may require different agent specializations with different domain knowledge. See [applied docs](applied/) for organization-specific security contexts.

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

- **Authority:** Label issues, assign priority, link related issues, create derivative issues
- **Considerations:** Must be hardened against prompt injection in issue text

#### Fix scope: narrow fix vs. broad pattern remediation

When the triage agent processes a bug, there's a decision beyond severity and routing: **should the fix target only the reported instance, or should it address the underlying pattern across the codebase?**

This is a consequential choice. A narrow fix is safer and faster but leaves identical bugs latent elsewhere. A broad fix prevents recurrence but has a larger blast radius and may exceed the intent authorization of the original issue.

##### The linter heuristic

A useful decision boundary: **can the pattern be expressed as a static analysis rule?**

- **If yes** (e.g., unchecked nil dereference, missing error return check, deprecated API usage, format string mismatch): the triage agent should recommend broad remediation. The pattern is mechanical, the fix is deterministic, and a linter or codemod can validate completeness. In this case, the triage agent creates a single issue scoped to the pattern, not the instance. The implementation agent applies the fix codebase-wide and ideally adds a linter rule or CI check to prevent recurrence.

- **If no** (e.g., a race condition in a specific interaction, a logic error in business rules, an incorrect algorithm for a particular domain case): the fix requires contextual judgment at each call site. Applying it broadly risks introducing incorrect behavior where the pattern superficially matches but the semantics differ. In this case, the triage agent should fix the reported instance and then **scan for similar occurrences to create derivative issues** — one per location — so each gets individual analysis and review.

The distinction matters because the failure modes are asymmetric. A narrow fix that misses similar bugs is a known-unknown — the bugs exist but can be found later. A broad fix that incorrectly "fixes" code that wasn't broken creates regressions that are harder to trace back to the pattern remediation.

##### Derivative issue creation

When the triage agent identifies a bug that likely recurs but doesn't qualify for broad remediation, it should:

1. Fix the reported instance (normal Tier 1 flow)
2. Search the codebase for structurally similar patterns
3. For each candidate location, create a **derivative issue** linked to the original, containing:
   - The location and the pattern match
   - Why the triage agent thinks this location may have the same bug
   - A flag indicating this is a derivative (so reviewers and the priority agent can batch or deprioritize if the pattern turns out to be a false positive)

This keeps each fix scoped and individually reviewable while ensuring the broader problem doesn't get forgotten. The priority agent can then decide whether to batch derivative issues or address them individually based on severity and available capacity.

##### Interaction with the tier model

Broad pattern remediation has a tier escalation risk. A single nil-check fix is Tier 1 (bug fix with a linked issue). But "apply nil-check discipline across the entire codebase and add a linter rule" may be Tier 2 — it's a codebase-wide convention change, not a surgical fix. The triage agent should flag this when recommending broad remediation, and the review agent must independently assess whether the scope warrants tier escalation (see [intent-representation.md](intent-representation.md#defense-independent-tier-classification-by-review-agents)).

Conversely, if the triage agent creates 30 derivative issues for the same pattern, that's a signal that broad remediation would have been cheaper. The quality/drift detection agent (which monitors aggregate trends) should detect this accumulation and recommend consolidation into a single pattern-level issue — potentially escalating to Tier 2 if the scope warrants it.

##### When the boundary is unclear

Some bugs sit between the two categories — the pattern is somewhat mechanical but has enough contextual variation that a blanket fix is risky. In these cases, a hybrid approach is possible:

- The implementation agent fixes the reported instance
- The triage agent creates derivative issues for candidate locations
- The derivative issues include a suggested fix but flag it as "needs verification" — the implementation agent working each derivative must confirm the fix is appropriate for that specific context before applying it

This is more expensive than either pure approach but avoids both the blast radius of incorrect broad fixes and the amnesia of purely narrow fixes.

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
- How does the triage agent determine whether a bug pattern is mechanical enough for broad remediation vs. context-dependent enough to require per-instance analysis? Can this classification itself be expressed as a heuristic, or does it always require LLM judgment?
- What's the threshold for derivative issue accumulation before the quality/drift detection agent should recommend consolidation into a pattern-level fix? Is it a count (e.g., 10+ derivatives for the same pattern), a density (e.g., more than N% of files in a package), or a cost measure?
- When the triage agent creates derivative issues, how should the priority agent handle them — batch all at the same priority as the original, deprioritize as "known technical debt," or evaluate each independently?
