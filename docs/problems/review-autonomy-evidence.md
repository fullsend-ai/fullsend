# Review Autonomy Evidence

What empirical evidence exists for and against granting autonomous merge authority to review agents, and how should that evidence inform autonomy policy decisions?

**Related:**
- [autonomy-spectrum.md](autonomy-spectrum.md) -- the binary per-repo autonomy model and graduation criteria
- [trustworthiness-evidence.md](trustworthiness-evidence.md) -- the structured portfolio model for composing trust signals
- [code-review.md](code-review.md) -- review sub-agent decomposition and the confidence problem
- [human-factors.md](human-factors.md) -- how human oversight effectiveness changes under automation
- [adaptive-agent-selection.md](adaptive-agent-selection.md) -- fitness-function design for runtime and model configuration choices
- [testing-agents.md](testing-agents.md) -- behavioral monitoring after instruction, model, or runtime changes

## The problem

The [autonomy spectrum](autonomy-spectrum.md) defines a graduation model: repos move from human-reviewed to autonomous once they meet readiness criteria. The [trustworthiness evidence](trustworthiness-evidence.md) framework defines *types* of evidence (including configuration health, behavioral evaluation, track record) but not a concrete corpus of observations from real PRs.

Without tracking specific evidence from real review outcomes, autonomy decisions rely on intuition rather than data. This document collects empirical observations from real PRs: agent-vs-human comparisons classified as evidence for or against autonomous review for specific change types, and same-diff comparisons of two review configurations that inform adaptive selection.

The evidence here informs two questions:

1. **Where is the review agent already sufficient?** Change types where agent review consistently matches human review are candidates for reduced oversight.
2. **Where does the review agent fall short?** Change types where humans consistently find issues the agent misses require continued human review, regardless of other autonomy signals.

A third, distinct question is whether a *change to the review configuration itself* (runtime, model, or sub-agent routing) moved quality. That comparison is two review configurations on the same diff, not agent vs. human, and belongs in its own subsection so it is not mistaken for autonomy-graduation evidence. It is intended as a version-tagged input to [adaptive agent selection](adaptive-agent-selection.md#fitness-function-design).

## Evidence corpus

Each agent-vs-human entry records a PR where agent and human review can be compared, the findings delta, and what the observation implies for autonomy policy. Same-diff comparisons of two review *configurations* (runtime, model, or sub-agent routing) are recorded separately; they inform adaptive selection, not autonomy graduation.

### Counter-evidence (agent review insufficient)

#### PR #4080: semver/regex Go implementation (2,379 additions, 11 files)

**PR:** [feat(repos): add upgrade and upgrade-mint subcommands](https://github.com/fullsend-ai/fullsend/pull/4080)
**Change type:** New Go CLI feature with semver comparison, regex-based YAML rewriting, floating-ref detection, and concurrent upgrade orchestration.
**Tracking issue:** [#5266](https://github.com/fullsend-ai/fullsend/issues/5266)

**Human reviewer found all 5 medium+ severity issues** that led to code changes:

| # | Finding | Severity | Agent status |
|---|---------|----------|--------------|
| 1 | Partial version refs (`v1.2`) bypass both floating-ref and downgrade checks | Medium | Missed |
| 2 | `replaceShimRef` regex `\s*` matches newlines, deleting standalone comment lines (data loss) | High | Found but rated Low/"benign" |
| 3 | ADR scope mismatch ("Upgrade" vs "Verifies" for mint) | Medium | Missed |
| 4 | Plan/implementation divergence (stale spec items) | Medium | Missed |
| 5 | Undocumented glob matching (mass-match risk for destructive commands) | Medium | Missed |

The human also identified SHA-pinned refs being rewritten to floating tags (security downgrade), acknowledged as a known limitation and documented.

**Agent performance:**
- Review bot found the regex issue (#2) but miscalibrated severity (Low instead of High)
- 37.5% false positive rate (6 of 16 inline comments), partly from stacked PR confusion
- Bot's unique true positives were all low-severity edge cases: single-word comment capture, `parseUint` overflow guard, build metadata in prerelease group, scope mismatch between regex patterns
- qodo-code-review bot found 2 genuine bugs (hardcoded `--direct` flag, prerelease downgrades allowed) with 100% true positive rate

**Root cause analysis (human advantage):**
1. **Concrete impact reasoning** -- the human constructed a YAML snippet demonstrating the regex deletes comment lines; the bot identified the same pattern but did not reason about its concrete impact
2. **Domain-specific edge cases** -- the human recognized `v1.2` (partial version tag) as a category that bypasses both floating-ref and downgrade checks
3. **Cross-document consistency** -- the human caught ADR/plan divergence from implementation, requiring familiarity with project documentation standards
4. **Security-relevant operational assessment** -- the human identified SHA-pin rewriting as a security downgrade, requiring understanding of how GitHub Actions pinning interacts with `sha_pinning_required` enforcement

**Implication:** For complex Go code involving regex patterns, semver parsing, and string-processing logic, the review agent cannot yet substitute for human review. The gap is in concrete impact reasoning and domain edge-case recognition, not in pattern detection.

**Confidence:** High for the observation; medium for generalization. This is one PR, albeit representative of complex Go CLI code in this repo.

#### PR #4079: repos sync/diff feature implementation (16 agent runs, 7 days)

**PR:** [feat(repos): add repos diff and repos sync CLI commands](https://github.com/fullsend-ai/fullsend/pull/4079)
**Change type:** New Go CLI feature implementing `repos diff` and `repos sync` subcommands with plan-spec requirements, JSON output, dry-run mode, and per-repo installation guard logic.
**Tracking issue:** [#5268](https://github.com/fullsend-ai/fullsend/issues/5268)
**Agent runs:** 16 iterations over 7 days ([29125182390](https://github.com/fullsend-ai/.fullsend/actions/runs/29125182390) through [29611294505](https://github.com/fullsend-ai/.fullsend/actions/runs/29611294505))

The review agent caught legitimate code-level bugs in early rounds: glob config resolution using `ResolveConfig()` instead of `ResolveConfigForEntry()` (HIGH), duplicate secret writes in `applyChanges` (MEDIUM), and misleading diff output for existing secrets (MEDIUM). These were fixed by the author in the first iteration cycle.

**The human reviewer found the following 6 representative high-impact findings** the agent missed (selected from 11 total distinct findings spanning 1 CRITICAL, 3 HIGH, 7 MEDIUM; selection basis: findings that led to code changes or revealed structural capability gaps):

| # | Finding | Severity | Category | Agent status |
|---|---------|----------|----------|--------------|
| 1 | Guard variable `FULLSEND_PER_REPO_INSTALL` not reconciled per the plan spec | Critical | Spec compliance | Missed in runs 1-9 (fixed after the human's review) |
| 2 | The fix for finding #1 introduced a regression that would brick future `repos install` | High | Fix regression | Approved the broken fix |
| 3 | `checkPerRepoScopes` reintroduced `--json` output pollution | High | Output correctness | Missed in runs 1-9 (the function was introduced during early iterations) |
| 4 | Missing test coverage for the `--json` purity fix | Medium | Test adequacy | Missed until addressed mid-sequence (after the human's review) |
| 5 | Schema inconsistency between `--dry-run` and normal JSON output (`DiffResult` vs `SyncResult`) | Medium | API contract | Missed in all 16 runs |
| 6 | Stale PR description coverage claims citing nonexistent functions | Medium | Documentation | Missed until addressed mid-sequence (after the human's review) |

**Agent performance (where it succeeded):**
- Found glob config resolution bug (`ResolveConfig()` vs `ResolveConfigForEntry()`) -- HIGH
- Found duplicate secret writes in `applyChanges` -- MEDIUM
- Found misleading diff output for existing secrets -- MEDIUM
- Persistent low-severity vigilance across iterations

**Root cause analysis (human advantage):**
1. **Spec cross-referencing** -- the human compared the implementation against the plan document to verify all requirements were met. In the 9 runs before the human's review, the agent never consulted the plan spec, missing the `FULLSEND_PER_REPO_INSTALL` reconciliation requirement entirely. A later run (after the fix had already landed) did surface a related plan/code mismatch at LOW severity, recommending the spec be updated rather than recognizing the gap had already been resolved -- itself a data point about miscalibrated severity and direction.
2. **Fix regression detection** -- the human evaluated whether the fix for the CRITICAL finding introduced new problems. The agent's reviews around the fix never flagged the regression, despite multiple runs after the fix landed.
3. **Output contract analysis** -- the human identified that `checkPerRepoScopes` wrote to stdout, polluting the `--json` output. This requires understanding the implicit contract that JSON-mode commands must not emit non-JSON to stdout.
4. **Test adequacy assessment** -- the human identified that the `--json` code path had zero test coverage. The agent never flagged the absence of tests for newly added features.
5. **API schema consistency** -- the human noticed that `--dry-run` returned a `DiffResult` while normal mode returned a `SyncResult`, creating an inconsistent API for consumers of the same `--json` flag.

**Implication:** For feature PRs implementing a plan or spec with multiple requirements, the review agent cannot yet replace human review for: spec-compliance validation, fix regression detection, API contract consistency, and test-adequacy assessment for new features. The agent's code-level correctness capabilities (wrong function call, duplicate API call, misleading output) remain valuable as a first-pass filter.

**Confidence:** High for the observation; low-to-medium for generalization given N=1. In the 9 runs before the human review, the agent never approached any of the 6 tabulated findings; a later run did surface a related plan/code mismatch (finding #1's subject matter) at LOW severity, but recommended the spec be changed rather than recognizing the issue was already resolved by the fix. This is consistent with prior counter-evidence ([#5266](https://github.com/fullsend-ai/fullsend/issues/5266), [#5251](https://github.com/fullsend-ai/fullsend/issues/5251)).

**Companion improvement proposals:**
- [agents#269](https://github.com/fullsend-ai/agents/issues/269) -- Review agent should read plan/spec docs linked from PR descriptions
- [agents#270](https://github.com/fullsend-ai/agents/issues/270) -- Review agent re-review should perform side-effect analysis on fix commits

#### Issue #5251: telemetry refactor counter-evidence

**Tracking issue:** [#5251](https://github.com/fullsend-ai/fullsend/issues/5251)
**PR:** [#4510](https://github.com/fullsend-ai/fullsend/pull/4510) -- 22-file refactor replacing bespoke telemetry with OTel Go SDK (+1,635/-2,851 lines, breaking change)

The human reviewer found 12 unique findings the agent missed, including multiple HIGH-severity correctness bugs. The gap was acute for large architectural refactors that delete and replace entire subsystems.

See issue for detailed analysis.

### Positive evidence (agent review sufficient)

The following PRs provide positive evidence that the review agent can match or exceed human review for certain change types:

- **[#4852](https://github.com/fullsend-ai/fullsend/issues/4852)** (PR #4102) -- dependency-bump + CI-workflow PR (+3 agent findings: 1 High protected-path, 2 Low consistency; human reviewer approved with zero additional findings). Agent fully covered human assessment.
- **[#4532](https://github.com/fullsend-ai/fullsend/issues/4532)** (PR #2986) -- test-only PR (+1 Medium scope-creep, 1 Medium implementation-coherence (informational), 1 Low comment accuracy from agent; human approved 10 days later with zero findings). Agent was strictly more thorough; human review added zero incremental quality signal.
- **[#4995](https://github.com/fullsend-ai/fullsend/issues/4995)** -- cross-repo data point (`konflux-ci/architecture#368`); the review agent outperformed three human reviewers on mechanical-consistency checks

These PRs demonstrate that for simpler, more mechanical changes the review agent's findings align well with -- and in some cases exceed -- human review.

### Runtime and configuration comparison evidence

These entries compare two review configurations on the same diff. They do not speak to agent-vs-human sufficiency and must not be folded into the counter-evidence or positive-evidence tallies above.

#### PR #6935: token-fallback regression test, re-reviewed after a runtime switch

**PR:** [fix(#6932): move seenResult flag after successful unmarshal](https://github.com/fullsend-ai/fullsend/pull/6935)
**Change type:** One-line production fix in `parseClaudeStream` plus a new regression test (`TestParseClaudeStreamMalformedResultFallsBackToTokensEvent`).
**Tracking issue:** [#7307](https://github.com/fullsend-ai/fullsend/issues/7307)
**Diff identity:** The second review's risk-assessment comment confirmed the PR's own files were byte-identical to the prior head SHA; the only intervening change was an unrelated merge commit from main.

| Review | Date | Runtime / model | Effort / cost | Verdict |
|--------|------|-----------------|---------------|---------|
| [33688286645](https://github.com/fullsend-ai/fullsend/actions/runs/33688286645) | 2026-09-02 | claude / opus (`claude-opus-4-6`) | high / $2.50 | Looks good to me |
| [34839926769](https://github.com/fullsend-ai/fullsend/actions/runs/34839926769) | 2026-09-14 | pi / sonnet (`claude-sonnet-5`) with sub-agent decomposition | high / $2.00 | Medium test-adequacy finding |

**Finding the first review missed:** as originally reviewed in PR #6935 (before a follow-up commit corrected it), the new test's fixture summed to `3000+1600+400+100=5100`, which is at or above `tokenThreshold` (5000, `internal/runtime/claude_progress.go`). An incremental `TokensEvent` already satisfied the test's assertions, so the test would still pass if the #6932 bug (`seenResult = true` before a successful unmarshal) were reintroduced. The second review spelled out that arithmetic and the `total > lastEmittedTotal` EOF-fallback condition. A same-branch follow-up commit (`test(#6932): fix fixture so regression test actually exercises deferred TokensEvent`) fixed the hole before merge, so the fixture on `main` today sums to `1000+300+200+50=1550`, kept below `tokenThreshold` so the only `TokensEvent` observed is the deferred one.

**Intervening configuration change:** [PR #7116](https://github.com/fullsend-ai/fullsend/pull/7116) (commit `80dcbef`, merged 2026-09-09) routed `review` (and `code`, `prioritize`) onto the `pi` runtime in `.fullsend/config.yaml`: orchestrator on `sonnet`, correctness/security/challenger on `xai/grok-4.6`, docs-currency/style-conventions on `google-vertex/gemini-3.8-flash`. `fix`/`triage`/`retro` stayed on `claude`/`sonnet`. #7116's merge gates cited local cost-parity and persona-resolution checks; they did not include a review-quality delta against the pre-switch baseline.

**Implication:** A load-bearing config change (which runtime and model review every PR in this repo) shipped without a committed before/after quality measurement -- the gap [adaptive agent selection](adaptive-agent-selection.md) and this corpus are meant to close. The observation itself is that the post-#7116 review caught a real test-adequacy hole the pre-#7116 review missed on a byte-identical diff.

**Confidence:** High for the observation (byte-identical diff plus reviewed arithmetic). Low for generalizing to "pi is better than claude for test-adequacy." N=1 cannot distinguish a genuine runtime/sub-agent effect from ordinary run-to-run verdict variance ([fullsend-ai/agents#1140](https://github.com/fullsend-ai/agents/issues/1140)) or from the second review simply getting a free second look.

#### Re-evaluation checkpoint for PR #7116

Trigger (whichever comes first):

- [ ] 20 subsequent `review` runs on `fullsend-ai/fullsend` whose diffs include a test-adequacy-relevant change (new or modified tests)
- [ ] 2026-10-15 (30 days after this entry)

When the trigger fires, report whether pi-runtime reviews on this repo show a measurably different test-adequacy catch/miss rate than the pre-#7116 (claude/opus) baseline, and record that result here as a version-tagged data point for [adaptive agent selection](adaptive-agent-selection.md#fitness-function-design). Do not treat the #6935 pair as that measurement.

## Patterns emerging from the evidence

### Change types where agents underperform

Based on the counter-evidence, the review agent struggles with:

1. **Spec-compliance validation** -- verifying that an implementation fulfills all requirements in a plan or spec document. The agent reviews the code in isolation, without cross-referencing the authorizing spec.
2. **Fix regression detection** -- evaluating whether a fix introduces new problems. The agent approves fixes based on whether they address the original finding, without analyzing second-order effects.
3. **API contract consistency** -- identifying schema inconsistencies, output pollution, and contract violations that require understanding implicit API guarantees.
4. **Test-adequacy assessment** -- recognizing when newly added features lack test coverage. The agent checks existing tests but does not flag the absence of tests for new code paths.
5. **Regex and string-processing logic** -- agents detect patterns but fail to reason about concrete impact (e.g., what inputs cause data loss).
6. **Semver and version comparison** -- domain-specific edge cases (partial version refs, prerelease ordering) require specialized knowledge the agent does not reliably apply.
7. **Cross-document consistency** -- verifying that ADRs, design docs, plan specs, and implementation agree requires holistic project understanding.
8. **Security-relevant operational reasoning** -- understanding how a code change interacts with deployment constraints (SHA pinning, Actions enforcement) is beyond current agent capabilities.

### Change types where agents perform well

Based on the positive evidence and the successful findings within counter-evidence PRs, the review agent reliably handles:

1. **Code-level correctness bugs** -- wrong function calls, duplicate API calls, misleading output
2. **Mechanical consistency checks** -- formatting, documentation structure, forge abstraction compliance
3. **Low-severity edge case identification** -- exhaustive enumeration of minor edge cases that humans may overlook
4. **Security surface analysis** -- identifying potential attack vectors and security-relevant code paths
5. **Convention adherence** -- verifying code follows established repo patterns
6. **Persistent vigilance** -- maintaining attention across many iterations with sustained low-severity identification

### Emerging pattern: surface vs. depth

A consistent pattern across all three counter-evidence PRs (#4079, #4080, #4510): the agent excels at surface-level code correctness (wrong function call, duplicate operation, naming error) but is blind to depth-level correctness (does this fulfill the spec? does this fix break something else? is this API contract consistent?). The human reviewer's advantage is cross-referencing the implementation against external context -- plan documents, API contracts, test coverage expectations, and second-order effects of changes.

This pattern is consistent across different change types (new CLI features, complex regex/semver logic, large refactors). Note: all three counter-evidence observations come from the same human reviewer using a multi-model review squad (varying model combinations across rounds -- e.g., Claude x2 + Gemini for some rounds, Claude x2 + Grok for others) with manual verification, so the comparison is between a single automated review pipeline and a human-curated multi-model process rather than agent vs. unaided human review. The consistency across change types increases confidence in a genuine capability gap, but the reviewer-specific-variation confound cannot be ruled out until observations from additional reviewers are added.

## Relationship to the protected-path mechanism

The existing protected-path downgrade in `post-review.sh` (now in [`fullsend-ai/agents`](https://github.com/fullsend-ai/agents)) prevents autonomous approval for PRs touching sensitive file paths. This is a path-based autonomy gate.

The evidence in this document suggests two complementary gate types:

1. **Change-type-based gate:** When the diff introduces or modifies regex patterns, semver comparison logic, or complex string-processing functions, downgrade approval to comment and require human review -- regardless of which paths are touched.

2. **Spec-reference gate:** When the PR description references a plan document or spec, require human review for spec-compliance validation. This would target the most critical gap identified in the PR #4079 evidence, though its effectiveness is unmeasured pending implementation.

Implementing such gates would require:

1. Diff analysis to detect the relevant change types (regex construction, semver parsing, string manipulation patterns)
2. Integration into the `post-review.sh` downgrade logic, parallel to the existing protected-path check
3. A mechanism for periodic reassessment as agent capabilities improve

These are not proposed as immediate implementations -- the agent-vs-human evidence corpus is still small (3 counter-evidence, 3 positive-evidence data points). Runtime-comparison evidence is a separate tally (1 data point, N=1) and is not evidence for or against autonomous merge. As more observations accumulate, the case for or against additional gates will strengthen.

## Open questions

- How many observations constitute a statistically meaningful sample for a given change type? Three counter-evidence PRs show a consistent pattern, but the threshold for policy action is undefined.
- Can spec cross-referencing (agents#269) and fix side-effect analysis (agents#270) close the most critical gaps? After these are implemented, the review agent should be re-run against PR #4079's diff to measure improvement.
- Can agent improvements (better impact reasoning, domain-specific skills) close the gaps identified here? After improvements from [#4680](https://github.com/fullsend-ai/fullsend/issues/4680) (stacked PR scoping) and impact-reasoning enhancements are deployed, the evidence should be reassessed.
- Should change-type gates be repo-specific (configured per-repo like protected paths) or global (applied across all fullsend-managed repos)?
- How should the evidence corpus interact with the [trustworthiness evidence](trustworthiness-evidence.md) portfolio model? Is review delta tracking a sixth evidence type, or a specialization of historical track record?
- What is the false negative rate for positive evidence? When an agent "matches" human review, how do we know the human did not also miss something?
- Does the agent's stale-finding repetition (known issue tracked by [#1013](https://github.com/fullsend-ai/fullsend/issues/1013), [#2959](https://github.com/fullsend-ai/fullsend/issues/2959), [#5007](https://github.com/fullsend-ai/fullsend/issues/5007), [#5265](https://github.com/fullsend-ai/fullsend/issues/5265)) contribute to the depth gap by consuming context window with repeated low-value findings?
- Did the #7116 runtime switch change the false-negative rate on test-adequacy, or is the #6935 pair ordinary re-run variance? The [re-evaluation checkpoint](#re-evaluation-checkpoint-for-pr-7116) defines the sample (20 test-adequacy-relevant review runs or 2026-10-15) after which a catch-rate delta should be recorded as a version-tagged input to [adaptive agent selection](adaptive-agent-selection.md#fitness-function-design).
