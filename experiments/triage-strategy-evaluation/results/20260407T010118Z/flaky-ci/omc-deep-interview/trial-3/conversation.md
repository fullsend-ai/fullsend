# Conversation: flaky-ci x omc-deep-interview

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — intermittent CI failures are frustrating, especially when they block releases.

Could you share the error output from a recent failed CI run? Specifically, which tests are failing and what the failure messages look like? Even if the failing tests vary between runs, a couple of examples would help us narrow down what's going on.

---

### reporter

I don't have the exact logs in front of me right now but it's the task ordering tests that fail. The ones from that PR we merged last week. They pass locally every single time so I don't understand why CI keeps choking on them. Can someone just look at this? It's been blocking us for days.

---

### triage-agent

Totally understand the urgency — let's figure this out.

One thing that jumps out: "task ordering tests" failing intermittently in CI but passing locally often points to a difference in how the tests are executed between the two environments. Does your CI run tests in parallel or in a randomized order, while you might be running them sequentially locally? And are those task ordering tests relying on any specific database state or seeded data that might not be cleaned up between test runs in CI?

---

### reporter

I don't think it's a parallelism thing, we run tests the same way locally and in CI as far as I know. And no, there's no database involved — these are unit tests for task ordering logic. They just check that tasks come back in the right order after sorting. Look, they pass every single time on my machine. Every. Time. The only difference is CI runs on Ubuntu and I'm on a Mac, but it's the same Go version so that shouldn't matter. Can we just get someone to actually look at the test code instead of guessing?

---

### triage-agent

I hear you — let's cut to the chase on the code itself.

The Mac-vs-Ubuntu detail is actually more relevant than it might seem for sorting tests. Quick question: in those task ordering tests from last week's PR, are any of the test cases checking the exact order of tasks that have the **same priority or sort value**? For example, if two tasks both have priority "high," does the test assert which one comes first?

The reason I ask is that Go's `sort.Slice` isn't a stable sort — the relative order of equal elements isn't guaranteed and can vary between runs and platforms. That would explain why it passes consistently on your Mac but fails intermittently in CI on Ubuntu. If you can check the test assertions or point me to the PR, I can confirm pretty quickly.

---

### reporter

Yeah, actually, I think some of them do check the exact order when tasks have the same sort key. They expect the tasks to come back in the order they were added. It works every time locally so I assumed that was fine. Are you saying that's not guaranteed? Because it literally never fails on my machine — I've run it like 50 times. If the sort isn't stable then fine, but why does it always work locally?

---

### triage-agent

[RESOLVED] {"title":"Task ordering tests fail intermittently in CI due to unstable sort on equal-priority items","problem":"Unit tests for task ordering logic, introduced in a recent PR, assert a specific output order for tasks that share the same sort key (e.g., same priority). The tests pass consistently on the developer's macOS machine but fail intermittently on the Ubuntu-based CI runner.","root_cause_hypothesis":"Go's sort.Slice uses an unstable sorting algorithm (pdqsort), meaning the relative order of elements with equal sort keys is not guaranteed and may vary across runs, platforms, and even between invocations on the same machine. The tests assume input-order preservation for equal elements, which happens to hold reliably on the developer's macOS environment due to differences in memory layout or allocator behavior, but breaks non-deterministically on Ubuntu in CI.","reproduction_steps":["Identify the task ordering tests added in the recent PR","Find test cases where multiple tasks share the same sort key (priority, due date, etc.)","Run those tests with Go's race detector or with -count=100 to surface the non-determinism: `go test -run TestTaskOrdering -count=100 ./...`","Alternatively, run on a Linux environment to reproduce more readily"],"environment":"CI: Ubuntu (Linux), Developer local: macOS. Same Go version. Tests are Go unit tests with no database dependency.","severity":"high","impact":"Blocking releases for multiple days. All team members affected. No workaround currently in use other than re-running CI and hoping for a pass.","recommended_fix":"Two options (apply one or both): (1) Replace sort.Slice with sort.SliceStable if preserving input order for equal elements is the intended behavior. (2) Fix the test assertions so they don't depend on the relative order of items with equal sort keys — either by using a comparison that treats equal-key permutations as equivalent, or by adding a tiebreaker field (e.g., creation timestamp or ID) to the sort comparator so the order is fully deterministic.","proposed_test_case":"Add a dedicated test with multiple tasks sharing the same sort key, run it with -count=1000, and assert that results are consistent across all iterations. This validates that the sort is deterministic regardless of platform.","information_gaps":["Exact PR number and test file path (developer can locate via recent merge history)","Whether the intended product behavior is to preserve insertion order for equal-priority tasks or if any stable ordering is acceptable — this affects whether the fix should be sort.SliceStable vs. adding a tiebreaker"]}

---
