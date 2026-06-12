# Roadmap

Where fullsend is, and where it is going. Organized as **Now / Next / Later** — what we are actively building, what follows immediately after, and what we see on the horizon.

## Foundation (done)

Fullsend reached MVP in April 2026 and scaled through May. The platform can be installed at the org level, enroll repositories, and run a full autonomous SDLC loop: triage issues, produce code and tests, review PRs, apply fixes from review feedback, and file retrospective improvement proposals. The core agent suite ships as **default agents** and is designed to be general, extensible, and replaceable.

What this phase established:

- **Sandboxed runner architecture** — agents execute in isolated environments with controlled access to forge credentials and repository content
- **Default agent suite** — default agents that enable an end-to-end bugfix workflow: triage, code, review, fix, and retro
- **Binary autonomy model** — per-repo opt-in, CODEOWNERS enforcing human approval on protected paths
- **The repo is the coordinator** — branch protection, CODEOWNERS, and status checks replace a coordinator agent
- **Trust derives from repository permissions, not agent identity**
- **Fullsend is using fullsend** — the platform dogfoods its own agent workflows
- **20+ Konflux repositories** running fullsend for bug triage, code production, and review

## Now

What we are actively building and shipping.

### Adoption and extensibility

Teams use fullsend as a platform — plugging in their own agents, skills, and orchestration while inheriting the platform's security model, sandbox isolation, and coordination layer. At the same time, teams adopt fullsend incrementally — enabling only the capabilities they want without committing to the full workflow or extensive infrastructure setup.

The custom agent interface needs to be clean enough that replatforming an existing agent is straightforward, not a rewrite. Prospective users are requesting simplified onboarding, selective agent enablement, and a clear authorization model that prevents non-maintainers from triggering agent workloads without team approval.

Examples of work that could move this forward:

- Harness definition architecture and config schema ([#173](https://github.com/fullsend-ai/fullsend/issues/173), [#179](https://github.com/fullsend-ai/fullsend/issues/179), [#235](https://github.com/fullsend-ai/fullsend/issues/235))
- Re-platform default agents as harness-driven configs ([#1986](https://github.com/fullsend-ai/fullsend/issues/1986), [#1985](https://github.com/fullsend-ai/fullsend/issues/1985))
- Skills loading policy and org/repo inheritance ([#237](https://github.com/fullsend-ai/fullsend/issues/237), [#236](https://github.com/fullsend-ai/fullsend/issues/236))
- Forge-portable harness schema ([#1605](https://github.com/fullsend-ai/fullsend/issues/1605), [#1848](https://github.com/fullsend-ai/fullsend/pull/1848))
- Per-repo workflow definitions ([#69](https://github.com/fullsend-ai/fullsend/issues/69))
- Secretless deployment via Workload Identity Federation ([#1952](https://github.com/fullsend-ai/fullsend/issues/1952), [#1604](https://github.com/fullsend-ai/fullsend/issues/1604))
- Per-repo installation (adopting without org-wide configuration) ([#727](https://github.com/fullsend-ai/fullsend/issues/727), [#1954](https://github.com/fullsend-ai/fullsend/pull/1954))
- Reducing infrastructure requirements during onboarding ([#1216](https://github.com/fullsend-ai/fullsend/issues/1216), [#1145](https://github.com/fullsend-ai/fullsend/issues/1145))
- Selective agent enablement in config ([#581](https://github.com/fullsend-ai/fullsend/issues/581), [#604](https://github.com/fullsend-ai/fullsend/issues/604))
- Authorization model for agent invocations ([#1662](https://github.com/fullsend-ai/fullsend/issues/1662), [#1687](https://github.com/fullsend-ai/fullsend/issues/1687))

### Quality protections

Build up the testing, staging, and evaluation infrastructure that gives us confidence in what we ship. Evals, behavioral tests, functional tests, dedicated staging environments, and improved end-to-end coverage — making it harder for regressions to slip through and easier to verify that agents behave correctly.

Examples of work that could move this forward:

- Behavioral test suites with dummy runtimes ([#346](https://github.com/fullsend-ai/fullsend/issues/346), [#1982](https://github.com/fullsend-ai/fullsend/pull/1982))
- Agent output evaluation frameworks ([#73](https://github.com/fullsend-ai/fullsend/issues/73), [#499](https://github.com/fullsend-ai/fullsend/issues/499), [#1682](https://github.com/fullsend-ai/fullsend/pull/1682))
- Layered and standalone distribution modes for testability ([#1954](https://github.com/fullsend-ai/fullsend/pull/1954))
- Expanded e2e coverage with authorization gate testing ([#1983](https://github.com/fullsend-ai/fullsend/pull/1983))
- Static analysis layer for testing agents ([#1826](https://github.com/fullsend-ai/fullsend/pull/1826))

### Agent capabilities

Improving what agents can do and the runtime they operate in. This covers cross-repo workflows, better context provisioning, onboarding reliability, runtime enhancements, and ongoing improvements to individual agents. Partnering with the OpenShell team to advance their agentic SDLC is part of this work.

Examples of work that could move this forward:

- Multi-repo context loading and cross-repo changes ([#298](https://github.com/fullsend-ai/fullsend/issues/298), [#401](https://github.com/fullsend-ai/fullsend/issues/401), [#1276](https://github.com/fullsend-ai/fullsend/issues/1276))
- Better context for agents before coding ([#932](https://github.com/fullsend-ai/fullsend/issues/932), [#1255](https://github.com/fullsend-ai/fullsend/issues/1255), [#1200](https://github.com/fullsend-ai/fullsend/issues/1200))
- Onboarding improvements and branch protection handling ([#1758](https://github.com/fullsend-ai/fullsend/issues/1758), [#1689](https://github.com/fullsend-ai/fullsend/issues/1689))
- Public mint finalization ([#2073](https://github.com/fullsend-ai/fullsend/issues/2073), [#2071](https://github.com/fullsend-ai/fullsend/issues/2071), [#1145](https://github.com/fullsend-ai/fullsend/issues/1145))
- OpenShell tracking and integration ([#773](https://github.com/fullsend-ai/fullsend/issues/773), [#1721](https://github.com/fullsend-ai/fullsend/issues/1721), [#1813](https://github.com/fullsend-ai/fullsend/issues/1813))
- OpenCode as an alternative agent runtime ([#1260](https://github.com/fullsend-ai/fullsend/issues/1260), [#1935](https://github.com/fullsend-ai/fullsend/issues/1935), [#608](https://github.com/fullsend-ai/fullsend/issues/608))
- Scribe agent enhancements ([#895](https://github.com/fullsend-ai/fullsend/issues/895), [#222](https://github.com/fullsend-ai/fullsend/issues/222), [#1674](https://github.com/fullsend-ai/fullsend/issues/1674))

### Versioning and pinning

Define and implement a strategy for versioning workflows, agents, and dependencies so that releases are deterministic and upgradable. An ADR is in progress to evaluate options before implementation begins. Referring to resources by digest enables more deterministic pinning; agents may eventually move to separate repositories for independent versioning.

Examples of work that could move this forward:

- Pin workflows to the version being installed ([#1933](https://github.com/fullsend-ai/fullsend/issues/1933))
- Schema versioning for harness definitions ([#235](https://github.com/fullsend-ai/fullsend/issues/235), [#179](https://github.com/fullsend-ai/fullsend/issues/179))
- Renovate automation for dependency pins ([#773](https://github.com/fullsend-ai/fullsend/issues/773), [#544](https://github.com/fullsend-ai/fullsend/issues/544))
- Plugin repository approach for independent agent versioning ([#631](https://github.com/fullsend-ai/fullsend/issues/631))
- Build from source fallback when no published release exists ([#2026](https://github.com/fullsend-ai/fullsend/issues/2026))

### Forge portability

GitHub is the starting point, not the boundary. GitLab support requires solving webhook-to-pipeline translation, MR-event security models, and forge interface abstraction. This work continues incrementally alongside higher-priority items.

Related: [gitlab-implementation](problems/gitlab-implementation.md)

Examples of work that could move this forward:

- GitLab webhook bridge ([#1964](https://github.com/fullsend-ai/fullsend/issues/1964), [#1816](https://github.com/fullsend-ai/fullsend/pull/1816))
- Forge-portable harness schema ([#1605](https://github.com/fullsend-ai/fullsend/issues/1605), [#1848](https://github.com/fullsend-ai/fullsend/pull/1848))

### Feature refinement

Agents participate in feature definition — not just bugfixes. When ideas are filed, agents can autonomously produce feature definitions, ask clarifying questions, and prepare material for refinement ceremonies. Teams still own the definition; agents accelerate it. Community members are already exploring JIRA integration independently — we should engage them and build on their work rather than starting from scratch.

Examples of work that could move this forward:

- Intent representation and downstream-upstream linking ([#1336](https://github.com/fullsend-ai/fullsend/issues/1336), [#802](https://github.com/fullsend-ai/fullsend/issues/802))
- Connecting feature specs to implementable units ([#1337](https://github.com/fullsend-ai/fullsend/issues/1337), [#1342](https://github.com/fullsend-ai/fullsend/issues/1342))

Related: [downstream-upstream](problems/downstream-upstream.md), [intent-representation](problems/intent-representation.md)

## Next

What follows once the current work stabilizes.

### Trustworthiness evidence

We accumulate evidence about the quality of agent-produced code and reviews. This informs future decisions about expanding agent autonomy. The question is not whether to trust agents more, but where and when the evidence supports it.

This area is thin on dedicated tracking issues — most related work is scattered across review agent improvements. Filing focused issues for measurement and evidence collection would help.

Examples of work that could move this forward:

- Rework rate tracking for agent-produced PRs
- Review outcome analysis (accepted vs. discarded) ([#295](https://github.com/fullsend-ai/fullsend/issues/295))
- Qualitative feedback collection from pilot teams

### Standalone local runtime

A standalone runtime that allows agents to run locally, reducing dependence on GitHub Actions and providing a flexible execution alternative for teams hitting usage limits or needing offline capabilities.

Examples of work that could move this forward:

- Standalone dev mint server without GCP dependency ([#1963](https://github.com/fullsend-ai/fullsend/issues/1963))
- Hosted mint defaults to reduce infrastructure requirements ([#1145](https://github.com/fullsend-ai/fullsend/issues/1145), [#2073](https://github.com/fullsend-ai/fullsend/issues/2073))
- Local harness invocation support ([#173](https://github.com/fullsend-ai/fullsend/issues/173))
- Self-hosted and network boundary support ([#595](https://github.com/fullsend-ai/fullsend/issues/595), [#918](https://github.com/fullsend-ai/fullsend/issues/918))

### JIRA-driven workflows

With feature refinement establishing the pattern, extend agent capabilities deeper into project management — picking up stories, refining acceptance criteria, and linking implementation back to tracking. This extends fullsend's trigger model beyond forge events into project management systems.

Examples of work that could move this forward:

- JIRA trigger model ([#2263](https://github.com/fullsend-ai/fullsend/issues/2263))
- Per-agent JIRA support: triage ([#2264](https://github.com/fullsend-ai/fullsend/issues/2264)), code ([#2265](https://github.com/fullsend-ai/fullsend/issues/2265)), prioritize ([#2266](https://github.com/fullsend-ai/fullsend/issues/2266)), retro ([#2267](https://github.com/fullsend-ai/fullsend/issues/2267)), review ([#2268](https://github.com/fullsend-ai/fullsend/issues/2268)), refine ([#1341](https://github.com/fullsend-ai/fullsend/issues/1341))
- JIRA identity and credential management ([#2269](https://github.com/fullsend-ai/fullsend/issues/2269))

### Auto-merge readiness

With trustworthiness evidence accumulating, we begin reasoning about where auto-merge is safe — identifying specific codepaths or repositories where the evidence supports it and defining what the threshold looks like.

Related: [autonomy-spectrum](problems/autonomy-spectrum.md), [code-review](problems/code-review.md)

Examples of work that could move this forward:

- Defining auto-merge criteria per repo or codepath ([#1574](https://github.com/fullsend-ai/fullsend/issues/1574), [#1772](https://github.com/fullsend-ai/fullsend/issues/1772))
- Monitoring rework rates against thresholds
- CODEOWNERS-based scope boundaries for auto-merge

## Later

Problems we are actively thinking about but not yet building. These are informed by the [problem documents](problems/) and will move into **Next** as the platform matures.

### Kubernetes and OpenShift execution

When the sandbox runtime matures to run practically in Kubernetes and OpenShift, fullsend should support that as an execution environment. This also opens the door to triggering agent workflows from sources beyond GitHub and GitLab — decoupling the agent runtime from the forge.

### Production feedback loops

Closing the loop between production signals and what agents work on next. Platform organizations generate structured execution data that can drive triage and prioritization without waiting for humans to notice failures.

- Related: [production-feedback](problems/production-feedback.md)

### Cross-forge orchestration

Coordinating agent work across multiple forges (GitHub + GitLab, or multiple GitHub orgs) when a single logical change spans organizational boundaries.

### Security hardening

Ongoing work informed by the [security threat model](problems/security-threat-model.md):

- Prompt injection detection and andon cord ([#172](https://github.com/fullsend-ai/fullsend/issues/172), [#174](https://github.com/fullsend-ai/fullsend/issues/174))
- Org guardrail protection ([#84](https://github.com/fullsend-ai/fullsend/issues/84))
- Workflow security scanning ([#159](https://github.com/fullsend-ai/fullsend/issues/159))
- Agent authority modeling ([#877](https://github.com/fullsend-ai/fullsend/issues/877))

### Human factors and governance

As autonomous contribution scales, the organizational questions become unavoidable: domain ownership shifts, review fatigue, contributor motivation, and who has authority to make binding decisions about agent behavior.

- Related: [human-factors](problems/human-factors.md), [governance](problems/governance.md), [contribution-volume](problems/contribution-volume.md)

### Agent attestations

Cryptographic attestation of agent-produced artifacts, enabling consumers to verify what agent produced a change, under what policy, and with what inputs.

- See [#267](https://github.com/fullsend-ai/fullsend/issues/267)
