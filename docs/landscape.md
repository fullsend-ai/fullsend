# Landscape Analysis

A survey of AI-driven code review systems and **adjacent agent infrastructure** (orchestration, protocol gateways), with analysis of how they relate to the fullsend vision of fully autonomous merge-to-production.

> **Conducted: 2026-03-06.** This landscape is moving fast. This document will rot. If you're reading this months after the date above, it likely needs updating or removing. Treat the specific tool capabilities and pricing as potentially stale; the architectural patterns and gaps analysis may age better.

## The industry consensus

The entire industry is solving "AI as first-pass reviewer that helps humans review faster." Nobody is seriously tackling the full autonomous merge problem. GitHub has published an [explicit position paper](https://github.blog/ai-and-ml/generative-ai/code-review-in-the-age-of-ai-why-developers-will-always-own-the-merge-button/) arguing that developers will always own the merge button. This is the question we're asking differently.

## Major tools

### CodeRabbit

[Website](https://www.coderabbit.ai/) | [Architecture docs](https://docs.coderabbit.ai/overview/architecture)

The most sophisticated decomposition in the field. Uses specialized agents in parallel: Review, Verification, Chat, Pre-Merge Checks, and Finishing Touches. Describes itself as a "[hybrid architecture](https://www.coderabbit.ai/blog/pipeline-ai-vs-agentic-ai-for-code-reviews-let-the-model-reason-within-reason)" — pipeline structure for reliability, agentic flexibility for reasoning.

**Context engineering:** Splits context into three parts:
- **Intent** — what the developer aims to achieve with the PR
- **Environment** — code dependencies, file relationships (AST-based code graph)
- **Historical learnings** — vector database of past reviews, stored and queried through LanceDB

**Verification layer:** A separate AI-powered quality assurance system validates review comments before posting. Explicitly addresses "notification fatigue" — mimics a principal engineer who only speaks when it matters.

**Infrastructure:** Each review spins up an isolated, sandboxed, short-lived environment (Cloud Run microVMs + Jailkit + cgroups). Torn down after review. Multi-model orchestration selects models for different review concerns.

**Relevance to fullsend:** CodeRabbit's decomposition and context engineering are the closest to our sub-agent model. Their intent/environment/history split maps loosely to our intent alignment agent / correctness agent / drift detection concerns. However, they stop at review — no merge authority. Their verification layer (agent checking other agents' work) is a practical implementation of the kind of inter-agent validation we need, though without zero-trust principles.

### Greptile

[Website](https://www.greptile.com/) | [Benchmarks](https://www.greptile.com/benchmarks)

Indexes the entire repo into a code graph. Uses multi-hop investigation to trace issues across files, check git history, and follow dependency chains. v3 (late 2025) rewrote core architecture on the Anthropic Claude Agent SDK.

**Architecture:** Full codebase indexing — AST analysis, dependency tracing, git history. When reviewing, follows a change through its dependency chain. Shows evidence from the codebase for every flagged issue.

**Trade-offs:** Deepest context awareness in the field, but also highest false positive rate in [independent benchmarks](https://www.greptile.com/benchmarks). The depth/noise trade-off is inherent.

**Scale:** $25M Series A (Benchmark-led), $180M valuation. $30/developer/month.

**Relevance to fullsend:** Greptile's codebase graph approach is relevant to our correctness agent — understanding cross-file impact requires this kind of indexing. Their false positive problem illustrates why decomposition matters: a single agent trying to do everything (deep context + security + style) is noisy. Specialized sub-agents with different context needs could use deep indexing selectively.

### Graphite

[Website](https://graphite.com/) | [Agent announcement](https://graphite.com/blog/introducing-graphite-agent-and-pricing)

Takes a fundamentally different angle: stacked PRs. Instead of reviewing one massive PR, work is broken into small, atomic PRs that build on each other. AI reviews 200-line focused PRs instead of 2000-line monoliths.

**Results:** 96% positive feedback rate. Under 3% unhelpful comment rate. When Graphite flags an issue, developers change the code 55% of the time (vs. 49% for human reviewers). Shopify reported 33% more PRs merged per developer. Asana saw engineers save 7 hours weekly.

**Merge queue:** Stack-aware merge queue batches and tests multiple PRs in parallel. "Merge when ready" auto-pilots stack merges once approved — but approval is still human.

**Relevance to fullsend:** The stacked PR insight is important for our implementation agents. Smaller, focused changes are easier for review sub-agents to evaluate with confidence. If implementation agents produce stacked PRs rather than monolithic ones, the review problem becomes more tractable. The merge queue concept is also relevant — our system needs something similar for sequencing autonomous merges.

### Qodo (formerly PR-Agent)

[Website](https://www.qodo.ai/) | [Docs](https://qodo-merge-docs.qodo.ai/) | [GitHub](https://github.com/qodo-ai/pr-agent)

Open-source core (PR-Agent) with commercial layer (Qodo Merge). Layered architecture: user interfaces, orchestration, specialized tools, and platform abstraction. Command dispatcher routes requests to specialized tools (`/review`, `/describe`, `/improve`, `/ask`).

**Multi-repo awareness:** Context engine indexes dozens or thousands of repos, mapping dependencies and shared modules so review agents see cross-repo impact. This is critical for any multi-repo organization where changes can span multiple repos.

**Governance:** Team- and org-level policies defined once, applied consistently across repos. Custom rules enforcement for coding standards, security policies, and best practices.

**Auto-review, not auto-merge:** Has `auto_review`, `auto_describe`, `auto_improve` triggers on PR open, but merge decisions stay with the git platform's branch protection.

**Relevance to fullsend:** Qodo's multi-repo governance model is directly relevant — we need org-wide policy enforcement across heterogeneous repos. Their command-dispatcher architecture (specialized tools invoked by an orchestrator) is a simpler version of our sub-agent model. The open-source PR-Agent core could potentially be extended or learned from.

### Sourcery

[Website](https://www.sourcery.ai/) | [Docs](https://docs.sourcery.ai/Code-Review/Overview/)

Uses "a series of AI code reviewers, each with different specialties" — e.g., a Complexity reviewer focused on simplicity. Static analysis engine with rules-based checks on top. Validation process to reduce false positives.

**Honest about limitations:** Their own blog concedes early comments ranged from useful to "dead wrong." Multi-check validator improved usefulness from low-40% to about 60%. Reviews changed files only — cannot reason about the rest of the codebase. Misses cross-file dependencies.

**Relevance to fullsend:** Sourcery's candor about their limitations is informative. Their specialized-reviewer approach validates our sub-agent decomposition, but their inability to reason about the broader codebase illustrates why context management per sub-agent matters. A correctness agent needs repo context; an injection defense agent needs raw PR content; an intent alignment agent needs the intent repo. Different agents, different context.

### GitHub Copilot Code Review

[Blog post](https://github.blog/ai-and-ml/generative-ai/code-review-in-the-age-of-ai-why-developers-will-always-own-the-merge-button/)

GA since April 2025. 1 million users in one month. Assign Copilot as a reviewer like any teammate. October 2025 update added context gathering — reads source files, explores directory structure, integrates CodeQL and ESLint.

**Relevance to fullsend:** GitHub's explicit "humans own the merge button" position means we cannot rely on GitHub's native tooling for autonomous merging. We'll need to build merge authority outside of (or on top of) GitHub's review/approval system.

### GitLab AI Merge Agent

[Announcement](https://www.webpronews.com/gitlabs-ai-merge-agent-automating-chaos-in-code-merges/)

Launched November 2025. The closest thing in the industry to autonomous merging. 85% success rate automating merges, 30% CI/CD time reduction. Resolves simple conflicts autonomously, flags complex ones for humans. Adheres to branch protection rules.

**Relevance to fullsend:** GitLab is solving the *mechanical* merge problem (conflict resolution, CI gating) but not the *judgment* problem (should this change exist?). Our problem is harder — we need the judgment layer. But GitLab's approach to adhering to existing branch protection rules while automating within them is a pattern worth studying.

### Others

- **Cursor Bugbot** — AI code review in the Cursor IDE and GitHub. Optimizes for catching hard-to-find bugs with low false positive rate.
- **Ellipsis** — Bridges review and implementation: takes reviewer comments and automatically implements requested changes.
- **Kodus (Kody)** — Open source. Scans old PRs to learn your team's review style, then mimics it. Learns over time.
- **OpenAI Codex** — Triggered by `@codex review` in GitHub PRs. Behaves as an additional reviewer focused on high-severity issues.
- **Bito** — Uses Claude Sonnet for human-like review. GitHub, GitLab, Bitbucket integration.

## Production agent orchestration systems

While the tools above focus on code review, a separate category of systems addresses end-to-end agent orchestration — from task intake through implementation and merge. These are closer to the fullsend vision than review-only tools.

### Stripe Minions

[Architecture blog post](https://stripe.dev/blog/minions-stripes-one-shot-end-to-end-coding-agents) | [Part 2](https://stripe.dev/blog/minions-stripes-one-shot-end-to-end-coding-agents-part-2)

One-shot coding agents that merge over 1,300 pull requests per week at Stripe. A task starts in a Slack message and ends in a CI-passing pull request ready for human review, with no interaction in between.

**Architecture:** Before the LLM runs, a deterministic orchestrator prefetches context — scanning threads for links, pulling tickets, and searching code via MCP. Each Minion gets its own isolated devbox (same machines human engineers use, spins up in 10 seconds). An internal MCP server called Toolshed provides ~500 tools, curated per task so the agent starts focused rather than overwhelmed.

**Blueprints:** Stripe's term for hybrid pipelines that combine deterministic code nodes (run linters, push changes) with agentic subtasks (implement feature, fix CI failures). This is not full autonomy — it's a structured pipeline with guardrails at each stage.

**Bounded retry:** If CI fails, the Minion gets one attempt to fix it. Two CI rounds maximum, then the task is handed off to humans. This explicit stopping condition prevents unbounded agent loops — a concrete answer to the iteration-count stopping condition discussed in [production-feedback.md](problems/production-feedback.md).

**Key insight:** "If a tool is good for human engineers, it's good for LLMs." Every investment in developer tooling, documentation, devboxes, and CI directly improved agent performance. The factory runs on the same infrastructure humans use. This aligns with the backpressure framing in [repo-readiness.md](problems/repo-readiness.md) — developer experience investment compounds for agents.

**Relevance to fullsend:** Stripe's deterministic-then-agentic pipeline pattern ("blueprints") is a concrete implementation of the hybrid approach we've been exploring. Their context prefetching (deterministic orchestrator before LLM invocation) and tool curation (subset of 500 tools per task) are practical solutions to the context management problem discussed in [codebase-context.md](problems/codebase-context.md). The bounded retry model provides a production-validated answer to our open question about stopping conditions. However, Minions are built for Stripe's proprietary codebase (hundreds of millions of lines of Ruby with internal libraries) — the approach requires significant internal tooling investment.

### Gas Town

[GitHub](https://github.com/steveyegge/gastown) | [Architecture overview](https://cloudnativenow.com/features/gas-town-what-kubernetes-for-ai-coding-agents-actually-looks-like/)

Steve Yegge's open-source multi-agent orchestration system, described as "Kubernetes for AI coding agents." Coordinates 20-30 parallel coding agents working on feature branches simultaneously.

**Architecture:** A "Mayor" agent acts as the coordinator, dispatching work to parallel coding agents called "Polecats." A "Refinery" manages the merge queue so parallel work doesn't collide. Git is the persistence layer — if the system crashes, it reads the git history and resumes. This is a concrete implementation of the repo-as-coordination-layer pattern, though with a coordinator agent (which fullsend's model avoids).

**Relevance to fullsend:** Gas Town's use of git as crash-recovery persistence validates the "repo is the coordinator" principle — all state is in git, not in ephemeral coordinator memory. However, it uses a coordinator agent (the Mayor), which conflicts with fullsend's position that coordination should happen through branch protection, CODEOWNERS, and status checks rather than through a coordinator agent. The Refinery merge queue concept is relevant to how we'd sequence autonomous merges.

### Ambient Code Platform (ACP)

[GitHub](https://github.com/ambient-code/platform)

Kubernetes-native pattern: custom resources and an operator drive Jobs or Pods that run agent CLIs (with UI for session management). Often discussed alongside Red Hat Emerging Tech’s [cloud-native ambient agents](https://next.redhat.com/2026/01/21/architecting-cloud-native-ambient-agents-patterns-for-scale-and-control/) write-up as a reference architecture for agents on Kube.

**Relevance to fullsend:** Useful as a **wiring reference** for running agents on Kubernetes. For **why it is a weak match** to our reliability, security, and scale goals—extra controller, UI/chat-first vs SCM–event automation, friction with Tekton-style pipelines, shared-workspace injection risk, limits of plain-Pod execution for tasks like image builds—see [agent-infrastructure.md](problems/agent-infrastructure.md#ambient-code-platform-acp).

## Agent connectivity and protocol gateways

A separate category from review tools and end-to-end orchestrators: **proxies and gateways** that sit on the paths agents already use to reach models, tools, and (in some designs) other agents. They standardize protocols and centralize policy instead of replacing git-mediated coordination.

### Agent Gateway

[GitHub](https://github.com/agentgateway/agentgateway) | [Documentation](https://agentgateway.dev/docs/)

Open-source proxy built around AI-native protocols — [MCP](https://modelcontextprotocol.io/introduction) for tool and data access, [A2A](https://developers.googleblog.com/en/a2a-a-new-era-of-agent-interoperability/) for agent-to-agent traffic — plus an OpenAI-compatible **LLM gateway** surface toward major providers. It targets the same connectivity problems as ad-hoc SDK configuration: multiple transports (stdio, HTTP, SSE, streamable HTTP), OAuth toward tools, OpenAPI-backed MCP, unified routing to models with budget and failover, and optional **Kubernetes Inference Gateway**–style routing signals (utilization, queues, adapters).

**Controls and observability:** Multi-layer **guardrails** (pattern filters, vendor moderation APIs, custom webhooks), authentication (JWT, API keys, OAuth), **RBAC** expressed with a CEL policy engine, rate limiting, TLS, and **OpenTelemetry** for metrics, logs, and traces.

**Relevance to fullsend:** This maps most directly to [agent-infrastructure.md](problems/agent-infrastructure.md) (where egress and tool access are enforced), [architecture.md](architecture.md) (sandbox network regulation and observability), and [governance.md](problems/governance.md) (org-wide guardrails and who can change gateway policy). Centralizing LLM and MCP traffic can improve **attribution, spend control, and consistent tool allowlists** — the same problems called out for headless runtimes that cannot rely on a laptop's implicit trust boundary.

It does **not** substitute for fullsend's intent tiering, zero-trust review composition, or **repo-as-coordinator** semantics. In particular, adopting an A2A gateway does not mean agents should coordinate merge decisions or trust through a side channel; see [agent-architecture.md](problems/agent-architecture.md#how-agents-communicate). A gateway is one way to implement **controlled egress** and **edge guardrails** for the traffic agents generate while still using GitHub-visible mechanisms for coordination.

## Architectural patterns in the field

Three distinct approaches:

### 1. Specialized sub-agent decomposition (Sourcery, CodeRabbit, Qodo)

Multiple reviewers with different specialties, orchestrated by a coordinator. CodeRabbit is the most mature implementation with parallel agents, verification layers, and context splitting.

### 2. Deep codebase indexing (Greptile)

Build a full code graph, trace dependencies across the entire repo. Deepest understanding, but noisiest output. Trade-off between catch rate and signal-to-noise.

### 3. Change-size reduction (Graphite)

Make the problem easier by making PRs smaller. Stacked PRs with clear scope are more tractable for AI review. Doesn't improve the agent's capability, but improves the input quality.

### 4. Deterministic-then-agentic pipelines (Stripe Minions)

Structure the workflow as a pipeline where deterministic steps (context prefetching, linting, pushing) alternate with agentic steps (implementation, CI fix attempts). The agent operates within a bounded, instrumented pipeline rather than with open-ended autonomy. Bounded retry limits prevent runaway loops.

These are complementary, not competing. A system could use stacked PRs (Graphite's insight) reviewed by specialized sub-agents (CodeRabbit's insight) with deep codebase context where needed (Greptile's insight), all orchestrated through a deterministic pipeline with agentic steps (Stripe's insight).

## What nobody is doing

None of these tools address:

- **Formal intent verification** — checking whether a change is authorized against a structured intent system. CodeRabbit's "intent" context is about understanding the PR's purpose, not verifying it against an authorization system.
- **Zero-trust inter-agent review** — agents treating each other's output as untrusted. Existing multi-agent systems implicitly trust the orchestrator and each other.
- **Autonomous merge with security-focused confidence** — the judgment problem of "should this change exist?" as distinct from "is this change correct?"
- **Tier-based autonomy** — different levels of agent authority for different types of changes.
- **Agent governance** — who controls the agents' policies and permissions.

These gaps define the novel problem space for fullsend.

## Industry data points

- Monthly code pushes crossed 82 million, merged PRs hit 43 million (GitHub Octoverse)
- ~41% of new code is AI-assisted
- 25-35% growth in code per engineer, but code review capacity remains tied to human limits
- Median PR size increased 33% (March-November 2025): 57 to 76 lines changed per PR
- Lines of code per developer grew from 4,450 to 7,839
- Estimated 40% code review quality deficit projected for 2026

The review bottleneck is getting worse as code generation accelerates. This is the tailwind behind the fullsend vision — the current model of human review doesn't scale.
