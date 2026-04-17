# Conversation: flaky-ci x structured-triage

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI failures are frustrating, especially when they block releases.

To help narrow this down: when the tests fail in CI, what error messages or failure output do you see? For example, are the tests timing out, throwing exceptions, or producing wrong assertions? If you can share the relevant portion of a recent failed CI run's log output, that would be very helpful.

---

### reporter

They're assertion failures. The tests expect tasks to come back in a certain order but they don't. Look, I don't have the logs in front of me right now but the test names are pretty obvious when they fail — they're the new ordering tests we added last week. Can someone just look at the CI output? It's all right there.

---

### triage-agent

Thanks for that detail — assertion failures on the ordering tests is a strong clue. That pattern (order-dependent tests passing locally but failing in CI) often comes down to differences in the environment between the two.

Could you share some details about your CI environment? Specifically:
- What CI platform are you using (e.g., GitHub Actions, GitLab CI, Jenkins)?
- What database does CI run against, and is it the same engine and version you use locally?
- Are tests run in parallel in CI but sequentially on your machine?

Any of these differences could explain why ordering comes back differently in CI.

---

### reporter

It's GitHub Actions, Ubuntu runners. There's no database involved here — it's the task ordering module, it's all in-memory Go code. And no, the tests aren't running in parallel, they run sequentially same as local. I already checked that. Look, the tests pass on my Mac every single time. They only flake in CI. Can we just focus on why the ordering comes back wrong sometimes?

---

### triage-agent

[RESOLVED] {"title":"Flaky ordering tests in CI: assertion failures due to non-deterministic task ordering in in-memory Go code","problem":"The ordering tests added last week fail intermittently in GitHub Actions CI with assertion errors — tasks are returned in an unexpected order. The same tests pass consistently on the developer's local Mac. The code is purely in-memory Go with no database involved, and tests run sequentially in both environments.","root_cause_hypothesis":"The task ordering code likely relies on non-deterministic iteration order, most probably Go map iteration (which is intentionally randomized by the Go runtime). Alternatively, the code may use sort.Sort or slices.Sort on elements with equal sort keys, producing an unstable ordering that varies across runs and platforms. The tests assert an exact order that happens to be stable on the developer's Mac but is not guaranteed.","reproduction_steps":["Push the branch with the new ordering tests to GitHub","Trigger a GitHub Actions CI run on Ubuntu runners","Observe that the ordering test suite intermittently fails with assertion errors on task order","Run the same tests locally on Mac — they pass consistently"],"environment":"CI: GitHub Actions, Ubuntu runners. Local: macOS. Language: Go. All in-memory, no database. Tests run sequentially in both environments.","severity":"high","impact":"Blocking releases for the team. Every CI run has a chance of spurious failure, requiring manual re-runs and eroding confidence in the test suite.","recommended_fix":"Inspect the ordering tests added last week and the code they exercise. Look for (1) iteration over a Go map whose keys feed into the result order, (2) use of an unstable sort (sort.Slice/sort.Sort) on elements with equal comparison keys, or (3) any other source of non-determinism. Fix by either ensuring a deterministic, fully-specified sort (add a tiebreaker such as ID), switching to sort.SliceStable, or updating test assertions to be order-insensitive where exact order is not a product requirement.","proposed_test_case":"After applying the fix, run the ordering tests 100 times in a loop (`go test -count=100 -run TestOrdering ./...`) on both Mac and an Ubuntu environment to confirm the flakiness is eliminated.","information_gaps":["Exact test names and assertion output (available in CI logs, which the developer pointed to but did not paste)","Whether the ordering code iterates over a map or uses a sort — requires code inspection"]}

---
