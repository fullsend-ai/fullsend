# Experiment: Integration Service Design Doc Drift Analysis

**Date:** 2026-03-20

**Tool:** [OpenCode](https://opencode.ai) (claude-opus-4-6 via google-vertex-anthropic)

## Hypothesis

An AI agent (OpenCode) can read an architecture design document, compare it against the actual codebase it describes, identify meaningful drift between the two, and propose concrete updates to bring the design document back into alignment with reality.

## Setup

- **Design document:** [`architecture/core/integration-service.md`](https://github.com/konflux-ci/architecture/blob/main/architecture/core/integration-service.md) from the `konflux-ci/architecture` repo
- **Codebase:** [`konflux-ci/integration-service`](https://github.com/konflux-ci/integration-service) (Go, ~1,984 commits, Kubernetes operator)
- **Method:** OpenCode read the full design document, then dispatched parallel agents to analyze the codebase across three domains (controller structure, API/CRD types, workflow/features). Findings were compared against each claim in the design document.
- **No code was cloned locally.** All codebase analysis was done by fetching raw files from GitHub.

## Summary Verdict: Meaningful Drift Found

**Yes, there is significant and meaningful drift.** The design document describes a system that is roughly 2-3 major iterations behind the actual codebase. The core workflow is recognizable, but every section of the document has material inaccuracies, omissions, or outdated information. The drift is not cosmetic -- it would actively mislead someone trying to understand the system by reading the design document.

The most consequential gaps:
1. An entire controller exists in the codebase that the design doc doesn't mention
2. The API has advanced two major versions beyond what the design doc references
3. Multiple first-class features (group snapshots, git provider status reporting, test re-runs, supersession/cancellation) are absent from the document
4. An active architectural migration (Application to ComponentGroup) is underway with no design doc coverage

---

## Detailed Drift Analysis

### 1. Controller Structure

The design document lists five controllers:

| Design Doc Controller | Exists? | Active? | Accurate? |
|---|---|---|---|
| Build Pipeline Controller | Yes | Yes | Partially -- description understates scope |
| Snapshot Controller | Yes | Yes | Partially -- missing group snapshot, re-run handling |
| Component Controller | Yes | Yes | Overstated -- design doc implies it triggers tests; actual code only manages finalizer lifecycle |
| Scenario Controller | Yes | **No (disabled)** | Wrong -- code exists but is commented out of registration, replaced by admission webhooks |
| StatusReport Controller | Yes | Yes | Partially -- design doc undersells this as a status updater; it actually reports to GitHub/GitLab/Forgejo |

**Missing from design doc:** The **Integration Pipeline Controller** (`internal/controller/integrationpipeline/`) is a fully active controller that watches integration test PipelineRuns and writes test results back to Snapshots. It is registered first in the controller setup list and is not mentioned anywhere in the design document.

**Scenario Controller is dead code.** The design doc describes it as validating IntegrationTestScenario configurations. In reality, it has been replaced by admission webhooks (`SetupIntegrationTestScenarioWebhookWithManager` and `SetupSnapshotWebhookWithManager` registered in `main.go`). The controller directory still exists but its registration is commented out and its only operation is a placeholder no-op.

### 2. API Versions

| Aspect | Design Doc Says | Actual |
|---|---|---|
| IntegrationTestScenario | `v1alpha1` | **`v1beta2`** (storage version) -- two generations ahead. `v1alpha1` carries a deprecation warning. |
| Snapshot | Owned by integration-service | Defined externally in `konflux-ci/application-api`, imported by integration-service |

This is a significant gap. The design doc's API reference link points to `v1alpha1`. Anyone following that reference gets a deprecated API version.

### 3. IntegrationTestScenario CRD Fields

The CRD has changed substantially between what the design doc describes and the current `v1beta2`:

| Field | Design Doc | Actual `v1beta2` | Change |
|---|---|---|---|
| `application` | Required | **Optional** (mutually exclusive with `componentGroup`) | Breaking change |
| `componentGroup` | Not mentioned | New optional field | New concept |
| `pipeline` + `bundle` | Two fields for Tekton bundle ref | **Removed** | Replaced by `resolverRef` |
| `resolverRef` | Not mentioned | `{resolver, params[], resourceKind}` | New -- uses Tekton Resolver pattern |
| `resolverRef.resourceKind` | N/A | `"pipeline"` or `"pipelinerun"` | New -- PipelineRun-as-template pattern |
| `environment` | `TestEnvironment` struct | **Removed entirely** | Breaking -- whole concept gone |
| `dependents` | Not mentioned | `[]string` -- ITS ordering/serialization | New |
| Timeout annotations | Not mentioned | Three annotations for pipeline/task/finally timeouts | New |

### 4. New CRD: ComponentGroup

An entirely new CRD (`v1beta2.ComponentGroup`) exists that the design doc does not mention. This is not a minor addition -- it represents an **active architectural migration** from the Application-centric model to a ComponentGroup model:

- Replaces Application CR as the component grouping mechanism
- Has `spec.components[]` with versioned component references
- Has `spec.dependents[]` for inter-group dependencies
- Has `spec.testGraph` for test serialization ordering
- Tracks its own `status.globalCandidateList[]`

The build pipeline controller has parallel code paths -- one for the old Application model and one for the new ComponentGroup model -- with `// TODO: remove after migration` comments throughout.

### 5. Snapshot CR Changes

| Field | Design Doc | Actual | Change |
|---|---|---|---|
| `application` | Required | **Optional** (mutually exclusive with `componentGroup`) | Changed |
| `componentGroup` | Not mentioned | New field | New |
| `components[].version` | Not mentioned | New optional field | New -- multi-version support |
| `components[].source` | Not mentioned | `ComponentSource` with `GitSource{URL, Revision}` | New -- git provenance tracking |

The design doc says Snapshots are "immutable once created." This is only true for `spec.components` -- status conditions, annotations, and labels are mutated freely throughout the lifecycle, and this immutability is not enforced at the CRD level.

### 6. Tekton Pipeline Results

| Result | Design Doc | Actual | Match? |
|---|---|---|---|
| `IMAGE_URL` | Listed | `PipelineRunImageUrlParamName = "IMAGE_URL"` | Yes |
| `IMAGE_DIGEST` | Listed | `PipelineRunImageDigestParamName = "IMAGE_DIGEST"` | Yes |
| `CHAINS-GIT_URL` | Listed | `PipelineRunChainsGitUrlParamName = "CHAINS-GIT_URL"` | Yes |
| `CHAINS-GIT_COMMIT` | Listed | `PipelineRunChainsGitCommitParamName = "CHAINS-GIT_COMMIT"` | Yes |

**No drift.** The four Tekton result names are identical.

### 7. Annotations and Labels

All annotations and labels still use the `appstudio.openshift.io` domain despite the project rebranding from AppStudio to Konflux. The design doc's annotation examples are still accurate in naming convention, but the codebase has added many new annotations not documented:

- `test.appstudio.openshift.io/pr-group` -- PR group tracking
- `test.appstudio.openshift.io/pr-group-sha` -- PR group hash
- `test.appstudio.openshift.io/integration-workflow` -- push vs pull-request
- `test.appstudio.openshift.io/run` -- test re-run trigger
- `test.appstudio.openshift.io/ignore-supersession` -- opt out of cancellation
- `test.appstudio.openshift.io/comment_strategy` -- git comment policy
- `test.appstudio.openshift.io/parent-snapshot` / `origin-snapshot` -- snapshot chaining
- `appstudio.openshift.io/version` -- component version
- `appstudio.openshift.io/component-group` -- ComponentGroup association

### 8. Major Features Missing from Design Doc

#### Group Snapshots
The codebase supports three snapshot types: `component`, `override`, and `group`. When multiple components have open PRs targeting the same branch, they are combined into a group snapshot for cross-component testing. The design doc only describes component snapshots.

#### Status Reporting to Git Providers
The design doc does not mention reporting back to git providers. The codebase has a full `ReporterInterface` abstraction with three implementations:
- **GitHubReporter** -- sets commit statuses and check runs via GitHub App
- **GitLabReporter** -- sets commit statuses and MR comments via PAC tokens
- **ForgejoReporter** -- sets commit statuses and PR comments via Forgejo/Gitea API

This is surfaced through a dedicated `statusreport` controller.

#### Test Re-run Capability
Users can re-run integration tests by adding the label `test.appstudio.openshift.io/run` to a Snapshot (value `all` or a specific scenario name). The snapshot controller handles this via `EnsureRerunPipelineRunsExist`.

#### Supersession / Cancellation
When a new build starts for the same PR, the system finds and cancels in-progress snapshots and their test PipelineRuns for the same component/PR. An opt-out annotation exists (`ignore-supersession`).

#### Override Snapshots
Users can manually create override snapshots to force-update the Global Candidate List for multiple components simultaneously, bypassing the normal build-triggered flow.

#### Tekton Chains Signing Requirement
Build PipelineRuns must have the `chains.tekton.dev/signed` annotation before snapshot creation proceeds. The reconciler requeues until signing completes.

#### CEL-based Auto-Release
The auto-release flag on ReleasePlan can contain CEL expressions (e.g., `updatedComponentIs("my-component")`), enabling conditional release logic.

#### Merge Queue Support
Explicit handling of GitHub merge queue events (`gh-readonly-queue/` branch prefix).

#### Metrics and Observability
Prometheus metrics for snapshot completion, pipeline run starts, integration response latency, release latency, and invalid snapshot counts.

#### Git Resolver Updates for PRs
For pull request snapshots, integration test pipeline tasks can have their git resolvers updated to use the PR's source code, enabling testing with modified pipeline definitions.

### 9. Workflow Differences

The design doc describes a simple linear 8-step workflow. The actual architecture is event-driven across 6 independent controllers:

| Design Doc Step | Actual Controller | Accurate? |
|---|---|---|
| Steps 1-4 (build watch, query, snapshot, GCL) | Build Pipeline Controller | Mostly, but GCL update logic is more complex (annotation-tracked, dual paths) |
| Step 5 (create test PipelineRuns) | Snapshot Controller | Yes, but also handles group snapshots, re-runs, override validation |
| Step 6 (watch test PipelineRuns) | **Integration Pipeline Controller** (not in design doc) | The design doc attributes this to the Snapshot Controller |
| Step 7-8 (auto-release) | Snapshot Controller | Yes, but with CEL expression support |
| Status reporting | StatusReport Controller + Build Pipeline Controller | Not in design doc at all |

---

## Proposed Changes to the Design Document

Based on the drift analysis, the following changes would bring the design document into alignment with the actual codebase. Changes are ordered from most to least impactful.

### 1. Add Integration Pipeline Controller to Controllers Section

Add a new controller entry:

> **Integration Pipeline Controller** -- Watches integration test PipelineRuns. When a test PipelineRun completes, it reads the test result and updates the associated Snapshot's status with the outcome. Also captures the PipelineRun log URL for observability.

### 2. Update Scenario Controller Description

Replace the current description with:

> **Scenario Controller** -- Currently disabled. IntegrationTestScenario validation and mutation are handled by admission webhooks rather than a reconciliation controller.

### 3. Add ComponentGroup Documentation

Add a new section documenting the ComponentGroup CRD and the ongoing migration from Application-centric to ComponentGroup-centric architecture. This should reference the dual code paths in the build pipeline controller and explain that both models are currently active.

### 4. Update API Version References

- Change all `v1alpha1` references for IntegrationTestScenario to `v1beta2`
- Update the API reference link from `v1alpha1` to `v1beta2`
- Note that Snapshot is defined in `konflux-ci/application-api`, not in integration-service itself

### 5. Update IntegrationTestScenario CRD Description

Document the current field structure:
- `application` is now optional (mutually exclusive with `componentGroup`)
- `resolverRef` replaces `pipeline` + `bundle`
- `environment` has been removed
- `dependents` field enables test ordering
- `resourceKind` supports both `pipeline` and `pipelinerun` templates
- Timeout annotations are available

### 6. Add Group Snapshots Section

Document the three snapshot types (`component`, `override`, `group`) and the PR group mechanism for cross-component testing.

### 7. Add Status Reporting Section

Document the `ReporterInterface` and its three implementations (GitHub, GitLab, Forgejo), including what information is reported, when, and how comment policy can be controlled.

### 8. Add Test Re-run Documentation

Document the label-driven re-run mechanism and how it interacts with the snapshot controller.

### 9. Add Supersession/Cancellation Documentation

Document how newer builds cancel in-progress snapshots and their test PipelineRuns for the same PR.

### 10. Update Workflow Description

Replace the linear 8-step workflow with a description that reflects the actual event-driven, multi-controller architecture. The current description creates a mental model that doesn't match the code.

### 11. Add Override Snapshot Documentation

Document how override snapshots work, including validation and GCL update behavior.

### 12. Update Auto-Release Description

Add CEL expression support to the auto-release documentation.

### 13. Add Annotations/Labels Inventory

Update the annotations section to include the new annotations for PR groups, workflow type, re-run triggers, supersession control, and comment policy.

### 14. Add Tekton Chains Signing Prerequisite

Document the signing requirement as a prerequisite for snapshot creation.

### 15. Note the AppStudio Naming Legacy

Add a note explaining that annotations still use the `appstudio.openshift.io` domain despite the project rebranding to Konflux, for backward compatibility.

---

## Comparison with Experiment 002 (ADR Drift Scanner)

| Dimension | Experiment 002 (ADR Scanner) | This Experiment (Design Doc Drift) |
|---|---|---|
| **What's compared** | ADR requirements vs. one Tekton task YAML | Architecture design doc vs. full codebase |
| **Scale** | ~150 lines of ADR vs. ~280 lines of YAML | ~300 lines of design doc vs. ~1,984 commits of Go code |
| **Agent** | Claude CLI (`claude -p`) | OpenCode (claude-opus-4-6 via parallel subagents) |
| **Approach** | Single prompt, single pass | Multi-agent: 3 parallel subagents analyzing controllers, APIs, and workflow separately |
| **Drift type** | Code violating stated requirements | Documentation not reflecting code reality |
| **Automation** | 40-line shell script | Manual agent session (could be scripted) |
| **Finding severity** | Moderate -- violations are fixable | High -- document is ~2-3 iterations behind |
| **Actionability** | "Fix these 7 steps" | "Rewrite these 15 sections" |

### Key difference in difficulty

The ADR scanner compared a **prescriptive** document (the ADR says "do X", the code does or doesn't do X). This experiment compared a **descriptive** document (the design doc says "the system does X", and we check whether that's still true). Descriptive drift is harder to detect because:

1. You need to understand the codebase holistically, not just check one artifact
2. Drift can be additive (features exist that aren't documented) or subtractive (documented features that are gone)
3. Some drift is intentional evolution, not a bug -- the challenge is identifying which

## Limitations

- **No local clone.** All analysis was done via GitHub raw file fetches. Some files may have been missed if they weren't on obvious paths.
- **Point-in-time.** Both the design doc and codebase were read as of March 20, 2026. Drift may increase or decrease with future changes.
- **Interpretation required.** Some drift items (e.g., whether the Scenario Controller being disabled is "drift" or "evolution") require human judgment to prioritize.
- **No ADR cross-reference.** The analysis did not read all referenced ADRs (0016, 0037, 0038, 0048) to determine whether some drift was intentional and documented via ADR rather than in the design doc.
- **Single model.** Only one model (claude-opus-4-6) was used. Different models may find different drift or interpret it differently.

## Conclusion

The integration-service design document in `konflux-ci/architecture` has drifted substantially from the actual codebase. The drift is not superficial -- it spans controller architecture, API versions, CRD fields, workflow behavior, and multiple first-class features. A reader relying solely on the design document would get an incomplete and partially incorrect understanding of the system.

The most valuable finding is not any individual drift item, but the pattern: **the codebase has evolved through at least 2-3 major architectural iterations (composite removal, GCL promotion, ComponentGroup migration) while the design document was updated for some of these (it references ADR-0037 and ADR-0038) but not for features that grew organically without ADRs** (group snapshots, git provider reporting, re-runs, supersession).

This suggests that design document drift is most likely for features that are added incrementally through PRs rather than through formal ADR processes. An automated drift scanner running periodically could flag when a codebase has diverged from its architecture documentation, prompting humans to decide whether the doc or the code needs to change.
