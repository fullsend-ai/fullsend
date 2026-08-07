# Platform Nativeness

When the platform you're automating is also the platform you're building on, what problems disappear — and what new constraints appear?

## Why this matters

Fullsend is an external system that integrates with GitHub. [GitHub Agentic Workflows (gh-aw)](https://github.github.com/gh-aw/) is a native GitHub feature that runs coding agents in GitHub Actions with strong guardrails. This architectural difference has a concrete consequence: a significant portion of fullsend's implemented complexity solves problems that are artifacts of its external position, not inherent to the goal of autonomous agentic development.

This document is an honest self-assessment. It sorts fullsend's problems into three categories: problems created by building externally, problems that exist regardless of platform position, and problems both approaches face where nativeness provides an advantage. The goal is to sharpen our understanding of where fullsend's engineering effort delivers unique value versus where it compensates for a self-imposed handicap.

## Problems that arise from external integration

These problems exist because fullsend is not part of GitHub. A native system does not have them.

### Cross-repo dispatch

Fullsend uses a centralized `.fullsend` config repo as the hub for agent pipelines. When something happens in an enrolled repo (a PR is opened, an issue is filed), a shim workflow in that repo must dispatch an event to `.fullsend` so the agent pipeline can run. This requires:

- `workflow_dispatch` as the cross-repo trigger mechanism, because `workflow_call` would require the caller to have the App PEM ([ADR 0008](../ADRs/0008-workflow-dispatch-for-cross-repo-dispatch.md))
- A fine-grained PAT (`FULLSEND_DISPATCH_TOKEN`) stored as an org secret with selected-repo visibility, so enrolled repos can trigger `.fullsend` workflows
- `pull_request_target` in the shim workflow, so PR authors cannot rewrite the dispatch code to steal the token ([ADR 0009](../ADRs/0009-pull-request-target-in-shim-workflows.md))
- Verification logic — the CLI dispatches `agent.yaml` with `event_type=verify` to confirm the token works

gh-aw workflows are defined **in the repo itself** as markdown files. Events trigger Actions workflows directly. There is no cross-repo dispatch to set up, no dispatch token to manage, no shim to secure.

**Compensating value:** The centralized model means org-wide configuration lives in one place. But GitHub already has org-level Actions policies and required workflows that could serve the same purpose natively.

### Per-role GitHub App creation

Fullsend creates one GitHub App per agent role (triage, coder, review, fullsend) through the [manifest flow](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest). This involves a local HTTP server, a browser redirect to GitHub, a callback to capture the PEM (available only at creation time), polling or prompting to install the app on the org, and logic to detect reuse vs. lost-key scenarios ([ADR 0007](../ADRs/0007-per-role-github-apps.md)). The credential lifecycle is particularly fragile: the PEM private key is returned only once during the manifest conversion, there is no API to rotate it, and if the key is lost the app must be deleted and recreated from scratch.

gh-aw uses GitHub's own `GITHUB_TOKEN`, scoped per-job. The agent gets a read-only token automatically; the write job gets a scoped write token. No App creation, no PEM lifecycle, no manifest flow, no rotation concern.

**Compensating value:** Per-role Apps give fine-grained identity separation — triage can't push code, reviewer can't merge. But GitHub's native per-job token scoping achieves a similar (if less granular) boundary with zero setup cost.

**A real gap for implicit dispatch — but not a fatal one.** GitHub's own [`GITHUB_TOKEN` documentation](https://docs.github.com/en/actions/concepts/security/github_token) states the general rule: events triggered by `GITHUB_TOKEN` do not create a new workflow run, with `workflow_dispatch` and `repository_dispatch` as the only exceptions. `pull_request` events (`opened`/`synchronize`/`reopened`) get a narrow carve-out — they still fire, but land in an **approval-required** state a human must manually release. Every other `pull_request` activity type, plus issues and label events, get no carve-out at all: an issue opened or a label applied via `GITHUB_TOKEN` silently does not trigger the next workflow.

That breaks a pipeline that hands off purely through *implicit* GitHub-native events — retro filing a follow-up issue, triage applying `ready-to-implement`, code opening a PR for review to pick up automatically. But `workflow_dispatch` and `repository_dispatch` are exempt from this suppression regardless of which token performs them, so a stage can sidestep the whole problem by explicitly calling `gh workflow run <next-stage>` as its own last step, using nothing more than `actions: write`. A pending fullsend proposal ([PR #5649](https://github.com/fullsend-ai/fullsend/pull/5649), not yet merged) documents exactly this pattern — an identity-light "Lite Auth Mode" built around explicit `workflow_dispatch` handoffs — and separately clarifies that a shared App's pushes and labels were never actually broken by this suppression rule; only the built-in `GITHUB_TOKEN` is affected, and [ADR 0033](../ADRs/0033-per-repo-installation-mode.md)'s original text overstated that constraint.

That weakens retriggering as its own justification for per-role Apps: it's solvable with an explicit dispatch call, not a different bot identity. What real identity separation still buys is (a) least-privilege blast-radius separation (ADR 0007's original argument) and (b) working around GitHub's `422 Can not approve your own pull request` error, which blocks one identity from both authoring and approving a PR regardless of token type — a narrower but real reason a review-stage identity must differ from the code-authoring identity once auto-merge is in play. gh-aw's [FAQ](https://github.github.com/gh-aw/reference/faq/) documents an operator-supplied `GH_AW_CI_TRIGGER_TOKEN` PAT as its own workaround for the same suppression, but doesn't appear to document the zero-privilege explicit-`workflow_dispatch` escape hatch as an alternative — that may be a simpler option available to gh-aw workflows too, not something inherent to fullsend's approach.

### Repository enrollment

Fullsend must inject a shim workflow into each target repo. The CLI creates a branch (`fullsend/onboard`), writes `.github/workflows/fullsend.yaml` with the `pull_request_target` trigger, and opens a PR. A human must merge this PR to complete enrollment ([ADR 0013, proposed](../ADRs/0013-admin-install-repo-enrollment-v1.md)).

gh-aw enrollment is adding a markdown file to your repo. `gh aw compile` generates the corresponding `.lock.yml`; `gh aw run` is a separate command that executes a workflow immediately in Actions.

**Compensating value:** The enrollment PR provides an explicit opt-in gate for repo owners. But a native system achieves the same opt-in by requiring someone to add the workflow file.

### The install/uninstall layer stack

The bulk of the Go CLI's complexity is an ordered, idempotent layer stack: config repo creation, workflow writing, secret provisioning, dispatch token setup, and enrollment ([ADR 0006](../ADRs/0006-ordered-layer-model.md)). Each layer has install, uninstall, analyze, and preflight operations. Uninstall runs in reverse order and collects errors. App deletion cannot be automated via API and requires manual browser interaction.

gh-aw's CLI installs with `gh extension install github/gh-aw`, but per-repo setup is not trivial: each repo needs a markdown workflow file (~50 lines of frontmatter + ~200 lines of agent instructions), compilation via `gh aw compile` to produce a hardened `.lock.yml` (currently ~1,800 lines, a 6-job Actions pipeline: pre_activation → activation → agent → detection → safe_outputs → conclusion), and both files committed to the repo. This must be repeated for every repo and every workflow. There is still no enforced org-level mechanism for cross-repo consistency — gh-aw documents a convention of a central template repo consumed via `gh aw add owner/repo/path@ref` and manual imports, but nothing propagates or enforces it automatically.

**Compensating value:** The layer model is well-engineered and idempotent. The per-repo setup cost of gh-aw is real but linear and local — each repo is self-contained. Fullsend's install stack manages cross-repo coordination infrastructure (dispatch tokens, centralized config, enrollment shims) that a native system doesn't need, but its centralized model does provide org-wide consistency that gh-aw lacks.

### Credential isolation architecture

Fullsend designs host-side REST proxies with L7 per-method/per-path policy to keep credentials out of the agent sandbox ([ADR 0017](../ADRs/0017-credential-isolation-for-sandboxed-agents.md)). The default approach is prefetch/post-process (no credentials in sandbox at all); the fallback is a capability-reducing REST proxy.

gh-aw's [security architecture](https://github.github.com/gh-aw/introduction/architecture/) solves this through a formal three-layer trust model. At the substrate level, the Agent Workflow Firewall (AWF) containerizes agents, uses iptables to redirect traffic through a Squid proxy enforcing domain allowlists, and drops capabilities before launching the agent. An API proxy routes model traffic while keeping credentials isolated. An MCP Gateway spawns each MCP server in its own isolated container with per-server domain allowlists and tool allowlisting. At the plan level, the SafeOutputs subsystem buffers all external writes as artifacts, with a separate threat detection job gating the write jobs. The agent's token literally cannot write — credential isolation is enforced by the platform, not by a proxy.

**Compensating value:** The L7 proxy design is more flexible (works with any runtime, not just Actions). But that flexibility is only needed if agents run outside Actions.

## Problems that arise from fullsend's ambition

These problems exist regardless of whether the system is native or external. They are inherent to the goal of fully autonomous agentic development and are not addressed by gh-aw.

### Autonomous merge judgment

gh-aw shipped an experimental [`merge-pull-request`](https://github.github.com/gh-aw/reference/safe-outputs-pull-requests/) safe-output (introduced via [PR #27193](https://github.com/github/gh-aw/pull/27193), merged 2026-04-20, and refined further in the June 2026 v0.80.4/v0.81.0 releases) that merges a PR once configured policy gates pass — required status checks, review decision, unresolved review-thread state, label/title/branch constraints, and GitHub mergeability — with merges to the repository default branch always refused, and `gh aw compile` emitting an experimental-feature warning on use. Docs state explicit graduation criteria (end-to-end cross-repo test coverage, a full release cycle without schema/behavior changes) before it leaves experimental status. A [GitHub blog post](https://github.blog/ai-and-ml/generative-ai/code-review-in-the-age-of-ai-why-developers-will-always-own-the-merge-button/) argues developers will always own the merge button; gh-aw's own product has since partially moved past that editorial position by shipping the mechanism, gated on a prior human review decision rather than autonomous judgment. Fullsend's thesis — that for routine changes with sufficient verification, the merge decision itself (not just the mechanical merge click after a human has already approved) can be automated — remains differentiated, but the gap with gh-aw is narrower than it was.

### Intent verification

Checking whether a change is authorized against a structured intent system — not just "is this change correct?" but "is this change one we actually want?" This is absent from every tool in the [landscape](../landscape.md), including gh-aw. A native system could implement it, but gh-aw's architecture does not. See [intent-representation.md](intent-representation.md).

### Intent-authorization-tier-based autonomy

Different agent authority for different types of changes: auto-merge a dependency bump, require human review for an API change, block agent-authored modifications to CODEOWNERS. gh-aw's [integrity filtering](https://github.github.com/gh-aw/reference/integrity/) implements a form of input trust tiering (`merged > approved > unapproved > none`, plus an absolute-deny `blocked` tier for listed users) and its [supply chain protection](https://github.github.com/gh-aw/reference/threat-detection/#supply-chain-protection-protected-files) flags modifications to sensitive files (dependency manifests, CI config, agent instruction files, CODEOWNERS) — the default `request_review` policy still creates the PR but attaches a blocking review requiring human approval before merge, rather than hard-blocking the write outright (a `blocked` policy exists but isn't the default) — but these are applied to *what the agent can see and touch*, not to *whether the agent's output should be merged*. The output model is no longer strictly flat now that an experimental `merge-pull-request` safe-output exists (see [Autonomous merge judgment](#autonomous-merge-judgment) above), but that gate is a single policy-checklist, not a tiered authority model that varies by change type. Fullsend's autonomy spectrum applies to the merge decision itself, with per-change-type tiering. See [autonomy-spectrum.md](autonomy-spectrum.md).

### Governance

Who controls agent policies org-wide? How do those policies evolve? Who can change what agents are allowed to do? gh-aw now ships a [governance layer](https://github.github.com/gh-aw/guides/governance/) — `gh aw env` manages default settings (model, credit limits, turns, timeouts) and boolean capability gates (e.g., disabling PR creation org-wide) as GitHub Actions variables, with an explicit precedence hierarchy (workflow frontmatter > repository > organization > enterprise). What's still missing is a gh-aw-specific audit trail of *who* changed *which* policy *when* — that relies entirely on GitHub's generic Actions-variable/audit-log tooling rather than anything gh-aw purpose-builds. See [governance.md](governance.md).

### Zero-trust inter-agent review

Agents treating each other's output as untrusted, with blocking power derived from forge permissions rather than narrative trust. gh-aw added [inline sub-agents](https://github.github.com/gh-aw/reference/inline-sub-agents/) — named agent blocks defined within the same workflow markdown file and invoked by the parent agent — so it's no longer strictly single-agent-per-workflow. But that's task delegation within one trusted workflow run and one author's control, not zero-trust review between independently-authored agents with forge-permission-derived blocking power; gh-aw still has no concept of the latter. See [agent-architecture.md](agent-architecture.md) and [code-review.md](code-review.md).

## Problems both face, where nativeness helps

These are shared concerns where gh-aw's native position provides a structural advantage.

### Run-trigger authorization (the "pwn request" gap)

GitHub Actions has no built-in approval gate for `issue_comment` or `issues.opened` triggers — unlike `pull_request` from public forks, which can require a maintainer's approval before running. Any user who can comment on an issue or open one fires the workflow immediately, full stop. [GitHub Security Lab documented a related class of vulnerability in 2021](https://securitylab.github.com/resources/github-actions-preventing-pwn-requests/) that they named "pwn request" — focused specifically on privilege escalation via `pull_request_target` code execution. The underlying issue is broader: an absent trigger-level authorization gate, which extends to `issue_comment` and `issues.opened` as well. There the risk isn't code execution, it's unauthorized cost and abuse exposure — for a workflow that runs paid LLM inference, an attacker doesn't need a security bug, just a repo they can comment on.

Fullsend closes this itself: nearly every dispatch path — slash commands and automatic event triggers alike — is gated on collaborator repository permission before dispatching an agent, via a parameterized `has_repo_permission` check against the collaborator-permission API (observation stages like triage/review require `triage`+, mutation stages like code/fix require `write`+) or, for label-triggered handoffs, the implicit requirement that applying a label itself needs write access ([ADR 0054](../ADRs/0054-require-authorization-on-all-agent-dispatch-paths.md)). The `issue_comment` needs-info re-triage path intentionally uses a weaker gate (any non-`NONE` author association, or the issue's own author), and `retro` on PR-close is intentionally left ungated for read-only lifecycle accounting. This was not the default behavior; it had to be built and applied consistently after discovering some dispatch paths were gated and others weren't.

gh-aw's docs now define [`on.roles`/`on.skip-roles`](https://github.github.com/gh-aw/reference/triggers/) — an actor-authorization allowlist against the triggering user's repository role, defaulting to `[admin, maintainer, write]` — automatically enforced by default for `push`, `issues`, and `pull_request` triggers, closing the gap for `issues.opened`. However, `issue_comment` (the event slash commands and comment-driven automation use — the primary "pwn request" vector this section describes) is not in that default-enforced list; a workflow author must explicitly add `roles:` to gate comment-triggered workflows, or they inherit the same ungated-trigger behavior as any other GitHub Actions workflow.

### Cost/token observability

Both systems need visibility into inference spend. Fullsend already emits cost and token metrics via OpenTelemetry to whatever backend the operator configures (MLflow, or any OTLP-compatible provider). gh-aw ships [`max-ai-credits`](https://github.github.com/gh-aw/reference/frontmatter/#ai-credits-guardrail-max-ai-credits) as a per-run hard budget plus [`gh aw logs` / `gh aw audit`](https://github.github.com/gh-aw/reference/cost-management/) for inline cost and token inspection from the CLI. The underlying capability is equivalent; gh-aw's edge is convenience — the numbers are visible directly against the workflow run without a separate dashboard. That is a UX nicety fullsend could add via a post-script or a separate, non-blocking workflow, not a capability gap to close.

### Injection defense

Both systems must defend against prompt injection via untrusted input (issue text, PR descriptions, code comments). gh-aw's defense is layered and substantially deeper than a single output scan:

- **Content sanitization** at the input boundary: @mention neutralization, bot trigger protection, XML/HTML tag conversion, URI filtering (only HTTPS from trusted domains), unicode normalization, content size limits (0.5MB/65k lines), and control character removal. This runs before the agent sees any untrusted content.
- **[Integrity filtering (DIFC)](https://github.github.com/gh-aw/reference/integrity/)**: a trust-based system that filters GitHub content by author association level (`merged > approved > unapproved > none > blocked`). On public repos, `min-integrity: approved` is applied by default — content from untrusted external contributors is removed before the agent sees it. Supports `trusted-users`, `blocked-users`, and `approval-labels` overrides, with centralized management via GitHub organization variables. Filtered events are logged as `DIFC_FILTERED` for audit.
- **Substrate containment**: read-only token, AWF network firewall with domain allowlisting, MCP server isolation per container.
- **Threat detection**: AI-powered output scan plus optional custom scanners (Semgrep, TruffleHog, LlamaGuard) before any write is externalized.
- **[Supply chain protection](https://github.github.com/gh-aw/reference/threat-detection/#supply-chain-protection-protected-files)**: static rule-based flagging of agent modifications to dependency manifests, CI/CD config, agent instruction files, and CODEOWNERS, with `request_review` (default — creates the PR but attaches a blocking review requiring human approval), `blocked` (hard-fails the write), `allowed`, and `fallback-to-issue` policies.

Fullsend's experiments show that pre-LLM scanners alone are insufficient — [Model Armor caught ~8–25% of payloads](https://github.com/fullsend-ai/experiments/tree/main/guardrails-eval); [LLM Guard in sentence mode caught ~83%](https://github.com/fullsend-ai/experiments/tree/main/guardrails-eval). gh-aw's approach of combining input sanitization, trust-based content filtering, containment, and output scanning is a more comprehensive defense-in-depth than any single layer. And gh-aw's containment means the *consequence* of a missed injection is bounded (the agent can't do anything destructive), while fullsend must build equivalent containment from scratch.

Neither approach solves the *semantic* injection problem — an agent tricked into producing subtly wrong but plausible output that passes all scans. That requires the judgment-layer defenses (intent verification, specialized injection-defense sub-agents) that gh-aw does not attempt.

### Multi-agent coordination

Fullsend builds custom dispatch infrastructure for multi-agent pipelines (triage → code → review). gh-aw already provides native [orchestration primitives](https://github.github.com/gh-aw/patterns/orchestration/): `dispatch-workflow` (async fan-out to worker workflows via `workflow_dispatch`) and `call-workflow` (same-run fan-out via `workflow_call`, preserving actor attribution). An orchestrator workflow can decide what to do, split work into units, and dispatch typed worker workflows with scoped permissions and tools. [Cross-repository operations](https://github.github.com/gh-aw/reference/cross-repository/) allow workflows to read from and write to external repos via `target-repo`, `allowed-repos`, and cross-repo checkout — partially addressing the org-wide coordination that fullsend's centralized `.fullsend` repo provides. Both `dispatch-workflow` and `call-workflow` are same-repo only per gh-aw's own [safe-outputs reference](https://github.github.com/gh-aw/reference/safe-outputs/). gh-aw's docs don't state the rationale explicitly, but it's plausibly because the target needs `actions: write` and cross-repo dispatch would complicate billing/actor attribution. Genuine cross-repo *workflow* fan-out instead relies on a separate, experimental `dispatch-repository` safe-output (`repository_dispatch` to external repos); `target-repo`/`allowed-repos` on individual safe-outputs cover writing resources in other repos, not triggering runs there. Either way, the caller must supply their own PAT or GitHub App token — gh-aw does not provide one for you.

Fullsend's SDLC pipeline is itself orchestrated natively via GitHub — labels and issue/PR events as the state machine, the repo as coordinator, no privileged orchestrator process ([ADR 0002](../ADRs/0002-initial-fullsend-design.md)). gh-aw's `dispatch-workflow`/`call-workflow` primitives are a more explicit expression of the same underlying event-driven pattern, not a different architecture. This comparison is orthogonal to the [per-role App question above](#per-role-github-app-creation): orchestration primitives determine how work fans out to other workflow runs, not which identity authors the writes those runs produce, so they do not change the `GITHUB_TOKEN` retriggering behavior described there.

### Org-wide configuration

Fullsend creates a `.fullsend` config repo with `config.yaml`, normative specs, and centralized secrets. GitHub already provides org-level Actions policies, required workflows, organization rulesets, and org secrets with selected-repo visibility. A native system could leverage these directly rather than creating a parallel configuration layer.

## The trade-offs of nativeness

Building natively is not strictly superior. Platform lock-in introduces constraints that an external system avoids.

### Forge lock-in

Fullsend's forge abstraction ([ADR 0005](../ADRs/0005-forge-abstraction-layer.md)) means the same design could work on GitLab, Forgejo, or other platforms. gh-aw is GitHub-only by definition. For organizations that use multiple forges or may migrate, this matters. For GitHub-only organizations, it's cost without benefit.

Multi-forge portability is not the only reason GitHub-only is a narrower target than it looks, though. GitHub is fullsend's highest-value starting point, not its ceiling, because it's the platform where the harder trust problem shows up first: internal/enterprise repos have natural contributor segregation (only employees or vetted collaborators can comment, open issues, or open PRs), which bounds the abuse surface without extra engineering. Wide-open public upstream repos have no such segregation — any anonymous account can trigger automation (see [Run-trigger authorization](#run-trigger-authorization-the-pwn-request-gap) above) — so the same threat model has to hold at internet scale, not org scale. That distinction holds regardless of which forge is in play, and is arguably a bigger driver of fullsend's design than portability alone.

### Runtime constraints

GitHub Actions runners have a 6-hour job timeout, limited compute options (unless self-hosted), and restrictions on what can run in containers. Agents that need long-running sessions, GPU access, specialized build tooling, or persistent state across invocations may outgrow Actions. An external system can run agents anywhere — on Kubernetes, dedicated VMs, or specialized agent platforms.

### Product dependency

gh-aw is in [public preview](https://github.blog/changelog/2026-06-11-github-agentic-workflows-is-now-in-public-preview/) (having progressed from [technical preview](https://github.blog/changelog/2026-02-13-github-agentic-workflows-are-now-in-technical-preview/) announced February 2026 to public preview announced June 2026) and may still change significantly before GA. Building on it means depending on GitHub's product decisions, deprecation timeline, and willingness to support autonomous use cases. gh-aw's maintainers have historically favored keeping humans on the merge button, though gh-aw has since shipped an experimental, policy-gated `merge-pull-request` safe-output (see [Autonomous merge judgment](#autonomous-merge-judgment)) — an editorial stance that's now visibly softening rather than a fixed platform-enforced restriction. An external system can still build merge authority independently of gh-aw's pace and gating choices.

### Isolation model

gh-aw's container isolation is strong for its use case, but the isolation boundary is defined by GitHub, not by the operator. Organizations with specific compliance, data residency, or security requirements may need control over the sandbox that a native system cannot provide.

## Open questions

- **Should fullsend adopt gh-aw as its containment and execution layer?** Rather than building parallel infrastructure for cross-repo dispatch, credential isolation, and agent sandboxing, fullsend could use gh-aw directly for the containment layer and focus its engineering effort on the judgment layer (intent verification, review composition, merge authority). gh-aw workflows are markdown files — agents can author them, aided by gh-aw's own [documentation MCP server](https://github.github.com/gh-aw/reference/gh-aw-as-mcp-server/). The `gh aw` CLI already handles compilation, security scanning, and lock file generation. Fullsend would not need to replicate any of this.

- **Is the forge abstraction worth the cost at this stage?** Fullsend's only concrete implementation is GitHub. If forge-neutrality is deferred to GitLab MVP, cross-forge orchestration, and Kubernetes/OpenShift work on the [roadmap](../roadmap.md), the current implementation could use GitHub-native primitives directly, simplifying the stack substantially.

- **Can gh-aw's safe-outputs model be extended for autonomous merge?** This is no longer hypothetical — gh-aw shipped an experimental `merge-pull-request` safe-output (introduced via [PR #27193](https://github.com/github/gh-aw/pull/27193), merged 2026-04-20, and refined further in the June 2026 v0.80.4/v0.81.0 releases), gated by required status checks, review decision, unresolved-thread state, labels, and branch allowlists, with default-branch merges always refused and `gh aw compile` flagging it as experimental. The open question is narrower now: whether/when it graduates to stable, how permissive its policy gates become, and whether it ever gates on anything beyond a prior human review decision (i.e., autonomous *judgment* about the change, not just mechanical merge-after-approval) — which is where fullsend's thesis remains differentiated.

- **Where is the boundary between containment and judgment?** gh-aw solves containment well. Fullsend's unique value is judgment. Should fullsend's implementation focus exclusively on the judgment layer and delegate containment to native platform features wherever possible?

## Possible next step: gh-aw containment POC

One way to test the native-vs-external trade-off empirically would be to implement one fullsend pipeline stage (e.g. triage or review) as a gh-aw workflow, checking whether gh-aw's containment model, orchestration primitives, and safe-outputs are sufficient as a substrate for fullsend's judgment layer.

gh-aw provides an [MCP server for CLI access](https://github.github.com/gh-aw/reference/gh-aw-as-mcp-server/) (compiling, running, and managing workflows), and we have configured a [gh-aw docs MCP server](https://github.github.com/gh-aw/llms.txt) in the fullsend development environment that agents can use to fetch gh-aw reference material (security architecture, safe-outputs spec, frontmatter reference, design patterns, etc.) while authoring workflow markdown. This means a POC could be largely agent-driven.

However, this approach carries real tensions that should be weighed:

- **ADR 0005 conflict:** The accepted [forge abstraction layer](../ADRs/0005-forge-abstraction-layer.md) decision assumes fullsend remains portable across GitHub, GitLab, and Forgejo. Adopting gh-aw as the containment layer would lock fullsend at the runtime and security architecture level — deeper than what the forge abstraction was designed to abstract over. A POC should be evaluated against whether it invalidates, defers, or supersedes ADR 0005.
- **Pre-GA platform risk:** gh-aw entered public preview in June 2026 (up from technical preview in February 2026) but has not reached GA. [Release velocity](https://github.com/github/gh-aw/releases) remains very high — v0.83.x as of July 2026, up from v0.68.x in April 2026, with 400+ tagged releases across roughly 11.5 months since the repo's August 2025 creation. It remains Linux-only: macOS and Windows Actions runners lack the container-job support the Agent Workflow Firewall sandbox requires. Feature surface is still changing significantly before GA, as shown by the April 2026 addition of the experimental `merge-pull-request` safe-output, itself further refined in June 2026. Building on it means depending on GitHub's product decisions, deprecation timeline, and feature stability.
- **Philosophical opposition:** gh-aw's maintainers have [publicly favored](https://github.blog/ai-and-ml/generative-ai/code-review-in-the-age-of-ai-why-developers-will-always-own-the-merge-button/) keeping humans on the merge button — fullsend's core thesis is that this decision should be automatable for routine changes. That stance no longer means gh-aw won't formalize merge as an official safe-output — it has, on an experimental basis, gated on a prior human review decision. Fullsend's differentiation narrows to whether the *decision* to merge (not just the mechanical action once a human has already approved) can itself be automated with sufficient verification — a case gh-aw's current gating model doesn't attempt to make.

If pursued, the POC would inform whether fullsend's implementation effort should shift from building containment infrastructure to building judgment-layer capabilities on top of gh-aw — but it should be treated as an experiment, not a commitment.
