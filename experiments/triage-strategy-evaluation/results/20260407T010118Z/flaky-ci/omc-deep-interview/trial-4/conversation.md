# Conversation: flaky-ci x omc-deep-interview

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

That sounds frustrating — intermittent CI failures are tricky to pin down. Could you share the error output from a recent failed run? Specifically, which tests are failing and what error messages or stack traces are you seeing? If you can link to a failed CI run, that would be ideal.

---

### reporter

I don't have the link handy right now but it's the task ordering tests that fail. The ones from that PR we merged last week. They just randomly fail with wrong order assertions. Look, they pass fine on my machine every single time — it's clearly something wrong with the CI setup.

---

### triage-agent

Thanks for narrowing it down to the task ordering tests — that's a helpful clue. "Wrong order assertions" that fail *randomly* often point to a non-deterministic sort or a race condition.

Could you tell me: do those task ordering tests rely on a specific sort order (e.g., by creation time, by priority), and does your local setup differ from CI in any way that might affect ordering — for example, a different database engine (SQLite locally vs. Postgres in CI), different test parallelism settings, or seeded test data vs. auto-generated data with timestamps?

---

### reporter

I don't know the exact details of what the tests do internally, I didn't write them — another dev on the team added them in that PR. They check that tasks come back in the right order after sorting. As for environment differences, we're on macOS locally and CI runs Ubuntu, both using Go 1.22. I don't think the environment should matter though, Go is Go. It works on my machine every time, I've run it like 20 times.

---

### triage-agent

That's really helpful context — the macOS vs. Ubuntu difference and the fact that another dev wrote the tests are both useful to know.

Could you share the name of the PR (or the branch/test file) that added these ordering tests? That way we can look at the test code directly to check whether the sort relies on something non-deterministic like insertion order or timestamp precision. That'll be the fastest path to a root cause since the test author isn't in this thread.

---

### reporter

I don't remember the exact branch name off the top of my head. It was merged like 4 days ago, added maybe 5 new tests to the task ordering module. You should be able to find it in the recent merge history. Can you just look at the CI config instead? I'm telling you, something is different about how CI runs things.

---

### triage-agent

Fair enough — let's look at it from the CI angle like you're suggesting. Can you tell me how your CI runs the test suite? Specifically:

1. What command does CI use to run tests? (e.g., `go test ./...` with any flags like `-race`, `-parallel`, `-count`, `-shuffle`)
2. Do you run the same command locally, or just `go test` in the package directory?

The reason I ask is that if CI uses `-shuffle` (randomizes test order) or `-parallel` with a higher count, tests that depend on a specific execution order would pass locally but fail randomly in CI — which matches exactly what you're describing.

---

### reporter

Honestly I don't know the exact CI command off the top of my head, it's whatever's in the pipeline config. I just run the tests locally with our dev config and they pass every time. I think the dev config sets some Go flags but I'd have to look it up. Can someone just look at the CI config and the dev config and compare them? I've already spent way too much time on this.

---

### triage-agent

[RESOLVED] {"title":"Task ordering tests fail intermittently in CI due to likely non-deterministic sort or CI-specific test flags","problem":"Tests added in a recent PR (~4 days ago) to the task ordering module fail intermittently in CI with wrong-order assertions, but pass consistently on the developer's local machine (macOS). The failures are random — sometimes all tests pass, sometimes a handful fail. This is blocking releases.","root_cause_hypothesis":"The task ordering tests assert a specific sort order that is non-deterministic under certain conditions. The most likely cause is one of: (1) CI uses `go test -shuffle` which randomizes test execution order, exposing tests that depend on implicit ordering between test cases or shared state; (2) CI uses higher `-parallel` or `-count` values causing race conditions in shared test fixtures; (3) the sort being tested is unstable (equal-priority items have no tiebreaker), and different environments resolve ties differently. The macOS-vs-Ubuntu difference may also manifest through different default sort behavior or timestamp precision in test data seeding.","reproduction_steps":["Identify the PR merged ~4 days ago that added ~5 tests to the task ordering module (check recent merge history)","Compare the CI pipeline test command against the local dev config test command — look specifically for flags like -shuffle, -parallel, -race, -count","Run the task ordering tests locally with the exact CI flags (e.g., `go test -shuffle=on -count=5 ./path/to/task/ordering/...`)","If that doesn't reproduce it, also try running on a Linux environment to match CI"],"environment":"Local: macOS, Go 1.22, dev config with unknown Go test flags. CI: Ubuntu, Go 1.22, pipeline config with unknown Go test flags.","severity":"high","impact":"Blocking all releases for the team. Every CI run is a coin flip, eroding confidence in the test suite. The team is spending significant time re-running pipelines hoping for green builds.","recommended_fix":"1. Pull up the CI config and local dev config side-by-side — identify differing test flags. 2. Read the failing test code to check whether the sort under test has a stable tiebreaker (e.g., if sorting by priority, what happens when two tasks have the same priority?). 3. If the sort is unstable, add a secondary sort key (e.g., by ID or creation timestamp) to make it deterministic. 4. If CI uses -shuffle and tests depend on execution order or shared state, fix the tests to be independent. 5. Ensure CI and local dev config use the same test flags to prevent future 'works on my machine' divergence.","proposed_test_case":"Create a test that inserts multiple tasks with identical sort keys (e.g., same priority, same timestamp) and asserts that the returned order is stable across 100 consecutive runs. This would catch unstable sorts that only manifest intermittently.","information_gaps":["Exact CI test command and flags (obtainable from pipeline config in repo)","Exact local dev config test flags (obtainable from dev config file in repo)","The specific test file and assertions that fail (obtainable from recent merge history ~4 days ago)","Whether the sort implementation uses a stable sort algorithm or has a tiebreaker"]}

---
