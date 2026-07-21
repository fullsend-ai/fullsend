# Review Autonomy Evidence

What empirical evidence exists for and against granting autonomous merge authority to review agents, and how should that evidence inform autonomy policy decisions?

**Related:**
- [autonomy-spectrum.md](autonomy-spectrum.md) -- the binary per-repo autonomy model and graduation criteria
- [trustworthiness-evidence.md](trustworthiness-evidence.md) -- the structured portfolio model for composing trust signals
- [code-review.md](code-review.md) -- review sub-agent decomposition and the confidence problem
- [human-factors.md](human-factors.md) -- how human oversight effectiveness changes under automation

## The problem

The [autonomy spectrum](autonomy-spectrum.md) defines a graduation model: repos move from human-reviewed to autonomous once they meet readiness criteria. The [trustworthiness evidence](trustworthiness-evidence.md) framework defines *types* of evidence (configuration health, behavioral evaluation, track record) but not a concrete corpus of observations from real PRs.

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

#### Issue #5251: telemetry refactor counter-evidence

**Tracking issue:** [#5251](https://github.com/fullsend-ai/fullsend/issues/5251)

Referenced as additional counter-evidence for review autonomy. See issue for detailed analysis.

### Positive evidence (agent review sufficient)

The following PRs provide positive evidence that the review agent can match human review for certain change types:

- **[#4852](https://github.com/fullsend-ai/fullsend/issues/4852)** -- positive evidence on simpler changes
- **[#4532](https://github.com/fullsend-ai/fullsend/issues/4532)** -- positive evidence on simpler changes
- **[#4995](https://github.com/fullsend-ai/fullsend/issues/4995)** -- positive evidence

These PRs demonstrate that for simpler, more mechanical changes the review agent's findings align well with human review.

## Patterns emerging from the evidence

### Change types where agents underperform

Based on the counter-evidence, the review agent struggles with:

1. **Regex and string-processing logic** -- agents detect patterns but fail to reason about concrete impact (e.g., what inputs cause data loss)
2. **Semver and version comparison** -- domain-specific edge cases (partial version refs, prerelease ordering) require specialized knowledge the agent does not reliably apply
3. **Cross-document consistency** -- verifying that ADRs, design docs, and implementation agree requires holistic project understanding
4. **Security-relevant operational reasoning** -- understanding how a code change interacts with deployment constraints (SHA pinning, Actions enforcement) is beyond current agent capabilities

### Change types where agents perform well

Based on the positive evidence, the review agent reliably handles:

1. **Mechanical consistency checks** -- formatting, documentation structure, forge abstraction compliance
2. **Low-severity edge case identification** -- exhaustive enumeration of minor edge cases that humans may overlook
3. **Security surface analysis** -- identifying potential attack vectors and security-relevant code paths
4. **Convention adherence** -- verifying code follows established repo patterns

## Relationship to the protected-path mechanism

The existing protected-path downgrade in [`post-review.sh`](../../internal/scaffold/fullsend-repo/scripts/post-review.sh) prevents autonomous approval for PRs touching 18 sensitive file paths. This is a path-based autonomy gate.

The evidence in this document suggests a complementary **change-type-based** autonomy gate: when the diff introduces or modifies regex patterns, semver comparison logic, or complex string-processing functions, downgrade approval to comment and require human review -- regardless of which paths are touched.

Implementing such a gate would require:

1. Diff analysis to detect the relevant change types (regex construction, semver parsing, string manipulation patterns)
2. Integration into the `post-review.sh` downgrade logic, parallel to the existing protected-path check
3. A mechanism for periodic reassessment as agent capabilities improve

This is not proposed as an immediate implementation -- the evidence corpus is still small. As more observations accumulate, the case for or against a change-type gate will strengthen.

## Open questions

- How many observations constitute a statistically meaningful sample for a given change type? One complex PR is an anecdote; the threshold for policy action is undefined.
- Should change-type gates be repo-specific (configured per-repo like protected paths) or global (applied across all fullsend-managed repos)?
- Can agent improvements (better impact reasoning, domain-specific skills) close the gaps identified here? After improvements from [#4680](https://github.com/fullsend-ai/fullsend/issues/4680) (stacked PR scoping) and impact-reasoning enhancements are deployed, the evidence should be reassessed.
- How should the evidence corpus interact with the [trustworthiness evidence](trustworthiness-evidence.md) portfolio model? Is review delta tracking a sixth evidence type, or a specialization of historical track record?
- What is the false negative rate for positive evidence? When an agent "matches" human review, how do we know the human did not also miss something?
