# Roadmap

Where fullsend is, and where it is going. Organized as **Now / Next / Later** — what we are actively building, what follows immediately after, and what we see on the horizon.

Categories are listed in the priority order established at the [July 1 planning session](#now). The ordering reflects team dot-voting decisions.

## At a glance

| Priority | Category | Focus | Horizon |
|:--------:|----------|-------|:-------:|
| 1 | [**BYOA**](#byoa) | Agent catalog, harness triggers, config knobs, shareable config profiles | Now |
| 2 | [**Infrastructure**](#infrastructure) | Drop per-org, unify installs, version pinning, OpenShell improvements, GitLab, OpenCode | Now |
| 3 | [**Observability**](#observability) | Cost measurement, telemetry phase 2 & 3, surfacing hidden agent failures | Now |
| 4 | [**Testing**](#testing) | Behavior tests for deterministic code, functional tests for all agents, evals, stage tests | Now |
| 5 | [**External Partnerships**](#external-partnerships) | OpenShell/Ansible/TektonCD using fullsend, community building, docs improvements | Now |
| 6 | [**JIRA**](#jira) | JIRA support for all default agents, mint for JIRA | Now |
| 7 | [**mint**](#mint) | Extract mint repo, finish public mint, e2e tests, move to prod GCP project | Now |
| 8 | [**Agent Data Access**](#agent-data-access) | Data connectors (JIRA, GitLab, Slack), multi-repo context, agent environment planning | Now |
| 9 | [**Exploration**](#exploration) | Persistent agent memories, auto-merge (tiny percentage) | Next |
| — | [Cross-forge orchestration](#cross-forge-orchestration) | Coordinating agent work across GitHub + GitLab orgs | Next |
| — | [Kubernetes and OpenShift execution](#kubernetes-and-openshift-execution) | K8s/OpenShift as agent runtime | Later |
| — | [Security hardening](#security-hardening) | Prompt injection defense, credential isolation, threat model | Later |
| — | [Human factors and governance](#human-factors-and-governance) | Domain ownership, review fatigue, contributor motivation | Later |
| — | [Production feedback loops](#production-feedback-loops) | Production signals driving triage and prioritization | Later |
| — | [Agent attestations](#agent-attestations) | Cryptographic provenance for agent output | Later |

## June 2026 (done)

June focused on platform architecture maturation, harness portability, review agent reliability, and developer experience. Over 90 PRs merged and 150+ issues closed.

What this phase delivered:

- **Agent registration and BYOA foundations** — ADR 0058 landed in three phases: agent registration schema, `fullsend agent` CLI subcommand, and runtime agent resolution from config ([#2768](https://github.com/fullsend-ai/fullsend/pull/2768), [#2769](https://github.com/fullsend-ai/fullsend/pull/2769), [#2770](https://github.com/fullsend-ai/fullsend/pull/2770))
- **Unified env var delivery** — ADR 0055 unified runner and sandbox environment variable delivery, with all default agents migrated to the new `env.runner`/`env.sandbox` schema ([#2582](https://github.com/fullsend-ai/fullsend/pull/2582), [#2763](https://github.com/fullsend-ai/fullsend/pull/2763), [#2762](https://github.com/fullsend-ai/fullsend/pull/2762), [#2759](https://github.com/fullsend-ai/fullsend/pull/2759))
- **PR-based scaffold delivery** — install now defaults to creating PRs instead of pushing directly to the default branch, with `--direct` flag for the old behavior and fork support for non-owner users ([#2533](https://github.com/fullsend-ai/fullsend/pull/2533), [#2630](https://github.com/fullsend-ai/fullsend/pull/2630))
- **Docs site migration** — migrated from Docusaurus to VitePress with a redesigned landing page, mermaid diagram rendering, and sidebar ordering control ([#2721](https://github.com/fullsend-ai/fullsend/pull/2721), [#2701](https://github.com/fullsend-ai/fullsend/pull/2701), [#2754](https://github.com/fullsend-ai/fullsend/pull/2754))
- **Dispatch hardening** — label-based gating for agent dispatch (ADR 0054), retro dispatch skip guards for bot dependency PRs, and rate-limit retry improvements ([#2679](https://github.com/fullsend-ai/fullsend/pull/2679), [#2764](https://github.com/fullsend-ai/fullsend/pull/2764))
- **Review agent reliability** — review sub-agent depth scaling, challenger pass dedicated sub-agent, line-number verification, finding deduplication, and severity-aligned verdicts ([#2695](https://github.com/fullsend-ai/fullsend/pull/2695))
- **OpenShell tracking** — tracked OpenShell through versions 0.0.38 to 0.0.72, resolving sandbox boundary checks, nftables requirements, JWT auth conflicts, and supervisor image pinning ([#1763](https://github.com/fullsend-ai/fullsend/issues/1763), [#1764](https://github.com/fullsend-ai/fullsend/issues/1764), [#1765](https://github.com/fullsend-ai/fullsend/issues/1765), [#1766](https://github.com/fullsend-ai/fullsend/issues/1766), [#1768](https://github.com/fullsend-ai/fullsend/issues/1768))
- **Renovate enablement** — self-hosted Renovate GitHub App with automerge for low-risk dependency PRs ([#2480](https://github.com/fullsend-ai/fullsend/pull/2480), [#2546](https://github.com/fullsend-ai/fullsend/pull/2546))
- **Agent status comments** — agents now post status comments on workflow start and completion, with timeline analysis and token-expiry resilience ([#1859](https://github.com/fullsend-ai/fullsend/issues/1859))
- **Security hardening** — all GitHub Actions pinned to full-length commit SHAs, DCO enforcement, SRI attributes on CDN scripts ([#2508](https://github.com/fullsend-ai/fullsend/pull/2508), [#2509](https://github.com/fullsend-ai/fullsend/pull/2509))
- **E2e expansion** — org pool expanded from 6 to 12, fork PR support, functional test gates with collaborator permission fallback ([#2766](https://github.com/fullsend-ai/fullsend/pull/2766))
- **Standalone runtime progress** — standalone mint with custom role support shipped ([#2537](https://github.com/fullsend-ai/fullsend/pull/2537)), default `--mint-url` now points to hosted public mint removing GCP provisioning requirement ([#2073](https://github.com/fullsend-ai/fullsend/issues/2073)), `fullsend run` wired with Lint diagnostics and `LoadWithBase` pipelines ([#2362](https://github.com/fullsend-ai/fullsend/pull/2362), [#2224](https://github.com/fullsend-ai/fullsend/pull/2224)), and `--vendor` flag for self-contained workflow assets ([#2145](https://github.com/fullsend-ai/fullsend/issues/2145))
- **Harness CEL dispatch** — ADR for harness-level CEL dispatch and NormalizedEvent v1 landed ([#2650](https://github.com/fullsend-ai/fullsend/pull/2650))
- **URL-based harness composition** — resolved scripts, skills, and declarative resources from URL-referenced base harnesses ([#2525](https://github.com/fullsend-ai/fullsend/pull/2525), [#2690](https://github.com/fullsend-ai/fullsend/pull/2690), [#2707](https://github.com/fullsend-ai/fullsend/pull/2707))

## Now

What we are actively building and shipping. Categories are ordered by priority from the July 2026 planning session.

### BYOA

Making fullsend a platform teams can adopt incrementally and extend freely. This is the team's highest priority for July — driven by user demand for custom agents, better configuration, and simplified adoption.

The custom agent interface needs to be clean enough that replatforming an existing agent is straightforward, not a rewrite. Easy local agent runs let users test-drive custom agents and skills before wiring them into CI. An agent catalog (an "awesome list" style repository for discovering and sharing agent definitions) will make the ecosystem more visible and navigable. Shareable config profiles let teams preconfigure a deployment with a single URL. Scribe agent enhancements address multiple outstanding user requests and will move to the agents repo as part of the re-platforming effort.

Examples of work that could move this forward:

- Harness triggers and dynamic agent dispatching ([#2565](https://github.com/fullsend-ai/fullsend/issues/2565))
- Major config knobs for agents — making agents more adaptable to user preferences ([#2832](https://github.com/fullsend-ai/fullsend/issues/2832))
- Scribe agent enhancements and migration to agents repo ([#895](https://github.com/fullsend-ai/fullsend/issues/895), [#222](https://github.com/fullsend-ai/fullsend/issues/222), [#1674](https://github.com/fullsend-ai/fullsend/issues/1674))
- Re-platform default agents as harness-driven configs ([#1986](https://github.com/fullsend-ai/fullsend/issues/1986), [#1985](https://github.com/fullsend-ai/fullsend/issues/1985))
- Harness definition architecture and config schema ([#173](https://github.com/fullsend-ai/fullsend/issues/173), [#179](https://github.com/fullsend-ai/fullsend/issues/179), [#235](https://github.com/fullsend-ai/fullsend/issues/235))
- Skills loading policy and org/repo inheritance ([#237](https://github.com/fullsend-ai/fullsend/issues/237), [#236](https://github.com/fullsend-ai/fullsend/issues/236))
- Selective agent enablement in config ([#581](https://github.com/fullsend-ai/fullsend/issues/581), [#604](https://github.com/fullsend-ai/fullsend/issues/604))
- Authorization model for agent invocations ([#1662](https://github.com/fullsend-ai/fullsend/issues/1662), [#1687](https://github.com/fullsend-ai/fullsend/issues/1687))
- Provider and profile resolution from URL-referenced bases ([#2672](https://github.com/fullsend-ai/fullsend/issues/2672))

### Infrastructure

Platform infrastructure, technical debt reduction, and runtime improvements. This category consolidates what was previously split across "Agent Capabilities & Runtime", "Upgrades & Versioning", and "New Forges" from the June plan — the team recognized these share enough infrastructure overlap to manage together.

Key themes: deprecating per-org installs in favor of a unified approach, version pinning and automatic upgrades, OpenShell improvements (Go SDK migration, API extensibility, Vertex API authorization fixes), running agents outside GitHub Actions (GitLab, Tekton infrastructure), and OpenCode alignment with the global engineering working group.

Examples of work that could move this forward:

- Drop per-org installs — deprecate and remove in favor of unified install ([#2454](https://github.com/fullsend-ai/fullsend/issues/2454), [#2302](https://github.com/fullsend-ai/fullsend/issues/2302))
- Version pinning and automatic upgrades ([#1933](https://github.com/fullsend-ai/fullsend/issues/1933), [#2454](https://github.com/fullsend-ai/fullsend/issues/2454))
- OpenShell improvements — simplification, Go SDK migration, API extensibility, Vertex authorization fixes ([#2692](https://github.com/fullsend-ai/fullsend/issues/2692))
- GitLab support — webhook bridge, GitLab CI as trigger/coordination layer ([#1964](https://github.com/fullsend-ai/fullsend/issues/1964))
- Forge-portable harness schema ([#1605](https://github.com/fullsend-ai/fullsend/issues/1605))
- OpenCode alignment with global engineering working group ([#1260](https://github.com/fullsend-ai/fullsend/issues/1260), [#1935](https://github.com/fullsend-ai/fullsend/issues/1935))
- Standalone local runtime and self-hosted support ([#1963](https://github.com/fullsend-ai/fullsend/issues/1963), [#595](https://github.com/fullsend-ai/fullsend/issues/595))
- Refactor runAgent for testability ([#2831](https://github.com/fullsend-ai/fullsend/issues/2831))

### Observability

Understanding what agents cost, how they perform, and where they silently fail. This is a new category for July — elevated because users are increasingly asking for visibility into agent behavior and costs.

A key problem surfaced in the planning session: agents can silently repeat the same mistakes across separate runs with no mechanism to surface previous failures. Cost measurement and aggregation will help teams understand their agent usage. Telemetry phase 2 & 3 build on existing tracing foundations to provide deeper operational insight.

Examples of work that could move this forward:

- Cost measurement and aggregation — per-repo and per-agent token/cost tracking ([#2668](https://github.com/fullsend-ai/fullsend/issues/2668))
- Telemetry phase 2 & 3 — OpenTelemetry Go SDK for trace export ([#2780](https://github.com/fullsend-ai/fullsend/issues/2780)), trace chain integrity ([#2779](https://github.com/fullsend-ai/fullsend/issues/2779))
- Agent error visibility — handling `is_error:true` responses from runtimes ([#2786](https://github.com/fullsend-ai/fullsend/issues/2786))
- OIDC token staleness when sandbox setup exceeds timeout ([#2783](https://github.com/fullsend-ai/fullsend/issues/2783))
- Release summary bot for automated changelog visibility ([#2778](https://github.com/fullsend-ai/fullsend/issues/2778))

### Testing

How we gain confidence in what we ship. Building comprehensive testing infrastructure across behavioral tests, functional tests, evaluation frameworks, and staging environments.

The team identified a gap between how e2e tests work (vendored files, per-commit tricks) and how users actually use fullsend. Stage tests running post-merge in a staging environment will close this gap and provide more realistic validation.

Examples of work that could move this forward:

- Behavior tests for deterministic code paths — tests that validate without running LLMs ([#346](https://github.com/fullsend-ai/fullsend/issues/346))
- Evaluation frameworks — SWE-bench pilot, Harbor for code-agent outcome eval ([#2510](https://github.com/fullsend-ai/fullsend/issues/2510))
- Statistical significance layer for non-deterministic evals ([#2460](https://github.com/fullsend-ai/fullsend/issues/2460))
- E2e test improvements — bot authorization fixes, auth alignment ([#2641](https://github.com/fullsend-ai/fullsend/issues/2641), [#2772](https://github.com/fullsend-ai/fullsend/issues/2772), [#2489](https://github.com/fullsend-ai/fullsend/issues/2489))
- Trustworthiness evidence — rework rate tracking, review outcome analysis ([#295](https://github.com/fullsend-ai/fullsend/issues/295))

### External Partnerships

Making fullsend visible, understandable, and usable by teams outside the core group. This category combines documentation improvements with active partnership engagement — recognizing that docs quality and external adoption are tightly linked.

Multiple teams are actively using or evaluating fullsend: OpenShell, Ansible, TektonCD, and potential enterprise partnerships. The Tekton CI team is hitting GitHub Actions resource limits, making non-GHA execution an increasingly relevant concern. Documentation improvements are a direct response to user feedback — people are adopting fullsend but struggling with the docs. The team plans to schedule screen-share sessions with users to observe how they interpret documentation and identify friction points.

Examples of work that could move this forward:

- Docs site experiments content and public mint docs ([#2757](https://github.com/fullsend-ai/fullsend/issues/2757))
- Document maintainer onboarding process ([#2653](https://github.com/fullsend-ai/fullsend/issues/2653))
- JIRA data leakage risk documentation for public repos ([#2513](https://github.com/fullsend-ai/fullsend/issues/2513))

### JIRA

Connecting fullsend to JIRA — extending the trigger model beyond forge events into project management. This is focused specifically on making fullsend agents work with JIRA data and workflows.

The scope covers JIRA support across all default agents (not just triage), credential management for JIRA service accounts, and the possibility of a dedicated mint for JIRA using Workload Identity Federation. The plan is to start with public JIRA projects to avoid private data exposure.

Examples of work that could move this forward:

- JIRA support for all default agents — triage ([#2264](https://github.com/fullsend-ai/fullsend/issues/2264)), code ([#2265](https://github.com/fullsend-ai/fullsend/issues/2265)), prioritize ([#2266](https://github.com/fullsend-ai/fullsend/issues/2266)), retro ([#2267](https://github.com/fullsend-ai/fullsend/issues/2267)), review ([#2268](https://github.com/fullsend-ai/fullsend/issues/2268)), refine ([#1341](https://github.com/fullsend-ai/fullsend/issues/1341))
- JIRA trigger model ([#2263](https://github.com/fullsend-ai/fullsend/issues/2263))
- Mint for JIRA — Workload Identity Federation for JIRA service accounts ([#2269](https://github.com/fullsend-ai/fullsend/issues/2269))

Related: [downstream-upstream](problems/downstream-upstream.md), [intent-representation](problems/intent-representation.md)

### mint

Extracting, hardening, and operationalizing the token mint as a standalone service. The mint is already fairly standalone in the codebase — this work completes the separation, adds proper test coverage, and moves it to production infrastructure. Longer-term goals include extracting the mint into its own repository and migrating mint infrastructure to a dedicated GCP project separate from dev/inference.

Examples of work that could move this forward:

- Finish public mint work — implementing ADR 0059 public mint mode ([#2773](https://github.com/fullsend-ai/fullsend/pull/2773), [#2073](https://github.com/fullsend-ai/fullsend/issues/2073))
- mint delete command for infrastructure teardown ([#2680](https://github.com/fullsend-ai/fullsend/issues/2680))
- Token caching with safe refresh across nested CLI invocations ([#2542](https://github.com/fullsend-ai/fullsend/issues/2542))
- Consolidate agent role lists and permission definitions ([#2449](https://github.com/fullsend-ai/fullsend/issues/2449))
- Evaluate database-backed persistence for mint identity ([#2564](https://github.com/fullsend-ai/fullsend/issues/2564))
- Mint service decomposition criteria ([#2437](https://github.com/fullsend-ai/fullsend/issues/2437))
- Deployment suggestions and health check capabilities ([#2438](https://github.com/fullsend-ai/fullsend/issues/2438))

### Agent Data Access

Giving agents access to data beyond the repository — JIRA, GitLab, Slack, Google Drive, and multi-repo context. This is distinct from the JIRA category (which focuses on JIRA-specific workflows) and addresses the broader challenge of connecting agents to external data sources securely.

The team recognized this requires more than just adding skills: it involves credential management, network policies, service accounts, and context-aware loading for data sources like JIRA, GitLab, Slack, and Google Drive. ADRs are needed before implementation to establish patterns rather than accumulating ad-hoc integrations.

Examples of work that could move this forward:

- Multi-repo context loading and cross-repo changes ([#298](https://github.com/fullsend-ai/fullsend/issues/298), [#401](https://github.com/fullsend-ai/fullsend/issues/401), [#1276](https://github.com/fullsend-ai/fullsend/issues/1276))
- Secretless deployment and credential management strategies ([#1952](https://github.com/fullsend-ai/fullsend/issues/1952), [#1604](https://github.com/fullsend-ai/fullsend/issues/1604))
- Least-privilege path for workflow file changes ([#2822](https://github.com/fullsend-ai/fullsend/issues/2822))
- Human-gated permission adjustments ([#2821](https://github.com/fullsend-ai/fullsend/issues/2821), [#2829](https://github.com/fullsend-ai/fullsend/issues/2829))

## Next

What follows once the current work stabilizes.

### Exploration

Ideas the team is actively thinking about but not yet committed to building. These received no votes in the July planning session but are tracked for future consideration.

- **Persistent agent memories** — agents retain context and history across sessions, enabling learning from past mistakes. The team agreed this must be traceable and transparent to humans — hidden memory is rejected. Security concerns around persistent threats through memory injection need resolution before this moves forward.
- **Auto-merge (tiny percentage)** — beginning to reason about where auto-merge is safe, starting with a very small percentage of changes where trustworthiness evidence supports it. Related: [autonomy-spectrum](problems/autonomy-spectrum.md), [code-review](problems/code-review.md), ADR 0062 ([#2791](https://github.com/fullsend-ai/fullsend/pull/2791))

### Cross-forge orchestration

Coordinating agent work across multiple forges (GitHub + GitLab, or multiple GitHub orgs) when a single logical change spans organizational boundaries.

## Later

Problems we are actively thinking about but not yet building. These are informed by the [problem documents](problems/) and will move into **Next** as the platform matures.

### Kubernetes and OpenShift execution

When the sandbox runtime matures to run practically in Kubernetes and OpenShift, fullsend should support that as an execution environment. This also opens the door to triggering agent workflows from sources beyond GitHub and GitLab — decoupling the agent runtime from the forge.

### Security hardening

Ongoing work informed by the [security threat model](problems/security-threat-model.md):

- Prompt injection detection and andon cord ([#172](https://github.com/fullsend-ai/fullsend/issues/172), [#174](https://github.com/fullsend-ai/fullsend/issues/174))
- Org guardrail protection ([#84](https://github.com/fullsend-ai/fullsend/issues/84))
- Workflow security scanning ([#159](https://github.com/fullsend-ai/fullsend/issues/159))
- Agent authority modeling ([#877](https://github.com/fullsend-ai/fullsend/issues/877))
- Separate permission profiles per run phase ([#2826](https://github.com/fullsend-ai/fullsend/issues/2826))
- Privileged operations only in deterministic automation ([#2828](https://github.com/fullsend-ai/fullsend/issues/2828))

### Human factors and governance

As autonomous contribution scales, the organizational questions become unavoidable: domain ownership shifts, review fatigue, contributor motivation, and who has authority to make binding decisions about agent behavior.

- Related: [human-factors](problems/human-factors.md), [governance](problems/governance.md), [contribution-volume](problems/contribution-volume.md)

### Production feedback loops

Closing the loop between production signals and what agents work on next. Platform organizations generate structured execution data that can drive triage and prioritization without waiting for humans to notice failures.

- Related: [production-feedback](problems/production-feedback.md)

### Agent attestations

Cryptographic attestation of agent-produced artifacts, enabling consumers to verify what agent produced a change, under what policy, and with what inputs.

- See [#267](https://github.com/fullsend-ai/fullsend/issues/267)

## Foundation (April–May 2026)

Fullsend reached MVP in April 2026 and scaled through May. The platform can be installed at the org level, enroll repositories, and run a full autonomous SDLC loop: triage issues, produce code and tests, review PRs, apply fixes from review feedback, and file retrospective improvement proposals. The core agent suite ships as **default agents** and is designed to be general, extensible, and replaceable.

What this phase established:

- **Sandboxed runner architecture** — agents execute in isolated environments with controlled access to forge credentials and repository content
- **Default agent suite** — default agents that enable an end-to-end bugfix workflow: triage, code, review, fix, and retro
- **Binary autonomy model** — per-repo opt-in, CODEOWNERS enforcing human approval on protected paths
- **The repo is the coordinator** — branch protection, CODEOWNERS, and status checks replace a coordinator agent
- **Trust derives from repository permissions, not agent identity**
- **Fullsend is using fullsend** — the platform dogfoods its own agent workflows
- **20+ Konflux repositories** running fullsend for bug triage, code production, and review
