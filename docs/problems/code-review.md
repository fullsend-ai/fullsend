# Code Review

How do agents review code effectively, including catching security issues, before and after PR submission?

## Why code review is the hardest problem

Code generation is largely solved — given a well-scoped task, modern agents produce working implementations reliably. But reviewing code is fundamentally different:

- **Generation is convergent** — there's a clear goal (make the tests pass, implement the spec)
- **Review is divergent** — you're looking for problems you don't know exist yet
- Review requires understanding not just what the code does, but what it *should* do (intent)
- Review requires understanding what the code *doesn't* do (missing error handling, edge cases, security implications)
- Review requires context that may not be in the diff (how does this interact with other systems?)

## Two phases of review

### Pre-PR review (internal review)

Before an implementation agent even opens a PR, can we catch problems early?

- Does the implementation match the intent of the issue?
- Are there obvious bugs, missing edge cases, or security issues?
- Does it follow the repo's patterns and conventions?
- Are the tests adequate?

This is potentially more impactful than post-PR review because it catches problems before they consume reviewer attention.

### Post-PR review (formal review)

The PR is open. Review agents evaluate it.

- Correctness: does it do what it claims?
- Security: does it introduce vulnerabilities?
- Intent alignment: does it address the right problem?
- Test coverage: are the changes tested?
- Style/conventions: does it follow the repo's patterns?

## Why review must be decomposed into sub-agents

### The context window argument

A single review agent asked to evaluate a PR must simultaneously consider: correctness, security (platform and content), intent alignment, test adequacy, style conformance, prompt injection defense, tier classification, and cross-repo impact. For a non-trivial diff in a complex codebase, this overwhelms the context window — not just in terms of token count, but in terms of attention quality.

Even as context windows grow, the problem persists. Research and practice consistently show that LLM attention degrades with volume. A 200k-token context window doesn't mean 200k tokens of equally-weighted analysis. Asking one agent to hold the full diff, the relevant codebase context, the intent specification, the security threat model, and the repo's conventions all at once means it does all of them poorly.

This isn't a temporary implementation limitation that will be solved by bigger models. It's an architectural constraint that should inform the system design.

### The defense-in-depth argument

A single monolithic review agent is a single point of failure. If that agent is fooled — by prompt injection, by a cleverly disguised malicious change, by a subtle logic error — all review is compromised. Multiple specialized sub-agents with different concerns create defense in depth: even if the correctness agent is fooled, the security agent might catch it. Even if the security agent misses something, the intent alignment agent might flag that the change doesn't match any authorized work.

This maps directly to the zero-trust principle from the [security threat model](security-threat-model.md). Sub-agents don't trust each other's judgments — each independently evaluates the change from its own perspective. A merge requires all sub-agents to pass, not just one generalist to approve.

### The specialization argument

Different review concerns require different context. A correctness reviewer needs deep understanding of the codebase's logic and patterns. A security reviewer needs knowledge of common vulnerability patterns and Konflux's specific threat model. An intent alignment reviewer needs access to the intent repo and the feature authorization state. Loading all of this context into one agent is wasteful — each sub-agent loads only the context relevant to its concern, using its context window more effectively.

## Review sub-agent decomposition

### Correctness agent

Evaluates whether the code does what it claims to do.

- Logic errors, off-by-one, nil/null handling
- Edge cases and error paths
- Consistency with existing codebase patterns
- Test adequacy — are the right things being tested?

**Context needed:** The diff, relevant surrounding code, test files, existing patterns in the repo.

### Intent alignment agent

Evaluates whether the change matches an authorized intent and whether its scope matches its claimed tier.

- Does this PR trace to a linked issue or authorized feature?
- Does the implementation match what the issue/feature describes?
- Is the change scope consistent with its tier classification? (The [tier escalation problem](intent-representation.md#the-tier-escalation-problem) — a "bug fix" that's really a feature request.)
- Does the change go beyond what was authorized?

**Context needed:** The diff summary, the linked issue/feature file, the intent repo state, the tier classification criteria.

### Platform security agent

Reviews changes for threats to Konflux itself.

- RBAC and authorization changes
- Authentication flows
- Data exposure risks
- Privilege escalation paths
- Injection vulnerabilities (SQL, command, LDAP, etc.)

**Context needed:** The diff, security-relevant code paths, RBAC configuration, known vulnerability patterns.

### Content security agent

Reviews changes that affect the CI/CD content passing through Konflux — protecting Konflux's users.

- Pipeline definition handling — can a user's pipeline definition escape its sandbox?
- Build configuration — can build parameters be manipulated?
- Release policy — can release gates be bypassed?
- Artifact integrity — can artifacts be tampered with?

**Context needed:** The diff, pipeline/build/release code paths, sandboxing mechanisms.

### Injection defense agent

Specifically looks for prompt injection patterns targeting other agents in the system.

- PR descriptions and commit messages
- Code comments and string literals
- Configuration files and test data
- Patterns that look like agent instructions embedded in code

This agent has a unique role: it's protecting the other review agents, not the codebase. It evaluates whether the PR content is trying to manipulate the review process itself.

**Context needed:** The raw PR content (description, commit messages, diff), known injection patterns. Notably, this agent should see the *unprocessed* content, not a summary — summaries might strip out the injection attempts.

### Style/conventions agent

Evaluates adherence to repo-specific patterns and conventions.

- Code style beyond what linters catch
- Naming conventions, API patterns, error handling idioms
- Documentation adequacy

**Context needed:** The diff, repo style guides, examples of existing patterns. This is the lowest-stakes review concern and could potentially be handled by the implementation agent's pre-PR self-review rather than a separate sub-agent.

## How sub-agents compose a decision

The sub-agents need to produce a combined review decision. Options:

### Unanimous approval

All sub-agents must approve for auto-merge. Any rejection blocks.

**Pros:** Maximum safety. Defense in depth — every concern is a veto.
**Cons:** High false-positive rate. One overly cautious sub-agent blocks everything. May need a human tiebreaker mechanism.

### Weighted voting

Each sub-agent produces a score. A weighted aggregate determines the outcome. Security agents might have higher weight (or veto power) while style agents have lower weight.

**Pros:** More nuanced. Allows low-stakes concerns to not block high-confidence approvals.
**Cons:** Weight tuning is hard. Gameable if an attacker understands the weights. Loses the clarity of "every concern must pass."

### Veto-based with tiers

Security and intent agents have veto power (any rejection blocks). Correctness and style agents can flag concerns but not block — their concerns are surfaced for human review or implementation agent iteration.

**Pros:** Balances safety with throughput. Security is non-negotiable, style is advisory.
**Cons:** Still need to define what counts as a "security" concern vs. a "correctness" concern. Boundary is fuzzy.

### Escalation-based

Sub-agents don't approve or reject — they produce findings. A separate merge-decision agent (or simple rule engine) evaluates the findings and decides: auto-merge, request changes, or escalate to human.

**Pros:** Separates analysis from decision. Findings are reusable. The decision logic can be a simple, auditable rule set rather than an LLM judgment.
**Cons:** Adds another agent to the chain. The merge-decision agent/rules become the real authority and thus the real attack target.

## The confidence problem

A human reviewer can say "I'm not sure about this, let me think" or "I need someone else to look at this." Agents need equivalent mechanisms:

- When should a review agent escalate to a human?
- How does an agent express uncertainty? Confidence scores? Explicit "I don't know" signals?
- Should there be a minimum number of agent reviewers that agree before auto-merge?

## Open questions

- Can we quantify review quality? How do we know if an agent's review is as good as a human's?
- How do we handle the case where an agent approves a PR that a human would have caught? (Learning from mistakes.)
- Should review agents have access to the full repo context, or just the diff? Full context is more accurate but more expensive and more vulnerable to injection from existing code.
- How do we prevent review agents from being "rubber stamps" — always approving because they're optimizing for throughput?
- What's the right interface for review feedback? GitHub PR comments? A structured report? Both?
- How do we handle multi-repo changes where the review needs to consider changes across repos together?
