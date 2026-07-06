# Flapping and Convergence

What happens when agents get stuck in loops, oscillate between competing approaches, or fail to converge on a stable outcome? Detection, circuit-breaking, and remediation for non-converging agent behavior.

**Related:**
- [agent-architecture.md](agent-architecture.md) — agent roles and coordination
- [code-review.md](code-review.md) — review agent behavior
- [autonomy-spectrum.md](autonomy-spectrum.md) — autonomy and escalation
- [adaptive-agent-selection.md](adaptive-agent-selection.md) — choosing agent configurations
- [cross-run-memory.md](cross-run-memory.md) — learning from prior failures

## The problem

Agents can get stuck in loops where they repeatedly attempt and fail the same task, or oscillate between competing approaches without settling on a solution. This "flapping" wastes LLM spend, blocks real work, and can pollute PR histories with dozens of failed attempts.

Flapping is distinct from normal retry behavior. A single retry after a transient failure is healthy. Flapping is when the system does not converge: each attempt either reproduces the same failure or introduces a new one that reverses the previous fix.

### Types of flapping

**Fix-break oscillation.** An agent fixes file X, which breaks file Y. It then fixes file Y, which breaks file X again. The agent cycles between two states, never reaching one where both files work. This is common when changes have coupled dependencies that the agent does not model holistically.

**Review ping-pong.** An agent submits a PR. The review agent requests changes. The code agent addresses the feedback, but the fix introduces a new issue. The review agent catches the new issue. The code agent fixes it but reintroduces the original problem. The PR accumulates comments without progress.

**Approach churn.** An agent tries approach A, it fails. It tries approach B, it also fails. It goes back to approach A (having forgotten it already tried it, or hoping conditions changed). This is especially common in stateless agents with no cross-run memory.

**Flaky-test loops.** An agent adds a test, the test passes locally but fails in CI (flaky). The agent modifies the test, CI passes, but a different test becomes flaky. The agent chases intermittent failures indefinitely.

### Why this matters at scale

A single flapping agent on one repo is annoying. Twenty flapping agents across an organization's repositories are a resource drain. At fullsend's target scale (20+ repos, multiple agent roles per repo), flapping becomes an operational problem:

- **Cost.** Each iteration consumes LLM tokens. Eight review cycles at $2-5 per cycle on a single PR adds up across hundreds of PRs.
- **Noise.** PRs with 40+ bot comments are impossible for humans to review. The signal is buried in the noise of failed attempts.
- **Blocking.** While an agent flaps on one issue, other issues in the same repo may be queued behind it, depending on concurrency limits.
- **Credibility.** Human maintainers who see agents repeatedly failing lose confidence in the system, even for tasks the agents handle well.

## Detection

### Cycle detection via state hashing

The harness tracks a fingerprint of each iteration's key state: the approach taken, the files modified, the test results, and the review outcome. If the same fingerprint appears twice within a configurable window (default: 3 iterations), the agent is cycling.

The fingerprint does not need to be an exact match. Fuzzy matching (e.g., "the same files were modified in similar ways") catches near-cycles where the agent makes trivially different changes that produce the same failure.

### Diff-distance damping

Each iteration's diff is compared to the previous iteration's diff. If the diffs are inverses (lines added in iteration N are removed in iteration N+1, and vice versa), the agent is oscillating. A damping metric measures how much of each diff reverses the previous diff: a value above a threshold (e.g., 70% reversal) triggers a flapping alert.

### Cost and turn accounting

The functional test framework (#1682) already enforces `max_turns` and `max_cost_usd` per run. Extending these thresholds across retries catches slow-burn flapping: an agent that uses 5 turns per attempt but makes 8 attempts has consumed 40 turns total, which may exceed the cross-run budget even if each individual run was within bounds.

### Review comment velocity

A simpler heuristic: if a PR accumulates more than N review comments (e.g., 20) without being merged or closed, something is wrong. This does not distinguish flapping from genuinely complex reviews, but it flags PRs that need human attention.

## Response strategies

### Circuit breaker with escalation

After detecting flapping (by any of the methods above), the harness stops the agent and escalates to a human. The escalation message includes: what was attempted, how many iterations, what the recurring failure pattern is, and a recommendation for what a human should look at.

This is the safest response. It prevents further waste and gives humans the context they need to intervene efficiently. The downside: it puts work back on humans, which is what automation was supposed to reduce.

### Strategy rotation

Instead of escalating immediately, the harness tries a different agent configuration. If the code agent with default instructions is flapping, try a different model, different temperature, or a specialized skill. If the review agent and code agent are ping-ponging, try a different review configuration.

This connects to [adaptive-agent-selection.md](adaptive-agent-selection.md): the selection mechanism could treat flapping as a negative fitness signal for the current configuration and promote an alternative.

The risk: strategy rotation without a flapping budget can itself become a form of flapping at the meta-level, cycling through configurations without converging.

### Cooldown with context injection

Pause the agent for a cooldown period, then resume with additional context: "you have attempted this task N times. Previous attempts failed because [summary]. Do not repeat approach X." This requires some form of cross-run memory (see [cross-run-memory.md](cross-run-memory.md)), which has its own trust and safety implications.

### Abandon and report

After exhausting retry and strategy budgets, close the PR or issue with a structured report: what was tried, why it failed, and what a human would need to resolve it. This is the most honest response: some tasks are beyond the agent's current capability, and acknowledging that is better than wasting resources.

## Thresholds and configuration

Flapping detection needs configurable thresholds because different repos and task types have different tolerance for iteration:

- A documentation repo might tolerate only 2 review cycles before escalating.
- A complex backend service might allow 5 cycles before escalating, because multi-file changes legitimately require more iteration.
- Security-sensitive paths should have lower thresholds than non-sensitive paths.

Default thresholds should be conservative (escalate early) and configurable per repo and per agent role.

## Relationship to other problem areas

- **[Adaptive Agent Selection](adaptive-agent-selection.md)** — Flapping is a negative fitness signal. Configurations that produce flapping should be penalized in the selection mechanism. Strategy rotation is a form of adaptive selection triggered by failure.
- **[Cross-Run Memory](cross-run-memory.md)** — Context injection during cooldown requires memory. Flapping detection across runs requires tracking prior attempts. Both create the trust and poisoning concerns that cross-run memory addresses.
- **[Operational Observability](operational-observability.md)** — Flapping agents are a key operational concern. Dashboards should surface flapping rate per repo, per agent role, and per task type.
- **[Code Review](code-review.md)** — Review ping-pong is one specific form of flapping. The code-review doc describes the review process but does not address what happens when the process does not converge.
- **[Trustworthiness Evidence](trustworthiness-evidence.md)** — Flapping rate is a negative trustworthiness signal. An agent that flaps frequently on a repo is less trustworthy than one that converges reliably.

## Open questions

- What is the right default flapping budget (max iterations before circuit-breaking)? Should it be count-based, cost-based, or time-based?
- Should strategy rotation happen before or after human escalation? Or should it be a configurable policy?
- How should flapping be reported to humans? A structured report is ideal, but generating a useful summary of N failed attempts is itself a non-trivial task.
- Can flapping be predicted from task characteristics (e.g., cross-module changes, missing tests, coupled dependencies) before the agent starts?
- Should the harness maintain a "known-flapping" list of issues that should not be retried without human intervention?
