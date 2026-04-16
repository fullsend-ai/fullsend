# Conversation: flaky-ci x omo-prometheus

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI is painful, especially when it blocks releases.

To start narrowing this down: can you share the exact error output from a recent failed CI run? Specifically:

1. Which tests are failing — is it always the same subset, or genuinely different tests each time?
2. What CI platform and runner environment are you using (e.g., GitHub Actions, Jenkins, etc.), and has anything changed in the CI config or runner images in the past few days?

A link to a failing CI run log would be ideal if you have one.

---

### reporter

Look, I don't have the logs in front of me right now but it's always the same tests that fail — the ones from the task ordering PR we merged like 4 days ago. 5 new tests for task ordering. They don't all fail every time though, sometimes 2 fail, sometimes 4, sometimes they all pass. It's on GitHub Actions, Ubuntu runners. Nothing changed in CI config, just that PR got merged and now everything's flaky. I've wasted hours re-running the pipeline hoping it passes.

---

### triage-agent

Thanks for narrowing it down to those 5 task-ordering tests — that's really helpful. The pattern you're describing (variable number of failures each run) is a strong signal for either shared mutable state or a race condition between tests.

Can you tell me: do those 5 tests share any setup — for example, do they all insert tasks into the same database table or modify the same in-memory list — and does your test runner execute them in parallel? Specifically, if you look at the test file, are the tests each creating their own isolated set of tasks, or are they relying on a shared fixture or `beforeAll` / `setUp` that creates tasks once for the whole suite?

---

### reporter

They each create their own tasks, they're not sharing state. And they don't run in parallel. Look, the tests are straightforward — they add some tasks, sort them, and check the order. They pass every single time on my machine. It's something about CI that's different but I don't know what. Can we just figure out why CI behaves differently?

---

### triage-agent

That's a useful detail — thanks for clarifying they're isolated and sequential.

The pattern you're describing (deterministic locally, non-deterministic in CI, and specifically with *ordering* tests) is a classic signature of a sort-stability issue. Here's my working theory: if your task-ordering logic sorts by a field where multiple tasks can have the **same value** (e.g., same priority, same created-at timestamp), the tiebreaker order is undefined in most sort implementations. Locally you might always get the same memory layout, but CI runners can behave differently.

Can you check the sorting logic from that PR and tell me:
1. What field(s) are tasks sorted by?
2. Is it possible for two or more tasks in your test data to have the **same value** for that sort key?
3. What do the test assertions check — exact array order, or just relative ordering of specific pairs?

If you can paste the sort function and one of the failing test cases, that would confirm this quickly.

---

### reporter

I don't have the code open right now but from what I remember the tests insert tasks, put them through the ordering function, and then assert they come back in insertion order. The sort key shouldn't matter because the point is the ordering is supposed to be stable — like if you put tasks in, they come out the same way. The assertions check exact order, yeah. It works perfectly on my Mac every time. I'm running the same Go version as CI so it's not a language version thing. Can you just tell me what's different about Ubuntu GitHub Actions runners that would cause this?

---

### triage-agent

[RESOLVED] {"title":"Flaky task-ordering tests in CI due to unstable sort (sort.Sort vs sort.Stable)","problem":"Five tests introduced in the recent task-ordering PR fail intermittently on GitHub Actions Ubuntu runners but pass consistently on the developer's Mac. The tests insert tasks, pass them through an ordering function, and assert exact insertion order is preserved. A variable subset (0-4 of 5) fail on any given CI run.","root_cause_hypothesis":"The ordering function uses Go's sort.Sort, which is an unstable sort (introsort). When multiple tasks have equal sort keys, their relative order is undefined and varies across runs. On the reporter's Mac, consistent memory layout produces a deterministic (but not guaranteed) order, masking the bug. CI runners have enough environmental variation to surface the instability. The tests assert exact output order, which depends on sort stability that sort.Sort does not provide.","reproduction_steps":["Check out the task-ordering PR (merged ~4 days ago)","Identify the sort call in the ordering function — expected to be sort.Sort or sort.Slice","Run the 5 ordering tests in a loop (e.g., `go test -run TestTaskOrder -count=100`). On most machines this will surface at least one failure","If the sort key allows ties (e.g., tasks with the same priority or timestamp), the order of tied elements will vary across runs"],"environment":"GitHub Actions, Ubuntu runners, Go (same version locally and in CI). No CI config changes; only the task-ordering PR was merged.","severity":"medium","impact":"Blocks the release pipeline. All team members are affected — CI must be re-run until tests happen to pass. No production impact (the flakiness is in tests, though the underlying unstable sort could also affect user-facing ordering).","recommended_fix":"Replace sort.Sort (or sort.Slice) with sort.Stable (or sort.SliceStable) in the task-ordering function. Alternatively, add a deterministic tiebreaker to the sort comparator (e.g., sort by primary key as a secondary key) so that no two elements are considered equal. The latter approach is preferred as it makes ordering deterministic regardless of sort algorithm. Also consider whether the production ordering should be stable for UX consistency, not just for tests.","proposed_test_case":"Add a test that creates multiple tasks with identical sort keys (e.g., same priority and timestamp) and asserts that the output order is deterministic across 100 consecutive runs. This directly tests sort stability and would have caught the original bug.","information_gaps":["Exact sort key field(s) used in the ordering function (reporter did not have code open, but a developer can inspect the PR)","Whether the unstable sort also affects user-facing behavior in production, not just tests"]}

---
