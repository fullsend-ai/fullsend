# Graduated Approval Policy

Moving from binary approve/reject to risk-scored approval routing. The current system makes a single decision (approve or request changes) without distinguishing between "this is clearly safe" and "this is probably fine but i'm not confident."

**Related:**
- [security-threat-model.md](security-threat-model.md) — defense in depth, fail closed principle
- [autonomy-spectrum.md](autonomy-spectrum.md) — autonomy tiers
- [agent-architecture.md](agent-architecture.md) — agent roles and coordination
- [code-review.md](code-review.md) — review agent responsibilities

## The problem

Review agents currently produce a binary verdict: approve or request changes. This binary forces a choice between two failure modes:

1. **Too aggressive (approve too much).** The agent approves changes it is not confident about because the alternative is blocking legitimate work. Medium-confidence findings get suppressed to avoid false-positive noise. The result: real issues slip through. Examples:
   - Submodule bumps approved without inspecting the actual changes ([#1462](https://github.com/fullsend-ai/fullsend/issues/1462))
   - Medium-severity correctness findings auto-approved instead of escalated ([#1453](https://github.com/fullsend-ai/fullsend/issues/1453))
   - Author uncertainty signals ("i think this is right but i'm not sure") ignored in approval decisions ([#1143](https://github.com/fullsend-ai/fullsend/issues/1143))

2. **Too conservative (block too much).** The agent requests changes on anything uncertain, creating a bottleneck that requires human intervention for changes that are almost certainly fine. The result: humans spend time on low-risk reviews, defeating the purpose of automation.

The binary model forces this trade-off because there is no middle ground between "i approve this" and "i block this." Human reviewers have a richer vocabulary: "LGTM", "looks fine to me but get a second pair of eyes on the crypto changes", "i'd approve this but the test coverage concerns me, can someone from the testing team check?" Agents have only approve/reject.

## Risk scoring

Instead of a binary verdict, the review agent (or a dedicated scoring layer) assigns a risk score to each PR based on multiple signals.

### Input signals

**From the diff itself:**
- Files changed: security-sensitive paths (auth, crypto, permissions, deployment) score higher
- Change type: deletions and modifications to existing code score higher, but additions of new attack surface (API endpoints, dependencies, permission grants) should be scored independently since they can have equal or greater blast radius
- Change scope: cross-module changes score higher than single-file changes
- Test coverage: changes without corresponding test changes score higher
- Binary/opaque files: non-reviewable content scores highest

**From the review agent's findings:**
- Number and severity of findings
- Confidence level of each finding (if the review agent can express uncertainty)
- Whether findings were contested between sub-agents (disagreement = higher risk)

**From context:**
- Author history: first-time contributors score higher than established maintainers
- Branch target: changes to default/release branches score higher than feature branches
- Time pressure: changes during merge freezes or release windows score higher
- Recency of related changes: changes touching recently-modified code score higher (higher chance of conflict or regression)

### Routing rules

The risk score maps to an action:

| Score range | Action | Rationale |
|---|---|---|
| Low (0-30) | Auto-approve | High confidence, low impact, routine changes |
| Medium-low (30-50) | Approve with advisory comment | Approve, but leave a comment noting areas of uncertainty for the author's awareness |
| Medium (50-70) | Request secondary review | Approve is not appropriate, but blocking is too aggressive. Label the PR for human review with a summary of concerns |
| High (70-90) | Request changes with explanation | Block the PR and explain what needs to change. The agent is confident enough in its findings to take a position |
| Critical (90-100) | Block and alert | Security-critical concern. Block the PR, add a security label, and notify the security team or repo owner directly |

The thresholds and actions should be configurable per repository and per organization. What counts as "low risk" varies significantly between a documentation repo and a production service.

## Approaches

### Approach 1: Scoring in the review agent

The review agent itself computes a risk score as part of its verdict. The harness reads the score from the agent's structured output and routes accordingly.

**Trade-offs:**
- Simplest implementation (extends existing output schema)
- The agent's risk assessment is influenced by the same context that might be poisoned
- Difficult to calibrate across different models and prompt versions
- Score consistency depends on the model's calibration, which is not guaranteed

### Approach 2: Separate scoring layer in the harness

A deterministic scoring function in the harness computes the risk score based on observable signals (diff stats, file paths, test coverage, author metadata). The review agent's findings feed into the score as one input, but the score is not entirely model-dependent.

**Trade-offs:**
- More robust against model manipulation (deterministic components are not influenced by prompt injection)
- Harder to capture nuanced signals that require understanding the code (e.g., "this change modifies error handling in a way that could mask failures")
- Requires maintaining scoring heuristics that may drift from actual risk
- Can combine with Approach 1: deterministic base score adjusted by agent assessment

### Approach 3: Multi-reviewer consensus

Instead of scoring, require multiple independent review agents to agree. Disagreement between reviewers triggers escalation to a human.

**Trade-offs:**
- Naturally handles uncertainty (disagreement = uncertainty = escalation)
- Expensive (multiple LLM runs per PR)
- Does not produce a continuous risk score, so routing is less granular
- The parallel sub-agent architecture ([PR #1550](https://github.com/fullsend-ai/fullsend/pull/1550)) includes a Challenger role that contests findings. Note: the Challenger is currently an intra-agent verification step within the orchestrator's own process (step 6e), not disagreement between independent sub-agents. The gap is that Challenger outcomes do not currently influence the approval decision; they only affect whether a finding survives.

## Relationship to existing mechanisms

**Autonomy spectrum.** The [autonomy-spectrum.md](autonomy-spectrum.md) defines a binary per-repo autonomy model (autonomous or not). Separately, [intent-representation.md](intent-representation.md) defines per-change intent tiers (Tier 0-3) based on change type (standing rules, tactical, strategic, organizational). Graduated approval operates orthogonally to both: even among changes at the same intent tier within an autonomous repo, some are higher risk than others. The autonomy spectrum determines *whether* an agent can act; graduated approval determines *how confidently* it should act.

**CODEOWNERS.** CODEOWNERS enforces mandatory human approval for specific paths. Graduated approval operates on the paths that CODEOWNERS does not cover. For CODEOWNERS-guarded paths, the graduated policy does not apply (human review is always required). For mixed-path PRs that touch both CODEOWNERS-guarded and non-guarded paths, the CODEOWNERS requirement takes precedence for the entire PR (human review is required regardless of risk score), but the risk score still provides useful context to the human reviewer about the non-guarded portion.

**Security hooks.** The existing PreToolUse/PostToolUse security hooks operate at the tool-call level during agent execution. Graduated approval operates at the PR level after execution. They address different layers: hooks prevent dangerous actions during the run; graduated approval evaluates the output of the run.

**Tool call risk assessment.** If a tool call risk assessment layer is implemented (see PR [#2009](https://github.com/fullsend-ai/fullsend/pull/2009)), its findings during the run could feed into the PR-level risk score. A run that triggered multiple high-risk tool call blocks (even if the blocks succeeded) is itself a signal that the agent was attempting unusual operations.

## Open questions

- How do we calibrate risk scores? What data would we need to tune thresholds (historical PR outcomes, incident correlation)?
- Should the scoring model be the same across all repos, or should each repo train its own baseline of what "normal risk" looks like?
- How do we handle the case where a risk score is high but the review agent's findings are all low-severity? (The signals disagree.)
- Should risk scores be visible in the PR (as a comment or label), or only used internally for routing?
- How do we prevent score gaming? If the scoring heuristics are public, an attacker could craft changes that score low while being high-risk.
- What is the right escalation path for "medium" scores? A GitHub label? A Slack notification? A required reviewer assignment?
- Should risk scores influence the autonomy tier classification, or should they remain a separate axis?
