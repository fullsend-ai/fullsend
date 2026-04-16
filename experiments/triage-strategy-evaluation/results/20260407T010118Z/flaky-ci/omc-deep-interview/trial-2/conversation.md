# Conversation: flaky-ci x omc-deep-interview

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — intermittent CI failures are really frustrating, especially when they block releases.

Could you share the error output from a recent failed CI run? Specifically, which tests are failing and what the failure messages look like. Even if different tests fail each time, a couple of examples would help narrow down whether this is a timing issue, a resource issue, or something else entirely.

---

### reporter

Ugh, I don't have the logs in front of me right now. But it's the task ordering tests that fail — the ones from that PR we merged last week. Like `TestTaskOrderAfterSort` and a couple others. They just say the order doesn't match what's expected. Look, they pass fine on my machine every single time. Can someone just look at the CI config or something?

---

### triage-agent

Totally understand — I'll take a look at the CI config too. One quick question that might crack this open fast: the PR from last week that added those task ordering tests — do those tests assert an exact ordering of tasks? And if so, does the sort have a tie-breaker (e.g., sorting by priority but also by ID when priorities are equal)? Intermittent ordering failures in CI but not locally is a classic sign of a non-deterministic sort where the tie-breaking behavior differs between environments. If you can point me to the PR or the test file, I can check directly.

---

### reporter

Yeah they check exact ordering. I don't know about tie-breakers, that's not really my area — someone else wrote the tests in that PR. All I know is they pass every time on my Mac and then randomly fail in CI. The PR is from like 4 days ago, added maybe 5 new tests for task ordering. Can you just look at the test file? I don't remember the exact path off the top of my head.

---

### triage-agent

That's really helpful — the exact-ordering assertion with no known tie-breaker is a strong lead.

One more thing that would help me confirm the theory: what OS and settings does your CI run on? Specifically:

- Is the CI runner Linux-based (e.g., Ubuntu on GitHub Actions)?
- Do the tests run in parallel in CI (e.g., `-parallel` flag, or multiple test packages at once)?

The reason I ask is that sort stability actually differs between environments — Go's `sort.Slice`, for example, is not guaranteed to be stable, and the underlying behavior can vary between macOS and Linux. If your CI is Linux and your local machine is macOS, that alone could explain why the order is consistent for you but shuffled in CI. If the tests also run in parallel there, that adds another source of non-determinism.

This would let me tell you exactly how to reproduce it locally so you're not blocked waiting on CI.

---

### reporter

Yeah CI is Ubuntu, GitHub Actions or whatever. I don't know about parallel flags, I didn't set up the CI config. And look, I'm a developer too, I get that sort stability is a thing, but like I said I didn't write these tests. Can you just look at the code and fix it? It's been 4 days and it's blocking us. I run macOS locally if that matters.

---

### triage-agent

[RESOLVED] {"title":"Intermittent task ordering test failures in CI due to non-deterministic sort (no tie-breaker)","problem":"Task ordering tests added ~4 days ago (including `TestTaskOrderAfterSort`) fail intermittently on Ubuntu CI runners but pass consistently on the developer's macOS machine. Failures report that the actual task order doesn't match expected order.","root_cause_hypothesis":"The task sort implementation lacks a deterministic tie-breaker for items with equal sort keys. Tests assert exact ordering. On macOS, the sort implementation happens to produce a stable order, masking the bug. On Linux (CI), the sort produces a different — and legally non-deterministic — order, causing intermittent failures depending on initial arrangement of equal-keyed items.","reproduction_steps":["Find the task ordering PR merged approximately 4 days ago","Identify the sort function used in the task ordering logic (likely sort.Slice or equivalent unstable sort)","Confirm the sort comparator does not include a tie-breaker (e.g., secondary sort by ID) for items with equal primary keys","On macOS, run the ordering tests repeatedly — they will likely always pass","On a Linux machine or container, run the same tests in a loop (e.g., 50 iterations) — failures should appear"],"environment":"CI: Ubuntu on GitHub Actions. Local: macOS. Language likely Go (based on test naming conventions and sort.Slice reference in discussion).","severity":"high","impact":"Blocking all releases for 4+ days. Affects the entire development team's ability to ship. No workaround other than re-running CI until tests happen to pass.","recommended_fix":"1. Add a deterministic tie-breaker to the task sort comparator (e.g., sort by priority, then by task ID as secondary key). 2. Switch from unstable sort (e.g., `sort.Slice`) to stable sort (e.g., `sort.SliceStable`) if preserving insertion order for equal items is desired. 3. Alternatively, if exact order doesn't matter for equal-priority items, relax the test assertions to only check ordering of items with distinct sort keys, or group equal-keyed items and assert set membership rather than exact position.","proposed_test_case":"Create a test with multiple tasks sharing the same priority/sort key but different IDs. Run the sort and assert that (a) the primary ordering is correct, and (b) the tie-breaker ordering (by ID) is also correct. Run this test 100 times in a loop to verify determinism across runs.","information_gaps":["Exact file path of the failing tests (findable from PR history or test name search)","Whether CI runs tests in parallel (secondary factor — sort instability alone explains the failures)","Exact sort function and comparator implementation (developer should inspect directly)"]}

---
