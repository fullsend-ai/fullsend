# Conversation: flaky-ci x omo-prometheus

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI is painful, especially when it's blocking releases.

To start narrowing this down: can you share the exact error output from one of the recent failing CI runs? Specifically, I need:

1. Which tests are failing (test names/files), and whether it's the same tests each time or different ones
2. The actual error messages or stack traces
3. Did anything change around the time this started — new dependency, CI config update, infrastructure change?

A link to a failing CI run would be ideal if you can share one.

---

### reporter

Look, I don't have the CI logs in front of me right now. The tests that fail are the ones from that task ordering PR we merged last week — there's like 5 new tests and they're the ones that keep failing. It's not always the same ones though, sometimes 2 fail, sometimes all 5, sometimes none. They all pass on my machine every single time. Nothing changed in CI config, we just merged that PR with the new ordering tests and now everything's broken. Can someone just look at why these tests are flaky instead of asking me to dig through logs?

---

### triage-agent

Understood — I won't ask you to dig through logs right now. The pattern you're describing (5 new tests, intermittent failures, always pass locally) strongly suggests a test isolation or concurrency issue.

One key question: do those new task ordering tests share any state with each other or with existing tests — for example, do they write to a shared database, modify in-memory task lists, or depend on a specific execution order? And are your CI tests run in parallel (e.g., multiple workers or shards), while locally you're running them sequentially?

---

### reporter

They don't share a database or anything like that. Each test creates its own task list, sorts it, and checks the order. They're pretty self-contained. And no, we run tests the same way locally and in CI — just `go test ./...`. It's not a parallelism thing. They just don't give consistent results in CI for some reason. Like I said, they pass every time on my machine, so I don't think it's a test isolation problem.

---

### triage-agent

That's helpful — thanks for clarifying.

Here's where my thinking is: if each test creates its own task list and sorts it, the intermittent failures might come from the sort itself rather than from shared state. Specifically:

What field(s) are the tasks being sorted by in these tests — priority, created timestamp, name, something else? And is it possible for two tasks in a test's list to have the **same value** for that field (i.e., ties in the sort key)?

I'm asking because Go's `sort.Slice` is not a stable sort, meaning items with equal keys can come out in any order between runs. That would explain passing locally (where memory layout happens to be consistent) but flaking in CI (different allocation patterns). If there are ties, that's very likely your root cause.

---

### reporter

I mean, I'm not sure exactly what fields are being sorted on — I didn't write the tests, another dev on the team did. I think the tasks have priorities and names and stuff. There could be ties, I guess? But honestly if `sort.Slice` was the problem wouldn't it fail locally too? It passes 100% of the time on my machine. Every. Single. Time. That's what's so frustrating — it's clearly something about the CI environment that's different. Can we just compare what's different between CI and local instead of guessing about sort implementations?

---

### triage-agent

[RESOLVED] {"title":"Flaky task ordering tests in CI — likely unstable sort with tied keys","problem":"Five new tests introduced in the recent task ordering PR fail intermittently in CI (sometimes 2 fail, sometimes all 5, sometimes none) but pass 100% of the time on the reporter's local machine. This is blocking releases.","root_cause_hypothesis":"The new tests sort tasks and assert a specific output order. Go's sort.Slice uses an unstable sort (pdqsort), meaning elements with equal sort keys can appear in any order. If any test constructs tasks with duplicate values in the sort key (e.g., same priority), the expected order is non-deterministic. Locally, consistent memory allocation patterns produce a repeatable ordering that happens to match assertions. In CI, different memory layout, ASLR, or scheduling causes a different — but equally valid — ordering, breaking the assertions.","reproduction_steps":["Identify the 5 new ordering tests from the recently merged task ordering PR","Examine the test data: look for tasks with identical values in the field(s) being sorted on","Run `go test -count=100 ./path/to/ordering/tests` locally to force re-execution and surface flakiness","If tests pass locally even with -count=100, try with `-race` or on a different machine/container to vary memory layout"],"environment":"Go test suite run via `go test ./...`, same command locally and in CI. CI environment details unknown but standard Go CI pipeline.","severity":"high","impact":"Blocking all releases. Affects the entire team. No workaround other than re-running CI until tests happen to pass.","recommended_fix":"1. Inspect the sort comparator in the task ordering code — check if it has a total ordering or allows ties. 2. If ties are possible, either add a tiebreaker to the sort (e.g., sort by priority then by ID/name) or use sort.SliceStable instead of sort.Slice. 3. Fix the test assertions to either account for non-deterministic ordering of equal elements OR ensure test data has no ties in the sort key. 4. Verify the fix by running `go test -count=100` to confirm stability.","proposed_test_case":"Add a dedicated test that creates tasks with deliberately identical sort keys and verifies the sort produces a deterministic result. Run this test with -count=1000 to confirm no flakiness. This test should fail before the fix and pass consistently after.","information_gaps":["Exact field(s) used as sort keys (requires code inspection — reporter didn't write the tests)","Whether sort.Slice or sort.SliceStable is currently used (requires code inspection)","Exact CI environment details (OS, Go version, container runtime) — unlikely to matter if the sort hypothesis is correct","Actual CI failure logs and error messages — would confirm assertion mismatches on ordering"]}

---
