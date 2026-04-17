# Conversation: flaky-ci x omo-prometheus

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — intermittent CI failures are frustrating, especially when they block releases.

To start narrowing this down: can you share the error output from a recent failing CI run? Specifically, I'm looking for:

1. The actual test failure messages (assertion errors, timeouts, exceptions, etc.)
2. Which specific tests failed in the last 2-3 failing runs — are they always the *same* tests failing, or genuinely different tests each time?

A link to a failing CI run or a pasted log snippet would be ideal.

---

### reporter

I don't have the logs in front of me right now but it's the same tests every time they fail — they're in the task ordering module. The ones from that PR we merged last week. They assert on the order of tasks and sometimes it's right, sometimes it's not. Look, they pass on my machine every single time. Can someone just look at why CI is different?

---

### triage-agent

Thanks for that — knowing it's specifically the task ordering module from last week's PR is really helpful. That pattern (same tests, asserting on order, passing sometimes) is a classic sign of non-deterministic ordering, so we're likely close to the root cause.

One key question: in the tests that fail, how is the expected task order determined? Specifically, are the tests sorting tasks by a field like `created_at` or `priority`, and could multiple tasks have the **same value** for that field? For example, if two tasks are inserted with the same timestamp or priority, the database or application may return them in an arbitrary order that differs between your machine and CI.

If you're not sure off the top of your head, could you point me to the test file name or the PR number? I can look at the code directly.

---

### reporter

They assert that tasks come back in insertion order after running the sort. I don't remember the exact details of what fields they sort on — the tests were added by a teammate in that PR about a week ago, there were like 5 new test cases. They all pass on my machine, every time, no flakiness at all. It's only CI that has this problem. Can you just look at what's different about the CI environment? That seems like the obvious thing to check here.

---

### triage-agent

[RESOLVED] {"title":"Intermittent CI failures in task ordering tests due to likely non-deterministic sort","problem":"Five new test cases added to the task ordering module in a PR merged approximately one week ago pass consistently on developer machines but fail intermittently in CI. The failing tests assert that tasks are returned in insertion order after a sort operation. When they fail, the order is wrong.","root_cause_hypothesis":"The sort operation uses a field (e.g., created_at, priority) where multiple tasks can have identical values. When values are tied, the sort is unstable — the database or application returns tied records in an arbitrary order that happens to be consistent on the developer's local environment (likely same DB engine/version, same data size, same query planner behavior) but differs in CI. This is a classic non-deterministic test caused by an unstable sort with no tiebreaker.","reproduction_steps":["Identify the PR merged ~1 week ago that added 5 test cases to the task ordering module","Run the test suite for that module repeatedly (10-20 times) — failures may not reproduce locally","Run the same tests against the CI environment's database engine/version if different from local","Inspect the test fixtures: look for tasks inserted with identical values in the sort field"],"environment":"CI environment (specific details unknown — investigating CI vs. local differences is part of the fix). Local environment passes consistently, suggesting a difference in database engine, version, or configuration.","severity":"high","impact":"Blocking releases for the team. All team members affected. No workaround other than re-running CI until tests happen to pass.","recommended_fix":"1. Find the recent PR and examine what field the sort operates on. 2. If tasks can share the same value for that field, add a deterministic tiebreaker to the sort (e.g., sort by `created_at, id` instead of just `created_at`). 3. Update the tests to either use distinct sort-key values in fixtures or assert order using the same tiebreaker. 4. Investigate whether the CI database engine differs from local (e.g., PostgreSQL vs. SQLite) — different engines have different default ordering behavior for tied rows.","proposed_test_case":"Create a test that inserts multiple tasks with identical values for the sort field (e.g., same timestamp, same priority) and asserts that the returned order is deterministic across repeated runs. This test should fail before the tiebreaker fix and pass after.","information_gaps":["Exact PR number and test file name (developer can find via recent merge history)","Specific sort field used in the ordering logic","CI database engine and version vs. local development database","Whether CI runs tests in parallel which could affect insertion timestamps"]}

---
