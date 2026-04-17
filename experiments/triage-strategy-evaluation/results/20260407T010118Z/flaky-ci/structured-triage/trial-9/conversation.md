# Conversation: flaky-ci x structured-triage

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI failures are frustrating, especially when they block releases.

To start narrowing this down: when tests fail in CI, what do the failure messages look like? For example, are you seeing assertion errors, timeouts, connection failures, or something else? If you can share the output from a recent failed CI run (even just the relevant snippet), that would be really helpful.

---

### reporter

They're assertion errors. The tests expect tasks to come back in a certain order but they come back in a different order. It's always the same handful of tests that fail — the ones from that PR we merged last week for task ordering. Like I said, they pass fine on my machine every single time. Can we just figure out what's wrong with the CI environment?

---

### triage-agent

Thanks for that detail — assertion errors on task ordering that only appear in CI is a strong clue. That pattern often comes down to differences in the environment between your local setup and CI.

Could you share some details about the CI environment? Specifically:
- What CI platform are you using (e.g., GitHub Actions, GitLab CI, Jenkins)?
- What OS and database does the CI runner use, and are they different from your local setup?
- What version of the app/runtime is CI running?

Knowing how the CI environment differs from your local machine will help us zero in on why the ordering is inconsistent.

---

### reporter

We use GitHub Actions. CI runs on Ubuntu 22.04 with Go 1.22. I'm on macOS locally, also Go 1.22. There's no database involved, these are unit tests. Look, the tests pass every time on my machine — I've run them like 50 times. Something is wrong with the CI setup, not the tests.

---

### triage-agent

That's really helpful context, thank you. The picture is getting clear — ordering-dependent assertions passing on macOS but failing intermittently on Ubuntu in Go is a well-known pattern (Go's map iteration order is randomized, and some sort behaviors can differ across OS).

One last thing that would help a developer jump straight to a fix: could you share the output from a recent failed CI run — specifically the failing test names and the assertion messages? For example, something like `expected [A, B, C] but got [B, A, C]`. That will pinpoint exactly which query or function is returning results in a non-deterministic order.

---

### reporter

Fine, I pulled up the last failed run. The failing tests are all in the task ordering module — things like TestTaskOrderAfterSort, TestInsertionOrderPreserved, TestMultipleTaskOrdering. They're all from that PR we merged 4 days ago. The assertion errors look like `expected [task1, task2, task3] got [task2, task3, task1]` — the tasks are all there but in the wrong order. Can someone just fix this already? It's been blocking us for days.

---

### triage-agent

[RESOLVED] {"title":"Flaky task ordering tests in CI due to non-deterministic sort/iteration order (Go, Ubuntu vs macOS)","problem":"Tests merged 4 days ago in the task ordering module (TestTaskOrderAfterSort, TestInsertionOrderPreserved, TestMultipleTaskOrdering) pass consistently on macOS but fail intermittently on Ubuntu 22.04 in GitHub Actions CI. Assertion errors show all expected elements present but in wrong order.","root_cause_hypothesis":"The task ordering code relies on non-deterministic iteration order (likely Go map iteration or an unstable sort). On macOS the memory layout or allocator happens to produce a consistent order, masking the bug. On Linux/Ubuntu the ordering varies between runs, exposing the flaky assertions. The PR merged 4 days ago likely introduced code that iterates a map or uses sort.Slice (which is unstable) without a tiebreaker, producing non-deterministic output.","reproduction_steps":["Check out the branch/commit that includes the task ordering PR merged ~4 days ago","Run the task ordering test suite on Ubuntu 22.04 with Go 1.22 (or in GitHub Actions)","Run repeatedly (may take several runs to see failure): go test -count=10 -run 'TestTaskOrderAfterSort|TestInsertionOrderPreserved|TestMultipleTaskOrdering' ./...","Observe assertion errors like 'expected [task1, task2, task3] got [task2, task3, task1]'"],"environment":"CI: GitHub Actions, Ubuntu 22.04, Go 1.22. Local (reporter): macOS, Go 1.22. Unit tests only, no database.","severity":"high","impact":"Blocking releases for the entire team. CI pipeline is unreliable, requiring re-runs and eroding trust in the test suite.","recommended_fix":"Review the task ordering PR for (1) map iteration used to build result slices — replace with sorted key iteration or a slice-based data structure, (2) use of sort.Slice — switch to sort.SliceStable or add a tiebreaker field (e.g., ID) to ensure deterministic ordering when primary sort keys are equal. Also consider running tests locally with -count=100 or setting GOFLAGS=-count=10 to catch such issues before merge.","proposed_test_case":"Add a test that inserts tasks with identical sort keys (e.g., same priority/timestamp) and asserts the output order is deterministic across 100 runs (go test -count=100). This validates that tiebreaking logic produces stable results regardless of platform.","information_gaps":["Exact source file and function in the task ordering PR that produces the non-deterministic order","Whether the code uses map iteration, unstable sort, or goroutine-based concurrency to build results"]}

---
