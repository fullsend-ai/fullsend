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

Code review happens twice — before and after PR submission. Both phases run the same review sub-agents. See [agent-architecture.md](agent-architecture.md) for the full two-phase model and how it interacts with the trust model.

### Phase 1: Pre-PR review (shift left)

Before the code agent commits or opens a PR, it invokes the review sub-agents locally. This catches problems before they consume attention at the PR level. The code agent iterates on its own work — fixing issues, improving test coverage, addressing security concerns — before exposing the change to the broader system.

This is a normal pattern for humans using coding agents today. It produces higher quality output faster and wastes fewer resources.

### Phase 2: PR-level review (the actual gate)

The PR is open. Review sub-agents evaluate it with no special trust granted because the code came from a code agent that already ran pre-PR review. The PR-level review is a fully independent evaluation — not a rubber stamp of Phase 1.

The review process is identical whether the PR author is an agent or a human. The review agents don't know or care about authorship. They evaluate:

## Why review must be decomposed into sub-agents

### The context window argument

A single review agent asked to evaluate a PR must simultaneously consider: correctness, security (platform and content), intent alignment, test adequacy, style conformance, prompt injection defense, tier classification, and cross-repo impact. For a non-trivial diff in a complex codebase, this overwhelms the context window — not just in terms of token count, but in terms of attention quality.

Even as context windows grow, the problem persists. Research and practice consistently show that LLM attention degrades with volume. A 200k-token context window doesn't mean 200k tokens of equally-weighted analysis. Asking one agent to hold the full diff, the relevant codebase context, the intent specification, the security threat model, and the repo's conventions all at once means it does all of them poorly.

This isn't a temporary implementation limitation that will be solved by bigger models. It's an architectural constraint that should inform the system design.

### The defense-in-depth argument

A single monolithic review agent is a single point of failure. If that agent is fooled — by prompt injection, by a cleverly disguised malicious change, by a subtle logic error — all review is compromised. Multiple specialized sub-agents with different concerns create defense in depth: even if the correctness agent is fooled, the security agent might catch it. Even if the security sub-agent misses something, the intent & coherence sub-agent might flag that the change doesn't match any authorized work.

This maps directly to the zero-trust principle from the [security threat model](security-threat-model.md). Sub-agents don't trust each other's judgments — each independently evaluates the change from its own perspective. A merge requires all sub-agents to pass, not just one generalist to approve.

### The specialization argument

Different review concerns require different context. A correctness reviewer needs deep understanding of the codebase's logic and patterns. A security reviewer needs knowledge of common vulnerability patterns and the organization's specific threat model. An intent & coherence reviewer needs access to the intent repo and the feature authorization state. Loading all of this context into one agent is wasteful — each sub-agent loads only the context relevant to its concern, using its context window more effectively.

## Review sub-agent decomposition

Six specialized sub-agents (implemented from a conceptual decomposition of nine axes), each independently evaluating the change from its own perspective. High-stakes adversarial dimensions (correctness, security) run on opus; mechanical-matching dimensions run on sonnet.

### Correctness agent (opus)

Evaluates whether the code does what it claims to do.

- Logic errors, off-by-one, nil/null handling
- Edge cases and error paths
- Consistency with existing codebase patterns
- Test adequacy — are the right things being tested?
- Do tests actually verify the behavior they claim to?
- If test files covering the changed code were recently modified, did those modifications weaken the test's ability to catch regressions?
- Split-payload attacks: a production change paired with a test modification that masks the real behavior. (See [security-threat-model.md](security-threat-model.md#cross-cutting-attack-pattern-temporal-split-payload-test-poisoning) for why this matters.)
- Coverage reduction: does the change remove or weaken existing test coverage?

**Context needed:** The diff, relevant surrounding code, test files, git history of relevant test files, existing patterns in the repo.

### Security agent (opus)

Reviews changes for threats to the platform and its users. Collapses the platform security and content security concerns into a single agent. Organization-specific concerns are configured in [applied docs](applied/).

- RBAC and authorization changes
- Authentication flows
- Data exposure risks
- Privilege escalation paths
- Injection vulnerabilities (SQL, command, LDAP, etc.)
- Content security: sandboxing gaps, XSS, SSRF
- Permission manifest changes (GitHub App manifests, workflow `permissions:` blocks, IAM policies, etc.)
- Code comments and string literals
- Configuration files and test data
- Prompt Injection (patterns that look like agent instructions embedded in code)
- **Non-rendering Unicode characters** — Tag characters (U+E0000–U+E007F), zero-width characters, bidirectional overrides, and other invisible codepoints that can encode hidden instructions. See [security-threat-model.md](security-threat-model.md#steganographic-injection-invisible-unicode-payloads) for the full threat description.

**Context needed:** The diff, security-relevant code paths, RBAC configuration, known vulnerability patterns, sandboxing mechanisms.

### Intent & Coherence (sonnet)

Evaluates whether the change matches an authorized intent and whether its scope matches its claimed tier.

- Does this PR trace to a linked issue or authorized feature?
- Does the implementation match what the issue/feature describes?
- Is the change scope consistent with its tier classification? (The [tier escalation problem](intent-representation.md#the-tier-escalation-problem) — a "bug fix" that's really a feature request.)
- Does the change go beyond what was authorized?
- Does the change fit the overall design of the module/system?
- Is the complexity proportional to the value delivered?
- Are there simpler alternatives that achieve the same goal?

**Context needed:** The diff summary, the linked issue/feature file, surrounding module architecture, design docs, the intent repo state, the tier classification criteria.

### Style/conventions agent (sonnet)

Evaluates adherence to repo-specific patterns and conventions.

- Code style beyond what linters catch
- Naming conventions, API patterns, error handling idioms, code organization

**Context needed:** The diff, repo style guides, examples of existing patterns. This is the lowest-stakes review concern and could potentially be handled by the code agent's pre-PR self-review rather than a separate sub-agent.

### Docs currency agent (sonnet)

Evaluates whether documentation is stale as a result of the change. Follows the `docs-review` skill inline.

- Do docs reference behavior, APIs, or configurations changed by the PR?
- Are any docs now inaccurate, stale, incomplete, or misleading?

**Context needed:** The diff, documentation files, file-change overlap analysis.

### Cross-repo contracts agent (sonnet)

Evaluates whether the change breaks API surfaces consumed by other repositories.

- Protobuf definitions, shared types, CLI flags, REST/gRPC endpoints
- Backward-incompatible changes to public interfaces
- Version contract violations
- API contract changes: does the change modify parameters sent to an
  external API, and does the API accept the new values for every code
  path?

**Context needed:** The diff, API surface definitions, known downstream consumers.

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

Security and intent agents have veto power (any rejection blocks). Correctness and style agents can flag concerns but not block — their concerns are surfaced for human review or code agent iteration.

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

### Dual-interpretation escalation

When an agent escalates to a human, the quality of that escalation matters. A vague "I'm not confident" wastes the human's time. A more useful pattern: when the agent's uncertainty stems from a change being legitimately interpretable in multiple ways, it presents its best interpretations as structured alternatives — while explicitly inviting the human to reject all of them.

For example, a review agent uncertain about tier classification could escalate with:

- **Reading A:** "This is a bug fix (Tier 1) — the existing behavior doesn't match the documented intent, and the change is scoped to correcting that gap. Requires: linked issue."
- **Reading B:** "This is a new feature (Tier 2) — the system never intended to do this, and the change adds new capability. Requires: authorized feature file in `approved/`."

Critically, the escalation must always include an explicit "none of the above" option — the human may see a framing the agent missed entirely, or may decide the change should be rejected outright. The agent's interpretations are a starting point for the human's decision, not an exhaustive menu. This avoids presenting a false dichotomy that pressures the human into picking whichever option seems least wrong.

The human sees coherent framings and can pick the one that matches their understanding, offer their own, or reject the change — rather than starting from scratch. This is faster and more structured than an open-ended "please review."

This pattern is most valuable at escalation boundaries — where the system has already decided it can't resolve something autonomously. It doesn't replace confidence scores or explicit uncertainty signals; it complements them by making the *nature* of the uncertainty actionable. It applies wherever agents interact with humans: tier classification (see [intent-representation.md](intent-representation.md#the-tier-escalation-problem)), the exploration phase for proposed features (see [intent-representation.md](intent-representation.md#the-try-it-phase)), and deadlock resolution between review sub-agents (see [agent-architecture.md](agent-architecture.md#how-deadlocks-are-resolved)).

[Forge-sdlc/forge](../landscape.md#forge-sdlcforge) has a concrete version of this idea in its `implement_review` flow: review comments are treated as their own task type, classified as actionable or contested before the agent acts, and contested comments trigger a structured response rather than silent compliance. That is a useful precedent for fullsend's review loops, especially when an agent should push back on incorrect feedback while still respecting the reviewer's blocking authority.

## Review as salvage

The sub-agent model above assumes a binary outcome: approve or reject. But when reviewing external contributions (especially AI-generated ones), a third outcome becomes important: *salvage* — extracting the valuable idea from a poor implementation and having a project agent rewrite it properly.

This adds a new composition model: **review + rewrite**. The review sub-agents identify what's wrong, and if the underlying idea has merit, a separate implementation pass fixes the issues rather than sending the PR back to the contributor for iteration. The review findings become input to a salvage decision rather than a merge/reject decision.

Whether this belongs in the review system or is a separate workflow operating on review output is an open question. For a fuller treatment of the salvage concept, including trade-offs and cost implications, see [contribution-volume.md](contribution-volume.md#the-salvage-question).

## Open questions

- Can we quantify review quality? How do we know if an agent's review is as good as a human's?
- How do we handle the case where an agent approves a PR that a human would have caught? (Learning from mistakes.)
- Should review agents have access to the full repo context, or just the diff? Full context is more accurate but more expensive and more vulnerable to injection from existing code.
- How do we prevent review agents from being "rubber stamps" — always approving because they're optimizing for throughput?
- What's the right interface for review feedback? GitHub PR comments? A structured report? Both?
- How do we handle multi-repo changes where the review needs to consider changes across repos together?
- Is there a "coherence" or "fitness" review dimension that can't be decomposed into the existing sub-agents? The [taste problem](contribution-volume.md#the-taste-problem) suggests that some review concerns require holistic project judgment rather than specialized analysis.
- At what volume does the salvage model become cost-effective compared to rejection? What's the token cost of salvaging a typical contribution vs. the contributor-retention value?
