# Roadmap

Where fullsend is, and where it is going. Organized as **Now / Next / Later** — what we are actively building, what follows immediately after, and what we see on the horizon.

## Foundation (done)

Fullsend reached MVP in April 2026. The platform can be installed at the org level, enroll repositories, and run a full autonomous SDLC loop: triage issues, produce code and tests, review PRs, apply fixes from review feedback, and file retrospective improvement proposals. The core agent suite — triage, code, review, fix, retro, and scribe — ships as **OOTB (out-of-the-box) agents** and is designed to be general, extensible, and replaceable.

What this phase established:

- **Binary autonomy model** — per-repo opt-in, CODEOWNERS enforcing human approval on protected paths
- **The repo is the coordinator** — branch protection, CODEOWNERS, and status checks replace a coordinator agent
- **Trust derives from repository permissions, not agent identity**
- **Fullsend is using fullsend** — the platform dogfoods its own agent workflows
- **10+ Konflux repositories** running fullsend for bug triage, code production, and review
- **Active engagement** with additional upstream organizations exploring adoption

## Now

What we are actively building and shipping.

### Secretless deployment (WIF)

Replace long-lived credentials with Workload Identity Federation. This is a prerequisite for per-repo deployment and a security improvement for existing org-level installs.

- See [#912](https://github.com/fullsend-ai/fullsend/issues/912), [#913](https://github.com/fullsend-ai/fullsend/issues/913), [#914](https://github.com/fullsend-ai/fullsend/issues/914), [#915](https://github.com/fullsend-ai/fullsend/issues/915)

### Per-repo deployment

Org-level installation is appropriate for some organizations but inappropriate for others. Per-repo deployment lets individual repositories adopt fullsend without requiring org-wide configuration — lowering the barrier for new organizations and enabling adoption in orgs where org-level access is impractical.

### MVP feedback iteration

Incorporating feedback from early adopters. The issue backlog reflects this ongoing work across all agents:

- Review agent stability and accuracy ([#947](https://github.com/fullsend-ai/fullsend/issues/947), [#898](https://github.com/fullsend-ai/fullsend/issues/898), [#925](https://github.com/fullsend-ai/fullsend/issues/925), [#887](https://github.com/fullsend-ai/fullsend/issues/887))
- Review-fix feedback loop improvements ([#902](https://github.com/fullsend-ai/fullsend/issues/902), [#870](https://github.com/fullsend-ai/fullsend/issues/870), [#924](https://github.com/fullsend-ai/fullsend/issues/924))
- Code agent reliability ([#934](https://github.com/fullsend-ai/fullsend/issues/934), [#935](https://github.com/fullsend-ai/fullsend/issues/935), [#871](https://github.com/fullsend-ai/fullsend/issues/871))
- Operational improvements ([#896](https://github.com/fullsend-ai/fullsend/issues/896), [#909](https://github.com/fullsend-ai/fullsend/issues/909), [#893](https://github.com/fullsend-ai/fullsend/issues/893))

### OpenShell improvements

Pulling in new OpenShell features as they become available, including package-based installation ([#878](https://github.com/fullsend-ai/fullsend/issues/878)) and host-side API server capabilities ([#879](https://github.com/fullsend-ai/fullsend/issues/879), [#880](https://github.com/fullsend-ai/fullsend/issues/880), [#881](https://github.com/fullsend-ai/fullsend/issues/881)).

## Next

What follows once the current work stabilizes.

### Bring Your Own Agent (BYOA)

The OOTB agents are designed to be good defaults, but many teams will want super-custom, super-bespoke agentic workflows that we could never anticipate. BYOA enables teams to use fullsend as a framework — plugging in their own agents, skills, and orchestration while inheriting the platform's security model, sandbox isolation, and coordination layer.

This is a foundational capability. It transforms fullsend from a fixed agent suite into an extensible platform.

- Harness definition architecture ([#173](https://github.com/fullsend-ai/fullsend/issues/173), [#101](https://github.com/fullsend-ai/fullsend/issues/101))
- Skills loading policy and org/repo inheritance ([#237](https://github.com/fullsend-ai/fullsend/issues/237), [#236](https://github.com/fullsend-ai/fullsend/issues/236))
- Per-repo workflow definitions ([#69](https://github.com/fullsend-ai/fullsend/issues/69))
- Config schema and versioning ([#179](https://github.com/fullsend-ai/fullsend/issues/179), [#235](https://github.com/fullsend-ai/fullsend/issues/235))

### Feature refinement

Extending the SDLC footprint beyond bug triage and code production into feature work: refining feature requests, breaking them into implementable units, prioritizing them, and linking that process to upstream agentic development.

- Related: [downstream-upstream](problems/downstream-upstream.md), [intent-representation](problems/intent-representation.md)

### Auto-merge trustworthiness

Monitoring rework rates and review outcomes to build confidence in auto-merge for specific codepaths and repositories. The question is not whether to auto-merge but where and when the evidence supports it.

- Related: [autonomy-spectrum](problems/autonomy-spectrum.md), [code-review](problems/code-review.md)

## Later

Problems we are actively thinking about but not yet building. These are informed by the [problem documents](problems/) and will move into **Next** as the platform matures.

### GitLab support

GitHub is the starting point, not the boundary. GitLab support requires solving webhook-to-pipeline translation, MR-event security models, and forge interface abstraction. The architectural groundwork is laid in [ADR-0028](ADRs/0028-gitlab-support.md).

- Related: [gitlab-implementation](problems/gitlab-implementation.md)

### Kubernetes and OpenShift execution

When OpenShell matures to run practically in Kubernetes and OpenShift, fullsend should support that as an execution environment. This also opens the door to triggering agent workflows from sources beyond GitHub and GitLab — decoupling the agent runtime from the forge.

### JIRA-driven agent workflows

Agents that work directly off JIRA issues — picking up stories, refining acceptance criteria, and linking implementation back to tracking. This extends fullsend's trigger model beyond forge events into project management systems.

### Cross-run memory

Agents are stateless by design, but they rediscover the same lessons on every run. The hard problem is preserving useful operational knowledge without creating a second, less-reviewed instruction channel.

- Related: [cross-run-memory](problems/cross-run-memory.md)

### Production feedback loops

Closing the loop between production signals and what agents work on next. Platform organizations generate structured execution data that can drive triage and prioritization without waiting for humans to notice failures.

- Related: [production-feedback](problems/production-feedback.md)

### Operational observability

How do the humans operating an autonomous software factory understand what it is doing, debug it when it goes wrong, and improve it over time?

- Related: [operational-observability](problems/operational-observability.md)

### Security hardening

Ongoing work informed by the [security threat model](problems/security-threat-model.md):

- Prompt injection detection and andon cord ([#172](https://github.com/fullsend-ai/fullsend/issues/172), [#174](https://github.com/fullsend-ai/fullsend/issues/174))
- Org guardrail protection ([#84](https://github.com/fullsend-ai/fullsend/issues/84))
- Workflow security scanning ([#159](https://github.com/fullsend-ai/fullsend/issues/159))
- Agent authority modeling ([#877](https://github.com/fullsend-ai/fullsend/issues/877))

### Human factors and governance

As autonomous contribution scales, the organizational questions become unavoidable: domain ownership shifts, review fatigue, contributor motivation, and who has authority to make binding decisions about agent behavior.

- Related: [human-factors](problems/human-factors.md), [governance](problems/governance.md), [contribution-volume](problems/contribution-volume.md)
