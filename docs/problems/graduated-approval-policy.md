# Graduated Approval Policy

Review agents currently make a single binary decision: approve or request changes. There is no middle ground. This forces a trade-off between two failure modes that human reviewers do not face, because humans naturally express graduated confidence.

**Related:**
- [security-threat-model.md](security-threat-model.md) — defense in depth, fail closed principle
- [autonomy-spectrum.md](autonomy-spectrum.md) — per-repo autonomy model
- [intent-representation.md](intent-representation.md) — intent authorization tiers
- [agent-architecture.md](agent-architecture.md) — agent roles and coordination
- [code-review.md](code-review.md) — review agent responsibilities

## The problem

### Failure mode 1: too aggressive

The agent approves changes it is not confident about because the alternative is blocking legitimate work. Medium-confidence findings get suppressed to avoid false-positive noise. The result: real issues slip through.

Examples from observed agent behavior:
- Submodule bumps approved without inspecting the actual changes ([#1462](https://github.com/fullsend-ai/fullsend/issues/1462)). The agent saw a "version bump" and approved, but the submodule contained security-relevant code changes that were never reviewed.
- Medium-severity correctness findings auto-approved instead of escalated ([#1453](https://github.com/fullsend-ai/fullsend/issues/1453)). The agent suppressed a finding to avoid a false-positive block, but the finding was real.
- Author uncertainty signals ("i think this is right but i'm not sure") ignored in approval decisions ([#1143](https://github.com/fullsend-ai/fullsend/issues/1143)). The agent treated the PR as any other, missing a clear signal that the author wanted additional scrutiny.

### Failure mode 2: too conservative

The agent requests changes on anything uncertain, creating a bottleneck that requires human intervention for changes that are almost certainly fine. Humans spend time reviewing low-risk changes, defeating the purpose of automation.

### Why binary verdicts cause this

The binary model forces this trade-off because there is no middle ground between "i approve this" and "i block this." Human reviewers have a richer vocabulary:

- "LGTM"
- "Looks fine to me but get a second pair of eyes on the crypto changes"
- "i'd approve this but the test coverage concerns me, can someone from the testing team check?"
- "Approving, but flagging: this touches auth code and the original author isn't sure about it"

These graduated responses do not exist for agents today. The agent must choose: approve or block. There is no "approve with concerns," no "flag for additional review," no way to route different parts of a PR to different reviewers based on confidence.

## What graduated approval would need

The core requirement is that the review outcome carries more information than a boolean. At minimum, the verdict should express:

1. **Confidence level.** How certain is the review agent in its assessment? A PR where all sub-agents agree and findings are clear is different from one where sub-agents disagreed or findings were borderline.

2. **Risk signals.** What specific factors increase or decrease risk? This might include which files changed (security-sensitive paths vs. documentation), whether tests were included, whether the change scope is narrow or broad, and whether the author is established or new.

3. **Routing recommendation.** Based on confidence and risk, what should happen next? This could range from "auto-approve, no concerns" through "approve but flag for awareness" to "escalate to human reviewer" to "block and alert."

## What we do not yet know

The hard problems are not in the concept but in the implementation:

- **Calibration.** How do you ensure that a "medium risk" score from the review agent today means the same thing as "medium risk" next month, after a model update or instruction change? Without calibration, risk scores are meaningless numbers.

- **Evidence for thresholds.** What data would justify setting auto-approve at score 30 vs. 40 vs. 50? The thresholds need to be derived from observed outcomes, not guessed. This connects to the eval framework: if we had eval cases where the current binary verdict gets it wrong, we could measure whether graduated scoring produces better outcomes on those cases.

- **Gaming resistance.** If the scoring is done by the review agent itself, a compromised or poorly-calibrated agent could assign low risk scores to manipulate routing. Deterministic scoring based on observable signals (diff stats, file paths, author metadata) is more robust but cannot capture nuanced risk factors that require understanding the code.

- **Sub-agent disagreement.** The parallel sub-agent architecture already has a Challenger role that contests findings. But disagreement between sub-agents does not currently influence the approval decision. It only affects whether a finding survives. If two sub-agents disagree about a finding, that disagreement is itself a risk signal, but there is no mechanism to capture it.

- **Interaction with CODEOWNERS.** For CODEOWNERS-guarded paths, human review is always required regardless of risk score. But what about PRs that touch both guarded and non-guarded paths? Does the graduated policy apply to the non-guarded portion, or is the entire PR treated as requiring human review because part of it does?

## Relationship to existing mechanisms

**Autonomy spectrum.** The autonomy spectrum defines per-repo autonomy based on change type. Graduated approval operates within a given autonomy level: even among changes classified at the same tier, some are higher risk than others. The autonomy spectrum determines *whether* an agent can act; graduated approval determines *how confidently* it should act.

**Intent authorization tiers.** The tiered intent system in [intent-representation.md](intent-representation.md) classifies changes by their authorization requirements (Tier 0-3). Graduated approval could use the intent tier as one input signal: a Tier 0 change (pre-authorized, like a dependency bump) gets a lower base risk score than a Tier 2 change (new feature requiring review).

**Security hooks.** The existing PreToolUse/PostToolUse security hooks operate at the tool-call level during agent execution. Graduated approval operates at the PR level after execution. They address different layers: hooks prevent dangerous actions during the run; graduated approval evaluates the output of the run.

**Tool call risk assessment.** If a tool call risk assessment layer is implemented, its findings during the run could feed into the PR-level risk score. A run that triggered multiple high-risk tool call warnings (even if the warnings did not block execution) is itself a signal that the agent was attempting unusual operations.

## Path forward

Ralph Bean noted on [PR #2012](https://github.com/fullsend-ai/fullsend/pull/2012) that this area needs eval coverage before architectural proposals. The right sequence:

1. **Write eval cases** where the current binary verdict gets it wrong (approves something it shouldn't, blocks something it shouldn't). These cases demonstrate the problem concretely.
2. **Show that graduated scoring produces better outcomes** on those cases. This is TDD for agent architecture.
3. **Propose the ADR** with those cases as evidence, and let the evals tell us if it's actually better.

This document intentionally stays at the problem level. The solution architecture, threshold design, and implementation approach belong in an ADR backed by evaluation evidence.

## Open questions

- What eval cases best demonstrate the binary verdict's failure modes? The three issues cited above (#1462, #1453, #1143) are starting points, but more cases would strengthen the evidence.
- Can we measure the current binary verdict's false-positive and false-negative rates on historical PRs?
- Should graduated approval be an agent-level change (the review agent outputs richer verdicts) or a harness-level change (the harness interprets review output and routes accordingly)?
- How does graduated approval interact with the functional test framework (#1682)? Can behavioral thresholds (max_turns, max_cost_usd) feed into a risk score?
