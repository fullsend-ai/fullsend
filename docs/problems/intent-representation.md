# Intent Representation

How do we capture, verify, and enforce what changes are wanted — so that agents can distinguish a desired change from a non-feature?

## Why this is hard

### The two components of today's intent system

Today's intent enforcement has two components that humans fuse together unconsciously:

1. **The formal record** (JIRA states, rank, refinement trackers) — this is explicit but gameable. In the current KONFLUX JIRA project, features move through states (draft, pending approval, approved, in progress, etc.) and are ranked. Higher-ranked features should be worked on first. Features in "pending approval" require review from named architects and product managers. However, there are no ACLs that prevent an unauthorized person from zipping through the refinement states and closing all the trackers, making a feature look authorized and ready to go.

2. **The informal network** (standup conversations, hallway knowledge, "I know what we talked about last week") — this is the real enforcement mechanism today. Humans detect anomalies because they have ambient awareness of what's "in flight." If a code review comes in for something unfamiliar, they look more closely. They cross-reference incoming PRs with what they know was discussed in meetings.

Agents don't have component 2. They can read JIRA, but they can't know that "nobody actually discussed this in any meeting, so it's suspicious that it's suddenly approved." This means any agentic intent system must either:

- Make the informal knowledge explicit and auditable
- Find a substitute mechanism that provides equivalent anomaly detection
- Build an intent system where the formal record *is* the enforcement (not bypassable like JIRA)

### What agents need to answer

At review time, an agent needs to answer: "Is this change something we actually want?" Without this, agents might:

- Implement changes that are technically correct but strategically wrong
- Merge features nobody asked for
- Optimize for the wrong thing (e.g., adding complexity where simplicity was needed)
- Be manipulated into implementing unauthorized changes that look legitimate in the formal system

## The spectrum of intent

Not all changes require the same level of intent verification:

| Change type | Intent signal needed | Example |
|---|---|---|
| Dependency update passing CI | None — pre-authorized category | Renovate bumps a patch version |
| Linter/formatter fix | None — pre-authorized category | Auto-fix style violations |
| Bug fix (clear reproduction) | Low — the bug report itself is the intent | "Fix nil pointer in controller when X is nil" |
| Small improvement | Low-moderate — linked issue sufficient | "Add retry logic for flaky API call" |
| New feature | High — needs explicit authorization | "Add support for multi-arch builds" |
| API change | High — affects consumers | "Change Snapshot CRD schema" |
| UX change | High — affects users | "Redesign pipeline visualization" |
| Architectural change | Very high — affects multiple repos | "Migrate from X to Y across the org" |
| New component | Very high — creates ongoing maintenance | "Add a new notification service" |

## Approach 1: Git as the intent ledger

Move intent representation out of JIRA and into git. A dedicated repo (e.g., `konflux-ci/intent` or `konflux-ci/features`) where features are proposed, explored, and authorized through PRs with CODEOWNERS-enforced signoff.

### Structure

```
features/
  proposed/        # Anyone can PR here. Low bar.
  explored/        # Agent has built a prototype PR. Still not approved for merge.
  approved/        # Requires CODEOWNERS signoff from architects + PM
  in-progress/     # Work is actively happening
  completed/       # Delivered
```

Each feature is a file (YAML? Markdown with frontmatter?) that moves between directories via PRs. Each transition requires different CODEOWNERS signoff.

### The "try it" phase

Moving from `proposed/` to `explored/` just means an agent builds a draft PR against the target repo so stakeholders can see what the change looks like.

For some types of change this phase *could* leverage a **vibe-to-spec** workflow. Developers use AI to rapidly *simulate* the feature's behavior to discover friction points before any production logic is written. Once the "vibe" is validated, the exploration is thrown away, and the AI generates the formal `spec-kit`-like requirements document.

Any draft PR created at this stage cannot merge — it's gated on the generated feature file being in approved/. Low cost, high information value. This decouples understanding a change from authorizing a change.

The exploration phase can also benefit from [dual-interpretation escalation](code-review.md#dual-interpretation-escalation): when a proposed feature has multiple plausible implementation approaches, the agent can build them as draft PRs so stakeholders compare concrete alternatives rather than debating in the abstract. Stakeholders may also reject all approaches or propose a direction the agent didn't consider.


### The "ship it" phase

Moving from `explored/` to `approved/` requires signoff from architects and PM via CODEOWNERS. Only then can the implementation PR merge to main and be released.

### What this buys you

- Full audit trail in git history — who approved what, when, with what justification
- ACLs via CODEOWNERS that are actually enforced (unlike JIRA states)
- Agents can programmatically check: "does this PR trace to a feature file in `approved/`?"
- The informal knowledge problem is partially solved — the approval PRs and their review discussions become the "meeting minutes"

### What's hard

- Overhead of maintaining feature files for small changes (bug fixes, minor improvements) — this is why tiering matters (see Approach 2)
- Cross-repo features need to reference multiple target repos
- Migrating from JIRA (organizational inertia, existing integrations)
- Feature files in `proposed/` could themselves contain prompt injection targeting agents that read them — CODEOWNERS on `approved/` prevents self-approval, but the content is still agent-consumed

## Approach 2: Tiered intent with different mechanisms per tier

Not everything needs the same process. Explicitly tier changes by scope, with different intent mechanisms at each tier.

### Tier 0: Standing rules (no per-change intent needed)

Pre-authorized categories of changes that the organization always wants:

- Dependency updates that pass CI
- Linter/formatter fixes
- Test additions that don't change production behavior
- Documentation typo fixes

The intent is "we always want these." An agent verifies the change actually falls in this category (static analysis: "this change only touches test files") and no further authorization is needed.

### Tier 1: Tactical (issue is sufficient)

- Bug fixes with a linked issue and reproduction
- Small improvements scoped to a single repo

An agent can act on these if there's a corresponding GitHub issue. The issue itself is the intent signal. Normal code review applies, no additional approval needed.

### Tier 2: Strategic (requires explicit multi-party authorization)

- New features
- API changes
- UX changes
- Architectural changes within a single repo
- New components

This is where the git-based authorization mechanism (Approach 1) kicks in. The feature must be explicitly authorized via the intent repo before agents can merge the implementation.

### Tier 3: Organizational (requires broader consensus)

- Cross-org changes affecting multiple repos
- Deprecations and removals
- Changes that affect the public API contract between Konflux and its users

Possibly requires an RFC-like process with a community review period, in addition to the git-based authorization.

Note: changes to the agentic system itself (agent policies, security policies, tier definitions) are a [governance](governance.md) concern, not an intent concern. Those changes are about modifying the rules of the system, not about authorizing work within the system.

### The key question for each tier

How does a review agent verify which tier a change falls into? This is non-trivial:

- **Tier 0** might be automatable via static analysis ("this change only touches test files")
- **Tier 1** needs issue linkage verification
- **Tier 2-3** need the formal authorization check against the intent repo
- **Tier gaming** is a threat — an attacker frames a strategic change as a tactical bug fix to avoid the higher approval bar. The review agent must independently assess change scope, not trust the author's classification.

## Approach 3: Intent as cryptographic attestation

Borrowing from Konflux's own SLSA provenance model — intent authorization produces a signed attestation.

- A human (or group of humans) signs a statement: "Feature X is authorized for implementation in repos A, B, C"
- The attestation is verifiable and tamper-proof
- Agents check for valid attestations before merging
- The attestation chain is auditable: who authorized it, when, based on what

### What this buys you

- Stronger security properties than git-based CODEOWNERS (cryptographic rather than access-control based)
- Thematically consistent with what Konflux already does for build provenance
- Harder to forge than JIRA state transitions or even git approvals

### What's hard

- More heavyweight than git-based approaches
- Key management is its own hard problem
- Doesn't solve the "try it" phase — attestation is binary (authorized or not), whereas the git-based approach has a natural progression through states

## Approach 4: Declarative intent in the codebase

Repos contain descriptions of desired behavior — like ADRs, or an `INTENT.md` that describes what the system should and shouldn't do. Agents evaluate changes against these declarations.

**Pros:** Intent travels with the code. Version-controlled. Agents can read it at review time.

**Cons:** Hard to keep current. Doesn't capture strategic/cross-repo intent well. Easy to game (an attacker could modify the intent document as part of a malicious PR). Better suited for cross-cutting standing rules (Tier 0) than for feature-level intent.

## Approach 5: Issues/specs as intent source

Human-authored GitHub issues or design documents are the source of truth. Agents can only work on explicitly created issues. A reviewing agent checks a PR against the linked issue's acceptance criteria.

**Pros:** Simple, familiar, low overhead for small changes. Humans retain clear control.

**Cons:** Issues are unstructured — hard for agents to evaluate programmatically. Acceptance criteria quality varies. Doesn't scale to strategic decisions. Suffers from the same ACL weakness as JIRA — anyone can create an issue.

## The "institutional knowledge" problem

Even with a formal intent system, there's value in agents having awareness of organizational context — the informal component that humans use today. Some possibilities:

### Meeting summaries as agent context

After planning meetings, someone (or an agent attending the meeting) produces a structured summary of decisions and priorities. This becomes part of the context agents use for triage and prioritization. The approval PRs in the intent repo could serve this purpose if they capture the reasoning.

### Communication channel integration

Agents monitor Slack or other communication channels (read-only) to build awareness of what's being discussed. Powerful but introduces a massive prompt injection surface — every message in every channel becomes a potential attack vector.

### Explicit "not authorized" signals

Instead of trying to replicate ambient awareness, provide a mechanism for humans to explicitly flag suspicious activity: "I didn't authorize this, investigate." This is reactive rather than proactive but may be more practical and secure.

## Lessons from existing systems

Kubernetes has KEPs (enhancement proposals). Rust has RFCs. Fedora has Changes. These are human-paced processes, but the structure is informative:

- Structured proposal with motivation, design, alternatives considered
- Explicit approval from designated reviewers
- Status tracking (provisional, implementable, implemented, deprecated)
- The proposal lives alongside or near the code

The difference for us: the process needs to be machine-readable enough for agents to evaluate, and fast enough that it doesn't bottleneck agent-speed development for lower tiers.

## Adversarial analysis

Any intent system needs to survive attack:

- **JIRA manipulation** — attacker fast-tracks a feature through refinement states. In a system using JIRA as the intent source, this attack works because there are no real ACLs on state transitions.
- **Git-based manipulation** — attacker submits a PR to the intent repo with a feature file containing prompt injection in the description. CODEOWNERS on `approved/` prevents self-approval, but `proposed/` is open and the content is agent-consumed.
- **Attestation forgery** — attacker compromises a signing key. Mitigated by requiring multiple signatures (m-of-n), but key management is complex.
- **Tier gaming** — attacker frames a strategic change as a tactical bug fix to avoid the higher approval bar. The review agent must independently assess change scope.
- **Intent composition** — three small "tactical" changes that individually look innocuous but together constitute an unauthorized feature. Detection requires cross-change awareness.

## The tier escalation problem

Tiering is necessary, but it introduces a specific weakness: low-tier changes have lightweight intent requirements, which creates an incentive to disguise high-impact changes as low-impact ones.

### How this happens today (without agents)

This is already a common pattern in human-driven development. A user expects the system to behave a certain way and reports the gap as a "bug" when — to the system owners — it's actually a feature request. The system never intended to do that thing. The user frames it as "this is broken" when the real ask is "please add this capability."

Experienced maintainers catch this: "That's not a bug, that's a feature request — let's discuss whether we actually want that." But this judgment requires understanding the system's intended behavior, not just its current behavior.

### How it gets worse with agents

An agent processing a "bug report" at Tier 1 (lightweight intent, just needs an issue) might:

- Implement a significant behavioral change because the issue describes it as a fix
- Add new API surface under the guise of "fixing" missing functionality
- Change security-relevant behavior because the reporter framed a policy decision as a defect

The agent is technically responsive to the issue, but it's implementing something that should have gone through Tier 2 authorization.

### Defense: independent tier classification by review agents

Review agents must independently assess what tier a change *actually* represents, regardless of how the author or issue classifies it. This means:

- **Scope analysis** — does this change add new behavior, or fix existing behavior? Adding new API endpoints is not a bug fix, even if the issue says "bug."
- **Impact analysis** — does this change affect security, UX, or API surface? If so, it's at least Tier 2 regardless of the issue label.
- **Intent verification** — does the linked issue actually describe what this PR does? And does the code do exactly what the intent file says, and nothing more? The vibe-to-spec workflow gives the agent a strict checklist. If someone tries to sneak a major new feature into a low-tier bug fix, the agent will automatically block it because the extra code won't match the generated spec.
- **Pattern detection** — multiple "small" changes from the same source that collectively add up to a feature should trigger escalation.

When tier classification is genuinely ambiguous, rather than making a weak call or defaulting to escalation without context, the review agent can use [dual-interpretation escalation](code-review.md#dual-interpretation-escalation) — presenting the human with its tier readings and the evidence for each, while always leaving room for the human to see a different framing or reject the change entirely.

This applies equally to review agents looking at code PRs *and* to agents evaluating intent changes in the intent repo itself. A low-tier intent statement that describes something high-impact should be flagged and escalated.

### The philosophical question

This is really about who defines "bug" vs. "feature." Today, human maintainers hold that authority through institutional knowledge of what the system is *supposed* to do. In an agentic system, this knowledge needs to be explicitly represented somewhere — which circles back to the declarative intent approach (Approach 4) as a complement to the tiered model. Standing descriptions of intended system behavior give review agents a baseline to evaluate whether a "bug fix" is really adding new behavior.

## Most promising direction

The combination of **Approach 1 (git as intent ledger) + Approach 2 (tiered intent)** appears strongest:

- Low-tier changes have lightweight intent requirements (or none for Tier 0)
- High-tier changes require git-tracked, CODEOWNERS-enforced authorization
- The "try it before you buy it" pattern (agents build exploratory PRs before authorization) provides high-information, low-risk exploration

This combination addresses the JIRA ACL weakness, provides audit trails, and scales across the change-type spectrum. But it needs experimentation to validate.

## Open questions

- How do agents classify a change's tier reliably? Can this be automated, or does a human need to label it?
- How do we handle emergent changes — a "small bug fix" that reveals a deeper architectural issue requiring Tier 2+ authorization?
- Can intent be composed? If three Tier 1 changes together constitute an unauthorized Tier 2 feature, who notices?
- How do we prevent the intent repo from becoming a bottleneck at agent speed?
- What does the feature file format look like? How much structure is needed for agents to evaluate programmatically, and could an AI-driven "vibe-to-spec" workflow using tools like spec-kit reliably generate this required structure (functional requirements, acceptance scenarios, state machines etc) directly from rapid human prototyping?
- How do we handle the migration from JIRA? Can the two systems coexist during transition?
- What's the relationship between intent tiers and CODEOWNERS in the target repos? Are guarded paths a proxy for "changes here are always Tier 2+"?
- Cross-repo intent: when a feature spans multiple repos, is it one feature file referencing multiple repos, or multiple feature files?
- How does the "try it" pattern work for changes that can't be meaningfully evaluated without merging? (e.g., infrastructure changes, deployment config)
- Who has authority to modify the tier definitions and authorization requirements? (See [governance.md](governance.md))
