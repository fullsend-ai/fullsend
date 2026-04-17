# Operational Observability

How do the humans responsible for a fully autonomous software factory understand what it is doing, debug it when it goes wrong, and improve it over time?

This document explores the operational challenges of running — not building — an autonomous agentic development system at organizational scale. It is distinct from [testing-agents.md](testing-agents.md) (which verifies agent behavior before deployment) and [production-feedback.md](production-feedback.md) (which feeds platform execution signals into agent work). Here the focus is on the humans and teams who operate the factory itself: what they need to see, what questions they need to answer, and what tools and practices make a system like this debuggable, improvable, and trustworthy over time.

## Why this is hard

Traditional CI/CD systems are already complex to operate, but they are deterministic: given the same inputs, you get the same outputs. Logs, metrics, and traces work well because the system's behavior is reproducible. An autonomous software factory built on LLM-based agents breaks these assumptions in several ways.

**Non-deterministic execution.** The same agent given the same PR can produce different reviews, different code, different decisions. Reproducing a failure requires capturing not just the inputs but the full execution trace — every prompt, completion, tool call, and intermediate state. Traditional "replay the request" debugging does not work. That said, non-determinism is not a fixed property of the system — it is a frontier that can be pushed back over time. When agents repeatedly make the same kind of judgment (catching a naming convention violation, flagging a missing null check), those recurring patterns can be codified into deterministic tools: a linter rule, a scanner policy, a test assertion. The agent's scope shrinks to genuinely novel judgments while the deterministic layer grows, making the system progressively more reproducible and auditable. This dynamic is explored in depth in [self-improvement-flywheel.md](self-improvement-flywheel.md).

**Opaque reasoning.** An agent's decision is the product of a system prompt, user input, model weights, temperature, and context window contents. Without capturing the full prompt/completion pairs, you cannot reconstruct why an agent did what it did. The reasoning is not in the code — it is in the model interaction. This is fundamentally different from debugging traditional software where you can read the source and step through the logic.

**Distributed agency.** In a multi-agent system (see [code-review.md](code-review.md), [agent-architecture.md](agent-architecture.md)), a single PR review involves multiple independent agents — triage, intent alignment, correctness, security, injection defense — each making separate decisions that compose into an outcome. Understanding "why did this PR get approved" requires tracing across all of them. This is analogous to distributed tracing in microservices, but harder because each "service" is non-deterministic.

**Scale of activity.** A mid-to-large org may have dozens of repos with heterogeneous languages, frameworks, and deployment patterns. Agents operating across all of them generate a volume of decisions, reviews, and changes that no human can read in full. The operators need aggregated views, anomaly detection, and drill-down capabilities — not raw logs.

**Slow feedback loops.** Some agent failures are obvious and immediate (crash, wrong syntax, failed CI). Others are subtle and delayed: an agent that slowly drifts toward approving lower-quality code, or one whose cost creeps up because prompt changes increased token usage. These problems are only visible through trend analysis over days or weeks, not individual event inspection.

**Community operation.** In an open-source project, the "operators" are not a dedicated SRE team. They are contributors, maintainers, and SIG leads who also write code, review PRs, and manage releases. Operational tooling must be accessible to people who are not full-time operators and who may not have deep observability expertise.

## What operators need to understand

The central question is not "what data should we collect" but "what questions do operators need to answer?" The data and tooling should follow from those questions.

### Is the system working?

The most basic operational question. At a glance, an operator should be able to tell:

- Are agents running? Are they picking up events (new PRs, issues, signals) within expected timeframes?
- Are agents completing their work? What is the success/failure rate of agent runs?
- Are there backlogs building up — PRs waiting for review, issues waiting for triage?
- Are any agents stuck, crashed, or in error loops?

This is the "dashboard on the wall" level of observability — system health, not individual decisions. It should be answerable without drilling into any specific agent run.

### What just happened?

When something goes wrong — a bad merge, a missed vulnerability, an unexpected approval — the operator needs to reconstruct the sequence of events:

- Which agent(s) acted on this PR/issue?
- What inputs did each agent receive (diff, context, prior agent outputs)?
- What instructions was each agent operating under (prompt version, model version)?
- What did each agent decide, and what reasoning did it produce?
- If multiple agents were involved, how did their individual decisions compose into the final outcome?

This is the debugging use case. It requires structured traces of individual agent runs that can be correlated into end-to-end workflows. The trace is the unit of debugging — when something goes wrong, you pull the trace and read it.

### Is the system getting better or worse?

Beyond individual incidents, operators need trend-level visibility:

- Are agent decisions improving over time (fewer human overrides, fewer reverts, fewer false positives)?
- Is cost per decision stable, increasing, or decreasing? Did a recent prompt change affect token usage?
- Are certain repos or agent types consuming disproportionate resources?
- Has a model update changed agent behavior? (See [testing-agents.md](testing-agents.md), "Measuring agent capability drift.")
- Are human overrides concentrated in specific areas, suggesting the agent needs better instructions for those cases?

This requires capturing human feedback signals (overrides, reverts, manual corrections) and correlating them with agent traces over time. It is not enough to know that an agent made a decision — you need to know whether that decision was ultimately right.

### Where should we invest improvement effort?

The factory is not static — it needs continuous tuning. Operators and contributors need to identify where improvements will have the most impact:

- Which agent roles have the highest error rates or override rates?
- Which repos are hardest for agents to work with (see [repo-readiness.md](repo-readiness.md), [agent-compatible-code.md](agent-compatible-code.md))?
- Which types of changes cause the most agent failures?
- What prompt or instruction changes have been most effective, and which were counterproductive?
- Are there recurring failure patterns that suggest a missing agent capability or a gap in codebase context (see [codebase-context.md](codebase-context.md))?

This is the continuous improvement use case. It requires not just data collection but analysis: connecting outcomes to causes and surfacing actionable insights.

### How much does it cost?

LLM-based agents have real per-invocation costs. At org scale across dozens of repos and multiple agent types, cost is an operational concern:

- What is the total cost of agent operations per day/week/month?
- How does cost break down by agent role, by repo, by model?
- Are there cost anomalies — a single PR review that cost 10x the median, or a repo where costs suddenly spiked?
- What is the cost trend? Are we becoming more efficient or less?
- Can we attribute cost to value — e.g., cost per successful merge, cost per caught vulnerability, cost per avoided regression?

Cost observability is not just a finance concern. Cost anomalies are often symptoms of other problems: an agent in a retry loop, a prompt that causes excessive tool calls, or a context window that is being filled with irrelevant content.

## Debuggability as a first-class concern

Traditional software systems become debuggable through a combination of structured logging, distributed tracing, error reporting, and reproducibility. Autonomous agent systems need equivalents for each, but adapted to the unique challenges.

### Structured traces

Every agent invocation should produce a structured trace capturing the full lifecycle: input references, system prompt (version-pinned), each LLM call (prompt, completion, model, token usage, latency), each tool call (operation, timing, result), the decision output, and metadata (repo, PR, agent role, instruction version, timestamp).

A key insight: much of an agent's input context is already stored durably in existing systems. The PR diff lives in GitHub. The codebase state is a git SHA. The issue description is on the issue tracker. Traces should reference these by pointer (repo + SHA, PR URL, issue URL) rather than duplicating them. The novel data that traces must capture is what those existing systems *don't* store: the LLM interactions themselves — the prompts assembled from that context, the completions returned, the tool calls made, and the reasoning that led to the decision. This distinction matters for storage cost, data sensitivity, and replay: to reconstruct an agent run, you combine the trace's LLM interaction log with the context retrievable from git and GitHub at the recorded references.

Traces need to be correlatable. A PR review workflow involving five agents needs a session-level view that shows all five traces, their ordering, and how their outputs composed into the final decision. This is the LLM equivalent of distributed tracing in microservices — but with the additional challenge that each "span" contains non-deterministic reasoning, not just timing and status.

### Human feedback capture

When a human overrides an agent decision — rejects a review, reverts a merge, manually fixes something the agent got wrong — that override is a signal. The observability system should capture:

- What the agent decided
- What the human did instead
- The agent trace that led to the overridden decision

Over time, these overrides become the most valuable data for improving agent instructions. They are ground truth about where the system is wrong. Without systematic capture, this feedback is lost in PR comments and Slack threads.

### Replay and counterfactual analysis

When debugging a bad agent decision, operators want to ask "what if": what if the prompt had been different? What if the context had included this file? What if a different model had been used? This requires the ability to replay an agent run with modified inputs — not against the live system, but in a sandbox.

This connects to the evaluation infrastructure in [testing-agents.md](testing-agents.md). The difference is that testing uses synthetic inputs; debuggability replay uses real production traces as inputs. The same infrastructure can serve both purposes if traces capture sufficient detail.

### Anomaly detection

At scale, no human can review every agent trace. The system needs automated anomaly detection:

- **Cost anomalies** — an agent run that costs 10x the median for its type
- **Latency anomalies** — an agent that takes significantly longer than usual
- **Behavioral anomalies** — an agent that suddenly starts approving (or rejecting) at a different rate than its historical baseline
- **Error rate spikes** — a sudden increase in agent failures or retries
- **Drift detection** — gradual shifts in agent behavior over time, even without instruction changes (see [testing-agents.md](testing-agents.md))

Anomaly detection is the bridge between "dashboard on the wall" health monitoring and "pull the trace" debugging. It tells operators where to look.

## The community operating model

In a corporate setting, a dedicated platform team might operate the agent factory full-time. In an open-source community, the operating model is different:

**Part-time operators.** The people tuning agent instructions, investigating failures, and monitoring costs are also writing code, reviewing PRs, and participating in SIG meetings. Tooling must surface important information proactively rather than requiring active monitoring.

**Distributed ownership.** Different SIGs own different repos. The SIG that owns a component should be able to see how agents are performing on their repos without needing access to the entire system. Per-repo views and per-SIG dashboards matter.

**Varied expertise.** Some contributors are deeply technical and comfortable with trace analysis. Others are domain experts who want to know "is the agent doing a good job on my repo" without reading prompt/completion pairs. The tooling needs multiple levels of abstraction: high-level health dashboards, mid-level trend analysis, and low-level trace inspection.

**Transparency as trust.** In open source, trust is earned through transparency. If the community cannot see what agents are doing and verify that they are behaving well, they will not trust the system regardless of how well it actually works. Observability is not just an operational tool — it is a social contract. This connects directly to the vision principle: "Transparency over trust. Every agent action should be auditable. Every decision should be traceable to its inputs."

**Onboarding.** New contributors need to understand not just how to write code but how the factory operates. Operational observability tooling is part of the contributor experience — it answers "what happens after I open a PR?" and "why did the agent say that?"

## Approaches to tooling

### LLM-specific observability platforms

Platforms like [Langfuse](https://langfuse.com/), [Arize Phoenix](https://phoenix.arize.com/), and [OpenLIT](https://openlit.io/) are purpose-built for LLM application observability. They provide structured tracing of LLM calls, token/cost tracking, evaluation frameworks, and prompt management. Langfuse in particular is open-source, self-hostable, and built on OpenTelemetry, which aligns with common open-source constraints around data residency and vendor independence.

These platforms address the debuggability problem directly: they capture the prompt/completion pairs, tool calls, and token usage that make individual agent runs inspectable. They also provide evaluation scoring that can track agent quality over time.

**Where they fit:** Individual agent run tracing, cost attribution, prompt version comparison, evaluation scoring, drift detection at the agent level.

**Where they fall short:** They are designed for single-application observability. The multi-agent, multi-repo, community-operated factory model requires correlation across agents, per-repo views, and community-accessible dashboards that these platforms do not provide out of the box. They are a component of the solution, not the whole solution.

### Existing infrastructure (Prometheus, Grafana, OpenTelemetry)

If the org already runs Prometheus, Grafana, and an OpenTelemetry collector, agent observability can be built as a layer on top. Agents emit OTel spans and custom metrics; Grafana provides dashboards; Alertmanager handles alerting.

**Where this fits:** System health monitoring, cost and latency metrics, alerting, integration with existing operational practices.

**Where it falls short:** General-purpose observability tools have no native concept of "prompt/completion pair," "token cost," or "evaluation score." The gap between "we have spans" and "we can debug why an agent made a bad decision" is significant. You can see *that* something happened but not *why*.

### Hybrid approach

The most realistic path combines both:

- An LLM observability platform (e.g., Langfuse) captures detailed agent traces — the prompt/completion pairs, tool calls, token usage, and evaluation scores that make individual runs debuggable.
- The existing metrics/dashboards stack (Prometheus/Grafana) provides system-level health monitoring, cost trending, alerting, and the "dashboard on the wall" view.
- A correlation layer links the two: a Grafana dashboard shows an anomalous cost spike; clicking through leads to the Langfuse trace of the expensive run.

This avoids trying to make Grafana do LLM-specific trace analysis, and avoids trying to make Langfuse do system-level fleet monitoring.

### Structured logging as a starting point

Before investing in platforms, a minimal viable approach is structured logging: every agent writes a JSON log per invocation with key fields (input hash, output, model, tokens, cost, latency, decision, instruction version, repo, PR). Logs go into existing log aggregation. Analysis is ad-hoc.

This works for early experimentation when the volume is low and the operators are the same people building the agents. It does not scale to continuous monitoring, trend analysis, or community operation — but it is zero-infrastructure and provides the raw data that a platform would later consume.

## Relationship to other problem areas

- **[Agent Infrastructure](agent-infrastructure.md)** — Infrastructure determines what observability is possible. Where agents run constrains whether we can instrument their execution, access their traces, and correlate across runs. The infrastructure document lists observability as one of four things agents need; this document explores what that means in practice.
- **[Testing the Agents](testing-agents.md)** — Testing verifies behavior before deployment; operational observability monitors behavior in production. They share infrastructure: evaluation frameworks, golden-set datasets, scoring metrics. Traces captured in production can become test inputs. Drift detection spans both — testing catches drift in controlled environments, observability catches it in production.
- **[Production Feedback](production-feedback.md)** — Production feedback is about platform execution signals (PipelineRun failures, task errors) feeding into agent work. Operational observability is about the agents' own execution being visible to operators. They intersect when an agent processes a production signal — the agent's trace shows how it interpreted and acted on that signal. They also share the cost tracking concern: production feedback loops that enter false-positive remediation cycles (see production-feedback.md) are detectable through cost anomaly monitoring.
- **[Security Threat Model](security-threat-model.md)** — Auditability is a cross-cutting security principle: "every action is logged, attributable, and reviewable." Operational observability implements that principle. Traces provide the audit trail for every agent action. Behavioral anomaly detection (unusual approval patterns, unexpected cost spikes) is also a security signal — a compromised agent may behave differently from its baseline.
- **[Governance](governance.md)** — Governance requires accountability: "trace an agent action back to the policy that authorized it." Observability provides the data. It also provides the feedback loop for governance decisions: if a policy change (e.g., adjusting autonomy levels per [autonomy-spectrum.md](autonomy-spectrum.md)) has unintended consequences, observability is how operators detect that.
- **[Code Review](code-review.md)** — The multi-agent review workflow is the most complex to trace and the most important to debug. A bad review decision that leads to a merged vulnerability needs a full audit trail across all participating sub-agents.
- **[Human Factors](human-factors.md)** — Observability affects the human experience of working alongside agents. Review fatigue, domain expertise atrophy, and contributor motivation are all influenced by whether humans can understand and trust what the agents are doing. Good observability does not just serve operators — it serves every contributor who interacts with agent decisions.
- **[Self-Improvement Flywheel](self-improvement-flywheel.md)** — Observability provides the raw data the flywheel needs: traces of agent decisions, human overrides, and correction patterns. The flywheel consumes this data to propose improvements. Observability also answers a key flywheel question — "is the system getting better?" — by tracking whether non-deterministic agent judgments are being successfully codified into deterministic tools over time (see "Non-deterministic execution" above).

## Open questions

- What is the right level of trace granularity? Input context is largely already stored in git and GitHub, so traces can reference it by pointer. But full prompt/completion pairs — the novel data — are expensive to store and may contain sensitive content. Is there a middle ground — e.g., capturing token counts and decision summaries by default, with full prompt/completion pairs available on demand?
- How should trace access be controlled? (JSONL trace exposure decided in [ADR 0021](../ADRs/0021-jsonl-reasoning-trace-exposure.md): owner-scoped storage with credential scanning as defense-in-depth. Broader question of balancing security and transparency for non-JSONL observability data remains open.)
- What retention policy applies to traces? Indefinite retention supports audit requirements but increases storage cost and data sensitivity exposure. Time-bounded retention (e.g., 90 days) limits exposure but may lose traces needed for incident investigation.
- How do we measure "is the system getting better"? What metrics constitute a meaningful quality signal for an autonomous software factory? Merge revert rate? Human override rate? Time-to-review? Cost per decision? Some composite score? The choice of metric shapes what gets optimized.
- At what scale does a dedicated LLM observability platform justify its operational overhead (Postgres, ClickHouse, Redis, S3 for something like Langfuse)? Is there a threshold of agent activity below which structured logging suffices?
- How do we handle the bootstrapping problem — the factory needs observability to improve, but building the observability infrastructure is itself work that competes with building the factory?
- Should observability data feed back into agent instructions automatically (e.g., auto-adjusting prompts when false positive rates exceed a threshold), or should it only inform human-driven instruction changes? Automatic feedback creates the risk of instruction oscillation; human-only feedback is slower but more controlled.
- How do we build community dashboards that are useful to contributors with different levels of technical depth — from "is the agent doing a good job on my repo" to "show me the trace of this specific review"?
- What is the cost of observability itself? Storing traces, running evaluators, maintaining dashboards — this has infrastructure cost. At what scale does it pay for itself in debugging time saved and quality improvement?
- How do we prevent observability from becoming a checkbox exercise — collecting data that nobody looks at? What practices ensure that operational data actually drives improvement?
