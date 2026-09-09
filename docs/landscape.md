# Landscape Analysis

A survey of AI-driven code review systems and **adjacent agent infrastructure** (orchestration, protocol gateways), with analysis of how they relate to the fullsend vision of fully autonomous merge-to-production.

> **Conducted: 2026-03-06.** This landscape is moving fast. This document will rot. If you're reading this months after the date above, it likely needs updating or removing. Treat the specific tool capabilities and pricing as potentially stale; the architectural patterns and gaps analysis may age better.

## The industry consensus

There isn't one. Projects range from entirely banning AI to having AI agents doing work with little structure or oversight.

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

**Relevance to fullsend:** CodeRabbit's decomposition and context engineering are the closest to our sub-agent model. Their intent/environment/history split maps loosely to our intent & coherence sub-agent / correctness sub-agent / drift detection concerns. However, they stop at review — no merge authority. Their verification layer (agent checking other agents' work) is a practical implementation of the kind of inter-agent validation we need, though without zero-trust principles.

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

**Relevance to fullsend:** Sourcery's candor about their limitations is informative. Their specialized-reviewer approach validates our sub-agent decomposition, but their inability to reason about the broader codebase illustrates why context management per sub-agent matters. A correctness sub-agent needs repo context; a security sub-agent needs raw PR content; an intent & coherence sub-agent needs the intent repo. Different sub-agents, different context.

### GitHub Copilot

[Website](https://github.com/features/copilot)

Very relevant to fullsend: has code review and also supports having background agents steered interactively. Backend infrastructure here is reused by GitHub Agentic Workflows (see below).

### GitLab AI Merge Agent

[Announcement](https://www.webpronews.com/gitlabs-ai-merge-agent-automating-chaos-in-code-merges/)

Launched November 2025. The closest thing in the industry to autonomous merging. 85% success rate automating merges, 30% CI/CD time reduction. Resolves simple conflicts autonomously, flags complex ones for humans. Adheres to branch protection rules.

**Relevance to fullsend:** GitLab is solving the *mechanical* merge problem (conflict resolution, CI gating) but not the *judgment* problem (should this change exist?). Our problem is harder — we need the judgment layer. But GitLab's approach to adhering to existing branch protection rules while automating within them is a pattern worth studying.

### OpenClaw

[Website](https://openclaw.ai/) | [GitHub](https://github.com/openclaw/openclaw)

An open-source personal AI assistant framework that runs on your own hardware and connects to 20+ messaging channels (Discord, Slack, Telegram, WhatsApp, iMessage, and others), plus native apps for macOS, iOS, Android, Windows, and Linux. Originally published as Warelay in November 2025, renamed to OpenClaw in January 2026. The fastest-growing open-source project in GitHub history: ~389K stars by mid-2026, surpassing React's 10-year record in roughly 60 days. Developed by the OpenClaw Foundation, an independent 501(c)(3), with infrastructure support from GitHub, NVIDIA, Vercel, and others.

**Architecture:** A long-running Node.js service organized around a local-first Gateway — a WebSocket control plane managing sessions, presence, cron jobs, and webhooks through a single local port. A multi-channel inbox routes inbound messages to isolated agent workspaces, each with its own session history and tools. Skills are compiled to WebAssembly modules for sandboxed execution. Agents maintain persistent memory via a directed graph of memory nodes, allowing context to survive across sessions without context-window overflow. Model-agnostic: supports Anthropic, OpenAI, local models, and others.

**Multi-agent model:** Multiple specialized agents can run within a single deployment, isolated by workspace (a security agent does not share tools with a help desk agent). This is *operational* isolation — reducing blast radius — not *trust* isolation; agents share the host machine's credentials and file system. The agent runtime uses a continuous ReAct (Reason + Act) loop, not a one-shot pipeline.

**Security track record:** Rapid growth brought significant security challenges. The ClawHavoc supply-chain attack (January 2026) planted malware in hundreds of skills in the ClawHub registry, including credential-stealing payloads that persisted by writing to the agent's memory files. Between January and April 2026, 470 security advisories were filed across three disclosure waves. The project responded with mandatory cryptographic skill verification (v2026.4.12) and ongoing hardening. Academic analyses ([arXiv](https://arxiv.org/html/2603.12644v1)) have noted that self-hosted deployment inherits the host machine's full trust surface, making credential isolation a persistent architectural challenge.

**Relevance to fullsend:** OpenClaw and fullsend occupy fundamentally different niches despite both involving AI agents. OpenClaw is a *personal assistant* platform — it connects LLMs to messaging channels and local tools so an individual user can automate tasks across their digital life. Fullsend is a *forge-native autonomous development* system — it connects agents to Git forge events (PRs, issues, merges) so an organization can automate software delivery with structured authority. The architectural differences follow from this purpose gap:

- *Authority model:* OpenClaw agents act with the permissions of the host machine's user account — "whatever the OS account can do." Fullsend agents act through per-role forge identities constrained by CODEOWNERS, branch protection, and required checks, with explicit intent-authorization tiers.
- *Coordination:* OpenClaw uses a centralized Gateway as the control plane. Fullsend uses the repository itself as coordinator — branch protection rules, status checks, and PR state drive agent behavior without a separate coordination service.
- *Review and merge:* OpenClaw has no concept of code review, merge authority, or CI integration — it is not a software development tool. Fullsend's entire problem domain is the judgment layer: deciding whether agent-produced code should ship, with zero-trust review decomposition across independent sub-agents.
- *Security posture:* OpenClaw inherits host-machine trust and has faced supply-chain attacks on its skill registry. Fullsend isolates agents in ephemeral sandboxes with controlled egress, credential isolation via L7 REST proxies, and pre-merge threat detection — treating the agent itself as an untrusted workload.

OpenClaw's scale (389K+ stars, 3M+ active users) validates broad interest in AI agent frameworks, and its multi-channel routing and persistent memory are well-executed for the personal-assistant use case. But its architectural decisions — local-first deployment, host-trust inheritance, messaging-channel orientation — serve a different problem than forge-native autonomous merge. The two projects share terminology (agents, tools, skills, memory) while operating with incompatible trust models.

### Others

- **Cursor Bugbot** — AI code review in the Cursor IDE and GitHub. Optimizes for catching hard-to-find bugs with low false positive rate.
- **Ellipsis** — Bridges review and implementation: takes reviewer comments and automatically implements requested changes.
- **Kodus (Kody)** — Open source. Scans old PRs to learn your team's review style, then mimics it. Learns over time.
- **OpenAI Codex** — Triggered by `@codex review` in GitHub PRs. Behaves as an additional reviewer focused on high-severity issues.
- **Bito** — Uses Claude Sonnet for human-like review. GitHub, GitLab, Bitbucket integration.
- **Caveman** — Output token compression via prompt engineering (~65% savings). Constrains agent output to terse, technical language while preserving reasoning depth. The `caveman-review` format (single-line, emoji-coded comments) is a concrete output format for review sub-agents. [GitHub](https://github.com/juliusbrussee/caveman)
- **PatchPatrol** — AI-powered commit review via pre-commit hooks with local (ONNX, llama.cpp) and cloud (Gemini) inference, offering code quality and OWASP security modes. Local backends eliminate cloud API credential requirements; does not address review decomposition or merge authority. [GitHub](https://github.com/4383/patchpatrol)

## Production agent orchestration systems

While the tools above focus on code review, a separate category of systems addresses end-to-end agent orchestration — from task intake through coding and merge. These are closer to the fullsend vision than review-only tools.

### Forge-sdlc/forge

[GitHub](https://github.com/forge-sdlc/forge) | [README](https://github.com/forge-sdlc/forge/blob/main/README.md) | [Container sandbox](https://github.com/forge-sdlc/forge/blob/main/containers/README.md) | [Skills](https://github.com/forge-sdlc/forge/blob/main/skills/README.md) | [implement_review proposal](https://github.com/forge-sdlc/forge/blob/main/proposals/007-implement-review-node.md)

Forge is an open-source SDLC orchestrator that connects Jira, GitHub, and Claude/Gemini-backed agents. It takes Jira features or bugs through planning artifacts, implementation, pull requests, CI repair, AI review, and human review.

**Bottom line:** Forge is worth studying, not adopting wholesale.

It validates several fullsend assumptions: issue-to-PR automation needs event-driven execution, sandboxed code agents, pre-PR review, CI repair loops, human-visible state, and layered agent instructions. Its strongest ideas are staged intent artifacts, Q&A at approval gates, resumable webhook workflows, project-specific skills, and audited manual overrides.

The mismatch is the authority model. Forge centralizes workflow truth in a FastAPI/Redis/LangGraph worker and treats Jira labels/comments as approval signals. Fullsend is trying to keep authority in repository-visible mechanisms: CODEOWNERS, branch protection, required checks, per-role forge identity, and auditable PR/issue state.

**Workflow:** Forge's feature path is:

`Jira Feature -> PRD -> approval/Q&A -> spec -> approval/Q&A -> epics -> approval/Q&A -> tasks -> approval/Q&A -> implementation -> local review -> PR -> CI/fix loop -> AI review -> human review`

The bug path is shorter:

`Jira Bug -> RCA -> approval/Q&A -> implementation -> PR -> CI/fix loop -> review`

**Architecture:** FastAPI receives Jira and GitHub webhooks, Redis Streams queue events for workers, and LangGraph checkpoints workflow state so later webhooks can resume the graph. A host orchestrator handles planning and Jira/GitHub interaction. Ephemeral Podman containers run implementation agents. Markdown skills resolve from `skills/default` plus per-Jira-project overrides. Prometheus and Langfuse provide metrics and tracing.

**Comparison to fullsend:**

| Area | Forge | Fullsend direction |
|---|---|---|
| Coordination | Central checkpointed workflow | Repository as coordinator |
| Authority | Jira labels/comments, GitHub reviews | CODEOWNERS, branch protection, required checks |
| Intent | Jira elaborated into PRD/spec/tasks | Tiered intent authorization with stronger strategic authorization |
| Review | Local review, AI review, human gate | Independent zero-trust review sub-agents |
| Sandbox | Productive Podman runner | Stricter credential isolation and egress policy |
| Portability | GitHub/Jira-centric | Forge-neutral `forge.Client` abstraction |

**Feature delta:** Forge has several shipped or proposed product features fullsend does not yet have in comparable form:

- PRD/spec/epic/task generation from a single feature ticket.
- Q&A mode at planning approval gates.
- LangGraph checkpointing for long-lived workflow state.
- Per-Jira-project skill overrides.
- A first-class `/forge skip-gate` command for audited CI bypasses.
- An implemented `implement_review` node for addressing or contesting PR feedback.

Fullsend has design commitments that Forge does not appear to cover:

- Repo-as-coordinator semantics for agent interaction.
- Zero-trust review decomposition across independent review sub-agents.
- Per-role forge identity and permission boundaries.
- Credential isolation and egress policy as first-class architecture concerns.
- Harness-level output schema enforcement.
- Multi-forge portability through `forge.Client`.

**Ideas to borrow:**

- *Staged intent artifacts.* Forge's PRD -> spec -> epics -> tasks sequence is a useful model for intent authorization tier 2+ work. Fullsend should borrow the artifact progression, not the Jira-label authority.
- *Q&A without approval.* Humans can ask questions at a gate without approving or rejecting. This fits fullsend's ambiguous-intent and dual-interpretation escalation problems.
- *Checkpointed pause/resume.* Forge waits for humans through durable workflow state, not idle implementation containers. Fullsend should keep this operational pattern while keeping authoritative state repo-visible.
- *Skill override resolution.* `skills/default` plus `skills/{project}` is a simple precedent for fullsend's harness layering.
- *Review feedback as a task type.* Forge's `implement_review` flow classifies review comments as actionable or contested before acting. That is relevant to fullsend's review loop and salvage/rewrite questions.
- *Audited CI gate skips.* `/forge skip-gate` is dangerous unless governed, but the UX is useful: constrained command, named check, PR confirmation, audit comment, and re-evaluation.

**Cautions:** Jira label approval is too weak for high-intent-authorization-tier intent. A workflow engine can dispatch work, but should not become merge authority. Forge's Podman runner is a productivity sandbox, not a full zero-trust boundary. A single AI review stage is not enough for autonomous merge confidence. CI skip mechanisms need permission checks, policy, and auditability from day one.

### OpenHands

[GitHub](https://github.com/all-hands-ai/openhands) | [Website](https://www.all-hands.dev/) | [Docs](https://docs.all-hands.dev/)

A model-agnostic AI coding agent platform (70k+ stars, $18.8M Series A from Oss Capital) that can take GitHub issues and produce draft PRs. OpenHands provides a web interface, a CLI, and a GitHub Actions resolver for autonomous issue-to-PR workflows. It also has a PR review capability. The platform supports multiple LLM backends (Claude, GPT, Gemini, local models via Ollama).

**Architecture:** OpenHands runs agents inside sandboxed Docker containers with a runtime that provides shell access, a code editor, and a web browser. The agent operates in an event-driven loop — it receives an observation (file content, command output, browser state), plans an action, executes it, and repeats. The event stream is the primary audit surface. The GitHub Actions resolver packages this loop for CI: given an issue, it clones the repo into a container, runs the agent, and opens a PR with the result. OpenHands has since pivoted toward an "Agent Canvas" model — a self-hosted developer control center that can run external coding agents (Claude Code, Codex, Gemini) via the Agent Client Protocol (ACP). This makes the platform agent-agnostic at the coding-agent layer, though adoption still requires the OpenHands Agent Server for orchestration.

**Licensing:** The entire project is MIT-licensed. Previously, the enterprise directory (`enterprise/`) was licensed under PolyForm Free Trial, restricting self-hosted cloud deployment via Kubernetes to paid licenses. That directory has since been removed and the project relicensed fully to MIT, eliminating the licensing constraint identified in earlier evaluations.

**Security history:** OpenHands has disclosed prompt injection vulnerabilities. In 2025, security researcher Johann Rehberger demonstrated zero-click token exfiltration and remote code execution via injection in issue text processed by the agent. OpenHands describes its LLM security analyzer as "a soft block, not a hard one" — it flags suspicious content but does not hard-reject it. The vulnerabilities are representative of the broader class of injection attacks that any agent processing untrusted user input faces; see [security-threat-model.md](problems/security-threat-model.md) for the fullsend threat model and why external injection is the highest-priority threat.

**What it doesn't address:** No zero-trust review decomposition — the agent trusts its own output without independent verification. No formal intent verification or intent-authorization tiering. No governance framework for controlling agent policies at the org level. No merge authority — PRs are opened as drafts for human review. The security analyzer is a single-pass check, not a layered defense.

**Relevance to fullsend:** OpenHands' problem space overlaps with fullsend's on code generation and agent sandboxing, but it does not address the problems fullsend considers hard: review decomposition, governance, trust boundaries, and prompt injection defense. Four specific observations:

- *Sandboxing model.* OpenHands' Docker-based sandbox provides process isolation and filesystem separation but does not implement credential isolation, egress filtering, or the kind of defense-in-depth that fullsend's [agent-infrastructure.md](problems/agent-infrastructure.md) and the credential isolation design ([ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md)) require. The sandbox is a productivity boundary, not a zero-trust boundary.
- *Injection surface.* The disclosed injection vulnerabilities confirm that agents processing untrusted issue text are vulnerable to the attacks fullsend's threat model prioritizes. OpenHands' "soft block" analyzer is not sufficient for autonomous merge — where a successful injection could land malicious code in production without human review. This is a concrete data point for the fullsend position that injection defense must be layered and that review agents must treat code-agent output as untrusted.
- *Event stream as audit trail.* The resolver produces a structured event stream that could serve as an observability substrate, relevant to [operational-observability.md](problems/operational-observability.md). Whether it meets enterprise audit trail requirements is an open question — see [#260](https://github.com/fullsend-ai/fullsend/issues/260) for planned experiments evaluating the event stream against fullsend's observability needs.
- *Orchestration vs. agent runtime.* OpenHands' ACP support means the platform is no longer locked to its own coding agent — it can run Claude Code, Codex, or Gemini as ACP subprocesses. This partially mitigates the over-specialization concern (depending on a single agent runtime means chasing innovations in other projects). However, adopting Agent Canvas still requires the OpenHands Agent Server for orchestration, creating a dependency on their orchestration layer even when the coding agent itself is external. The trade-off is agent-agnosticism at the coding layer in exchange for coupling at the orchestration layer.

Concrete experiments against the resolver are tracked in [#260](https://github.com/fullsend-ai/fullsend/issues/260), covering prompt injection red-teaming, event stream audit evaluation, review quality scoring, and tiered intent experiments.

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

Steve Yegge's multi-agent orchestration system, evolved from Gas Town (the original monolith) to Gas City (an orchestration-builder SDK, v1.4, Go, 5,900+ commits as of September 2026). Gas Town coordinates 20-30 parallel coding agents working on feature branches simultaneously. Gas City extracts the reusable infrastructure into composable primitives.

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

### Goosetown

[GitHub](https://github.com/aaif-goose/goosetown) | [Goose CLI](https://github.com/block/goose)

A multi-agent orchestration layer built on Block's [Goose](https://github.com/block/goose) CLI (54k+ stars), explicitly inspired by Gas Town. Goosetown coordinates "flocks" of AI agents — researchers, writers, workers, reviewers — through an orchestrator/delegate pattern with parallel execution. JavaScript/Python, Apache 2.0, early-stage (149 stars, 7 commits as of September 2026).

**Architecture:** An orchestrator session decomposes a request into phases (research → build → review) and dispatches parallel delegates. Twelve role-specific skills ship out of the box: an orchestrator, eight specialized researchers (arxiv, beads, GitHub, Jira, local files, Reddit, Slack, Stack Overflow), a reviewer, a worker, and a writer. Each delegate receives its role-specific skill at spawn time. Communication happens through two mechanisms:

- **gtwall (Town Wall):** A broadcast channel backed by a position-tracked log file. Per-session walls allow multiple Goosetown instances to run simultaneously without interference. Delegates post discoveries, warnings, and progress; siblings read the wall to avoid duplicate work and conflicting edits. A tiered wrap-up protocol (5-min warning → 60-sec warning → force-cancel) gives delegates structured deadlines for completing and summarizing work.
- **Telepathy:** Orchestrator → delegate push messages for urgent paging. The orchestrator writes to a shared file; delegates check their `<info-msg>` for pings. Scoped addressing (`@all`, `@name`) lets the orchestrator page specific delegates or the entire flock.

A real-time dashboard (Python/uv, per-instance, port-isolated) visualizes flock activity by querying Goose's session database and the wall file. Knowledge management follows a structured local-first pattern: GUIDES/, PLANS/, RESEARCH/, and WORK_LOGS/ directories with YAML frontmatter, canonical tags, supersession tracking, and a catalog index.

**Crossfire review:** The review phase uses "crossfire" — multi-model adversarial QA where multiple reviewer delegates (potentially backed by different LLM providers) independently evaluate the work. This is closer to an ensemble approach than a single-agent review pass.

**Relationship to Gas Town:** Goosetown acknowledges Gas Town as direct inspiration but differs in implementation approach. Where Gas City enforces Zero Framework Cognition (no judgment in Go, no skills system, no MCP), Goosetown embraces a skills-and-tools model — twelve predefined skill files loaded into delegates, with Goose's MCP extension system and tool ecosystem available. Where Gas City builds reusable SDK primitives (bead store, event bus, agent protocol), Goosetown is a ready-to-use project scaffold: clone, run `./goose`, describe what to build. The trade-off is flexibility vs. immediacy — Gas City targets framework builders; Goosetown targets practitioners who want multi-agent coordination today.

**Relevance to fullsend:** Three observations and one gap.

- *Research-first phasing* validates the insight that codebase context gathering is a discrete, parallelizable phase that should complete before implementation begins — relevant to [codebase-context.md](problems/codebase-context.md). The eight specialized researcher roles (each querying a different source: GitHub, Jira, arxiv, local files, etc.) are a concrete decomposition of the context-gathering problem.
- *gtwall as coordination primitive* is an interesting middle ground between fullsend's "repo as coordinator" (no inter-agent communication channel) and Gas City's full event bus. The broadcast-and-read-position model is simple enough to reason about but rich enough to prevent duplicate work across parallel delegates. The tiered wrap-up protocol is a practical answer to the session-timeout problem — delegates get structured notice rather than hard kills.
- *Crossfire review* aligns with fullsend's zero-trust review decomposition more than most tools in this landscape — multiple independent reviewers evaluating the same work product. However, Goosetown's reviewers operate cooperatively within the same orchestrator session and trust each other's outputs, unlike fullsend's independent review sub-agents that treat each other's output as untrusted.
- *The gap:* No security threat model, no discussion of prompt injection (delegate skills are loaded from local files, not validated against tampering), no merge authority (the orchestrator produces artifacts but does not merge), no governance framework, and no autonomy spectrum. The orchestrator is a cognitive coordinator — it makes judgment calls about phasing, delegation, and synthesis — which is the opposite of Gas City's ZFC discipline and orthogonal to fullsend's repo-as-coordinator position.

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

### Kiro Crew

[Announcement (2026-08-04)](https://kiro.dev/blog/introducing-kiro-crew/) | [Kiro Crew repo](https://github.com/kirodotdev/kirocrew) | [Kiro CLI repo](https://github.com/kirodotdev/Kiro) | [Kiro docs](https://kiro.dev/docs/)

Kiro is AWS's spec-driven AI IDE: a "unified agent harness" spanning desktop, CLI, web, and mobile surfaces, all reading the same `.kiro/` project configuration — specs (requirements/design/tasks), steering files (project standards), hooks (event-triggered automation), skills, and MCP server config. Kiro Crew, open-sourced 2026-08-04 (started internally at Amazon as "MeshClaw"), is an orchestration layer on the Kiro CLI aimed at multi-session, multi-hour work — incident investigation across repos, migrations, recurring code review/test-fix jobs, ticket triage — that keeps moving through checkpoints and retries while a developer works on something else.

**Architecture:** Three layers. **Surfaces** are how a developer works with it — desktop app, web dashboard, TUI, CLI, Slack, Telegram, WeCom. The **Gateway** is the orchestration layer: it persists session state, injects memory and skills, starts scheduled work, coordinates sub-agents, brokers approvals, and enforces runtime policy — deliberately separating *where the agent runs* from *where you work with it*, so a developer can check in from a phone while the Gateway runs elsewhere. **Agent Sessions** are the execution layer, running `kiro-cli` over the [Agent Client Protocol](https://agentclientprotocol.com) — an existing open standard for editor/agent communication, analogous to LSP and originated at Zed, adopted here rather than invented (not to be confused with "Ambient Code Platform," also abbreviated ACP, discussed below). The protocol gives an "Activity view" where task planning, sub-agent spawning, tool selection, and approvals are observable live instead of hidden inside one opaque chat, and lets a parent conversation delegate to sub-agents that "return their results to the parent conversation." "Apps" package a UI with agents, skills, schedules, integrations, and backend services into a shareable interface for recurring work (examples shipped at launch: work-tree management, a long-running task runner, PR/issue triage, and a LaunchDarkly feature-flag app built on an MCP server), plus an SDK for building more.

**Deployment model:** Local-first and self-hosted, not a managed service — "run it locally or on a remote machine you control," including "your Mac, inside a container on your machine, or on a remote Linux host you control." There is no AWS/Kiro-hosted execution tier. This does allow "always-on" operation (a Gateway running on a home server or cloud instance you administer, reached from Slack or the web dashboard), but the state model is single-tenant: session history, memory, config, and the security audit log all live in one local store (`~/.kiro/crew/`, overridable via `KIROCREW_HOME`) per install. The docs and blog post frame everything around "your crew" and "your work"; there is no workspace, tenant, or per-team isolation concept, and the only "enterprise" references are about an admin locking down security policy on an installed instance, not multiple teams or projects sharing one instance with separated state.

**Memory model:** A three-tier stack — **Memory** (preferences and project context carried into new sessions), **Lessons** (corrections that become "durable lessons," some workspace-scoped), and **Skills** (repeated patterns promoted into inspectable, editable, removable artifacts) — kept visible throughout so a developer can decide what a crew carries forward.

**Security:** Described as "defense in depth from day one" — OS-level sandbox, denied-by-default commands, suspicious-pattern and sensitive-path blocking, credential redaction, per-tool-call approval gates, and a signed audit log. This is a hardened-workstation/CLI sandbox model, not a per-event ephemeral container model — a different threat surface than gh-aw's Actions-runner isolation, closer to "the agent runs where the developer runs."

**Relevance to fullsend:** Kiro Crew's Memory/Lessons/Skills split independently arrives at close to the same three-way distinction [cross-run-memory.md](problems/cross-run-memory.md) draws between stable repo guidance, recent operational state, and agent self-assessment — a real-world existence proof that the split is practically necessary, not just tidy in theory. The difference is promotion policy: Kiro Crew promotes an observation to a durable lesson automatically and leaves it inspectable after the fact, whereas fullsend's open question is whether that promotion needs to be review-gated *before* it can influence a different agent (see the memory-as-attack-surface discussion in that document). Kiro Crew's sub-agent delegation and protocol-based observability are a concrete instance of the parent/child pattern discussed in [agent-architecture.md](problems/agent-architecture.md#relationship-to-multi-agent-frameworks) — a single conversation remains the coordinator, and a sub-agent's returned result is consumed by the parent without the zero-trust composition fullsend's independent review sub-agents use. "Apps" as schedule-plus-agent bundles are a lighter-weight cousin of what fullsend would call a workflow harness, but the trust boundary is a developer's own machine (or a server they administer), not a repo's branch protection and CODEOWNERS.

The single-tenant deployment model is itself a useful data point: Kiro Crew answers "how does one developer's agent act on their behalf across sessions and tools" convincingly, but it is architected as *a developer's* crew, not *an org's* factory — there is no described mechanism for one Gateway to safely serve multiple teams or repos with separated memory, audit, and policy. That gap is exactly the shared-vs-per-repo instance question in [agent-infrastructure.md](problems/agent-infrastructure.md#relationship-to-other-problem-areas) and the "who controls the agents' policies" question in [governance.md](problems/governance.md) — fullsend has to answer both to be a viable multi-team, multi-repo system, where Kiro Crew's single-user framing lets it not.

### Cursor Origin

[Product page](https://cursor.com/origin) | [Cloud Agents docs](https://cursor.com/docs/cloud-agent) | [Origin API docs](https://cursor.com/docs/api/origin) | [Automations docs](https://cursor.com/docs/cloud-agent/automations) | [Cloud Agents announcement](https://cursor.com/cloud)

Cursor Origin is an agent-first git hosting platform from Anysphere (the Cursor team, acquired by SpaceX in August 2026 for $60B). Announced at Compile on June 17, 2026 and rolled out in early beta to paid plans on August 17, 2026, Origin hosts repositories, pull requests, code browsing, and CI connections inside the Cursor editor. Its premise is explicit: the primary users of a version control system are increasingly agents, not humans. The platform is built on Graphite's stacked PR technology (Cursor acquired Graphite in December 2025).

Origin is distinct from [Cursor Bugbot](#others), which is a review tool. Origin is a code hosting platform plus an autonomous coding agent runtime — closer to "forge + compute" than "review bot."

**Architecture:** Origin is one layer in a vertical stack: editor (Cursor IDE) → agent runtime (Cloud Agents) → code hosting (Origin). Cloud Agents (launched February 2026, formerly "Background Agents") run in isolated VMs (Firecracker microVMs) that clone a repository, execute a coding task end-to-end, and produce a pull request with a video recording of the agent demonstrating its work. Each agent gets its own Linux VM with a full development environment — file system, terminal, browser, running application instance. A "Builds" system uses filesystem snapshots so new agent sessions fork a live, initialized machine rather than boot from scratch, cutting startup time by ~3x. Teams can run 10–20 agents in parallel, each in its own sandbox on a separate branch.

The agent harness has five layers: interface (Slack, GitHub, iOS, web, IDE), orchestration (task planning, model routing — agents can use GPT-5, Claude, Gemini, or Cursor's own Composer 2 within the same session), execution (isolated VMs), verification (computer use, video recording, screenshots, logs), and output (PR with artifacts). The video recording layer is distinctive: the agent runs the software it built inside its sandbox, records itself interacting with web pages and validating behavior, and attaches the recording to the PR so reviewers watch a demo instead of mentally simulating a diff.

**Cloud Agent launch surfaces:** Tasks can be started from wherever a developer works — Cursor IDE, Cursor for iOS, Cursor Web (cursor.com/agents), Slack (`@cursor`), or GitHub/Bitbucket (commenting `@cursor` on a PR or issue). 35% of Cursor's own merged PRs are created by Cloud Agents.

**Origin-specific capabilities:**

- *Stacked PRs.* The PR creation API accepts a `parentPullNumber`, allowing dependent changes to be chained as stacked PRs. The merge API merges everything from the stack root up to a specified PR in one operation.
- *Automations.* Event-driven triggers that launch Cloud Agents on push events, PR events, CI completion, review submission, label changes, and custom webhooks. Automations carry persistent memory across executions — an automation reviewing a codebase in month one builds context that improves month six reviews.
- *GitHub sync.* Existing GitHub repos can be mirrored into Origin. Comments sync bidirectionally — post in Origin, it appears on GitHub and vice versa. GitHub remains the source of truth for synced repos; Origin does not require teams to migrate on day one.
- *Integrations.* Vercel, Depot, and Buildkite at launch, with an app ecosystem expanding.
- *Auth and rate limiting.* Scoped permissions (repository metadata, code content, PRs, reviews, checks). Installation tokens get 3,000 points/minute; app JWTs and service-account keys get 600 points/minute. Short-lived tokens and OIDC JWT minting from a local socket in the VM, so agents can assume cloud roles without storing long-lived keys.

**Auto-review (governed autonomy):** Cursor ships an "Auto-review" run mode — a classifier agent that governs local agent autonomy as a dial rather than a switch. Developers define an allowlist of shell commands, MCP tool calls, and HTTP fetch operations. Actions on the allowlist run immediately; anything else is routed to a sandbox that pauses execution and requires explicit user approval. The design acknowledges that asking for permission too often creates its own safety problem — users stop reading prompts carefully and approvals become meaningless.

**What it doesn't address:** Origin stops at the pull request. Cloud Agents create PRs; what happens after merge (deployment, staging, rollback) is the team's responsibility. There is no autonomous merge authority — human review is required. There is no zero-trust review decomposition (agents trust each other within a session). No formal intent verification or intent-authorization tiering. No multi-tenant governance model — Origin's security controls (OIDC, scoped tokens, domain allowlists) govern the agent runtime, not the merge decision. The single-vendor vertical stack (editor + hosting + agents + models, all controlled by one company) raises governance questions that Cursor has not publicly addressed: when one company controls the entire loop, what governs what it does with the code?

**Relevance to fullsend:** Origin is the most vertically integrated agent-first platform in this landscape — it controls the editor, the agent runtime, the code hosting, and the model routing. This gives it low-friction agent workflows (start a task from Slack, watch a video demo on the PR, merge in the IDE) but creates a vendor lock-in surface that fullsend's forge-neutral `forge.Client` abstraction is designed to avoid. Three specific lessons and one structural contrast:

- *Video-as-verification* is a novel approach to the review problem. Instead of reading a diff and inferring behavior, reviewers watch the agent demonstrate its work. This is a concrete implementation of verification artifacts that fullsend's review sub-agents could learn from — attaching behavioral evidence to a PR changes the reviewer's cognitive load. The analog for fullsend would be structured verification artifacts (test output, coverage reports, behavioral demonstrations) attached to the PR by the review harness.
- *Automations with persistent memory* independently arrive at the same cross-run state problem that [cross-run-memory.md](problems/cross-run-memory.md) explores. Cursor's automations accumulate context across executions and use it to improve future runs — but there is no discussion of whether that accumulated memory is an attack surface (a compromised PR could poison the memory that influences future reviews). Fullsend's open question about memory-as-attack-surface applies here.
- *Stacked PRs as a native primitive* validates Graphite's insight (already noted [above](#graphite)) that smaller, focused changes are more tractable for agent review. Origin bakes this into the forge itself rather than layering it on top of GitHub — a structural advantage for agent-scale workflows where 10–20 agents may be producing PRs in parallel.
- *The structural contrast* is the authority model. Origin is a productivity platform: agents do work, humans approve it. Fullsend is pursuing autonomous merge — agents doing work *and* making the judgment call about whether the work should ship. Origin's Auto-review dial moves autonomy along a spectrum within the IDE, but the spectrum ends at "create a PR." Fullsend's autonomy spectrum extends past the PR to the merge decision, which requires the zero-trust review decomposition, intent authorization, and governance layers that Origin does not attempt.

### Vibe Kanban

[GitHub](https://github.com/BloopAI/vibe-kanban) | [Website](https://vibekanban.com/)

An open-source (Apache 2.0) kanban board for orchestrating AI coding agents, built by BloopAI. 26k+ GitHub stars, 30k+ users before the company shut down in April 2026. The core thesis: with agents now writing the code, the human bottleneck has shifted from implementation to planning and review — so the tool optimizes those two activities by giving each agent an isolated workspace behind a kanban-style task board.

**Architecture:** Rust backend (Axum) with a React frontend, distributed as a single npm package (`npx vibe-kanban`). The backend orchestrates workspace lifecycle, git operations (via the `git2` crate), WebSocket event streaming (SQLite-backed), agent process management, and GitHub PR status polling. The frontend provides kanban issue management, inline diff review, and one-click PR creation.

**Git worktree isolation:** The defining feature. Each kanban issue becomes a workspace, and each workspace is a git worktree — a separate working directory on a dedicated branch, sharing the underlying `.git` repository data. Five agents can work in parallel without file conflicts. A dedicated port-management daemon (`dev-manager-mcp`) assigns each workspace a free port for its dev server, so isolation extends to the network layer. When work is complete, the system rebases onto main, merges, and cleans up the worktree.

**MCP dual role:** Vibe Kanban implements the [Model Context Protocol](https://modelcontextprotocol.io/introduction) in both directions. As an MCP *client*, it connects to external MCP servers (databases, search APIs) and exposes those tools to agents in each workspace. As an MCP *server*, it exposes the kanban board itself — external agents can create tasks, move cards, and read board status programmatically. A planning agent can decompose a feature into subtasks and populate the board without human intervention, then downstream agents pick up the generated cards.

**Agent-agnostic orchestration:** The agent abstraction is deliberately thin — Vibe Kanban does not wrap or proxy agent commands. It assumes the developer has authenticated with their preferred agent and provides a terminal where the agent runs normally. Supports 10+ backends: Claude Code, Codex, Gemini CLI, GitHub Copilot, Amp, Cursor, OpenCode, Droid, CCR, and Qwen Code. This makes it an orchestration layer, not an agent framework.

**Review workflow:** Mirrors pull request reviews. When an agent completes a task, the built-in diff tool displays changes. The developer can leave inline comments that feed directly back to the agent (the agent sees the comment and revises), then approve and merge or create a GitHub PR with an AI-generated description.

**Current status:** BloopAI shut down in April 2026, citing inability to find a viable business model despite strong adoption ("the vast majority are free users"). The project transitioned to community maintenance under Apache 2.0. Cloud features were removed; the tool now runs on a fully local architecture. Active forks exist (e.g., [kanvibe](https://github.com/GroupLang/kanvibe)). The BloopAI shutdown is itself a data point about the viability of developer-facing agent tooling as a standalone product — the tool was popular but could not monetize.

**Relevance to fullsend:** Vibe Kanban occupies a different niche from the CI-driven and platform-native systems elsewhere in this landscape. It is a developer productivity tool, not an autonomous merge system. Three patterns and two structural observations are relevant:

- *Worktree-per-task isolation* is the most practical implementation of parallel agent execution in this survey. Each agent gets its own branch, directory, and port — the same isolation primitive that [agent-infrastructure.md](problems/agent-infrastructure.md) identifies as necessary for parallel agent work. Fullsend's sandbox model achieves stronger isolation (ephemeral containers with credential separation), but the worktree pattern is a lightweight alternative for trusted-environment scenarios and validates the requirement.
- *MCP as a coordination API* is a concrete instance of using a standard protocol for inter-agent coordination, relevant to the discussion in [agent-architecture.md](problems/agent-architecture.md#how-agents-communicate). Instead of agents coordinating through git state or issue labels, they interact with the board programmatically — a planning agent creates cards, a coding agent picks them up, and the board state is the shared medium. This is a side-channel coordination pattern (agents talk through the board, not through the repository), which fullsend's repo-as-coordinator position deliberately avoids. The contrast is instructive: MCP coordination is lower-friction but harder to audit than repo-visible coordination.
- *Inline review feedback loop* — the developer reviews diffs and leaves comments that the agent sees and acts on — is a concrete implementation of the review-feedback cycle that [production-feedback.md](problems/production-feedback.md) discusses. Vibe Kanban's version is human-in-the-loop (the developer leaves the comment), but the pattern of structured feedback flowing back to the implementing agent maps to fullsend's review loop, where the review agent's findings flow back to the code agent for revision.
- *The shutdown as signal.* BloopAI's failure to monetize a popular agent orchestration tool suggests that developer-facing agent tooling may commoditize quickly — the value gets absorbed by the agents themselves (Claude Code, Codex) or by the platforms (GitHub, Cursor). This is relevant to fullsend's positioning: fullsend's value proposition is the autonomous merge judgment layer, not the agent orchestration surface, which aligns with the "what nobody is doing" gaps at the end of this document.
- *No trust model.* Vibe Kanban assumes cooperative agents and a trusted developer. There is no inter-agent trust boundary, no injection defense, no intent verification, and no merge authority beyond the developer clicking "merge." This places it firmly on the human-supervised end of the [autonomy spectrum](problems/autonomy-spectrum.md) — useful for productivity but not a path toward autonomous merge confidence.

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

Repository automation from GitHub Next and Microsoft Research, running coding agents (Copilot, Claude, Codex) in GitHub Actions with strong guardrails. Workflows are defined in markdown files with YAML frontmatter specifying triggers (schedule, events), permissions, and safe-output constraints. A `gh aw` CLI extension compiles each markdown definition into a `.lock.yml` GitHub Actions workflow, performing schema validation, expression safety checks, action SHA pinning, and security scanning (actionlint, zizmor, poutine) at compile time. gh-aw entered technical preview in February 2026 and moved to public preview in June 2026; it has not yet reached GA and may still change significantly before then.

**Architecture:** The agent runs in an isolated container on an Actions runner with a read-only `GITHUB_TOKEN`. It produces a structured artifact (SafeOutputs) describing its intended actions. A separate job with scoped write permissions reads the artifact and applies only what the workflow explicitly permits — hard limits per operation, required title prefixes, label constraints. The agent requests; the gated job decides. An [orchestration pattern](https://github.github.com/gh-aw/patterns/orchestration/) supports multi-workflow fan-out via `dispatch-workflow` (async) and `call-workflow` (same run) safe outputs, and [cross-repository operations](https://github.github.com/gh-aw/reference/cross-repository/) allow reading from and writing to external repos via `target-repo` and `allowed-repos` parameters.

**Security model (three trust layers):** gh-aw adopts a formal [defense-in-depth architecture](https://github.github.com/gh-aw/introduction/architecture/) with three trust layers, each constraining failures above it:

1. **Substrate-level trust** — the Actions runner VM, kernel, container runtime, and three privileged containers: the Agent Workflow Firewall (AWF) that uses iptables to redirect HTTP/HTTPS through a Squid proxy enforcing a domain allowlist, an API proxy that routes model traffic while keeping credentials isolated, and an MCP Gateway that spawns isolated containers for each MCP server with per-server domain allowlists and tool allowlisting.
2. **Configuration-level trust** — declarative artifacts (workflow frontmatter, network policies, MCP configs) that constrain what components are loaded, how they connect, and what credentials they receive. Includes [content sanitization](https://github.github.com/gh-aw/introduction/architecture/#content-sanitization) of untrusted input (@mention neutralization, URI filtering to trusted domains, XML/HTML tag conversion, unicode normalization, 0.5MB/65k-line limits) and [integrity filtering (DIFC)](https://github.github.com/gh-aw/reference/integrity/) — a trust-based system that filters GitHub content by author association level (`merged > approved > unapproved > none > blocked`), with support for `trusted-users`, `blocked-users`, and `approval-labels` overrides.
3. **Plan-level trust** — the compiler decomposes workflows into stages. The SafeOutputs subsystem buffers all external writes as artifacts, runs a threat detection job (AI-powered scan plus optional custom scanners like Semgrep, TruffleHog, LlamaGuard), and only externalizes writes after the scan passes. [Supply chain protection](https://github.github.com/gh-aw/reference/threat-detection/#supply-chain-protection-protected-files) flags agent modifications to dependency manifests, CI/CD config, agent instruction files, and CODEOWNERS — the default `request_review` policy still creates the PR but attaches a blocking review requiring human approval, with `blocked` (hard-fail), `allowed`, and `fallback-to-issue` as configurable alternatives.

**Relevance to fullsend:** gh-aw is the most mature implementation of "GitHub Actions as agent runtime" (pattern #5 below) and substantially more sophisticated than its homepage summary suggests. Its native position within GitHub eliminates entire categories of problems that fullsend must solve externally: cross-repo dispatch wiring ([ADR 0008](ADRs/0008-workflow-dispatch-for-cross-repo-dispatch.md)), GitHub App manifest creation ([ADR 0007](ADRs/0007-per-role-github-apps.md)), enrollment shim security ([ADR 0009](ADRs/0009-pull-request-target-in-shim-workflows.md)), and the install/uninstall layer stack ([ADR 0006](ADRs/0006-ordered-layer-model.md)). Its credential isolation via the substrate layer achieves the same security goal as fullsend's host-side L7 REST proxy design ([ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md)) with substantially less complexity.

Its integrity filtering system is particularly interesting — it implements a form of input trust tiering (`merged > approved > unapproved > none`) that addresses a subset of what fullsend explores in [autonomy-spectrum.md](problems/autonomy-spectrum.md), though applied to content visibility rather than merge authority. The content sanitization pipeline is a concrete implementation of pre-LLM injection defense, complementing the post-LLM threat detection scan. The orchestration pattern (`dispatch-workflow` / `call-workflow`) provides native multi-workflow coordination that fullsend builds custom infrastructure for.

The comparison raises a structural question for fullsend: which problems in our implementation are inherent to the goal of autonomous development, and which are artifacts of building externally to the platform we're automating? See [platform-nativeness.md](problems/platform-nativeness.md) for the full analysis.

## Security frameworks and threat taxonomies

### SAFE-MCP

[GitHub](https://github.com/safe-agentic-framework/safe-mcp) | [Website](https://www.safemcp.org/) | [Parent project](https://www.secureagenticframework.org/)

A **threat knowledge framework** — not a runtime security tool — that catalogs adversary tactics, techniques, and procedures (TTPs) targeting MCP implementations and AI agent ecosystems. Initiated by [Astha.ai](https://www.astha.ai/) and now governed under the **Linux Foundation** and the **OpenID Foundation** via the OpenSSF SIG-SAFE-MCP working group, with contributions from engineers at Meta, Microsoft, Google, Red Hat, Intel, eBay, Okta, American Express, and others. Think of it as "MITRE ATT&CK for MCP."

**Structure:** The framework defines **14 tactic categories** (mirroring ATT&CK: Initial Access, Execution, Persistence, Privilege Escalation, Defense Evasion, Credential Access, Discovery, Lateral Movement, Collection, Exfiltration, Impact, Command and Control, Resource Development, Reconnaissance) and **80+ documented techniques** (SAFE-T identifiers). Each technique includes severity ratings, detection strategies, and compliance crosswalks to NIST SP 800-53 and the EU AI Act. Mitigations (SAFE-M identifiers) are categorized as Architectural, Preventive, or Detective.

**Notable techniques:**

- **SAFE-T1001 — Tool Poisoning Attack.** Malicious instructions embedded in MCP tool descriptions that are invisible to users but parsed by LLMs. The MCPTox benchmark measured a 36.5% average attack success rate across 20 LLMs. Sub-techniques include full-schema poisoning and cross-tool poisoning.
- **SAFE-T1102 — Prompt Injection.** Multi-vector exploitation of LLMs' inability to distinguish instructions from data across tool outputs, file contents, database queries, and API responses.
- **SAFE-T1201 — MCP Rug Pull Attack.** Legitimate-appearing tools that undergo delayed malicious modification after gaining user trust, exploiting MCP's dynamic tool definitions.
- **SAFE-T1002 — Supply Chain Compromise.** Distribution of backdoored MCP server packages through compromised repositories.

**Architecture (three pillars):**

1. **Identification and Intent** — OpenID Connect–backed identity, scoped tokens, least-privilege access.
2. **Screening** — detection of prompt manipulation, suspicious tool behavior, poisoned responses.
3. **Policy Enforcement** — context-aware authorization with real-time rule evaluation.

The framework separates a **Control Plane** (signed policy distribution, authorization, sampling budgets) from a **Data Plane** (runtime enforcement of tool execution and resource access). All external inputs — tool descriptions, API responses — are treated as pure data in the Data Plane.

**Notable mitigations:**

- **SAFE-M-1 — Control/Data Flow Separation.** Architectural defense that separates trusted control flow from untrusted data flow. References Google's CaMeL system (77% task completion with provable security guarantees).
- **SAFE-M-7 — Content Rendering Parity.** Ensures what users see matches what the LLM processes — addressing the same class of invisible-payload attacks as [steganographic injection](problems/security-threat-model.md#steganographic-injection-invisible-unicode-payloads), but framed as a general mitigation rather than a Unicode-specific defense.
- **SAFE-M-21 — Output Context Isolation.** Delimiter-based separation preventing data interpretation as instructions.
- **SAFE-M-23 — Tool Output Truncation.** Limiting output size to constrain injection surface area.

**Mapping to fullsend's existing controls:**

| SAFE-MCP concept | Fullsend equivalent | Coverage |
|---|---|---|
| SAFE-T1001 Tool Poisoning | [tool-call-risk-assessment.md](problems/tool-call-risk-assessment.md) (semantic risk beyond pattern matching) | Partial — fullsend identifies the gap between pattern matching and semantic understanding but has not shipped an LLM-as-judge pre-tool hook |
| SAFE-T1102 Prompt Injection | [security-threat-model.md](problems/security-threat-model.md#threat-1-external-prompt-injection) (Threat 1, including steganographic variants) | Strong — fullsend's threat model covers visible injection, invisible Unicode payloads, indirect disclosure, and social pressure vectors |
| SAFE-T1201 Rug Pull / dynamic tool modification | [mcp-config-drift.md](problems/mcp-config-drift.md) (Scenario 2: endpoint replacement, Approach 1: baseline and diff) | Partial — fullsend's mcp-config-drift.md addresses config-level endpoint replacement (Scenario 2), but SAFE-T1201 describes server-side behavioral changes (tools that modify their own definitions after gaining trust); Approach 1 explicitly acknowledges it "does not detect changes to what the MCP server *serves*" |
| SAFE-T1002 Supply Chain Compromise | [security-threat-model.md](problems/security-threat-model.md#threat-4-supply-chain-attacks) (Threat 4, model-as-toolchain) | Strong — fullsend extends supply chain analysis beyond dependencies to the model itself as a Thompson-analog trust boundary |
| Control/Data Plane separation | [ADR 0016](ADRs/0016-unidirectional-control-flow.md) (unidirectional control flow), [ADR 0017](ADRs/0017-credential-isolation-for-sandboxed-agents.md) (credential isolation) | Strong — fullsend enforces this structurally: the harness (control plane) validates and constrains agent output (data plane) without the agent being able to influence the harness |
| SAFE-M-1 Control/Data Flow Separation | [Cross-cutting principle 6 (immutable agent policy)](problems/security-threat-model.md#cross-cutting-security-principles), [ADR 0022](ADRs/0022-harness-level-output-schema-enforcement.md) (output schema enforcement) | Strong — fullsend's architecture enforces this at the sandbox boundary, not just as guidance |
| Content Rendering Parity (SAFE-M-7) | [security-threat-model.md](problems/security-threat-model.md#steganographic-injection-invisible-unicode-payloads) (input sanitization for non-rendering Unicode) | Partial — fullsend addresses the Unicode-specific case but does not frame rendering parity as a general mitigation class |
| 5-level privilege hierarchy | [intent-representation.md](problems/intent-representation.md) (intent authorization tiering) | Different framing — SAFE-MCP's levels (READ-ONLY through SYSTEM ADMIN) are static per-tool ACLs; fullsend's intent authorization tiers are per-change risk classifications that determine autonomy level |
| Compliance crosswalks (NIST, EU AI Act) | Not addressed | Gap — fullsend has no explicit compliance mapping |

**What SAFE-MCP offers that fullsend does not have:**

- *A shared taxonomy for MCP threats.* Fullsend's [security-threat-model.md](problems/security-threat-model.md) is a thorough problem document, but it uses narrative descriptions rather than a structured, cross-referenceable taxonomy. SAFE-MCP's SAFE-T/SAFE-M identifier scheme gives security teams a common vocabulary for discussing and tracking MCP-specific threats.
- *Compliance crosswalks.* Mapping specific attack techniques to NIST SP 800-53 controls and the EU AI Act is useful for organizations that need to demonstrate regulatory compliance of their agent infrastructure. Fullsend does not currently address regulatory framing.
- *Quantified attack benchmarks.* The MCPTox benchmark's 36.5% average attack success rate across 20 LLMs provides an empirical baseline that fullsend's threat model does not have — its discussion of prompt injection effectiveness is qualitative ("fundamentally hard" to detect) rather than quantitative.

**What fullsend covers that SAFE-MCP does not:**

- *Zero-trust inter-agent composition.* SAFE-MCP catalogs threats to individual MCP sessions; fullsend's [Threat 5](problems/security-threat-model.md#threat-5-agent-to-agent-prompt-injection) addresses how agents in a multi-agent pipeline can compromise each other through their outputs.
- *Autonomous merge authority.* SAFE-MCP's scope is the agent–tool boundary (what an agent can access and execute). Fullsend's security model extends past execution to the judgment layer: should the agent's output be merged without human review? This is the domain of [intent authorization tiering](problems/intent-representation.md), [review autonomy evidence](problems/review-autonomy-evidence.md), and [governance](problems/governance.md) — none of which SAFE-MCP attempts.
- *Temporal attack patterns.* Fullsend's [temporal split-payload test poisoning](problems/security-threat-model.md#cross-cutting-attack-pattern-temporal-split-payload-test-poisoning) and [agent drift](problems/security-threat-model.md#threat-3-agent-drift) address threats that unfold across multiple sessions and PRs. SAFE-MCP's per-session threat model does not capture multi-session attack chains.
- *Agent self-report unreliability.* Fullsend's [cross-cutting concern](problems/security-threat-model.md#cross-cutting-concern-agent-self-report-unreliability) about agents misrepresenting their own actions is not in SAFE-MCP's scope.

**Relevance to fullsend:** SAFE-MCP is a useful **reference taxonomy**, not an adoption candidate. Its structured TTP catalog validates fullsend's threat model coverage — the core MCP attack vectors (tool poisoning, prompt injection, rug pulls, supply chain compromise) are already identified and addressed in fullsend's problem documents, in most cases with deeper treatment. The main gaps it surfaces are presentation-level, not architectural: fullsend could benefit from a structured identifier scheme for its own threats (enabling cross-referencing and compliance mapping) and from quantitative benchmarks for attack success rates. The compliance crosswalks to NIST SP 800-53 and the EU AI Act are relevant for organizations using fullsend that need to demonstrate regulatory compliance — this is something fullsend's documentation does not currently address and could reference SAFE-MCP's crosswalks for.

The framework does not address fullsend's core differentiators (zero-trust agent composition, autonomous merge judgment, intent authorization tiering), so it is complementary rather than competing. The recommended action is to reference SAFE-MCP's taxonomy when discussing MCP-specific threats in fullsend documentation, and to evaluate whether its compliance crosswalks are useful for the applied docs of organizations with regulatory requirements.

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

Use GitHub's native workflow engine as both the orchestration layer and the compute runtime. Agents are invoked by workflow triggers (label changes, issue comments, schedules), run in ephemeral Actions runners, and coordinate through issues and labels. Zero infrastructure beyond a GitHub repo and an API key. gh-aw adds significant depth to this pattern with containerized execution, network firewalling, artifact-based safe outputs, and AI-powered threat detection — demonstrating that Actions-native agents can have strong guardrails without external infrastructure. The trade-off: tightly coupled to GitHub's event model, limited to Actions runner capabilities and timeouts.

These are complementary, not competing. A system could use stacked PRs (Graphite's insight) reviewed by specialized sub-agents (CodeRabbit's insight) with deep codebase context where needed (Greptile's insight), all orchestrated through a deterministic pipeline with agentic steps (Stripe's insight), running on GitHub Actions (Collo.dev's insight for teams that want zero infrastructure overhead).

### 6. Multi-agent swarm frameworks (MetaGPT, MAGIS, Swarms.ai, CrewAI)

A growing category of frameworks tackles the broader problem of multi-agent software development, not just review. [MetaGPT](https://github.com/FoundationAgents/MetaGPT) assigns software-company roles (product manager, architect, engineer) and enforces SOPs as structured handoffs between agents. [MAGIS](https://arxiv.org/abs/2403.17927) (NeurIPS 2024) uses four agents (Manager, Repository Custodian, Developer, QA Engineer) for GitHub issue resolution, achieving 8x improvement over raw GPT-4. [Swarms.ai](https://docs.swarms.world/en/latest/swarms/concept/swarm_architectures/) provides a toolkit of swarm topologies (sequential, parallel, DAG, mesh, hierarchical). [CrewAI](https://www.crewai.com/) offers role-based agent teams with a focus on developer experience.

All of these assume a central coordinator and cooperative inter-agent trust — the agents on a team trust each other's outputs. See [agent-architecture.md](problems/agent-architecture.md#relationship-to-multi-agent-frameworks) for how fullsend's approach diverges and what ideas are worth borrowing from this space.

## What nobody is doing

None of these tools address:

- **Formal intent verification** — checking whether a change is authorized against a structured intent system. CodeRabbit's "intent" context is about understanding the PR's purpose, not verifying it against an authorization system.
- **Zero-trust inter-agent review** — agents treating each other's output as untrusted. Existing multi-agent systems implicitly trust the orchestrator and each other.
- **Autonomous merge with security-focused confidence** — the judgment problem of "should this change exist?" as distinct from "is this change correct?"
- **Intent-authorization-tier-based autonomy** — different levels of agent authority for different types of changes.
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
