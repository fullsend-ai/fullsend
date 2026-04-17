# Conversation: flaky-ci x omc-deep-interview

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — intermittent CI failures are frustrating, especially when they block releases.

Could you share the error output from a recent failed CI run? Specifically, which tests are failing and what do the failure messages say? Even if different tests fail each time, a couple of examples would help narrow down what's going on.

---

### reporter

Ugh, I don't have the exact output in front of me right now. They're in the task ordering module — the tests that were added in that PR from last week. Something about the order not matching what's expected. Look, they pass every time on my machine, it's only CI that's broken. Can you just look at the CI logs? The test names are right there when they fail.

---

### triage-agent

Totally understand — I can dig into the CI logs on my end. One thing that would really help me narrow this down: does your CI run tests in parallel or with any randomized ordering? And do you know if your local setup differs from CI in any way — for example, a different database, different OS, or different test runner flags? That local-vs-CI gap is usually where intermittent failures like this hide.

---

### reporter

I mean, CI is Ubuntu and I'm on a Mac, but that shouldn't matter for unit tests. We're both on Go 1.22. I don't think we run tests in parallel — it's just `go test`. I know we have some different config between dev and CI but I haven't dug into what exactly differs. Honestly I don't see why any of that matters, the tests are straightforward — they just check that tasks come back in the right order after sorting. It works every single time locally. Something is wrong with the CI environment.

---

### triage-agent

That's helpful context — the combination of 'task ordering' tests and intermittent failures is a recognizable pattern.

Quick question about the sorting logic these tests cover: when two tasks have the same priority (or whatever field you're sorting by), does the test expect them in a specific order? For example, if you sort by priority and two tasks are both "high," does the test assert which one comes first?

The reason I ask is that if the sort isn't stable — or if the input order varies between runs — equal-ranked items can come back in any order, which would explain why it passes sometimes and fails other times. A quick way to check on your Mac: run `go test -count=100 ./path/to/task/ordering/...` and see if it ever fails locally too.

---

### reporter

Hmm, I mean yeah, some of them probably have the same priority. I didn't write the tests — they came in from that PR last week — but I think they just check that the tasks come back in the order they were inserted after the sort. I haven't tried running them 100 times locally, I just know they pass when I run the suite. Fine, I'll try that count thing, but I still think this is a CI issue, not a test issue.

---

### triage-agent

[RESOLVED] {"title":"Intermittent CI failures in task ordering tests due to unstable sort on equal-priority items","problem":"Tests in the task ordering module fail intermittently in CI (Ubuntu) but pass consistently in local development (macOS). Failures report unexpected ordering of tasks after sorting. The tests were introduced in a recent PR and assert that tasks are returned in a specific order after sorting by priority.","root_cause_hypothesis":"The sorting implementation likely uses Go's sort.Slice, which is not a stable sort. When multiple tasks share the same priority, their relative order is non-deterministic. The tests assert insertion order is preserved for equal-priority items, which happens to hold locally due to consistent memory layout but fails under different conditions in CI. The macOS-vs-Linux difference in memory allocation patterns makes the non-determinism more visible in CI.","reproduction_steps":["Identify the task ordering test file added in the recent PR (in the task ordering module)","Run `go test -count=100 ./path/to/task/ordering/...` locally to surface the flake","Inspect the test assertions for cases where multiple tasks share the same sort key (e.g., same priority)","Confirm that the sort implementation uses sort.Slice (unstable) rather than sort.SliceStable"],"environment":"CI: Ubuntu, Go 1.22. Local dev: macOS, Go 1.22. Test runner: `go test` (default settings, no explicit parallelism).","severity":"high","impact":"Blocking releases for the team. Every CI run is a coin flip, eroding confidence in the test suite and slowing development velocity.","recommended_fix":"Either (a) switch from sort.Slice to sort.SliceStable to preserve insertion order for equal-priority items, or (b) add a deterministic tiebreaker to the sort comparison (e.g., sort by priority then by task ID or creation timestamp), or (c) fix the test assertions to not depend on the ordering of equal-priority items (e.g., only assert that items are grouped by priority, or sort the equal-priority subset before comparing). Option (b) is usually the best long-term choice since it makes the API behavior deterministic for users too, not just for tests.","proposed_test_case":"Create a test with multiple tasks that share the same priority. Run it with -count=1000 and assert that the output order is deterministic. This validates that the sort is either stable or uses a tiebreaker, preventing future flakes.","information_gaps":["Exact test file path and test function names (discoverable from CI logs or the recent PR)","Whether go test -count=100 reproduces the failure locally (strongly expected to, but unconfirmed)","Whether any other CI config differences (env vars, test flags) contribute beyond the sort instability"]}

---
