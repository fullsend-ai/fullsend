# Conversation: flaky-ci x superpowers-brainstorming

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI is really frustrating, especially when it's blocking releases.

To help narrow this down: when the tests fail in CI, what kind of failures are you seeing? For example:

1. **Timeouts** — tests hang or exceed a time limit
2. **Assertion failures** — tests produce wrong results (e.g., expected X but got Y)
3. **Connection/resource errors** — failures talking to a database, API, or service
4. **Something else**

If you have a recent failed CI run you could link, that would be especially helpful.

---

### reporter

They're assertion failures. Expected X got Y type stuff. I don't have a link handy but the test names are right there in the CI logs when they fail.

---

### triage-agent

That's helpful, thanks. One more thing that would really narrow this down: when tests fail across different CI runs, is it roughly the **same set of tests** that fail each time, or do **different tests** fail each run?

If you're not sure, even a rough sense helps:
1. **Same tests** — it's always the same ~handful that are unreliable
2. **Different tests** — which ones fail seems to shuffle each run
3. **Not sure** — haven't compared across runs yet

---

### reporter

Same tests. It's the ones from that PR we merged last week — the task ordering tests. They're the only ones that ever fail.

---

### triage-agent

Thanks — that narrows it significantly. Here's my working theory:

The task ordering tests from last week's PR are likely **assuming a specific order** that isn't guaranteed — for example, a database query without an explicit `ORDER BY`, or relying on insertion order that happens to be stable locally but not in CI. A common reason this differs between local and CI is:

1. **Parallel test execution** — CI runs tests concurrently (or in a different order), so data from other tests leaks in or timing changes
2. **Different database/seed state** — CI starts fresh each run, while your local DB has a stable state that happens to produce the "right" order
3. **Something else about the CI environment**

Does that hypothesis sound right, or does something about it not fit what you're seeing?

---

### reporter

Yeah the ordering thing sounds about right. They pass every single time on my machine though, that's what's driving me crazy. It's not a database thing — it's just sorting tasks. Look, I don't really care why it's different, I just need it to stop failing so we can ship. We've been re-running the pipeline hoping it passes and that works sometimes but it's not a real fix.

---

### triage-agent

[RESOLVED] {"title":"Task ordering tests from recent PR fail intermittently in CI due to non-deterministic sort","problem":"Task ordering tests introduced in a PR merged last week fail intermittently in CI with assertion errors (expected vs. actual ordering mismatches), but pass consistently when run locally. The failures are limited to this specific set of tests and are blocking releases.","root_cause_hypothesis":"The task ordering tests rely on a sort that is non-deterministic when elements have equal sort keys. Locally, the sort happens to be stable (producing the expected order every time), but in CI, differences in runtime, memory layout, or parallel test execution cause the unstable sort to produce a different-but-equally-valid order. This is a classic unstable-sort or missing-tiebreaker bug.","reproduction_steps":["Identify the task ordering tests added in the recent PR (visible in CI failure logs)","Run the test suite multiple times in CI or with CI-equivalent settings (e.g., parallel execution, clean state)","Observe that the ordering assertions fail when tasks with equal sort keys are reordered"],"environment":"CI environment (specific platform not identified); failures do not reproduce locally, suggesting a difference in sort stability, parallelism, or initial state between local and CI","severity":"high","impact":"Blocking releases for the team. The team is currently working around it by re-running the pipeline, which is unreliable and wastes CI resources.","recommended_fix":"Review the task ordering logic in the recent PR. Look for sorts without a tiebreaker — if tasks can have equal sort keys (e.g., same priority, same date), add a stable secondary sort key (e.g., task ID or creation timestamp). Then update the tests to either assert using the stable sort or to be order-insensitive where ordering is not guaranteed.","proposed_test_case":"Create a test with multiple tasks that have identical values for the primary sort key, and verify they are returned in a consistent, well-defined order (by the tiebreaker). Run this test repeatedly (or in a loop) to confirm stability.","information_gaps":["Exact PR and test file names (findable from CI logs and recent merge history)","Whether CI uses parallel test execution vs. local sequential (may explain why local always passes but doesn't change the fix)"]}

---
