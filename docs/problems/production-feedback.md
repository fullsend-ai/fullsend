# Production Feedback

## Problem

Any project adopting an agentic software factory model faces a general question:

**How should production signals feed back into the agents developing and maintaining the system itself?**

Instead of waiting for humans to notice failures and file issues, agents could potentially observe system behavior directly and use those signals to guide triage, prioritization, and validation of fixes. This applies to any observable production system — latency regressions, error rate spikes, resource exhaustion, SLO burn — not just pipelines.

For Konflux specifically, the most immediate and richest production signals are pipeline execution signals: PipelineRun failures, TaskRun error distributions, scheduling latency, and integration test outcomes. These are the Konflux-specific particulars of what is otherwise a general problem.

## Why this is a unique opportunity for Konflux

Most software projects must create synthetic feedback loops through tests and simulations — they have no direct window into production behavior beyond what they instrument explicitly.

As a CI/CD platform, Konflux continuously generates high-volume, structured execution data across tenant namespaces:

- Every PipelineRun completion, failure, and timeout
- Every TaskRun failure with its error category and log output
- Queue depth, scheduling latency, and resource contention signals
- Integration test outcomes across snapshots and components
- Release pipeline success and failure rates

This data reflects actual production behavior of the platform.

The question is how this signal surface should be incorporated into an agentic development lifecycle.

**The opportunity**: close the loop between what the platform does in production and what agents work on next.

## Three categories of signal

### Platform execution signals

The aggregate behavior of Konflux as a platform across all tenant namespaces:

- PipelineRun failure rates by failure category (timeout, task failure, scheduling failure, image pull failure)
- TaskRun failure distributions by task type (e.g., elevated failures in a specific build, scan, or signing task across multiple tenants)
- Queue depth and scheduling latency trends
- Integration test outcome distributions by integration scenario type
- Release pipeline failure rates and which stages fail most often

These signals reflect platform-level reliability. A spike in a specific failure category across many tenants is not a user problem — it's a platform bug.

### Tenant pipeline signals

Tenant teams use Konflux to build. The PipelineRuns in Konflux's own build-definitions and tenant namespaces are a subset of platform signals, but they carry additional meaning:

- Test failures in Konflux's own e2e test suite often correlate directly with code paths
- Build failures in Konflux's own component builds trace back to specific repos and commits
- Integration test failures across Konflux snapshots reflect cross-service compatibility breaks

These are the signals that today trigger human investigation. With structured logging and code path correlation, they can trigger agent investigation instead.

### User-reported failure patterns

GitHub issues, support tickets, and user-filed bugs represent a third signal category — lagging indicators that often correspond to platform-observable leading indicators. A user who files "my PipelineRun keeps timing out" is reporting something that should be visible in the platform signal days or weeks before the ticket appears.

Correlating user-reported problems with platform signals serves two purposes:

1. Validates that platform signals are actually capturing user-impacting failures (not just noise)
2. Provides natural language descriptions of failure modes that agents can use when generating issue content

## Potential agent interactions with signals

**Triage agent** monitors signal distributions and creates issues when failure patterns exceed thresholds — without waiting for a human to notice and report. A sustained increase in TaskRun failures across tenant namespaces for a given task type is equivalent to dozens of individual bug reports. The agent files a single well-scoped issue with affected versions, sample logs, and time-of-onset. This is signal-driven rather than report-driven triage: the signal is the bug report. Broad, multi-tenant patterns suggesting architectural root causes should be escalated to Tier 2 at creation time.

**Priority agent** weights open issues by breadth of impact (tenants affected), depth (fraction of PipelineRuns failing), duration, and rate of change. Priority updates dynamically as the signal evolves — not only when a human re-triages.

**Review agent** uses platform reliability history to calibrate scrutiny on PRs. A code path responsible for a high fraction of recent scheduling timeouts or failure spikes warrants deeper edge-case analysis than a low-traffic utility. This also feeds tier classification — a "bug fix" touching a historically high-blast-radius path may warrant Tier 2 treatment regardless of how the issue was filed.

**Implementation agent** uses failure logs, error distributions, and timing correlation as starting context — richer than a human-written issue. The agent can correlate log patterns to code paths and generate a root cause hypothesis before writing any code.

**Post-merge validation** is where the loop closes. A fix that passes CI but doesn't move the platform failure rate has not solved the problem. If the signal doesn't return to baseline after deploy, the issue re-opens and the agent flags for human review.

A distinct and more urgent case is when the merge itself causes the failure rate to increase. This is not a failed fix — it is a regression introduced by the change. The post-merge validation layer needs rollback triggers, blast radius detection, and escalation protocols for this scenario. An "andon cord" model applies: if a deploy drives a signal significantly above pre-deploy baseline, the system halts further agent-driven changes in the affected code path, initiates a revert proposal, and escalates to human investigation before proceeding. The threshold for triggering an andon cord should be lower and faster-acting than the threshold for declaring a fix successful — asymmetric risk tolerance.

## The closed-loop model

```
platform signal shows elevated failure rate
        ↓
Triage agent detects pattern, creates issue with signal data
        ↓
Priority agent weights issue by impact breadth
        ↓
Implementation agent uses failure logs as context, implements fix
        ↓
Review agents evaluate change, with execution-informed risk context
        ↓
Fix merges, deploys (existing release process)
        ↓
platform signal monitored post-deploy
        ↓
Signal returns to baseline → issue auto-closed
Signal unchanged after N iterations → loop halts, escalates to human (stopping condition)
Signal worsens significantly → andon cord: revert proposed, agent activity in path quarantined, human escalation
```

## The attribution problem

The hardest problem is distinguishing platform failures from user configuration errors or supply chain changes. A TaskRun failure spike across tenants has three candidate causes: a platform bug, simultaneous user config changes (unlikely but possible), or an external dependency change (base image, registry, upstream tool). platform signals alone don't resolve this — attribution requires correlating the spike with recent Konflux deploys, task version changes, external signals, and failure log content.

Without reliable attribution, the triage agent files issues for problems Konflux doesn't own. A platform bug causes correlated failures across tenants with different codebases; user bugs don't — but this distinction is statistical, not deterministic.

## Challenges and risks

**Signal-to-code path mapping**: Elevated failure rates don't identify the responsible code path. This requires structured error tagging in components (machine-parseable error codes mapped to code paths) or log parsing with sufficient context.

**Causal inference**: A signal change following a deploy doesn't prove causation. Multiple changes ship simultaneously. Distinguishing correlation from causation requires canary rollouts with per-cohort signal comparison, or statistical change point detection.

**Feedback latency**: Post-merge validation closes slowly when pipeline volume is low. If only a few hundred PipelineRuns execute daily in affected configurations, confirming a fix took effect may take days — creating pressure to declare success prematurely.

**Privacy and multi-tenancy boundaries**: Aggregated metrics are safe to consume; raw TaskRun logs from user pipelines may contain sensitive content. Signals must be aggregated or sanitized before entering agent context, which reduces attribution accuracy.

**Signal gaming**: A failure rate already declining before a fix deploys looks like a success. Post-merge validation must compare against baseline trends, not point-in-time snapshots.

**Alert fatigue at agent scale**: Filing an issue for every anomalous signal generates more work than can be absorbed. The triage agent needs minimum thresholds on duration, breadth, and statistical significance before acting.

**False-positive remediation loops**: When attribution misclassifies a user error or supply chain change as a platform bug, the implementation agent proposes a fix. The fix merges, deploys, and the signal is unchanged — because the root cause was never in Konflux's code. The issue re-opens and the cycle repeats. Each iteration adds real codebase changes that increase complexity without improving reliability. If reviewers see repeated small patches to the same area with no visible effect, they may begin rubber-stamping — eroding the oversight that would otherwise catch the loop.

Detection requires two complementary stopping conditions. First, iteration count: if post-merge validation shows no improvement across N consecutive iterations on the same signal, the loop must halt and escalate to human investigation. Second, cost budget: each iteration burns compute resources and token budget — CI runs, agent context processing, code review cycles. A runaway loop is not just an oversight risk; it is a measurable resource cost. Budget exhaustion per signal (e.g., cumulative agent cost above a threshold for a single issue lineage) should be an independent stopping condition, not a consequence of hitting the iteration cap. Both limits must be built in explicitly and both must trigger escalation, not silent abandonment.

Prevention: the triage agent should not generate an implementation-ready issue without sufficient corroborating evidence — a correlated deploy event, minimum cross-tenant breadth, signal-to-noise ratio above threshold, and failure log content consistent with a platform origin. Below that threshold, the output is a flagged observation for human triage, not an actionable issue.

## Relationship to other problems

This problem does not exist in isolation. The closed-loop model described above intersects with several other open problems in the repo.

**Intent representation**: When a triage agent detects a production signal that warrants a fix, the natural question is where that intent lives. In a system with a shared intent repository (e.g., an issue tracker or structured backlog that all agents read from), signal-driven issues should probably be represented the same way as human-filed ones — otherwise agents working from different input formats will have split views of system state. But there is a real question of whether production-feedback-driven issues should carry additional structured metadata (signal source, tenant breadth, time-of-onset, confidence score) that human-filed issues don't. The design of the intent schema needs to accommodate both.

There is also a governance question: if production feedback can autonomously create and prioritize intent without a human filing anything, does that change what "intent" means? A human filing an issue implies judgment. A signal crossing a threshold does not — it implies detection. The intent representation problem needs to account for this distinction.

**Stopping conditions and loop termination**: The false-positive remediation loop risk and the iteration/cost budget questions below relate to how the broader agentic loop handles convergence failure. These stopping conditions need to be represented somewhere in the agent coordination model — not just in this problem's scope.

## Open questions

- What platform-level metrics does Konflux already expose for agent consumption? How granular is the failure categorization in the existing Prometheus stack?
- How do we implement structured error tagging so failures carry machine-parseable identifiers mapping to code paths? Greenfield or extensible from existing logging?
- Should agents have direct read access to platform metrics at review time, or should reliability history be pre-computed as code-path annotations?
- What is the right granularity for code path reliability history — per-function is too noisy, per-service too coarse; per-subsystem or per-file may be right.
- Can the closed-loop model work for dependency updates — if a task version bump causes a failure spike, should the system propose an automatic revert? What approvals does that require?
- What are the right thresholds and response times for an andon cord trigger? How is blast radius assessed before a revert is proposed, and what agent activity should be quarantined while a regression is under investigation?
- What is the right stopping condition for a false-positive remediation loop — iteration count, cost budget, or both — and who resets those counters once a human has investigated? How is cumulative agent cost tracked per issue lineage?
- What attribution confidence threshold separates an implementation-ready issue from a human-triage observation, and how is that confidence measured in practice?
- Should signal-driven issue creation be gated behind human approval initially (shadow mode) before graduating to fully autonomous triage?
- How do we ensure failure signals don't leak user-sensitive content from raw TaskRun logs into agent context?
