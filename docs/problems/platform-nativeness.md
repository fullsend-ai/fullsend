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

### Repository enrollment

Fullsend must inject a shim workflow into each target repo. The CLI creates a branch (`fullsend/onboard`), writes `.github/workflows/fullsend.yaml` with the `pull_request_target` trigger, and opens a PR. A human must merge this PR to complete enrollment ([ADR 0013, proposed](../ADRs/0013-repository-enrollment-v1.md)).

gh-aw enrollment is adding a markdown file to your repo. `gh aw run` generates the corresponding `.lock.yml`.

**Compensating value:** The enrollment PR provides an explicit opt-in gate for repo owners. But a native system achieves the same opt-in by requiring someone to add the workflow file.

### The install/uninstall layer stack

The bulk of the Go CLI's complexity is an ordered, idempotent layer stack: config repo creation, workflow writing, secret provisioning, dispatch token setup, and enrollment ([ADR 0006](../ADRs/0006-ordered-layer-model.md)). Each layer has install, uninstall, analyze, and preflight operations. Uninstall runs in reverse order and collects errors. App deletion cannot be automated via API and requires manual browser interaction.

gh-aw: `gh extension install github/gh-aw`. Done.

**Compensating value:** The layer model is well-engineered and idempotent. But it exists to manage infrastructure that a native system doesn't need.

### Credential isolation architecture

Fullsend designs host-side REST proxies with L7 per-method/per-path policy to keep credentials out of the agent sandbox ([ADR 0017](../ADRs/0017-credential-isolation-for-sandboxed-agents.md)). The default approach is prefetch/post-process (no credentials in sandbox at all); the fallback is a capability-reducing REST proxy.

gh-aw's [security architecture](https://github.github.com/gh-aw/introduction/architecture/) solves this through a formal three-layer trust model. At the substrate level, the Agent Workflow Firewall (AWF) containerizes agents, uses iptables to redirect traffic through a Squid proxy enforcing domain allowlists, and drops capabilities before launching the agent. An API proxy routes model traffic while keeping credentials isolated. An MCP Gateway spawns each MCP server in its own isolated container with per-server domain allowlists and tool allowlisting. At the plan level, the SafeOutputs subsystem buffers all external writes as artifacts, with a separate threat detection job gating the write jobs. The agent's token literally cannot write — credential isolation is enforced by the platform, not by a proxy.

**Compensating value:** The L7 proxy design is more flexible (works with any runtime, not just Actions). But that flexibility is only needed if agents run outside Actions.

## Problems that arise from fullsend's ambition

These problems exist regardless of whether the system is native or external. They are inherent to the goal of fully autonomous agentic development and are not addressed by gh-aw.

### Autonomous merge judgment

gh-aw explicitly keeps humans in the loop. Its safe-outputs model produces artifacts that a gated write job applies, but merge is never one of the permitted operations. GitHub's own [position paper](https://github.blog/ai-and-ml/generative-ai/code-review-in-the-age-of-ai-why-developers-will-always-own-the-merge-button/) argues developers will always own the merge button. Fullsend's thesis is that for routine changes with sufficient verification, the merge decision can be automated. This is a judgment problem that no amount of platform nativeness resolves — it requires intent verification, confidence scoring, and a governance model for when to override.

### Intent verification

Checking whether a change is authorized against a structured intent system — not just "is this change correct?" but "is this change one we actually want?" This is absent from every tool in the [landscape](../landscape.md), including gh-aw. A native system could implement it, but gh-aw's architecture does not. See [intent-representation.md](intent-representation.md).

### Tier-based autonomy

Different agent authority for different types of changes: auto-merge a dependency bump, require human review for an API change, block agent-authored modifications to CODEOWNERS. gh-aw's [integrity filtering](https://github.github.com/gh-aw/reference/integrity/) implements a form of input trust tiering (`merged > approved > unapproved > none`) and its [supply chain protection](https://github.github.com/gh-aw/reference/threat-detection/#supply-chain-protection-protected-files) blocks modifications to sensitive files (dependency manifests, CI config, CODEOWNERS) by default — but these are applied to *what the agent can see and touch*, not to *whether the agent's output should be merged*. The output model remains flat: agent proposes, human decides, regardless of change type. Fullsend's autonomy spectrum applies to the merge decision itself. See [autonomy-spectrum.md](autonomy-spectrum.md).

### Governance

Who controls agent policies org-wide? How do those policies evolve? Who can change what agents are allowed to do? gh-aw leaves this to whoever writes the markdown workflow files — there is no governance framework, no policy hierarchy, no audit trail of policy changes. See [governance.md](governance.md).

### Zero-trust inter-agent review

Agents treating each other's output as untrusted, with blocking power derived from forge permissions rather than narrative trust. gh-aw's single-agent-per-workflow model has no concept of inter-agent interaction. See [agent-architecture.md](agent-architecture.md) and [code-review.md](code-review.md).

## Problems both face, where nativeness helps

These are shared concerns where gh-aw's native position provides a structural advantage.

### Injection defense

Both systems must defend against prompt injection via untrusted input (issue text, PR descriptions, code comments). gh-aw's defense is layered and substantially deeper than a single output scan:

- **Content sanitization** at the input boundary: @mention neutralization, bot trigger protection, XML/HTML tag conversion, URI filtering (only HTTPS from trusted domains), unicode normalization, content size limits (0.5MB/65k lines), and control character removal. This runs before the agent sees any untrusted content.
- **[Integrity filtering (DIFC)](https://github.github.com/gh-aw/reference/integrity/)**: a trust-based system that filters GitHub content by author association level (`merged > approved > unapproved > none > blocked`). On public repos, `min-integrity: approved` is applied by default — content from untrusted external contributors is removed before the agent sees it. Supports `trusted-users`, `blocked-users`, and `approval-labels` overrides, with centralized management via GitHub organization variables. Filtered events are logged as `DIFC_FILTERED` for audit.
- **Substrate containment**: read-only token, AWF network firewall with domain allowlisting, MCP server isolation per container.
- **Threat detection**: AI-powered output scan plus optional custom scanners (Semgrep, TruffleHog, LlamaGuard) before any write is externalized.
- **[Supply chain protection](https://github.github.com/gh-aw/reference/threat-detection/#supply-chain-protection-protected-files)**: static rule-based blocking of agent modifications to dependency manifests, CI/CD config, agent instruction files, and CODEOWNERS, with `blocked/allowed/fallback-to-issue` policies.

Fullsend's experiments show that pre-LLM scanners alone are insufficient — [Model Armor caught ~8–25% of payloads](../../experiments/guardrails-eval/); [LLM Guard in sentence mode caught ~83%](../../experiments/guardrails-eval/). gh-aw's approach of combining input sanitization, trust-based content filtering, containment, and output scanning is a more comprehensive defense-in-depth than any single layer. And gh-aw's containment means the *consequence* of a missed injection is bounded (the agent can't do anything destructive), while fullsend must build equivalent containment from scratch.

Neither approach solves the *semantic* injection problem — an agent tricked into producing subtly wrong but plausible output that passes all scans. That requires the judgment-layer defenses (intent verification, specialized injection-defense sub-agents) that gh-aw does not attempt.

### Multi-agent coordination

Fullsend builds custom dispatch infrastructure for multi-agent pipelines (triage → code → review). gh-aw already provides native [orchestration primitives](https://github.github.com/gh-aw/patterns/orchestration/): `dispatch-workflow` (async fan-out to worker workflows via `workflow_dispatch`) and `call-workflow` (same-run fan-out via `workflow_call`, preserving actor attribution). An orchestrator workflow can decide what to do, split work into units, and dispatch typed worker workflows with scoped permissions and tools. [Cross-repository operations](https://github.github.com/gh-aw/reference/cross-repository/) allow workflows to read from and write to external repos via `target-repo`, `allowed-repos`, and cross-repo checkout — partially addressing the org-wide coordination that fullsend's centralized `.fullsend` repo provides.

### Org-wide configuration

Fullsend creates a `.fullsend` config repo with `config.yaml`, normative specs, and centralized secrets. GitHub already provides org-level Actions policies, required workflows, organization rulesets, and org secrets with selected-repo visibility. A native system could leverage these directly rather than creating a parallel configuration layer.

## The trade-offs of nativeness

Building natively is not strictly superior. Platform lock-in introduces constraints that an external system avoids.

### Forge lock-in

Fullsend's forge abstraction ([ADR 0005](../ADRs/0005-forge-abstraction-layer.md)) means the same design could work on GitLab, Forgejo, or other platforms. gh-aw is GitHub-only by definition. For organizations that use multiple forges or may migrate, this matters. For GitHub-only organizations, it's cost without benefit.

### Runtime constraints

GitHub Actions runners have a 6-hour job timeout, limited compute options (unless self-hosted), and restrictions on what can run in containers. Agents that need long-running sessions, GPU access, specialized build tooling, or persistent state across invocations may outgrow Actions. An external system can run agents anywhere — on Kubernetes, dedicated VMs, or specialized agent platforms.

### Product dependency

gh-aw is in early development and may change significantly. Building on it means depending on GitHub's product decisions, deprecation timeline, and willingness to support autonomous use cases. GitHub has explicitly stated that humans should own the merge button — a position that directly conflicts with fullsend's thesis. An external system can build merge authority independently of the platform's product philosophy.

### Isolation model

gh-aw's container isolation is strong for its use case, but the isolation boundary is defined by GitHub, not by the operator. Organizations with specific compliance, data residency, or security requirements may need control over the sandbox that a native system cannot provide.

## Open questions

- **Should fullsend adopt gh-aw as its containment and execution layer?** Rather than building parallel infrastructure for cross-repo dispatch, credential isolation, and agent sandboxing, fullsend could use gh-aw directly for the containment layer and focus its engineering effort on the judgment layer (intent verification, review composition, merge authority). gh-aw workflows are markdown files — agents can author them, aided by gh-aw's own [documentation MCP server](https://github.github.com/gh-aw/reference/gh-aw-as-mcp-server/). The `gh aw` CLI already handles compilation, security scanning, and lock file generation. Fullsend would not need to replicate any of this.

- **Is the forge abstraction worth the cost at this stage?** Fullsend's only concrete implementation is GitHub. If forge-neutrality is deferred to Phase 4 (domain specificity in the [roadmap](../roadmap.md)), the current implementation could use GitHub-native primitives directly, simplifying the stack substantially.

- **Can gh-aw's safe-outputs model be extended for autonomous merge?** If a safe-output type of "merge this PR" were added (gated by all required checks passing and a confidence threshold), gh-aw's architecture could support tiered autonomy without fullsend's external plumbing. This depends on GitHub's willingness to add such a capability — which their current position suggests is unlikely.

- **Where is the boundary between containment and judgment?** gh-aw solves containment well. Fullsend's unique value is judgment. Should fullsend's implementation focus exclusively on the judgment layer and delegate containment to native platform features wherever possible?

## Recommended next step

Build a proof-of-concept that implements one fullsend pipeline stage (e.g. triage or review) as a gh-aw workflow. This would test whether gh-aw's containment model, orchestration primitives, and safe-outputs are sufficient as a substrate for fullsend's judgment layer, without requiring any of the custom infrastructure (Apps, dispatch tokens, enrollment shims, L7 proxies) that the current Go CLI manages.

gh-aw provides an [MCP server for CLI access](https://github.github.com/gh-aw/reference/gh-aw-as-mcp-server/) (compiling, running, and managing workflows), and we have configured a [gh-aw docs MCP server](https://github.github.com/gh-aw/llms.txt) in the fullsend development environment that agents can use to fetch gh-aw reference material (security architecture, safe-outputs spec, frontmatter reference, design patterns, etc.) while authoring workflow markdown. This means the POC can be largely agent-driven — an agent with access to both the fullsend codebase context and the gh-aw documentation can draft and iterate on workflow definitions without requiring a human to learn the gh-aw authoring model first.

If the POC validates the approach, it would inform whether fullsend's implementation effort should shift from building containment infrastructure to building judgment-layer capabilities on top of gh-aw.
