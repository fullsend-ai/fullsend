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

**Relevance to fullsend:** The stacked PR insight is important for our code agents. Smaller, focused changes are easier for review sub-agents to evaluate with confidence. If code agents produce stacked PRs rather than monolithic ones, the review problem becomes more tractable. The merge queue concept is also relevant — our system needs something similar for sequencing autonomous merges.

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
- **Caveman** — Output token compression via prompt engineering (~65% savings). Constrains agent output to terse, technical language while preserving reasoning depth. The `caveman-review` format (single-line, emoji-coded comments) is a concrete output format for review sub-agents. [GitHub](https://github.com/juliusbrussee/caveman)

## Production agent orchestration systems

While the tools above focus on code review, a separate category of systems addresses end-to-end agent orchestration — from task intake through coding and merge. These are closer to the fullsend vision than review-only tools.

### Stripe Minions

[Architecture blog post](https://stripe.dev/blog/minions-stripes-one-shot-end-to-end-coding-agents) | [Part 2](https://stripe.dev/blog/minions-stripes-one-shot-end-to-end-coding-agents-part-2)

One-shot coding agents that merge over 1,300 pull requests per week at Stripe. A task starts in a Slack message and ends in a CI-passing pull request ready for human review, with no interaction in between.

**Architecture:** Before the LLM runs, a deterministic orchestrator prefetches context — scanning threads for links, pulling tickets, and searching code via MCP. Each Minion gets its own isolated devbox (same machines human engineers use, spins up in 10 seconds). An internal MCP server called Toolshed provides ~500 tools, curated per task so the agent starts focused rather than overwhelmed.

**Blueprints:** Stripe's term for hybrid pipelines that combine deterministic code nodes (run linters, push changes) with agentic subtasks (implement feature, fix CI failures). This is not full autonomy — it's a structured pipeline with guardrails at each stage.

**Bounded retry:** If CI fails, the Minion gets one attempt to fix it. Two CI rounds maximum, then the task is handed off to humans. This explicit stopping condition prevents unbounded agent loops — a concrete answer to the iteration-count stopping condition discussed in [production-feedback.md](problems/production-feedback.md).

**Key insight:** "If a tool is good for human engineers, it's good for LLMs." Every investment in developer tooling, documentation, devboxes, and CI directly improved agent performance. The factory runs on the same infrastructure humans use. This aligns with the backpressure framing in [repo-readiness.md](problems/repo-readiness.md) — developer experience investment compounds for agents.

**Relevance to fullsend:** Stripe's deterministic-then-agentic pipeline pattern ("blueprints") is a concrete implementation of the hybrid approach we've been exploring. Their context prefetching (deterministic orchestrator before LLM invocation) and tool curation (subset of 500 tools per task) are practical solutions to the context management problem discussed in [codebase-context.md](problems/codebase-context.md). The bounded retry model provides a production-validated answer to our open question about stopping conditions. However, Minions are built for Stripe's proprietary codebase (hundreds of millions of lines of Ruby with internal libraries) — the approach requires significant internal tooling investment.

### Gas Town / Gas City

[Gas Town GitHub](https://github.com/steveyegge/gastown) | [Gas City GitHub](https://github.com/gastownhall/gascity) | [Architecture overview](https://cloudnativenow.com/features/gas-town-what-kubernetes-for-ai-coding-agents-actually-looks-like/)

Steve Yegge's multi-agent orchestration system, evolved from Gas Town (the original monolith) to Gas City (an orchestration-builder SDK, v0.13, Go, 1,600+ commits). Gas Town coordinates 20-30 parallel coding agents working on feature branches simultaneously. Gas City extracts the reusable infrastructure into composable primitives.

**Architecture:** Gas Town uses a "Mayor" agent as coordinator, dispatching work to parallel coding agents ("Polecats"). A "Refinery" manages the merge queue. Git is the persistence layer — if the system crashes, it reads git history and resumes.

Gas City refactors this into 5 irreducible primitives (agent protocol, bead store, event bus, config, prompt templates) and 4 derived mechanisms (messaging, formulas/molecules, dispatch, health patrol). Each derived mechanism is provably composable from the primitives — no new infrastructure required. Strict layering invariant: Layer N never imports Layer N+1. The SDK contains zero hardcoded role names; all role behavior is user-supplied prompt configuration.

**Zero Framework Cognition (ZFC):** The most distinctive design principle. The framework handles mechanics only (lifecycle, routing, persistence); ALL judgment is deferred to the LLM via prompts. Enforced through a [primitive test](https://github.com/gastownhall/gascity/blob/main/engdocs/contributors/primitive-test.md) with three conditions: (1) Atomicity — can it be decomposed into existing primitives? (2) Bitter Lesson — does it become MORE useful as models improve? If a smarter model would do it better from the prompt, it fails. (3) ZFC — does Go handle transport only, with no judgment calls? This leads to permanent exclusions: no skills system (the model IS the skill system), no capability flags (a prompt sentence suffices), no MCP/tool registration, no decision logic in Go. See also [ZFC article](https://steve-yegge.medium.com/zero-framework-cognition-a-way-to-build-resilient-ai-applications-56b090ed3e69).

**Progressive capability model:** Capabilities activate based on config section presence — 8 levels from minimal (agent + tasks) to full orchestration. Config IS the feature flag. An empty `city.toml` gives Level 0-1; adding sections incrementally activates capabilities. No feature flags, no capability toggles.

**Convergence loops:** Bounded iterative refinement with gate evaluation — an agent does work, a gate (shell script, human approval, or hybrid) evaluates it, the system iterates up to N times or terminates. This is Gas City's answer to "how do you know when an agent's work is good enough?" and maps directly to the stopping-condition questions in [production-feedback.md](problems/production-feedback.md).

**Reliability model (NDI):** "Nondeterministic Idempotence" — the system converges to correct outcomes through persistent state (beads survive session crashes) plus idempotent observers, not deterministic execution. The controller follows Erlang/OTP supervision patterns: let sessions crash, restart with backoff, quarantine crash loops. Multiple runtime providers: tmux (production), subprocess, exec (script-backed), Kubernetes.

**Relevance to fullsend:** Gas City's approach contains several important lessons for fullsend:

- *Coordinator nuance:* Fullsend says "the repo is the coordinator" with no coordinator agent. Gas City *does* have a controller process driving reconciliation and health patrol — but enforces that it contains zero cognition (ZFC). The distinction is not "coordinator vs. no coordinator" but "cognitive coordinator vs. infrastructure-only coordinator." Fullsend's repo-level coordination (branch protection, CODEOWNERS, status checks) naturally satisfies ZFC because these mechanisms are deterministic infrastructure, not judgment calls.
- *The Bitter Lesson test* is a useful design discipline for fullsend's own tooling: anything a smarter model would handle from the prompt doesn't belong in the orchestration layer. As models improve, framework intelligence becomes technical debt.
- *Convergence loops* address the stopping-condition problem that [production-feedback.md](problems/production-feedback.md) and [agent-architecture.md](problems/agent-architecture.md) flag as open questions. Gas City's gate-evaluated bounded iteration is a concrete implementation.
- *Progressive capability* is relevant to the [autonomy spectrum](problems/autonomy-spectrum.md) — graduated activation without binary on/off decisions.
- *Beads as universal substrate* is a different design choice from fullsend's git-as-substrate. Beads offer more flexible work tracking (everything is a bead: tasks, mail, molecules, convoys) but require additional infrastructure (Dolt database). Git-as-substrate requires less infrastructure but is less flexible for non-code work units.
- *Exec providers across all seams* (beads, events, runtime, mail each accept script-backed implementations) make the system extensible without code changes — a pattern relevant to [agent infrastructure](problems/agent-infrastructure.md).

**Vibe Maintainer workflow:** Yegge's ["Vibe Maintainer" (2026-03-31)](https://steve-yegge.medium.com/vibe-maintainer-a2273a841040) describes the maintainer-side problem: handling ~50 community PRs/day across Beads and Gas Town, most AI-generated by external contributors. His approach uses worker agents to triage and salvage incoming PRs rather than gatekeeping quality — he calls this "optimizing for community throughput." This is agents used defensively (processing incoming contributions), complementing fullsend's focus on agents used offensively (generating and merging internal contributions). See [contribution-volume.md](problems/contribution-volume.md) for the broader problem.

### Unbound Force

[GitHub org](https://github.com/unbound-force) | [Meta repo](https://github.com/unbound-force/unbound-force) | [Dewey (knowledge layer)](https://github.com/unbound-force/dewey) | [Replicator (orchestration)](https://github.com/unbound-force/replicator) | [Gaze (test-quality analysis)](https://github.com/unbound-force/gaze)

An Apache-2.0, Go-first, superhero-themed multi-agent framework. The central thesis (stated on the org profile) is that "engineers shift from manual coding to directing AI agents through specifications and governance — treating specs and rules as the medium through which human intent is manifested into code." The org is young: 0 stars on the meta repo, 1 follower, 8 public repos, active commits through Apr 22–23, 2026, latest meta release `v0.12.0` (Apr 14, 2026).

**Architecture:** Five named "hero" personas each mapped to a traditional software role — Muti-Mind (Product Owner), Cobalt-Crush (Developer), The Divisor (PR Reviewer), Gaze (Tester), Mx F (Manager). In practice only Gaze lives in its own repo today; the other four are embedded inside the meta `unbound-force` repo. Distribution happens through a Go CLI (`uf`) installed via Homebrew or RPM; `uf init` scaffolds 50 files (templates, scripts, commands, agents, review personas, convention packs) into a target repository, with tool-owned files auto-updated on re-run and user-owned files preserved.

**Specification framework:** A two-tier workflow, which is the most distinctive design choice. *Speckit* is the strategic path — a 9-phase pipeline (`/speckit.specify` → `/speckit.implement`) for architectural work. *OpenSpec* is the tactical path for bug fixes and small changes (`/opsx:propose` → `/opsx:archive`). A separate `/workflow` command set manages a 6-stage feature lifecycle across the heroes. Every proposal includes an explicit "alignment assessment" against the org's four-principle [constitution](https://github.com/unbound-force/unbound-force/blob/main/.specify/memory/constitution.md): Autonomous Collaboration (artifact-mediated, no runtime coupling), Composability First (every hero usable alone), Observable Quality (machine-parseable output with provenance), Testability (isolated testing without shared state).

**Knowledge layer:** [Dewey](https://github.com/unbound-force/dewey) — a standalone MCP server providing 44 tools across Markdown vaults, semantic search, source code indexing, and persistent memory. Uses local embeddings via Ollama; indexes local files, GitHub, web docs, and Go source. The design intent is that agents query the graph for relevant fragments rather than loading whole files into context windows.

**Orchestration:** [Replicator](https://github.com/unbound-force/replicator) — a separate Go MCP server with 53 tools organized into four concerns: Org (work tracking), Comms (inter-agent messaging), Forge (task decomposition with git worktree isolation), and a Dewey bridge (semantic memory). 15MB binary, claims <50ms startup. Worktree-per-task isolation is a concrete answer to the "how do agents work in parallel without stepping on each other" question.

**Test-quality analysis:** [Gaze](https://github.com/unbound-force/gaze) is the most interesting sub-project for this landscape. It's a Go static analyzer that "detects side effects and scores test quality — know whether AI-generated tests actually verify behavior, not just cover lines." This directly addresses a problem identified in [production-feedback.md](https://github.com/fullsend-ai/fullsend/blob/main/docs/problems/production-feedback.md) and implicit in [repo-readiness.md](https://github.com/fullsend-ai/fullsend/blob/main/docs/problems/repo-readiness.md): coverage-as-fitness is gameable by agents, because an agent can hit 100% line coverage with tests that assert nothing. Gaze is a sober, language-specific (Go) attempt to measure what tests *do* rather than what they *touch*.

**Observability and governance constraints:** The constitution's "Observable Quality" principle — machine-parseable output (JSON minimum) with provenance metadata — is closer to fullsend's audit-trail needs than most frameworks in this space, which treat agent output as free-text. Hero repos are required to maintain their own constitutions aligned with the org one, giving a tree-structured governance model.

**What it doesn't address:** No discussion of prompt injection or adversarial input in the constitution or the meta-repo README, no zero-trust framing between heroes (Autonomous Collaboration is about decoupling, not mutual distrust — the heroes are assumed cooperative), no autonomy spectrum (the framework ships with a fixed 6-stage lifecycle and fixed command sets), no discussion of merge authority (it's a scaffold-into-your-repo tool, so the merge question is punted to whatever branch protection the consuming repo has configured). The Divisor "PR Reviewer" persona is a review role, not a merge gate.

**Relevance to fullsend:** Three useful takeaways, one caution.

* *Specs-as-medium, governance-by-constitution* is close to the direction [intent-representation.md](https://github.com/fullsend-ai/fullsend/blob/main/docs/problems/intent-representation.md) and [governance.md](https://github.com/fullsend-ai/fullsend/blob/main/docs/problems/governance.md) are pointing. The hierarchical constitution (org constitution + hero constitutions that must not contradict it) is a concrete precedent for fullsend's own governance model, particularly the requirement that every proposal carries an explicit alignment assessment. Worth studying as prior art for how rule precedence and conflict resolution get encoded.
* *Gaze's test-quality scoring* is directly relevant to [testing-agents.md](https://github.com/fullsend-ai/fullsend/blob/main/docs/problems/testing-agents.md) and to the "fitness function has to be honest and not gameable" concern that any adaptive-selection experiment has to solve. Coverage as a fitness signal collapses as soon as agents optimize for it; side-effect/behavior scoring is one way to raise the bar. Go-only today, but the idea ports.
* *Dewey's MCP-gated context* and *Replicator's worktree-per-task isolation* are two narrow, well-scoped primitives worth looking at independently of the wider hero metaphor. They map respectively to [codebase-context.md](https://github.com/fullsend-ai/fullsend/blob/main/docs/problems/codebase-context.md) (how agents acquire codebase understanding without bloating context) and [agent-infrastructure.md](https://github.com/fullsend-ai/fullsend/blob/main/docs/problems/agent-infrastructure.md) (how parallel agents get isolated workspaces). Either can be adopted without adopting the Speckit/OpenSpec workflow.

### Ambient Code Platform (ACP)

[GitHub](https://github.com/ambient-code/platform)

Kubernetes-native pattern: custom resources and an operator drive Jobs or Pods that run agent CLIs (with UI for session management). Often discussed alongside Red Hat Emerging Tech’s [cloud-native ambient agents](https://next.redhat.com/2026/01/21/architecting-cloud-native-ambient-agents-patterns-for-scale-and-control/) write-up as a reference architecture for agents on Kube.

**Relevance to fullsend:** Useful as a **wiring reference** for running agents on Kubernetes. For **why it is a weak match** to our reliability, security, and scale goals—extra controller, UI/chat-first vs SCM–event automation, friction with Tekton-style pipelines, shared-workspace injection risk, limits of plain-Pod execution for tasks like image builds—see [agent-infrastructure.md](problems/agent-infrastructure.md#ambient-code-platform-acp).

## Kubernetes-native agent hosting (SIG)

### Kubernetes SIG Agent Sandbox

[GitHub](https://github.com/kubernetes-sigs/agent-sandbox) | [Project site](https://agent-sandbox.sigs.k8s.io)

A Kubernetes SIG project: controllers and **Custom Resources** for **isolated, stateful, singleton** agent workloads (durable pod-per-session style runtimes), not ephemeral CI-shaped jobs.

**Relevance to fullsend:** Useful reference for long-lived, cluster-hosted agent sessions. For task-scoped automation, the CR-centric lifecycle is a poor fit next to [Tekton](https://tekton.dev/)–style pipelines **triggered from SCM events** (pull requests, pushes, and similar), and the project does not currently ship observability primitives aligned with per-task attribution and audit needs — see [agent-infrastructure.md](problems/agent-infrastructure.md#kubernetes-sig-agent-sandbox).

## Agent connectivity and protocol gateways

A separate category from review tools and end-to-end orchestrators: **proxies and gateways** that sit on the paths agents already use to reach models, tools, and (in some designs) other agents. They standardize protocols and centralize policy instead of replacing git-mediated coordination.

### Agent Gateway

[GitHub](https://github.com/agentgateway/agentgateway) | [Documentation](https://agentgateway.dev/docs/)

Open-source proxy built around AI-native protocols — [MCP](https://modelcontextprotocol.io/introduction) for tool and data access, [A2A](https://developers.googleblog.com/en/a2a-a-new-era-of-agent-interoperability/) for agent-to-agent traffic — plus an OpenAI-compatible **LLM gateway** surface toward major providers. It targets the same connectivity problems as ad-hoc SDK configuration: multiple transports (stdio, HTTP, SSE, streamable HTTP), OAuth toward tools, OpenAPI-backed MCP, unified routing to models with budget and failover, and optional **Kubernetes Inference Gateway**–style routing signals (utilization, queues, adapters).

**Controls and observability:** Multi-layer **guardrails** (pattern filters, vendor moderation APIs, custom webhooks), authentication (JWT, API keys, OAuth), **RBAC** expressed with a CEL policy engine, rate limiting, TLS, and **OpenTelemetry** for metrics, logs, and traces.

**Relevance to fullsend:** This maps most directly to [agent-infrastructure.md](problems/agent-infrastructure.md) (where egress and tool access are enforced), [architecture.md](architecture.md) (sandbox network regulation and observability), and [governance.md](problems/governance.md) (org-wide guardrails and who can change gateway policy). Centralizing LLM and MCP traffic can improve **attribution, spend control, and consistent tool allowlists** — the same problems called out for headless runtimes that cannot rely on a laptop's implicit trust boundary.

It does **not** substitute for fullsend's intent tiering, zero-trust review composition, or **repo-as-coordinator** semantics. In particular, adopting an A2A gateway does not mean agents should coordinate merge decisions or trust through a side channel; see [agent-architecture.md](problems/agent-architecture.md#how-agents-communicate). A gateway is one way to implement **controlled egress** and **edge guardrails** for the traffic agents generate while still using GitHub-visible mechanisms for coordination.

### Collo.dev AI Scrum Master Template

[GitHub](https://github.com/plusai-solutions/ai-scrum-master-template) | [Website](https://collo.dev)

Open-source GitHub Actions template that deploys four Claude-powered agents — Scrum Master, Planner, Fullstack Dev, QA Tester — coordinated through a Kanban board issue. Nine workflow YAMLs trigger agents via label transitions. Users fork the template, add an API key, and comment on a Kanban issue to initiate feature development. The pipeline runs: human describes feature → Scrum Master creates backlog tickets → Planner creates implementation plan → human approves plan → Dev implements and opens PR → QA Tester runs lint/test/build → human merges.

**Architecture:** Labels drive a state machine (`feature-request` → `approved-plan` → `tests-passed` → `ready-for-merge`). Each agent has a dedicated prompt config in `.claude/agents/`. All coordination happens in GitHub Actions workflow YAML — the "Scrum Master" agent is primarily a Kanban board updater rather than a true coordinator. Agents read a CLAUDE.md file for project context, making the template stack-agnostic.

**Human checkpoints:** Plan approval (add `approved-plan` label) and PR merge. All PR merges target a `develop` branch; only humans merge `develop` → `main`.

**What it doesn't address:** No security threat model, no injection defense, no intent verification beyond human plan approval, no inter-agent trust model, no governance framework, no drift detection, no autonomy spectrum. The QA Tester is a single agent running lint/test/build — no decomposed review. Merge authority always stays with humans.

**Relevance to fullsend:** The template is a concrete implementation of the happy path that fullsend's problem documents explore in depth. It independently converged on labels as the state machine primitive and CLAUDE.md as the context mechanism, validating those patterns. However, it illustrates the gap between "agents that help build features" and "agents trusted to merge autonomously" — the template assumes good-faith actors and benign inputs, with no defense against prompt injection via issue text or PR descriptions (fullsend's highest-ranked threat). The Scrum Master role is a coordinator agent in thin disguise, conflicting with fullsend's repo-as-coordinator position. The template is useful as a reference for what a minimal viable agent pipeline looks like and what problems surface first when you ship one.

### GitHub Agentic Workflows (gh-aw)

[Website](https://github.github.com/gh-aw/) | [Security architecture](https://github.github.com/gh-aw/introduction/architecture/) | [Blog post](https://github.blog/news-insights/product-news/automate-repository-tasks-with-github-agentic-workflows/)

Repository automation from GitHub Next and Microsoft Research, running coding agents (Copilot, Claude, Codex) in GitHub Actions with strong guardrails. Workflows are defined in markdown files with YAML frontmatter specifying triggers (schedule, events), permissions, and safe-output constraints. A `gh aw` CLI extension compiles each markdown definition into a `.lock.yml` GitHub Actions workflow, performing schema validation, expression safety checks, action SHA pinning, and security scanning (actionlint, zizmor, poutine) at compile time. Early development; may change significantly.

**Architecture:** The agent runs in an isolated container on an Actions runner with a read-only `GITHUB_TOKEN`. It produces a structured artifact (SafeOutputs) describing its intended actions. A separate job with scoped write permissions reads the artifact and applies only what the workflow explicitly permits — hard limits per operation, required title prefixes, label constraints. The agent requests; the gated job decides. An [orchestration pattern](https://github.github.com/gh-aw/patterns/orchestration/) supports multi-workflow fan-out via `dispatch-workflow` (async) and `call-workflow` (same run) safe outputs, and [cross-repository operations](https://github.github.com/gh-aw/reference/cross-repository/) allow reading from and writing to external repos via `target-repo` and `allowed-repos` parameters.

**Security model (three trust layers):** gh-aw adopts a formal [defense-in-depth architecture](https://github.github.com/gh-aw/introduction/architecture/) with three trust layers, each constraining failures above it:

1. **Substrate-level trust** — the Actions runner VM, kernel, container runtime, and three privileged containers: the Agent Workflow Firewall (AWF) that uses iptables to redirect HTTP/HTTPS through a Squid proxy enforcing a domain allowlist, an API proxy that routes model traffic while keeping credentials isolated, and an MCP Gateway that spawns isolated containers for each MCP server with per-server domain allowlists and tool allowlisting.
2. **Configuration-level trust** — declarative artifacts (workflow frontmatter, network policies, MCP configs) that constrain what components are loaded, how they connect, and what credentials they receive. Includes [content sanitization](https://github.github.com/gh-aw/introduction/architecture/#content-sanitization) of untrusted input (@mention neutralization, URI filtering to trusted domains, XML/HTML tag conversion, unicode normalization, 0.5MB/65k-line limits) and [integrity filtering (DIFC)](https://github.github.com/gh-aw/reference/integrity/) — a trust-based system that filters GitHub content by author association level (`merged > approved > unapproved > none > blocked`), with support for `trusted-users`, `blocked-users`, and `approval-labels` overrides.
3. **Plan-level trust** — the compiler decomposes workflows into stages. The SafeOutputs subsystem buffers all external writes as artifacts, runs a threat detection job (AI-powered scan plus optional custom scanners like Semgrep, TruffleHog, LlamaGuard), and only externalizes writes after the scan passes. [Supply chain protection](https://github.github.com/gh-aw/reference/threat-detection/#supply-chain-protection-protected-files) blocks agent modifications to dependency manifests, CI/CD config, agent instruction files, and CODEOWNERS by default, with `blocked/allowed/fallback-to-issue` policies.

**Relevance to fullsend:** gh-aw is the most mature implementation of "GitHub Actions as agent runtime" (pattern #5 below) and substantially more sophisticated than its homepage summary suggests. Its native position within GitHub eliminates entire categories of problems that fullsend must solve externally: cross-repo dispatch wiring ([ADR 0008](../ADRs/0008-workflow-dispatch-for-cross-repo-dispatch.md)), GitHub App manifest creation ([ADR 0007](../ADRs/0007-per-role-github-apps.md)), enrollment shim security ([ADR 0009](../ADRs/0009-pull-request-target-in-shim-workflows.md)), and the install/uninstall layer stack ([ADR 0006](../ADRs/0006-ordered-layer-model.md)). Its credential isolation via the substrate layer achieves the same security goal as fullsend's host-side L7 REST proxy design ([ADR 0017](../ADRs/0017-credential-isolation-for-sandboxed-agents.md)) with substantially less complexity.

Its integrity filtering system is particularly interesting — it implements a form of input trust tiering (`merged > approved > unapproved > none`) that addresses a subset of what fullsend explores in [autonomy-spectrum.md](problems/autonomy-spectrum.md), though applied to content visibility rather than merge authority. The content sanitization pipeline is a concrete implementation of pre-LLM injection defense, complementing the post-LLM threat detection scan. The orchestration pattern (`dispatch-workflow` / `call-workflow`) provides native multi-workflow coordination that fullsend builds custom infrastructure for.

However, gh-aw explicitly stops at human-in-the-loop automation. It does not address autonomous merge judgment, intent verification, inter-agent trust, or agent governance. Its orchestration is workflow fan-out, not the specialized sub-agent composition with zero-trust review that fullsend envisions. And it inherits GitHub's product constraints — including the position that [developers will always own the merge button](https://github.blog/ai-and-ml/generative-ai/code-review-in-the-age-of-ai-why-developers-will-always-own-the-merge-button/).

The comparison raises a structural question for fullsend: which problems in our implementation are inherent to the goal of autonomous development, and which are artifacts of building externally to the platform we're automating? See [platform-nativeness.md](problems/platform-nativeness.md) for the full analysis.

## Architectural patterns in the field

Five distinct approaches:

### 1. Specialized sub-agent decomposition (Sourcery, CodeRabbit, Qodo)

Multiple reviewers with different specialties, orchestrated by a coordinator. CodeRabbit is the most mature implementation with parallel agents, verification layers, and context splitting.

### 2. Deep codebase indexing (Greptile)

Build a full code graph, trace dependencies across the entire repo. Deepest understanding, but noisiest output. Trade-off between catch rate and signal-to-noise.

### 3. Change-size reduction (Graphite)

Make the problem easier by making PRs smaller. Stacked PRs with clear scope are more tractable for AI review. Doesn't improve the agent's capability, but improves the input quality.

### 4. Deterministic-then-agentic pipelines (Stripe Minions)

Structure the workflow as a pipeline where deterministic steps (context prefetching, linting, pushing) alternate with agentic steps (implementation, CI fix attempts). The agent operates within a bounded, instrumented pipeline rather than with open-ended autonomy. Bounded retry limits prevent runaway loops.

### 5. GitHub Actions as agent runtime (Collo.dev, gh-aw)

Use GitHub's native workflow engine as both the orchestration layer and the compute runtime. Agents are invoked by workflow triggers (label changes, issue comments, schedules), run in ephemeral Actions runners, and coordinate through issues and labels. Zero infrastructure beyond a GitHub repo and an API key. gh-aw adds significant depth to this pattern with containerized execution, network firewalling, artifact-based safe outputs, and AI-powered threat detection — demonstrating that Actions-native agents can have strong guardrails without external infrastructure. The trade-off: tightly coupled to GitHub's event model, limited to Actions runner capabilities and timeouts, and (in the case of gh-aw) constrained by GitHub's product position against autonomous merging.

These are complementary, not competing. A system could use stacked PRs (Graphite's insight) reviewed by specialized sub-agents (CodeRabbit's insight) with deep codebase context where needed (Greptile's insight), all orchestrated through a deterministic pipeline with agentic steps (Stripe's insight), running on GitHub Actions (Collo.dev's insight for teams that want zero infrastructure overhead).

### 6. Multi-agent swarm frameworks (MetaGPT, MAGIS, Swarms.ai, CrewAI)

A growing category of frameworks tackles the broader problem of multi-agent software development, not just review. [MetaGPT](https://github.com/FoundationAgents/MetaGPT) assigns software-company roles (product manager, architect, engineer) and enforces SOPs as structured handoffs between agents. [MAGIS](https://arxiv.org/abs/2403.17927) (NeurIPS 2024) uses four agents (Manager, Repository Custodian, Developer, QA Engineer) for GitHub issue resolution, achieving 8x improvement over raw GPT-4. [Swarms.ai](https://docs.swarms.world/en/latest/swarms/concept/swarm_architectures/) provides a toolkit of swarm topologies (sequential, parallel, DAG, mesh, hierarchical). [CrewAI](https://www.crewai.com/) offers role-based agent teams with a focus on developer experience.

All of these assume a central coordinator and cooperative inter-agent trust — the agents on a team trust each other's outputs. See [agent-architecture.md](problems/agent-architecture.md#relationship-to-multi-agent-frameworks) for how fullsend's approach diverges and what ideas are worth borrowing from this space.

## What nobody is doing

None of these tools address:

- **Formal intent verification** — checking whether a change is authorized against a structured intent system. CodeRabbit's "intent" context is about understanding the PR's purpose, not verifying it against an authorization system.
- **Zero-trust inter-agent review** — agents treating each other's output as untrusted. Existing multi-agent systems implicitly trust the orchestrator and each other.
- **Autonomous merge with security-focused confidence** — the judgment problem of "should this change exist?" as distinct from "is this change correct?"
- **Tier-based autonomy** — different levels of agent authority for different types of changes.
- **Agent governance** — who controls the agents' policies and permissions.
- **Contribution volume management** — how maintainers handle the flood of AI-generated external contributions. See [contribution-volume.md](problems/contribution-volume.md).

These gaps define the novel problem space for fullsend.

gh-aw addresses the *containment* side of agent security more comprehensively than any external system can — its three-layer trust model (substrate isolation, configuration-level policies, plan-level staged execution) achieves strong containment natively. Its integrity filtering system implements a form of trust-based input tiering, and its supply chain protection blocks agent modifications to sensitive files by default. But these mechanisms control *what the agent can see and touch*, not *whether the agent's output should be merged without human review*. The gaps above are all about the judgment layer that sits beyond containment: deciding what should happen, verifying intent, and governing who controls the agents. See [platform-nativeness.md](problems/platform-nativeness.md) for a deeper analysis of which fullsend problems are inherent to the goal versus artifacts of building externally.

## Industry data points

- Monthly code pushes crossed 82 million, merged PRs hit 43 million (GitHub Octoverse)
- ~41% of new code is AI-assisted
- 25-35% growth in code per engineer, but code review capacity remains tied to human limits
- Median PR size increased 33% (March-November 2025): 57 to 76 lines changed per PR
- Lines of code per developer grew from 4,450 to 7,839
- Estimated 40% code review quality deficit projected for 2026

The review bottleneck is getting worse as code generation accelerates. This is the tailwind behind the fullsend vision — the current model of human review doesn't scale.
