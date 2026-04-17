# Applied: konflux-ci

Organization-specific considerations for applying fullsend to the [konflux-ci](https://github.com/konflux-ci/) GitHub organization.

Konflux is a Kubernetes-native CI/CD platform that builds and deploys software with supply chain security guarantees. It is the original downstream consumer of fullsend — the project began here — and remains the primary proving ground.

## Why konflux-ci is an interesting proving ground

Konflux is a CI/CD system that provides security features to its users. This creates a dual security requirement:

- The system itself must be secure against threats (prompt injection, insider attacks, agent drift)
- The CI/CD content passing *through* the system must also be protected — agents reviewing pipeline definitions and build configurations need a security mindset

This dual requirement makes it a harder and more interesting target for autonomous agents than a typical application.

## Technology landscape

The konflux-ci org is heterogeneous:

- **Go** — controllers, operators, services (build-service, integration-service, release-service, etc.)
- **React** — UI components
- **Python** — tooling (hermeto/cachi2, utilities)
- **Tekton** — pipeline definitions, task definitions (build-definitions)
- **Shell** — scripts, CI glue

This heterogeneity tests fullsend's generality — what works for a Go controller may not work for a Tekton pipeline definition.

## Organization-specific details by problem area

### Architecture repo

The [konflux-ci/architecture](https://github.com/konflux-ci/architecture) repo contains per-service overview documents, 60+ Architecture Decision Records (ADRs), and architecture diagrams. It is organized per service (build-service, integration-service, release-service, enterprise-contract, pipeline-service, hybrid-application-service, konflux-ui).

This is the natural source of [architectural invariants](../../architectural-invariants.md) for Konflux. Examples of enforceable invariants from specific ADRs:

- ADR-0053 defines the trusted task model — all tasks in build pipelines must use it
- ADR-0006 defines log conventions
- ADR-0013 defines integration service API contracts
- ADR-0012 defines namespace name format
- ADR-0011 defines RBAC roles
- ADR-0030 defines Tekton Results naming conventions
- ADR-0046 defines the common task runner image

Contribution requirements for the architecture repo: significant changes require ADRs and 2 peer approvals; clarifications require 1 approval.

### Codebase context

Specific considerations for Konflux's org-level context:

- **Structured frontmatter on service docs.** Each service doc should have machine-parseable metadata: scope, related services, related ADRs, key CRDs. Use YAML lists, not comma-separated strings. Enforce the schema in CI.
- **Structured frontmatter on ADRs.** Add status, applies_to (list of repos/services), superseded_by, and topics as frontmatter.
- **Minimal CLAUDE.md in the architecture repo.** Should contain: what this repo is (1 sentence), how to find things (grep frontmatter), and core architectural constraints (API model is Kubernetes CRDs, all operations async, Tekton-based execution).
- **API standards.** The emerging API Review SIG (CODEOWNERS on API files across repos) needs a documented rubric starting from Kubernetes API conventions. Agents reviewing CRD changes consult it the same way they consult ADRs.
- **Cross-repo context.** Multi-repo changes — a CRD schema change affecting build-service and integration-service — need context from both repos simultaneously, pushing limits of single-repo agent operation.
- **Non-service repos.** Context for repos that aren't pure services (e.g., operator-toolkit, qe-tools, ci-helper-app) needs different frontmatter than service-oriented docs.

### Agent-compatible code

Code owned by konflux-ci — services, controllers, tooling — should meet the language criteria described in the [general problem doc](../../agent-compatible-code.md) directly. When new tooling is created or experiments graduate to production, prefer languages with static typing, error locality, refactoring safety, mature tooling, and simple deployment.

For consumed dependencies (hermeto/cachi2, PatternFly, Tekton, third-party libraries), the language criteria shift to the integration boundary. Example: hermeto (the build dependency solver) is written in Python. It runs as a container image invoked from Tekton tasks. The integration boundary is already container-isolated — hermeto's internal implementation language doesn't leak into the rest of the system. The risks are config schema drift and unvalidated outputs. The mitigation isn't rewriting hermeto; it's schema-validating the inputs, pinning the image digest, and testing the contract.

### Contributor guidance

Konflux aspires to be an "upstream" open source project capable of accepting contributions from the general public. This means contribution guidance must work for the full range of contributors — from first-time human contributors to organization-internal agents.

Specific considerations:

- Using AI must not be required to contribute to Konflux.
- Each Konflux repo may have its own CODEOWNERS boundaries and local conventions.
- Architecture docs in the konflux-ci/architecture repo provide cross-repo invariants and design decisions.

Examples of implicit knowledge that should be made explicit:

- "We use controller-runtime's predicate functions rather than manual filtering because we had memory leaks with the manual approach in 2024"
- "The CRD schema uses webhooks for validation rather than CEL because CEL wasn't mature enough when we designed this in 2023"
- "New contributors often forget to update the mock when changing an interface. Run `make generate` to catch this."
- "We prefer table-driven tests. See `pkg/controller/component_test.go` for the pattern."

Example minimal CLAUDE.md for a Konflux repo:
```
See CONTRIBUTING.md for full contribution guidelines. Key non-obvious points:

- CRD schema changes: see CONTRIBUTING.md#api-changes. Always check architecture repo.
- We had production incidents from unvalidated enum fields in Q3 2024 - all new fields need validation.
- Cross-repo impact: if changing Snapshot CRD, integration-service likely needs updates.

See BOOKMARKS.md for architectural context and external standards.
```

### Code review

The platform security and content security review sub-agents have Konflux-specific concerns:

**Platform security agent** — Reviews changes for threats to Konflux itself: RBAC and authorization changes, authentication flows, data exposure risks, privilege escalation paths, injection vulnerabilities.

**Content security agent** — Reviews changes that affect the CI/CD content passing through Konflux — protecting Konflux's users:
- Pipeline definition handling — can a user's pipeline definition escape its sandbox?
- Build configuration — can build parameters be manipulated?
- Release policy — can release gates be bypassed?
- Artifact integrity — can artifacts be tampered with?

### Repo readiness

Data from the [coverage dashboard](https://konflux-ci.dev/coverage-dashboard/) (as of March 2026):

**No coverage data (12 repos):**
application-api, build-trusted-artifacts, ci-helper-app, coverport, devlake, dockerfile-json, kargo, konflux-ci, may, namespace-generator, renovate-log-analyzer, test-data-sast

**Strong (>75%):**
| Repo | Coverage |
|---|---|
| segment-bridge | 90.8% |
| release-service | 87.5% |
| notification-service | 85.0% |
| repository-validator | 82.4% |
| internal-services | 78.6% |
| sprayproxy | 78.5% |
| image-rbac-proxy | 78.0% |

**Moderate (50-75%):**
| Repo | Coverage |
|---|---|
| image-controller | 71.1% |
| integration-service | 68.4% |
| caching | 62.2% |
| project-controller | 59.3% |
| smee-sidecar | 58.4% |
| operator-toolkit | 56.7% |
| tekton-kueue | 56.1% |
| multi-platform-controller | 53.6% |
| build-service | 52.6% |

**Thin (<50%):**
| Repo | Coverage |
|---|---|
| mintmaker | 45.1% |
| workspace-manager | 45.1% |
| coverage-dashboard | 34.6% |
| qe-tools | 32.3% |
| tektor | 30.5% |
| kueue-external-admission | 12.3% |

### Intent representation

Konflux's current intent system is the KONFLUX JIRA project, where features move through states (draft, pending approval, approved, in progress, etc.) and are ranked. Higher-ranked features should be worked on first. Features in "pending approval" require review from named architects and product managers.

However, there are no ACLs that prevent an unauthorized person from zipping through the refinement states and closing all the trackers, making a feature look authorized and ready to go. This is one of the motivations for git-based intent representation.

The cryptographic attestation approach (Approach 3 in the general doc) is thematically consistent with what Konflux already does for build provenance — SLSA provenance for builds, hermetic builds, trusted artifact chains, Enterprise Contract policy evaluation.

Open questions specific to Konflux:
- How do we handle the migration from JIRA? Can the two systems coexist during transition?
- Changes that affect the public API contract between Konflux and its users warrant Tier 3 treatment.

### Performance verification

Konflux is a Kubernetes-native CI/CD platform with architecture-specific performance challenges:

- **The "database" is the Kubernetes API server.** Performance problems manifest as excessive LIST/WATCH calls, unbounded label selector queries, or controllers that re-list resources they should be watching.
- **Controller logic needs a cluster to test.** Reconciliation logic needs a real or simulated Kubernetes environment (envtest).
- **Builds and tests are Tekton PipelineRuns.** Pipeline execution time and resource consumption are core concerns. A task change that adds 5 minutes to every build pipeline affects every tenant.
- **The platform runs its own CI.** Konflux builds itself using Konflux — a performance regression can slow down detection of the regression.
- **Multi-tenant resource contention matters.** A controller that works fine with 10 tenants might overwhelm the API server with 1000.

See the [general problem doc](../../performance-verification.md) for the full treatment of detection approaches and agent-specific anti-patterns.

### Production feedback

Konflux's richest production signals are pipeline execution data:

- PipelineRun failure rates by failure category (timeout, task failure, scheduling failure, image pull failure)
- TaskRun failure distributions by task type
- Queue depth and scheduling latency trends
- Integration test outcome distributions by integration scenario type
- Release pipeline failure rates

Tenant pipeline signals carry additional meaning — test failures in Konflux's own e2e test suite often correlate directly with code paths, and build failures in component builds trace back to specific repos and commits.

The attribution problem is especially acute: distinguishing platform failures from user configuration errors or external dependency changes requires correlating spikes with recent Konflux deploys, task version changes, and failure log content.

Privacy and multi-tenancy boundaries matter: aggregated metrics are safe to consume, but raw TaskRun logs from user pipelines may contain sensitive content.

### Security threat model

The dual security context is Konflux-specific — agents have two distinct security responsibilities:

1. **Protecting the system itself** — agents reviewing changes to Konflux components need to guard against threats to the platform
2. **Protecting content passing through the system** — agents reviewing pipeline definitions, build configurations, and release policies need to guard against threats that would affect Konflux users

For the supply chain threat, Konflux already provides significant protections for the source-to-binary boundary: SLSA provenance, hermetic builds, trusted artifact chains, and Enterprise Contract policy evaluation. These remain necessary but are not sufficient when the source itself may be the product of a compromised model.

The question of whether the Thompson analog strengthens the case for extending trust claims from "this binary came from this source" to "this source faithfully implements this intent" is particularly relevant to Red Hat's value proposition.

### Testing agents

Agent evaluations in Konflux would run as Tekton tasks, so cost includes both LLM API costs and cluster resources.

The [lightspeed-evaluation](https://github.com/instructlab/lightspeed-evaluation) framework from the InstructLab project is worth noting because of its proximity to the Red Hat ecosystem that konflux-ci operates in.

### Agent infrastructure

Konflux runs work as Kubernetes/Tekton workloads; anything in [agent infrastructure](../../agent-infrastructure.md#challenges-in-headless-and-cluster-hosted-runtimes) about headless and cluster-hosted agents applies here directly.

### Human factors

Konflux-ci is an open-source project. Contributors participate for reasons beyond a paycheck — learning, building reputation, solving interesting problems, and being part of a community. Most konflux-ci contributors are paid engineers, which adds a professional dimension to the concerns about role shift and job security.

### Governance

The relationship between governance of the agentic system and governance of Konflux itself is an open question: are they the same body, or separate?

## Experiments

The experiments in this repo were originally conducted against the konflux-ci codebase:

- **[adr46-scanner](https://github.com/fullsend-ai/experiments/tree/main/adr46-scanner)** — Python CLI tool that detects Tekton task images drifting from ADR-0046 (the common task runner image ADR)
- **[adr46-claude-scanner](https://github.com/fullsend-ai/experiments/tree/main/adr46-claude-scanner)** — LLM-based drift scanner that analyzes Tekton tasks against any ADR
- **[prompt-injection-defense](https://github.com/fullsend-ai/experiments/tree/main/prompt-injection-defense)** — Defense-in-depth experiment testing layered prompt injection defenses
- **[agent-outage-fire-drill](https://github.com/fullsend-ai/experiments/blob/main/003-agent-outage-fire-drill.md)** — Proposed experiment: disable agents for 2 weeks to test human capability atrophy
