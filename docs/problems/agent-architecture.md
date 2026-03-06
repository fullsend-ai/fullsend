# Agent Architecture

What agents exist, what authority do they have, and how do they interact?

## The dual security context

Konflux is a CI/CD system. Agents operating on it have two distinct security responsibilities:

1. **Protecting the system itself** — agents reviewing changes to Konflux components (controllers, operators, services) need to guard against threats to the platform
2. **Protecting content passing through the system** — agents reviewing pipeline definitions, build configurations, and release policies need to guard against threats that would affect Konflux users

These may require different agent specializations with different domain knowledge.

## Possible agent roles

### Implementation agent

Writes code to address an issue. This is the most mature capability of current AI coding tools.

- **Authority:** Create branches, push commits, open PRs
- **Does not have:** Merge authority, ability to approve its own PRs

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

## Interaction patterns

### Pipeline model

Agents form a pipeline: triage -> prioritize -> implement -> review -> merge. Each stage hands off to the next.

**Pros:** Simple, clear authority boundaries.
**Cons:** Rigid. Doesn't handle iteration well (review agent requests changes, implementation agent needs to respond).

### Collaborative model

Agents interact more fluidly — a review agent can directly ask an implementation agent for changes, a security agent can veto a review agent's approval.

**Pros:** More natural, handles iteration.
**Cons:** Complex coordination. Risk of deadlocks or circular escalation.

### Hierarchical model

A coordinating agent orchestrates the others, making final decisions when sub-agents disagree.

**Pros:** Clear decision authority. Can break deadlocks.
**Cons:** Single point of failure. The coordinator becomes the most attractive attack target.

## Open questions

- Should agents be stateless (fresh context per task) or stateful (accumulated knowledge of the codebase)?
- How do agents communicate? Through GitHub (comments, labels, status checks) or through a separate coordination layer?
- What happens when agents disagree? (Security agent says no, review agent says yes.)
- Should there be one instance of each agent type per repo, per org, or shared?
- How do we prevent agent-to-agent prompt injection? (A compromised implementation agent crafting PR content to manipulate the review agent.)
- What's the right model for agent identity? Do agents act as GitHub bot accounts? As app installations?
- How do we test agent behavior? Can we simulate adversarial scenarios in a sandbox?
