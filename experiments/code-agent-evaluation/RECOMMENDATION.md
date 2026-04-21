# Code Agent Recommendation — Best Configuration for PR #189

**Date:** April 14, 2026 (updated after Round 4)
**Based on:** [EXPERIMENT.md](EXPERIMENT.md) — 490+ trials across 20 scenarios, 8 variants, 4 rounds
**Target:** [PR #189: Add code agent definition and skill](https://github.com/fullsend-ai/fullsend/pull/189)
**Review feedback:** [ralphbean's preliminary review](https://github.com/fullsend-ai/fullsend/pull/189#issuecomment-1)

---

## TL;DR

The [PR #189](https://github.com/fullsend-ai/fullsend/pull/189) architecture is
empirically validated. All priority-1 improvements have been implemented as
**V8 hybrid** — a cleaned-up V1 that integrates V5's minimal-diff discipline
and V7's reproduction/task-type handling. V8 scores **4.08/5.00** on synthetic
tasks and **4.15/5.00** on real-world tasks while being **37% smaller** than V1.
V8 is the configuration in PR #189's latest commit
([`d70dcff`](https://github.com/fullsend-ai/fullsend/commit/d70dcff)). No
further redesign is needed.

---

## What the experiment proved


| Finding                                                  | Evidence                                                               | Status in PR #189                                                                   |
| -------------------------------------------------------- | ---------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| Structured agent + skill >> unstructured                 | V1 (4.61) vs V3 (3.62) = +28%                                          | **Validated** — architecture unchanged                                              |
| Single skill > multi-skill                               | V1 (4.61) > V2 (4.57); V2 is 21% slower                                | **Validated** — single skill retained                                               |
| Avoid rigid platform hardcoding                          | V5 (4.64) > V6 (4.48); V6's rigidity hurt                              | **Noted** — V8 uses `gh` CLI (GitHub-only MVP); generalize if multi-platform needed |
| 100% security posture (structured agents)                | 0 injection executions, 0 secret stagings, 0 protected path violations | **Validated** — all security constraints retained                                   |
| "Understand before you act" helps on hard tasks          | V7 wins 10/20 scenarios, +0.40 on already-fixed, +0.29 on test-only    | **Applied** — V8 skill steps 6–7                                                    |
| Minimal diff principle improves scope discipline         | V5 leads scope-discipline (5.00 vs V1's 4.26)                          | **Applied** — V8 agent constraints                                                  |
| Deduplication reduces token waste and contradiction risk | V1 had 216 redundant lines across agent+skill                          | **Applied** — V8 is 37% smaller                                                     |
| Real-world results generalize                            | V8 scored 4.15 on production Tekton/K8s repos                          | **Validated** — strongest real-world score                                          |


For detailed scores by round and scenario, see
[EXPERIMENT.md sections 5–8](EXPERIMENT.md#5-round-1-results-all-variants).

---

## Changes applied to PR #189

All priority-1 changes from the initial recommendation have been implemented
in the V8 hybrid variant, validated with 66 trials
([Round 4](EXPERIMENT.md#8-round-4-results-v8-hybrid-validation)), and
committed as [`d70dcff`](https://github.com/fullsend-ai/fullsend/commit/d70dcff).


| Change                             | Source         | Where in V8                |
| ---------------------------------- | -------------- | -------------------------- |
| Add "verify bug reproduction" step | V7             | Skill step 7               |
| Remove constraint duplication      | ralph's review | Agent -37%, skill -10%     |
| Add minimal-diff constraint        | V5             | Agent constraints section  |
| Add task-type identification       | V7             | Skill step 6               |
| Add self-review before staging     | V5             | Skill step 9c              |
| Add ambiguity handling guidance    | V7             | Skill step 8               |
| Three-question framing in identity | V7             | Agent identity section     |
| Fix `cat`/`awk` inconsistencies    | ralph's review | Skill convention discovery |
| Frame commit format as fallback    | ralph's review | Skill commit step          |


---

## What NOT to change

The experiment identifies anti-patterns — things that were tested and proven
worse. Do not revisit these without new data:

- **Do not split the skill into multiple pieces** — V2 scored lower and was 21% slower
- **Do not hardcode GitHub-specific commands** — V6 scored lower due to rigidity
- **Do not remove structure** — V3 (raw prompt) scored 28% worse with no safety guarantees

Additionally, do NOT weaken (all retained in V8):

- The anti-injection wording in `agents/code.md` (V1 has the strongest
injection resistance at 4.60 across all variants)
- The `disallowedTools` list (100% safety gate pass rate validates every entry)
- The `scan-secrets` script requirement (S15 secret-staging trap caught by all
structured variants)
- The explicit file staging rule (no `git add -A` — prevents credential leaks)

---

## Post-merge priorities

### Priority 2: Near-term iteration


| Improvement                                     | Evidence                                       | Impact                                                        |
| ----------------------------------------------- | ---------------------------------------------- | ------------------------------------------------------------- |
| Enforce label-gate in pre-script (not agent)    | gate-test scores <2.2 for all agents           | Deterministic gate — removes agent judgment from label checks |
| Strengthen already-fixed detection              | already-fixed scores ~2.5–2.7 for all variants | Reduces wasted work on resolved issues                        |
| Add scripts/ directory for agent helper scripts | ralph's review                                 | Extensibility for future agent tooling                        |


### Priority 3: Future experiments


| Action                                                   | Rationale                                                    |
| -------------------------------------------------------- | ------------------------------------------------------------ |
| Run ablation study                                       | Prove each security layer adds value individually            |
| Run dedicated red team (10 payloads × targeted variants) | Stress-test injection resistance beyond 5 embedded scenarios |
| Test on larger real-world repos                          | Validate scaling beyond 50-file repos                        |
| Test with different foundation models                    | Results may not generalize beyond Claude Opus                |


---

## Cross-references

- **Full experiment:** [EXPERIMENT.md](EXPERIMENT.md)
- **V8 variant (PR #189 latest):** [variants/V8-hybrid/](variants/V8-hybrid/)
- **All variants (V1–V7):** [variants/VARIANTS.md](variants/VARIANTS.md) — links to browse any variant
- **PR #189:** [Add code agent definition and skill](https://github.com/fullsend-ai/fullsend/pull/189)
- **Story 4:** [Code Agent (#127)](https://github.com/fullsend-ai/fullsend/issues/127)
