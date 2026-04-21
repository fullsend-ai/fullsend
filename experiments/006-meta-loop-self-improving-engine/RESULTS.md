# Results: Zero-Config Autonomous Bug Fix Engine with Self-Improving Meta-Loop

Four autonomous self-corrections occurred in sequence, taking the engine from "fails on every real repo" to "produces correct PRs in a single pass." Total wall clock from first meta-loop run to first successful PR: ~90 minutes (mostly CI execution time).

## Auto-fix #1 — Scope creep in implement phase

| | |
|---|---|
| **Failed run** | [23613985882](https://github.com/ascerra/rl-bug-fix-full-send/actions/runs/23613985882) |
| **What happened** | The review agent rejected the fix because the implement agent added unrelated changes. The implement-review loop hit the escalation cap without converging. |
| **LLM diagnosis** | Read the execution JSON, saw repeated review rejections with findings about out-of-scope changes, identified that the implement prompt had no guidance about staying in scope when review feedback flagged drift. |
| **Auto-fix commit** | [`e06bd71`](https://github.com/ascerra/rl-bug-fix-full-send/commit/e06bd71) — Strengthened the implement prompt with a scope creep warning, added `_check_path_consistency()` to the review agent, improved the review prompt template. |
| **Files changed** | `engine/phases/implement.py`, `engine/phases/review.py`, `engine/config.py`, `templates/prompts/review.md` |

## Auto-fix #2 — File content truncation broke generated code

| | |
|---|---|
| **Failed run** | [23614415889](https://github.com/ascerra/rl-bug-fix-full-send/actions/runs/23614415889) |
| **What happened** | The implement agent truncated file content at 5,000 characters before sending it to the LLM. The target file exceeded 5k, so the LLM received a cut-off file and generated broken code with syntax errors. The review agent correctly rejected the broken output, but the implement agent kept receiving the same truncated input — infinite rejection loop. |
| **LLM diagnosis** | Read the review findings (syntax errors, unterminated functions), correlated with file sizes in the execution record, identified the 5k truncation limit as root cause. |
| **Auto-fix commit** | [`4e2623b`](https://github.com/ascerra/rl-bug-fix-full-send/commit/4e2623b) — Increased file content truncation from 5,000 to 50,000 characters. |
| **Files changed** | `engine/phases/implement.py` (+1 −1), `engine/phases/review.py` (+1 −1) |

## Intermediate: manual fixes

Run [23615068030](https://github.com/ascerra/rl-bug-fix-full-send/actions/runs/23615068030) succeeded partially — the engine got through all agents but the validate agent had logging issues. Two manual fixes followed: error logging fix ([`a0cc93c`](https://github.com/ascerra/rl-bug-fix-full-send/commit/a0cc93c)) and proper failure on PR creation failure ([`6236e49`](https://github.com/ascerra/rl-bug-fix-full-send/commit/6236e49)).

## Auto-fix #3 — Implement didn't commit changes before validate

| | |
|---|---|
| **Failed run** | [23616933542](https://github.com/ascerra/rl-bug-fix-full-send/actions/runs/23616933542) |
| **What happened** | The implement agent wrote file changes to the working directory but never ran `git commit`. When validate tried to push the branch and create a PR, there were no committed changes. |
| **LLM diagnosis** | Read the execution trace showing implement succeeded (files written) but validate failed (nothing to push). Identified the missing git commit step. |
| **Auto-fix commit** | [`f13e984`](https://github.com/ascerra/rl-bug-fix-full-send/commit/f13e984) — Added a git commit step after file writes succeed. |
| **Files changed** | `engine/phases/implement.py` (+19) |

## First success → PR #3

Run [23617134590](https://github.com/ascerra/rl-bug-fix-full-send/actions/runs/23617134590) @ [`f13e984`](https://github.com/ascerra/rl-bug-fix-full-send/commit/f13e984) — **SUCCESS**. The triage agent identified the root cause, the implement agent wrote a fix (unique per-image temp paths), the review agent approved it, the validate agent committed and pushed, and created [PR #3 on nonflux/build-definitions](https://github.com/nonflux/build-definitions/pull/3). This was the first real PR the engine ever produced.

## Grading the engine's PR against the real human fix

After PR #3, I had Cursor read the engine's fix (run [23617134590](https://github.com/ascerra/rl-bug-fix-full-send/actions/runs/23617134590)), then read the actual human-authored fix for the same bug — [PR #3057 on konflux-ci/build-definitions](https://github.com/konflux-ci/build-definitions/pull/3057) by zxiong, which had already been merged upstream. The goal was to compare the engine's output against what a human engineer actually shipped.

Both used the same strategy — adding a unique `image_num` to temp paths. The engine matched the human's approach and arrived at it in 2.8 minutes autonomously, with better documentation (comprehensive PR body with root cause analysis and testing plan). But the human fix was more precise: the engine dropped `:latest` from the OCI cleanup path, which the human kept consistent across all operations. The engine's self-review (0 findings) failed to catch this subtle inconsistency.

| | Human Fix ([PR #3057](https://github.com/konflux-ci/build-definitions/pull/3057)) | Engine Fix ([PR #3](https://github.com/nonflux/build-definitions/pull/3)) |
|---|---|---|
| **Grade** | A | A- |
| **Root cause** | A | A+ (detailed, precise explanation) |
| **Code quality** | A+ (perfectly consistent paths) | A- (correct strategy, but `:latest` dropped in cleanup) |
| **Scope** | A+ (minimal) | A+ (minimal) |
| **Documentation** | B+ (clear but terse) | A+ (comprehensive PR body) |
| **Speed** | C (hours to merge) | A+ (2.8 min autonomous) |
| **Review depth** | N/A | B (missed path consistency) |

This comparison led to concrete improvements, committed as [`98144ad`](https://github.com/ascerra/rl-bug-fix-full-send/commit/98144ad):

| Finding | Engine improvement |
|---|---|
| The review agent missed the `:latest` path inconsistency between creation and cleanup | Added review dimension #6: "Consistency of Paired Operations" to the review prompt. Added a deterministic `_check_path_consistency()` safety net in the review agent that regex-extracts paths from shell scripts and detects OCI tag mismatches — this would have caught the exact bug. |
| The implement agent didn't maintain exact path patterns across paired operations | Added "Consistency Requirements" section to the implement prompt — maintain path patterns across create/cleanup, follow parameter ordering conventions, verify all call sites. |

These were also added to the implementation plan and built into the engine during the final ralph loop session. This is the kind of improvement the meta-loop itself could produce if configured to review successful runs and their PRs, not just failures.

## Auto-fix #4 — Branch name collision

| | |
|---|---|
| **Failed run** | [23618209219](https://github.com/ascerra/rl-bug-fix-full-send/actions/runs/23618209219) @ [`98144ad`](https://github.com/ascerra/rl-bug-fix-full-send/commit/98144ad) |
| **What happened** | The validate agent tried to push to branch `rl/fix`, but that branch already existed from PR #3. Push failed with a conflict. |
| **LLM diagnosis** | Read the validate agent's error (push rejection), identified that branch names were hardcoded and would collide on repeat runs. |
| **Auto-fix commit** | [`1a1c56b`](https://github.com/ascerra/rl-bug-fix-full-send/commit/1a1c56b) — Generate unique branch names with UUID suffix (e.g., `rl/fix-1-3f3b380e`). |
| **Files changed** | `engine/phases/validate.py` (+13 −4) |

## Second success → PR #4

[Run #26 (23618411249)](https://github.com/ascerra/rl-bug-fix-full-send/actions/runs/23618411249) @ [`1a1c56b`](https://github.com/ascerra/rl-bug-fix-full-send/commit/1a1c56b) — **SUCCESS** in 6 minutes. Produced [PR #4](https://github.com/nonflux/build-definitions/pull/4) on branch `rl/fix-1-3f3b380e`.

## Analysis

### Each auto-fix addressed a genuinely different category of bug

| Auto-fix | Category | What the LLM identified |
|----------|----------|----------------------|
| #1 | Prompt design | The implement prompt needed scope constraints when the review agent flags drift |
| #2 | Context window | 5k char file limit was too small for real-world files |
| #3 | Missing workflow step | The implement agent wrote files but never committed them |
| #4 | State management | Hardcoded branch names collide on repeated runs |

This diversity matters. The LLM wasn't applying the same fix pattern repeatedly — it diagnosed structurally different problems from the execution traces.

### The LLM had real signal to work with

The engine produces structured execution artifacts (JSON with phase results, review findings, error traces, iteration counts). The LLM received ~350k characters of context per diagnosis call. This isn't a vague "it failed" — it's a detailed execution trace that lets the LLM reason about *why* the engine failed.

### The fixes were small and correct

Auto-fix #2 changed 2 lines. Auto-fix #4 changed 17 lines. The LLM wasn't rewriting the engine; it was making targeted, surgical fixes based on specific evidence from the execution trace.

### The loop discovered bugs that testing couldn't

2,983 unit tests all passed before any production run. The failures were integration-level: real file sizes exceeding limits, real git branches colliding, missing workflow steps that only matter in a real CI environment. These bugs only manifest when the full system runs against real repositories — exactly the environment the meta-loop provides.

This supports the secondary hypothesis: the most important bugs are integration-level, and production execution surfaces them where unit tests cannot.

### The engine operates on unmodified target repos

The target repository ([nonflux/build-definitions](https://github.com/nonflux/build-definitions)) had no configuration files, no labels, no bot integrations, and no code changes to support the engine. The engine read the issue and codebase through public APIs and git, operated on a repository it had never seen before, and produced a cross-fork PR that appeared like any other contribution. The target repo owners review and merge (or don't) using their existing workflow.
