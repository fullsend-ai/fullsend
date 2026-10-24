# SPIKE: Gemara policy artifacts in an auto-merge lab

**Issue:** [#8789](https://github.com/fullsend-ai/fullsend/issues/8789) ·
**Companion experiment:** [fullsend-ai/experiments#64](https://github.com/fullsend-ai/experiments/issues/64) ·
**Date:** 2026-10-24

## Status and scope

This is a research note. It records what a public lab demonstrated when it
used [Gemara](https://github.com/gemaraproj/gemara/tree/9d36c253484d14922010252bfffe58bdcd49a144)
policy and evaluation artifacts to gate auto-merge for Fullsend-reviewed pull
requests. It also records which capabilities came from upstream, which
integration the lab authored, and which questions remain open.

It is **not** an adoption decision. Fullsend has not selected Gemara, the
OpenSSF Baseline, or any other policy architecture.
[PR #7151](https://github.com/fullsend-ai/fullsend/pull/7151) (dedicated
auto-merge authority boundary) is open, not an accepted decision. This note
changes no runtime behavior, schema, credential, or branch protection.

Statements below use four labels:

- **Observed:** a historical observation from a public run, PR, or commit.
  Run and PR pages can change after the snapshot date.
- **Configured:** configuration at a recorded commit. Configuration can change
  later. The lab has since been returned to shadow mode.
- **Proposal:** a suggestion that nobody has implemented or decided.
- **Unknown:** a question the lab did not answer.

## What the lab was

The lab had three parts:

1. A **shared policy module**
   ([ascerra/fullsend-gemara-integration](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/README.md)).
   It holds common control catalogs, assessment definitions, and deterministic
   evaluation code. Consumers pin it by commit.
2. A **consumer repository**
   ([fullsend-ai/auto-merge](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/.fullsend/config.yaml)).
   It runs Fullsend Review and collects evidence from GitHub. It evaluates the
   shared policy from its trusted base branch and owns enforcement.
3. A **separate writer and the forge**. A GitHub App writer asks GitHub's
   protected merge queue to merge. The queue then re-evaluates the combined
   revision and runs CI before GitHub performs the merge.

## Who supplied what

| Capability | Supplied by | Notes |
|---|---|---|
| ControlCatalog, Policy, and EvaluationLog definitions | Gemara schemas (upstream) | 17 CUE files loaded for validation |
| Native Go types and assessment machinery | [go-gemara](https://github.com/gemaraproj/go-gemara/tree/5e33fda460a80ffa99e9efbe19afb43e7d4c4dd2) SDK (upstream) | Records Passed, Failed, or Unknown results |
| Requirement text for OSPS-QA-03.01 | [OpenSSF Baseline](https://github.com/ossf/security-baseline/blob/a26a7963f6fd098c1e82829322ec9e1196a4f294/baseline/OSPS-QA.yaml#L84-L101) (upstream) | Text only; no executable check |
| Controls, evidence adapters, policy mappings, decision composition | Lab-authored | Shared module plus consumer code |
| Evidence authentication, policy pinning, trusted-base execution, queue revalidation | Lab-authored | Integration work, not a schema feature |
| Merge authority | GitHub App writer and GitHub protected merge queue | The SDK mints no token and performs no merge |

## Findings

### 1. Native artifact reuse worked

**Observed.** Gemara supplied the ControlCatalog, Policy, and EvaluationLog
definitions. The Go SDK supplied the assessment machinery
([native plan implementation](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/gemara-auto-merge/native-evaluator/policy_assessment.go),
[entry point](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/gemara-auto-merge/native-evaluator/main.go),
[schema and toolchain lock](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/gemara-auto-merge/toolchain-lock.json)).
The lab authored the controls, evidence adapters, policy mappings, and the
composition of results into a decision.

### 2. Shared policy reuse worked, with repository bindings

**Configured.** The common rules live in the shared module
([catalog](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/policy/catalog.json),
[common policy](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/policy/common.json),
[evaluation](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/evaluate.go),
[candidate composition](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/candidate.go)).
The [GitHub](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/examples/github/binding.json)
and [GitLab](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/examples/gitlab/binding.json)
binding examples keep forge identity and evidence sources in each consumer's
binding. The examples alone do not prove a live deployment.

The [adoption guide](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/docs/ADOPTION.md)
draws the ownership line. The shared module owns catalogs, assessment
definitions, and evaluation code. Each consumer owns forge identity, trusted
evidence sources, current revisions, owner delegation, and enforcement. A
forge-neutral assessment does not remove forge-specific evidence collection or
enforcement.

### 3. Missing evidence held

**Observed (privately); public reproduction proposed.** A GitLab consumer
lacked the required risk assessment, so the common requirement held the
change. That live deployment is private and is not a public source. The
companion experiment should reproduce the case with a synthetic GitLab
fixture. This note claims no GitLab live merge.

### 4. A historical quality signal blocked an otherwise eligible candidate

**Observed.** On [PR #53](https://github.com/fullsend-ai/auto-merge/pull/53)
([evaluation run 39594830599](https://github.com/fullsend-ai/auto-merge/actions/runs/39594830599),
[shared assessment artifact 12344096345](https://github.com/fullsend-ai/auto-merge/actions/runs/39594830599/artifacts/12344096345)),
the review-quality requirement counted 238 passing judgments out of 254 scored
approvals (93.70%). The configured threshold was 99%, so the combined
assessment returned **hold**. The writer's enqueue job was skipped, and PR #53
remains open.

The number is a **judged-approval pass rate**: the share of the review
agent's approvals that an LLM judge scored as correct. It is not general review
accuracy, it is not coverage, and it is not a 99% confidence guarantee about a
future PR
([quality-check semantics](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/docs/REVIEW-QUALITY.md),
[threshold implementation](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/judge_check.go),
[aggregate collector](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/scripts/collect_mlflow_judge.py),
[synthetic threshold policy](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/examples/quality/policy.json)).

### 5. A different threshold changed the policy result, not the measurement

**Observed.** In a separately authorized trial, the owner set the threshold to
90%. The same 238/254 observation satisfied that quality requirement, and all
other required conditions still applied. The 90% setting was an owner-selected
experiment. It is not a recommended production setting, and it does not
improve review quality.

[PR #56](https://github.com/fullsend-ai/auto-merge/pull/56) merged in an
earlier demonstration.
[PR #54](https://github.com/fullsend-ai/auto-merge/pull/54) is **unmerged**
and is not the successful merge.

### 6. A maintained OpenSSF requirement was selected and exercised

**Configured and observed.** The lab imported OSPS-QA-03.01 (parent control
OSPS-QA-03, release `v2026.08.28`) with provenance
([source pin](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/gemara-auto-merge/upstream/osps-baseline/source.json),
[catalog wrapper](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/gemara-auto-merge/upstream/osps-baseline/catalog.json),
[mapping](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/gemara-auto-merge/upstream/osps-baseline/policy.json),
[owner flow](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/docs/ADD-AN-UPSTREAM-CONTROL.md)).
It mapped the requirement to an `all-checks-passed` assessment. Lab code
implements that assessment with exact-revision GitHub checks and statuses
([evidence adapter](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/gemara-auto-merge/configured_policy.py#L51-L123)).

The requirement text is maintained upstream. The executable adapter is the
lab's own. The adapter also checks observed optional pass/fail checks, which
adds value beyond checking only required contexts. Passing one requirement is
not certification against the whole Baseline or any maturity level.

### 7. A separate writer and the forge performed the merge

**Observed.** For [issue #57](https://github.com/fullsend-ai/auto-merge/issues/57)
and [PR #58](https://github.com/fullsend-ai/auto-merge/pull/58), these steps
all happened before the protected merge:

1. Current-head Fullsend Review
   ([run 39955761534](https://github.com/fullsend-ai/auto-merge/actions/runs/39955761534),
   [review-evidence artifact 12502054995](https://github.com/fullsend-ai/auto-merge/actions/runs/39955761534/artifacts/12502054995)).
2. Shared assessment and the selected OpenSSF assessment at enrollment
   ([run 39956959534](https://github.com/fullsend-ai/auto-merge/actions/runs/39956959534),
   [shared artifact 12502977284](https://github.com/fullsend-ai/auto-merge/actions/runs/39956959534/artifacts/12502977284),
   [native/OpenSSF artifact 12503645276](https://github.com/fullsend-ai/auto-merge/actions/runs/39956959534/artifacts/12503645276)).
3. The App queue request
   ([writer job 120426280311](https://github.com/fullsend-ai/auto-merge/actions/runs/39956959534/job/120426280311),
   [writer artifact 12503242005](https://github.com/fullsend-ai/auto-merge/actions/runs/39956959534/artifacts/12503242005)).
4. Cumulative merge-group evaluation and CI on the combined revision
   ([evaluation run 39957499141](https://github.com/fullsend-ai/auto-merge/actions/runs/39957499141),
   [decision/OpenSSF artifact 12503845545](https://github.com/fullsend-ai/auto-merge/actions/runs/39957499141/artifacts/12503845545),
   [CI run 39957499387](https://github.com/fullsend-ai/auto-merge/actions/runs/39957499387)).

PR #58 merged at 2026-10-23 13:03:51 UTC as
[fee00797](https://github.com/fullsend-ai/auto-merge/commit/fee00797c19a07a2e3bdd83ce7fd1dbc917087f1).
The writer used a short-lived, repository-scoped App token. The recorded queue
actor was `gemara-auto-merge-pilot-lab[bot]`. The SDK did not mint a token or
perform the merge.

### 8. Trust boundaries required significant custom code

**Observed.** The following were integration work. None came automatically
from adopting a schema:

- Policy and evidence pinning.
- Trusted-base execution, so a candidate branch cannot replace the policy or
  evidence that authorizes its own merge
  ([trusted authorizer](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/gemara-auto-merge/authorize_auto_merge.py),
  [shared-code execution boundary](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/scripts/shared_gate_runtime.py)).
- Handling of stale and current revisions
  ([evidence collector](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/gemara-auto-merge/github_pr_evidence.py),
  [review-evidence contract](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/gemara-auto-merge/review_evidence_contract.py),
  [shared gate adapter](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/gemara-auto-merge/shared_gate.py)).
- Scoped writer authority and queue revalidation
  ([evaluation workflow](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/.github/workflows/gemara-auto-merge.yml),
  [App writer](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/.github/workflows/gemara-queue-writer.yml),
  [combined-revision validation](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/.github/workflows/gemara-merge-queue.yml)).

A digest establishes identity and integrity only relative to a trusted source.
A digest alone does not establish that the source is authentic. Likewise, a
Gemara policy or evaluation artifact records an assessment result. It is not
an authenticated attestation, and it does not authorize a merge.

Some earlier lab setup deployments used separately approved, temporary admin
bypasses, and protections were restored afterward. Those bypasses are distinct
from the protected PR #58 merge. Bypassing protections is not an installation
step.

### 9. The trial was revoked

**Observed.** The lab returned to shadow mode and disabled its grants
([commit 4c86073e](https://github.com/fullsend-ai/auto-merge/commit/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71),
[cleanup CI 39958588002](https://github.com/fullsend-ai/auto-merge/actions/runs/39958588002),
[shadow observation workflow](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/.github/workflows/gemara-shadow-observation.yml)).
The lab's separate production-readiness verdict remained **hold**.
Enforcement in the lab was historical. It is not currently enabled, general
autonomy.

## Limitations

- **Judge, not human labels.** The quality signal is an LLM judge's score on
  the review agent's approvals. Agreement with a judge is not independent
  human validation. It says nothing about the review agent's rejections.
- **Cohort.** The aggregate mixed experiment-wide scored approvals across
  review versions and rubrics. It was not a reliably isolated
  current-agent-version or per-repository cohort. The source ID did not pin a
  judge revision.
- **Selection and coverage.** Skipped and unscored cases, selection bias in
  which runs were judged, and judge or rubric drift are unresolved.
- **Freshness versus cohort validity.** The collector bounded snapshot age,
  but a fresh snapshot of old review outcomes is still not evidence about the
  current PR. Freshness says when data was read. Cohort validity says whether
  the data describes the agent being trusted.
- **Current-PR versus historical evidence.** Review, risk, and check results
  were bound to the PR's exact revision. The quality signal was experiment-wide
  history. Composing them with AND does not turn either into the other.
- **Status versus value.** Gemara recorded Passed, Failed, or Unknown. Missing,
  stale, or malformed data was Unknown and held. An empty population never
  counted as 100%. The numeric rate alone does not carry that distinction.
- **Outcome is not safety.** A successful protected merge does not establish
  that the merged code was safe.
- **GitLab.** The missing-risk negative case is not yet publicly reproduced,
  and there was no GitLab live merge.
- **Scope.** The lab did not qualify an agent against all five
  [trustworthiness evidence](../problems/trustworthiness-evidence.md#types-of-trustworthiness-evidence)
  categories and did not adopt a production scorecard schema.

## Maintenance and value inventory

Physical line counts at the recorded revisions. They include comments, blank
lines, and lab utilities, and exclude binaries. Recount if source revisions
change.

| Component | Size | Qualification |
|---|---|---|
| Gemara schemas | 17 CUE files, 1,422 lines | Loaded for validation |
| go-gemara SDK | 16 imported Go files, 3,513 lines (1,213 in the generated types file) | Imported package footprint, not executed lines or work saved |
| Selected OpenSSF source file | 250 YAML lines | Supplies zero executable GitHub checks |
| Shared integration | 1,726 source/tooling, 960 test, 619 policy/config/CI | Lab-authored |
| Auto-merge integration | 7,215 source/tooling, 5,157 test, 2,649 policy/config/orchestration | Lab-authored |
| Local Fullsend patch | +190/−6 source, +170 test, +63 docs | A lab build, not an upstream release ([patch](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/gemara-auto-merge/local-fullsend.patch), [build provenance](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/.fullsend/local-build.json)) |
| OSPS-specific dedicated files | 165 adapter, 132 policy/pin, 157 test (454 total) | Excludes reused generic machinery and copied upstream YAML and license; not a complete marginal-change count |

These figures are not a minimum customer installation, a pure "Gemara tax", or
a measure of engineering effort saved.

**Supported conclusion.** Gemara standardized the policy and evaluation
artifacts and let the lab connect existing evidence to auditable results.
**Unknown:** whether that benefit outweighs the dependency, schema, and
integration cost compared with simpler native structures. A comparative
implementation and UX study is future work. Line counts alone cannot answer
it.

## Relationship to Fullsend problem areas

- [Trustworthiness evidence](../problems/trustworthiness-evidence.md#gemara-lab-observation)
  — the primary home: threshold composition, freshness, status versus value,
  and why an evaluation artifact is not an attestation.
- [Governance](../problems/governance.md#2-configuration-security) — shared
  policy distribution with repository-owned bindings, and tracing a merge to
  its authorizing policy version.
- [Autonomy spectrum](../problems/autonomy-spectrum.md#the-probationary-period) —
  eligibility for one gated merge is not graduation to general autonomy.
- [Graduated approval policy](../problems/graduated-approval-policy.md#path-forward)
  — composition evidence, not calibration evidence.
- [Code review](../problems/code-review.md#escalation-based) — structured
  assessment around externally produced findings.
- [Review autonomy evidence](../problems/review-autonomy-evidence.md#open-questions)
  — a methodological limitation, not a new agent-versus-human datapoint.
- [Testing agents](../problems/testing-agents.md#measuring-agent-capability-drift)
  — judge results as policy inputs, and the cohort problem.
- [Operational observability](../problems/operational-observability.md#structured-traces)
  — historical telemetry became a policy input. Collecting or exporting
  metrics is distinct from authorizing a merge.
- [Platform nativeness](../problems/platform-nativeness.md#autonomous-merge-judgment)
  — the Gemara assessment and the App writer play different roles. The forge
  keeps queue and check enforcement.
- [GitLab implementation](../problems/gitlab-implementation.md) — the
  missing-risk negative case above is the only GitLab result. It does not
  change that document.
- [Repo readiness](../problems/repo-readiness.md) — related, but outside this
  experiment's selected-control question. The lab did not scan repository
  readiness.
- [Landscape](../landscape.md#policy-and-assurance-artifacts) — where Gemara
  and the OpenSSF Baseline sit among adjacent tools.

## Open questions

- Would a per-agent-version cohort with independent human labels change the
  quality signal enough to justify using it as a gate?
- What window, sample minimum, and threshold would an owner need to justify a
  quality condition, and who reviews those choices?
- Does Gemara's artifact standardization pay for itself compared with a
  native Fullsend structure such as the
  [trust scorecard](../problems/trustworthiness-evidence.md#a-concrete-artifact-the-trust-scorecard)?
- How should revocation work when a historical signal degrades below
  threshold after enrollment but before the queue merges?
- Which parts of the trust-boundary integration (pinning, trusted-base
  execution, queue revalidation) would Fullsend need regardless of the policy
  format?

## Public source register

Sources were checked on 2026-10-24. Source-code links are immutable
snapshots. Issue, PR, and run pages describe historical observations and can
change independently. GitHub Actions artifact downloads may require
authentication and expire. An artifact link is a locator, not a permanent
evidence archive.

**Upstream building blocks**

- [Gemara schemas at the experiment pin](https://github.com/gemaraproj/gemara/tree/9d36c253484d14922010252bfffe58bdcd49a144)
- [go-gemara SDK at the experiment pin](https://github.com/gemaraproj/go-gemara/tree/5e33fda460a80ffa99e9efbe19afb43e7d4c4dd2)
- [OpenSSF OSPS-QA-03.01](https://github.com/ossf/security-baseline/blob/a26a7963f6fd098c1e82829322ec9e1196a4f294/baseline/OSPS-QA.yaml#L84-L101)
  (release `v2026.08.28`)

**Shared policy module** (pinned at `61a01465`)

- [Overview](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/README.md),
  [adoption and ownership boundary](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/docs/ADOPTION.md),
  [limitations and future work](https://github.com/ascerra/fullsend-gemara-integration/blob/61a01465478e50d8e9b3e21dd056fdef90baff30/docs/ROADMAP.md)
- Policy, evaluation, binding, and quality-check sources are linked from
  findings 2 and 4.

**Consumer implementation** (pinned at `4c86073e`)

- [Consumer configuration](https://github.com/fullsend-ai/auto-merge/blob/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71/.fullsend/config.yaml)
- Evaluator, OpenSSF, trust-boundary, and workflow sources are linked from
  findings 1, 6, 8, and 9, and from the inventory table.

**Hosted evidence**

- 99% hold: [PR #53](https://github.com/fullsend-ai/auto-merge/pull/53)
  (finding 4).
- Merged demonstrations: [PR #56](https://github.com/fullsend-ai/auto-merge/pull/56)
  and [PR #58](https://github.com/fullsend-ai/auto-merge/pull/58)
  (findings 5 and 7). [PR #54](https://github.com/fullsend-ai/auto-merge/pull/54)
  is unmerged.
- Revocation: [commit 4c86073e](https://github.com/fullsend-ai/auto-merge/commit/4c86073ea6e05aff69b22b261eb7e2c74f3a5f71)
  (finding 9).

**Related Fullsend work (not adoption evidence)**

- [#7911](https://github.com/fullsend-ai/fullsend/issues/7911): configurable
  auto-merge and deterministic-rule design.
- [#5808](https://github.com/fullsend-ai/fullsend/issues/5808): existing
  `defaults.auto_merge` parsed but not consumed;
  [#5849](https://github.com/fullsend-ai/fullsend/issues/5849): merge-queue
  integration gap; [#6892](https://github.com/fullsend-ai/fullsend/issues/6892):
  revert and defect visibility.
- [PR #7151](https://github.com/fullsend-ai/fullsend/pull/7151): dedicated
  auto-merge authority boundary. **Open**, not an accepted decision.
- [PR #8467](https://github.com/fullsend-ai/fullsend/pull/8467): earlier
  repo-restricted writer prototype. **Closed without merge.** The successful
  lab writer lives in the auto-merge repository, not in Fullsend.
