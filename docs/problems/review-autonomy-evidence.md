# Review Autonomy Evidence

What empirical evidence exists for and against granting autonomous merge authority to review agents, and how should that evidence inform autonomy policy decisions?

**Related:**
- [autonomy-spectrum.md](autonomy-spectrum.md) -- the binary per-repo autonomy model and graduation criteria
- [trustworthiness-evidence.md](trustworthiness-evidence.md) -- the structured portfolio model for composing trust signals
- [code-review.md](code-review.md) -- review sub-agent decomposition and the confidence problem
- [human-factors.md](human-factors.md) -- how human oversight effectiveness changes under automation

## The problem

The [autonomy spectrum](autonomy-spectrum.md) defines a graduation model: repos move from human-reviewed to autonomous once they meet readiness criteria. The [trustworthiness evidence](trustworthiness-evidence.md) framework defines *types* of evidence (including configuration health, behavioral evaluation, track record) but not a concrete corpus of observations from real PRs.

Without tracking specific evidence from real review outcomes, autonomy decisions rely on intuition rather than data. This document collects empirical observations from PRs where both agents and humans reviewed the same change, classifying each observation as evidence for or against autonomous review for specific change types.

The evidence here informs two questions:

1. **Where is the review agent already sufficient?** Change types where agent review consistently matches human review are candidates for reduced oversight.
2. **Where does the review agent fall short?** Change types where humans consistently find issues the agent misses require continued human review, regardless of other autonomy signals.

## Evidence corpus

Each entry records a PR where agent and human review can be compared, the findings delta, and what the observation implies for autonomy policy.

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
| 4 | Missing test coverage for the `--json` purity fix | Medium | Test adequacy | Missed in all 16 runs |
| 5 | Schema inconsistency between `--dry-run` and normal JSON output (`DiffResult` vs `SyncResult`) | Medium | API contract | Missed in all 16 runs |
| 6 | Stale PR description coverage claims citing nonexistent functions | Medium | Documentation | Missed in all 16 runs |

**Agent performance (where it succeeded):**
- Found glob config resolution bug (`ResolveConfig()` vs `ResolveConfigForEntry()`) -- HIGH
- Found duplicate secret writes in `applyChanges` -- MEDIUM
- Found misleading diff output for existing secrets -- MEDIUM
- Persistent low-severity vigilance across iterations

**Root cause analysis (human advantage):**
1. **Spec cross-referencing** -- the human compared the implementation against the plan document to verify all requirements were met. The agent never consulted the plan spec, missing the `FULLSEND_PER_REPO_INSTALL` reconciliation requirement entirely.
2. **Fix regression detection** -- the human evaluated whether the fix for the CRITICAL finding introduced new problems. The agent approved the broken fix immediately (run at 02:28 UTC, Jul 17) without analyzing second-order effects.
3. **Output contract analysis** -- the human identified that `checkPerRepoScopes` wrote to stdout, polluting the `--json` output. This requires understanding the implicit contract that JSON-mode commands must not emit non-JSON to stdout.
4. **Test adequacy assessment** -- the human identified that the `--json` code path had zero test coverage. The agent never flagged the absence of tests for newly added features.
5. **API schema consistency** -- the human noticed that `--dry-run` returned a `DiffResult` while normal mode returned a `SyncResult`, creating an inconsistent API for consumers of the same `--json` flag.

**Implication:** For feature PRs implementing a plan or spec with multiple requirements, the review agent cannot yet replace human review for: spec-compliance validation, fix regression detection, API contract consistency, and test-adequacy assessment for new features. The agent's code-level correctness capabilities (wrong function call, duplicate API call, misleading output) remain valuable as a first-pass filter.

**Confidence:** High for the observation; low-to-medium for generalization given N=1. The agent had 16 runs (9 before the human review) and never approached any of the 6 tabulated findings. This is consistent with prior counter-evidence ([#5266](https://github.com/fullsend-ai/fullsend/issues/5266), [#5251](https://github.com/fullsend-ai/fullsend/issues/5251)).

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

- **[#4852](https://github.com/fullsend-ai/fullsend/issues/4852)** -- positive evidence on simpler changes
- **[#4532](https://github.com/fullsend-ai/fullsend/issues/4532)** -- positive evidence on simpler changes
- **[#4995](https://github.com/fullsend-ai/fullsend/issues/4995)** -- cross-repo data point (`konflux-ci/architecture#368`); the review agent outperformed three human reviewers on mechanical-consistency checks

These PRs demonstrate that for simpler, more mechanical changes the review agent's findings align well with -- and in some cases exceed -- human review.

## Patterns emerging from the evidence

### Change types where agents underperform

Based on the counter-evidence, the review agent struggles with:

1. **Spec-compliance validation** -- verifying that an implementation fulfills all requirements in a plan or spec document. The agent reviews the code in isolation, without cross-referencing the authorizing spec.
2. **Fix regression detection** -- evaluating whether a fix introduces new problems. The agent approves fixes based on whether they address the original finding, without analyzing second-order effects.
3. **API contract consistency** -- identifying schema inconsistencies, output pollution, and contract violations that require understanding implicit API guarantees.
4. **Test-adequacy assessment** -- recognizing when newly added features lack test coverage. The agent checks existing tests but does not flag the absence of tests for new code paths.
5. **Regex and string-processing logic** -- agents detect patterns but fail to reason about concrete impact (e.g., what inputs cause data loss).
6. **Cross-document consistency** -- verifying that ADRs, design docs, plan specs, and implementation agree requires holistic project understanding.
7. **Security-relevant operational reasoning** -- understanding how a code change interacts with deployment constraints (SHA pinning, Actions enforcement) is beyond current agent capabilities.

### Change types where agents perform well

Based on the positive evidence and the successful findings within counter-evidence PRs, the review agent reliably handles:

1. **Code-level correctness bugs** -- wrong function calls, duplicate API calls, misleading output
2. **Mechanical consistency checks** -- formatting, documentation structure, forge abstraction compliance
3. **Low-severity edge case identification** -- exhaustive enumeration of minor edge cases that humans may overlook
4. **Security surface analysis** -- identifying potential attack vectors and security-relevant code paths
5. **Convention adherence** -- verifying code follows established repo patterns
6. **Persistent vigilance** -- maintaining attention across many iterations, catching regressions introduced by fixes (when the regression is at the code level)

### Emerging pattern: surface vs. depth

A consistent pattern across all three counter-evidence PRs (#4079, #4080, #4510): the agent excels at surface-level code correctness (wrong function call, duplicate operation, naming error) but is blind to depth-level correctness (does this fulfill the spec? does this fix break something else? is this API contract consistent?). The human reviewer's advantage is cross-referencing the implementation against external context -- plan documents, API contracts, test coverage expectations, and second-order effects of changes.

This pattern is consistent across different change types (new CLI features, complex regex/semver logic, large refactors). Note: all three counter-evidence observations come from the same human reviewer using a multi-model review squad (Claude x2 + Grok with manual verification), so the comparison is between a single automated review pipeline and a human-curated multi-model process rather than agent vs. unaided human review. The consistency across change types increases confidence in a genuine capability gap, but the reviewer-specific-variation confound cannot be ruled out until observations from additional reviewers are added.

## Relationship to the protected-path mechanism

The existing protected-path downgrade in [`post-review.sh`](../../internal/scaffold/fullsend-repo/scripts/post-review.sh) prevents autonomous approval for PRs touching sensitive file paths. This is a path-based autonomy gate.

The evidence in this document suggests two complementary gate types:

1. **Change-type-based gate:** When the diff introduces or modifies regex patterns, semver comparison logic, or complex string-processing functions, downgrade approval to comment and require human review -- regardless of which paths are touched.

2. **Spec-reference gate:** When the PR description references a plan document or spec, require human review for spec-compliance validation. This would target the most critical gap identified in the PR #4079 evidence, though its effectiveness is unmeasured pending implementation.

Implementing such gates would require:

1. Diff analysis to detect the relevant change types (regex construction, semver parsing, string manipulation patterns)
2. Integration into the `post-review.sh` downgrade logic, parallel to the existing protected-path check
3. A mechanism for periodic reassessment as agent capabilities improve

These are not proposed as immediate implementations -- the evidence corpus is still small (3 counter-evidence, 3 positive-evidence data points). As more observations accumulate, the case for or against additional gates will strengthen.

## Open questions

- How many observations constitute a statistically meaningful sample for a given change type? Three counter-evidence PRs show a consistent pattern, but the threshold for policy action is undefined.
- Can spec cross-referencing (agents#269) and fix side-effect analysis (agents#270) close the most critical gaps? After these are implemented, the review agent should be re-run against PR #4079's diff to measure improvement.
- Can agent improvements (better impact reasoning, domain-specific skills) close the gaps identified here? After improvements from [#4680](https://github.com/fullsend-ai/fullsend/issues/4680) (stacked PR scoping) and impact-reasoning enhancements are deployed, the evidence should be reassessed.
- Should change-type gates be repo-specific (configured per-repo like protected paths) or global (applied across all fullsend-managed repos)?
- How should the evidence corpus interact with the [trustworthiness evidence](trustworthiness-evidence.md) portfolio model? Is review delta tracking a sixth evidence type, or a specialization of historical track record?
- What is the false negative rate for positive evidence? When an agent "matches" human review, how do we know the human did not also miss something?
- Does the agent's stale-finding repetition (known issue tracked by [#1013](https://github.com/fullsend-ai/fullsend/issues/1013), [#2959](https://github.com/fullsend-ai/fullsend/issues/2959), [#5007](https://github.com/fullsend-ai/fullsend/issues/5007), [#5265](https://github.com/fullsend-ai/fullsend/issues/5265)) contribute to the depth gap by consuming context window with repeated low-value findings?
