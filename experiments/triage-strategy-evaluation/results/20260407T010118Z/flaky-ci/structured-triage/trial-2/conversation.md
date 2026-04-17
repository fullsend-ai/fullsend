# Conversation: flaky-ci x structured-triage

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI failures are really frustrating, especially when they're blocking releases.

To help narrow this down, could you share what the test failures actually look like? Specifically:

- What error messages or assertion failures do you see in the CI logs when tests fail?
- Are there any particular test names or test files that tend to show up in the failures, or is it truly a different set each time?

Even a snippet from a recent failed CI run would be very helpful.

---

### reporter

Look, it's always the same tests — the ones from the task ordering PR we merged last week. TestTaskOrder-something, there's like 5 of them. They fail with assertion errors saying the order doesn't match what's expected. But they pass on my machine every single time! I've run them like 50 times locally. Can we just figure out what's wrong with the CI environment?

---

### triage-agent

Thanks for that detail — knowing it's specifically the TestTaskOrder tests from last week's task ordering PR is really helpful. The pattern you're describing (order assertions pass locally but fail intermittently in CI) is a classic sign of either a different runtime environment or non-deterministic ordering.

Could you share some details about your CI environment?

- What CI system are you using (e.g., GitHub Actions, Jenkins, GitLab CI)?
- What OS and runtime version does CI use vs. your local machine?
- Is CI running tests in parallel, or with a different test runner configuration than you use locally?

This will help pinpoint why the ordering behaves differently in CI.

---

### reporter

It's GitHub Actions, Ubuntu something, I don't remember the exact version. We're both running Go 1.22 so it's not a language version thing. I'm on a Mac locally. I don't think we're running tests in parallel or anything weird — it's just `go test`. Can you just look at the CI config yourself? I don't understand why the same Go version would give different results.

---

### triage-agent

[RESOLVED] {"title":"TestTaskOrder tests fail intermittently in CI with order assertion mismatches (pass locally on Mac)","problem":"Five TestTaskOrder tests introduced in a recent task ordering PR fail intermittently in GitHub Actions CI with assertion errors indicating the returned order doesn't match expectations. The same tests pass consistently on the reporter's local Mac with the same Go version (1.22).","root_cause_hypothesis":"The task ordering implementation likely relies on non-deterministic ordering that happens to be stable on macOS but not on Linux. Most probable causes: (1) iterating over a Go map to build the ordered result — map iteration order is randomized per-spec but can appear stable on one platform; (2) using sort.Sort or slices.Sort on elements with equal keys — Go's sort is not stable, and tie-breaking may differ across platforms; (3) database or filesystem ordering differences between macOS and Ubuntu.","reproduction_steps":["Check out the branch containing the task ordering PR merge","Run `go test -count=100 -run TestTaskOrder ./...` on a Linux machine or in a GitHub Actions runner","Observe intermittent assertion failures where the returned task order doesn't match the expected order","Compare with running the same command on macOS where it likely passes consistently"],"environment":"CI: GitHub Actions, Ubuntu (exact version not specified), Go 1.22. Local: macOS, Go 1.22. Test runner: `go test` with default settings.","severity":"high","impact":"Blocking all releases. The entire team cannot merge or ship because CI is unreliable. Flaky tests also erode trust in the test suite.","recommended_fix":"Examine the TestTaskOrder tests and the task ordering implementation from the recent PR. Look for: (1) map iteration used to produce ordered output — replace with sorted slice or ordered data structure; (2) unstable sort on items with equal sort keys — switch to slices.SortStableFunc or add a deterministic tiebreaker (e.g., sort by ID when priority is equal); (3) any reliance on insertion order from a database query without an explicit ORDER BY clause. Running `go test -count=100 -run TestTaskOrder` locally should reproduce the issue on Linux even if Mac appears stable.","proposed_test_case":"Add a test that creates tasks with identical sort keys (e.g., same priority, same creation time) and asserts that the output order is deterministic across 100 runs using `go test -count=100`. Alternatively, assert that the output is sorted by the documented ordering criteria rather than comparing against a hardcoded expected sequence.","information_gaps":["Exact Ubuntu runner version and whether it recently changed","Whether the CI config has any test caching or parallelism flags not visible to the reporter","Exact assertion error text from CI logs (reporter described but did not paste)"]}

---
