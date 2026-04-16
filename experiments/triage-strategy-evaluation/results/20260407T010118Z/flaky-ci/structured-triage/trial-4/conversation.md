# Conversation: flaky-ci x structured-triage

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — intermittent CI failures are frustrating, especially when they block releases.

To start narrowing this down: **which tests are failing, and what error messages or output do you see when they fail?** If you can share the relevant portion of a failed CI run's log (even just the test names and their error messages), that would be really helpful.

---

### reporter

Look, I don't have the logs in front of me right now but it's the task ordering tests. The ones from that PR we merged like 4 days ago. They fail with assertion errors saying the order is wrong. It works fine on my machine every single time. Can we just figure out why CI is different?

---

### triage-agent

Thanks for that context — task ordering tests with assertion errors on order definitely points toward a non-deterministic ordering issue that only surfaces in certain environments.

To help pinpoint why CI behaves differently from your machine: **what does your CI environment look like compared to your local setup?** Specifically:
- What OS and runtime versions does CI use? (e.g., Ubuntu + Node 18, etc.)
- Does CI run tests in parallel, or with any different configuration flags?
- Is there a shared database or any external service involved in these tests?

Even a link to your CI config file (e.g., `.github/workflows/...`) would work if that's easier than listing it out.

---

### reporter

CI is Ubuntu 22.04 with Go 1.22. I'm on macOS 14 with Go 1.22 locally. I don't think there's any parallel test stuff going on and there's no database involved — these are unit tests. I don't know what other config differences there are, that's kind of what I need help figuring out. It passes every time locally so something about the CI environment is clearly different.

---

### triage-agent

[RESOLVED] {"title":"Task ordering tests fail intermittently in CI due to non-deterministic ordering","problem":"Task ordering unit tests introduced ~4 days ago fail intermittently in CI (Ubuntu 22.04 / Go 1.22) with assertion errors indicating incorrect order, but pass consistently on the developer's local machine (macOS 14 / Go 1.22). The failures are blocking releases.","root_cause_hypothesis":"The tests likely depend on non-deterministic iteration order — most probably Go map iteration order or an unstable sort (sort.Slice is not stable). Go randomizes map iteration order at runtime, and the specific memory layout or hash seed differences between Linux and macOS can make one platform appear deterministic while the other exposes the randomness. The recently merged PR likely introduced code that iterates a map or uses sort.Slice to produce task ordering, and the tests assert on a specific order without accounting for ties or equivalent keys.","reproduction_steps":["Identify the task ordering tests added in the PR merged ~4 days ago","Run those tests repeatedly in a Linux environment (e.g., Ubuntu 22.04 container): `go test -count=100 -run TestTaskOrder ./...`","Observe intermittent assertion failures on task order"],"environment":"CI: Ubuntu 22.04, Go 1.22. Local (passing): macOS 14, Go 1.22. Unit tests only — no database, no parallel test execution.","severity":"high","impact":"Blocking releases for the team. All developers merging to the main branch are affected by intermittent CI failures.","recommended_fix":"Review the task ordering code from the recent PR for: (1) map iteration used to build ordered output — replace with sorted slice of keys; (2) sort.Slice used on items with equal sort keys — switch to sort.SliceStable or add a tiebreaker (e.g., by ID); (3) test assertions that compare slice order — either sort the output deterministically before asserting, or use an order-independent comparison where order doesn't matter. Running `go test -count=100` locally will confirm the fix eliminates flakiness.","proposed_test_case":"Add a test that creates tasks with identical sort keys (e.g., same priority/timestamp) and verifies the output is in a deterministic order (e.g., secondary sort by ID). Run with `-count=100` to confirm stability.","information_gaps":["Exact test file and function names (can be found by reviewing the recent PR)","Exact assertion error messages from a failed CI run (would confirm the specific ordering discrepancy)"]}

---
