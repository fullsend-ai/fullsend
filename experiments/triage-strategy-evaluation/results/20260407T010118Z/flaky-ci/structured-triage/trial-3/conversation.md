# Conversation: flaky-ci x structured-triage

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI failures are really frustrating, especially when they block releases.

To start narrowing this down: when the failures happen, is it always the same tests that fail, or a different subset each time? And could you share the error output or failure messages from a recent failed CI run? Even a link to the CI log would be very helpful.

---

### reporter

It's the same tests every time they fail — it's those new task ordering tests that got added last week. There's like 5 of them. They either all pass or some of them fail, it's completely random. I don't have a link handy right now but the test names are right there in the CI output, they're pretty obvious. Can we just figure out why they pass locally and not in CI? That's clearly the issue here.

---

### triage-agent

Thanks for clarifying — knowing it's specifically the new task ordering tests is really helpful, and the non-deterministic pass/fail pattern strongly suggests a test isolation or ordering dependency issue.

To understand why these behave differently in CI vs. locally, could you share some details about your CI environment? Specifically:
- What CI platform are you using (GitHub Actions, Jenkins, GitLab CI, etc.)?
- Are tests run in parallel in CI but sequentially locally (or vice versa)?
- What OS and runtime versions does the CI runner use compared to your local machine?

Even just the CI platform and whether parallelism differs would help narrow this down significantly.

---

### reporter

It's GitHub Actions, Ubuntu 22.04 runners. I run macOS locally. We're both on Go 1.22 so I don't think it's a version thing. I don't know about the parallelism stuff — I just run `go test` and CI does whatever it does. Look, these tests pass every single time on my machine. Every time. It's only CI that's flaky. So it's obviously something about the environment, right?

---

### triage-agent

I hear you — it definitely points to an environment difference, and we're getting close to having enough to hand this off to a developer.

One thing that would really speed up the investigation: could you grab the actual failure output from one of those failed CI runs? Specifically the error messages or assertion failures from those task ordering tests. You can find it in the GitHub Actions logs for any recent failed run — even a copy-paste of just the failing test output would work.

The reason this matters: knowing *how* the tests fail (e.g., wrong ordering, timeout, race condition panic) will tell a developer exactly where to look, versus having to re-trigger failures just to see the error.

---

### reporter

Ugh, fine. I don't have the exact output in front of me but from what I remember it's assertion failures — the tests expect tasks to come back in a specific order and they come back in a different order. It's not a timeout or a crash, the ordering is just wrong. Like the test says 'expected [A, B, C]' and gets '[B, A, C]' or whatever. Can someone just look at the PR that added these tests? It was merged like 4 days ago and that's exactly when this started. I feel like we're going in circles here.

---

### triage-agent

[RESOLVED] {"title":"Task ordering tests fail intermittently in CI due to non-deterministic result ordering","problem":"Five new task ordering tests added approximately 4 days ago pass consistently on the developer's local macOS machine but fail intermittently in GitHub Actions CI. When they fail, assertion errors show that tasks are returned in an unexpected order (e.g., expected [A, B, C] but got [B, A, C]). This is blocking releases.","root_cause_hypothesis":"The task ordering tests assert on a specific element order, but the underlying query or data structure does not guarantee deterministic ordering. Locally on macOS the order happens to be stable (likely due to filesystem, memory layout, or platform-specific behavior differences), while the Ubuntu CI runner exposes the non-determinism. Common causes: missing ORDER BY clause in a database query, reliance on map iteration order in Go, use of an unstable sort, or concurrent goroutines writing results without synchronization.","reproduction_steps":["Identify the PR merged ~4 days ago that added the task ordering tests","Run `go test ./... -count=10` on the package containing the task ordering tests to surface non-deterministic failures locally","Alternatively, trigger a CI run on the main branch and observe the task ordering test results"],"environment":"CI: GitHub Actions, Ubuntu 22.04, Go 1.22. Local: macOS, Go 1.22.","severity":"high","impact":"Blocks all releases for the team. Every CI run has a chance of failing, requiring manual re-runs and eroding confidence in the test suite.","recommended_fix":"Examine the task ordering tests from the recent PR. Check whether the code under test guarantees a deterministic order (e.g., via an explicit ORDER BY or stable sort). If ordering is not semantically required, change the test assertions to be order-independent (e.g., sort both slices before comparing, or use an unordered set comparison). If ordering is required, ensure the implementation enforces it with a tiebreaker on a unique field. Run with `-count=100` to verify the fix eliminates flakiness.","proposed_test_case":"Run the task ordering tests with `go test -count=100 -race` to confirm they pass deterministically across 100 iterations with race detection enabled.","information_gaps":["Exact test names and file paths (developer can find via the recent PR)","Exact CI log output from a failed run (developer can check GitHub Actions history)","Whether tests are run in parallel in CI via `-parallel` flag or a matrix strategy"]}

---
