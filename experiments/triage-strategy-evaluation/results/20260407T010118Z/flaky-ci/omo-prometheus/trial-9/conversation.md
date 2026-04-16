# Conversation: flaky-ci x omo-prometheus

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — intermittent CI failures are frustrating, especially when they block releases.

To start narrowing this down, I need some specifics:

**What do the failures actually look like?** Can you paste the error output (or a representative sample) from a recent failed CI run? I'm looking for the actual test names that failed and the error messages/stack traces — not just "3 tests failed." A link to a failed CI run would also work.

---

### reporter

I don't have the logs in front of me right now but the failing tests are all in the task ordering module — they're the ones from that PR we merged last week. Names like TestTaskOrdering... something. They assert that tasks come back in a specific order after sorting. Look, they pass every time on my machine, it's only CI where they randomly fail. Can you just look at the recent CI runs? The test names are right there in the output.

---

### triage-agent

I understand the logs aren't in front of you right now, but I do need to see them — I don't have access to your CI system.

While you pull those up, one specific question that will help me form a theory:

**When these ordering tests fail, do they fail because items come back in a *wrong* order, or because items are *missing/duplicated*?** For example, does the assertion show something like `expected [A, B, C]` but got `[B, A, C]` (same items, different order)? Or is it a different kind of failure entirely?

Also: **do any of the tasks being sorted share the same priority/sort key value?** If two tasks have equal priority and the test asserts a specific order between them, that would explain why it passes locally (deterministic sort implementation) but fails in CI (different environment, different tie-breaking behavior).

---

### reporter

Yeah it's the wrong order thing — same items, just scrambled. Like expected [A, B, C] got [C, A, B] or whatever. Nothing missing or duplicated.

As for the sort keys, I honestly don't remember off the top of my head. The tests were added by a teammate in that PR from like 4 days ago. They might have equal priorities in some cases, I'd have to go look at the test code. But again — they pass EVERY time locally. That's what's driving me crazy.

---

### triage-agent

Okay, this is sounding very much like a classic **unstable sort / equal-key** problem. Here's my working theory:

The test data in those `TestTaskOrdering` tests contains two or more tasks with the **same priority or sort key value**. Your test then asserts a specific order for those equal-priority items. The sort algorithm doesn't guarantee a particular order among equal items, so:

- **Locally** it happens to produce the same order every time (same OS, same memory layout, same standard library build).
- **In CI** the environment differs just enough (different OS/arch, container runtime, Go/Python/Node version, etc.) that the sort breaks ties differently — and sometimes differently across runs.

Could you check one thing to confirm or rule this out? **Open the test file for the task ordering tests and look at the test fixture data — do any two tasks in the input share the same value for whatever field is being sorted on (priority, due date, position, etc.)?** Even one pair of duplicates would explain everything you're seeing.

---

### reporter

Look, I'm a developer not a QA person, I can go look at the code but I don't have time to do a full investigation right now — that's why I filed this issue. I think some of them might have the same priority, yeah, but I'm not sure that explains it because the SORT should be deterministic right? Like it's the same Go version locally and in CI (1.22). The thing that bugs me is it's not even consistently wrong in CI — sometimes it passes, sometimes it doesn't. If it was just a different tie-breaking order it would fail every time in CI wouldn't it?

---

### triage-agent

[RESOLVED] {"title":"Intermittent CI failures in TestTaskOrdering tests due to unstable sort with equal-priority items","problem":"Tests in the task ordering module (TestTaskOrdering*) fail intermittently in CI but pass consistently locally. Failures show the correct set of items returned in a different order than expected — no missing or duplicated items.","root_cause_hypothesis":"The test fixtures contain tasks with identical sort key values (likely priority). The tests assert a specific order among these equal-keyed items. Go's sort.Slice is an unstable sort — it does not guarantee any particular order among equal elements, and its tie-breaking behavior depends on initial element positions in memory, which vary between runs. This explains the intermittency: it's not that CI always breaks ties differently, it's that the tie-breaking is nondeterministic. Locally, more stable memory allocation patterns happen to produce a consistent order, creating the illusion of correctness.","reproduction_steps":["Identify the TestTaskOrdering tests added in the PR merged ~4 days ago","Examine test fixture data for tasks sharing the same sort key (priority, position, etc.)","Run the test in a loop locally: `go test -run TestTaskOrdering -count=100 ./...` — with enough iterations, failures should appear even locally","Alternatively, use `-race` or run under different GOMAXPROCS values to perturb scheduling"],"environment":"Go 1.22, CI environment (likely containerized). Local dev machine passes consistently. Issue appeared after a PR merged approximately 4 days ago that added the ordering tests.","severity":"medium","impact":"Blocking releases for the team. The flaky tests cause CI pipeline failures that require manual re-runs, slowing all PRs. No production bug — the sort itself may work correctly, but the tests are asserting too strongly.","recommended_fix":"1. **Confirm equal keys:** Open the test fixtures and identify items sharing the same sort key value. 2. **Fix the sort:** Either switch from `sort.Slice` to `sort.SliceStable` if a stable sort is desired behavior, OR add a deterministic tiebreaker field (e.g., sort by priority, then by task ID). 3. **Fix the tests:** If order among equal-priority items is genuinely unspecified, update assertions to only verify order among items with distinct sort keys — use a comparator that groups equal-key items and checks only inter-group ordering. Option 2 (adding a tiebreaker) is the strongest fix since it makes the application behavior deterministic, not just the tests.","proposed_test_case":"Add a test with explicitly duplicated sort keys (e.g., three tasks all with priority=1) and verify that the output is sorted correctly by the tiebreaker field. Additionally, run the existing ordering tests with `-count=100` to confirm they no longer flake.","information_gaps":["Exact test file path and fixture data (discoverable from the recent PR)","Whether sort.Slice or sort.SliceStable is currently used (discoverable from the sorting implementation)","Whether the product intentionally defines an order among equal-priority tasks (product decision, not blocking the fix)"]}

---
