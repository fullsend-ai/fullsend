# Trustworthiness Evidence

How does an organization build confidence that its autonomous agents are safe to trust with increasing authority? What evidence should be required before granting auto-merge, and how should that evidence be produced, composed, and maintained?

**Related:**
- [security-threat-model.md](security-threat-model.md) — defense in depth, fail closed principle
- [testing-agents.md](testing-agents.md) — CI for prompts, behavioral evaluation
- [autonomy-spectrum.md](autonomy-spectrum.md) — autonomy tiers and the decision to auto-merge
- [operational-observability.md](operational-observability.md) — understanding what agents are doing
- [cross-run-memory.md](cross-run-memory.md) — learning from prior outcomes

## The problem

The autonomy spectrum defines *when* an agent can act independently. But it does not define *what evidence* an organization should require before granting that autonomy. Today, the decision to trust an agent is largely based on human judgment: someone evaluates the agent's behavior over time, decides it seems safe, and enables auto-merge. That process is informal, non-reproducible, and does not scale.

If twenty repositories want to enable autonomous agents, each needs a trust decision. Without a structured evidence model, organizations face two failure modes:

1. **Under-trust.** Every repo requires extensive human supervision because there is no way to demonstrate that the agent is ready for autonomy. The organization gets the cost of running agents without the benefit of reduced human review.

2. **Over-trust.** An agent is granted broad autonomy based on anecdotal success ("it's been working fine for two weeks") without systematic evidence. When it fails, there is no audit trail showing what was evaluated or why trust was granted.

The missing piece is a structured portfolio of trustworthiness signals, each independently produced and verifiable, that together provide confidence proportional to the authority being granted.

## Types of trustworthiness evidence

### 1. Configuration health (static analysis)

Before an agent runs, its configuration (system instructions, skills, commands, hooks, MCP configs, agent definitions) can be analyzed statically for known problems. This is analogous to linting application code: it catches structural issues, security patterns, and quality problems without executing anything.

Categories of static checks:

- **Security patterns.** Does the configuration contain prompt injection vectors, credential access patterns, data exfiltration channels, obfuscation, or reverse shell patterns? Does it use taint-unsafe constructs where user input flows into dangerous operations?
- **Structural integrity.** Are all referenced files present? Are there circular dependencies? Is the token budget within the model's context window?
- **Quality signals.** Are instructions precise or vague? Is there redundant guidance that could confuse the agent? Are there unfinished placeholders or stale references?
- **Permission surface.** Does the MCP configuration follow least privilege? Are hook scripts scoped to their intended function? Do agent definitions restrict tool access appropriately?
- **Cross-component consistency.** Do skills overlap with each other or with system instructions? Do triggers conflict? Are dependencies between components valid?

Static analysis is fast, deterministic, and reproducible. It catches problems before they become behavior. But it only validates the configuration, not the agent's actual behavior under that configuration.

### 2. Behavioral evaluation (dynamic testing)

Does the agent produce correct outcomes when given controlled tasks? This is the behavioral complement to static analysis: it tests what the agent actually does, not just what its configuration looks like.

Behavioral evidence includes:

- **Functional test results.** Given a controlled scenario (a bug to triage, a PR to review, an issue to implement), does the agent produce the expected outcome? Scored by deterministic assertions and LLM judges.
- **Behavioral thresholds.** Does the agent complete tasks within acceptable bounds: number of turns, token cost, time to completion? An agent that produces correct output but burns $15 per triage is not trustworthy at scale.
- **Regression signals.** When the agent's configuration changes, do previously passing test cases still pass? Behavioral regression testing catches capabilities that silently disappear after instruction changes (the "absence detection" problem from [testing-agents.md](testing-agents.md)).

Behavioral evaluation is expensive and non-deterministic. It requires running the agent against real or simulated environments with real LLM calls. But it is the only way to verify that a configuration produces the intended behavior, not just that it avoids known-bad patterns.

### 3. Audit trail integrity

Can you verify what the agent actually did during a run? Trustworthiness requires accountability, which requires tamper-resistant records.

Evidence in this category:

- **Hash-chained audit logs.** Each event in the agent's activity log includes a cryptographic hash of the previous event, forming a chain. Modifying or deleting any entry breaks the chain from that point forward, making tampering detectable.
- **Post-run verification.** After each run, the harness verifies the audit chain before considering the run complete. A broken chain means the run's output cannot be trusted.
- **Provenance metadata.** Each run record captures: which configuration was used (by hash), which model, which tools were available, what the agent's input was, and what it produced. This allows forensic reconstruction of any run.

Audit integrity is a prerequisite for all other evidence. If the process log can be tampered with, behavioral test results and static analysis scores lose their evidentiary value.

### 4. Historical track record

Has the agent performed reliably over time on this repository? Past performance is a weak signal individually, but it compounds: an agent that has successfully triaged 200 issues without a revert is more trustworthy than one that has triaged 5.

Track record evidence includes:

- **Success rate.** What percentage of the agent's contributions were accepted without revision?
- **Revert frequency.** How often were the agent's changes reverted after merge?
- **Escalation rate.** How often did the agent escalate to humans, and were those escalations appropriate?
- **Cost trend.** Is the agent getting more or less expensive per task over time?

Track records are meaningful only when the configuration is stable. A configuration change resets the track record for the dimensions affected by that change.

### 5. Configuration drift detection

Has the agent's configuration changed since it was last validated? Trustworthiness evidence is only meaningful relative to a specific configuration. If the configuration drifts, the evidence may no longer apply.

This connects directly to [MCP configuration drift](mcp-config-drift.md) but applies more broadly: any change to system instructions, skills, hooks, agent definitions, or tool surface should be detected and should trigger re-evaluation of the relevant evidence categories.

## Composing evidence into trust decisions

No single evidence type is sufficient. Static analysis catches configuration problems but not behavioral failures. Behavioral testing catches behavioral problems but is expensive and non-deterministic. Audit integrity is necessary but not sufficient. Track records are meaningful but fragile.

The question is how these signals compose:

**Threshold model.** Each evidence type has a minimum passing threshold. All thresholds must be met before autonomy is granted. Simple and auditable, but rigid: a strong track record cannot compensate for a failing security scan.

**Weighted portfolio.** Each evidence type contributes a weighted score to an overall trust assessment. Allows trade-offs between categories, but introduces calibration complexity: what weights are correct?

**Tiered requirements.** Different autonomy levels require different evidence portfolios. Auto-triaging issues (low risk) requires only static analysis and basic behavioral tests. Auto-merging code (high risk) requires all five evidence types with strict thresholds. This aligns with the autonomy spectrum's per-repo, per-path granularity.

## Relationship to other problem areas

- **[Testing Agents](testing-agents.md)** — Behavioral evaluation is one type of trustworthiness evidence. The testing-agents doc covers CI for prompts; this doc frames testing as part of a broader trust portfolio.
- **[Autonomy Spectrum](autonomy-spectrum.md)** — The autonomy spectrum defines authority tiers. Trustworthiness evidence provides the basis for tier assignment: what evidence justifies granting tier N?
- **[Security Threat Model](security-threat-model.md)** — Static analysis of agent configurations catches threats at the configuration layer, before the agent runs. Security scan results are one evidence type.
- **[Operational Observability](operational-observability.md)** — Track records and audit trails overlap with observability data. The distinction: observability serves operators during and after runs; trustworthiness evidence serves the trust decision before granting autonomy.
- **[Governance](governance.md)** — Who decides what evidence is sufficient? Who reviews the thresholds? Governance determines the policy; trustworthiness evidence provides the mechanism.
- **[MCP Configuration Drift](mcp-config-drift.md)** — Drift detection is both a specific defense (for MCP configs) and a general evidence concern (any configuration change invalidates prior evidence).

## Open questions

- Who produces the evidence? Should it be self-assessed (the agent's own harness runs the checks), externally assessed (a separate system evaluates the agent), or both?
- How often must evidence be refreshed? Is a monthly security scan sufficient, or should every run re-verify?
- What happens when evidence degrades? If an agent's track record drops below threshold, is autonomy revoked immediately or after a grace period?
- Should evidence be portable across organizations? If an agent configuration is shared (via a module or template), does the evidence travel with it?
- How do we prevent evidence gaming? An agent that is optimized to pass trust checks but behaves differently on real tasks is a sophisticated adversary.
- Can static analysis of agent configurations be integrated as a preflight step in the dispatch pipeline, so that trust checks run automatically before every agent execution?
