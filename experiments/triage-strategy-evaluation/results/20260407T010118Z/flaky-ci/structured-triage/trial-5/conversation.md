# Conversation: flaky-ci x structured-triage

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI failures are frustrating, especially when they block releases.

To start narrowing this down: when the CI failures occur, are the same tests failing each time, or is it a different set of tests on each run? If you can share the names of a few tests that have failed (or a snippet of the CI failure output), that would help a lot.

---

### reporter

Yeah it's the same tests every time they fail. They're in the task ordering module — the ones from that PR we merged last week. I don't have the exact names in front of me but they're pretty obvious in the CI logs. Look, I've already wasted a bunch of time re-running the pipeline hoping it'll pass. Can someone just fix this?

---

### triage-agent

I hear you — re-running pipelines and hoping for green is no fun, and I want to help get this unblocked.

Could you paste the relevant section of the CI log output from one of the failing runs? Specifically the test failure messages and any error traces for those task ordering tests. Even a screenshot would work. That will tell us whether this is a timing issue, a resource constraint, or something else entirely — and point us toward the right fix much faster than guessing.

---

### reporter

I don't have a CI log open right now and I'm not going to go dig through pipeline runs for you. The test names are right there in the logs if you look — they're the 5 new tests from the task ordering PR that got merged like 4 days ago. They assert on the order tasks come back after sorting. Sometimes they pass, sometimes they don't. It works every single time on my machine. Can you just look at the CI logs yourself? They're not hard to find.

---

### triage-agent

Totally understand — and based on what you've described (tests that assert on sort order, pass locally but fail intermittently in CI), this has the hallmarks of a non-deterministic sort issue, which is very fixable.

One thing that would help confirm this: what does the CI environment look like? Specifically:
- What CI platform (GitHub Actions, Jenkins, GitLab CI, etc.)?
- What OS and runtime version do the CI runners use?
- Are tests run in parallel in CI?

If any of these differ from your local setup, that's likely where the inconsistency is coming from.

---

### reporter

I don't know all the details of the CI setup, that's not really my area. I think it's GitHub Actions on Ubuntu something. We're using Go 1.22. I run macOS locally. I don't know if tests run in parallel, I just hit the button and wait. Look, you said it sounds like a non-deterministic sort issue — can't you just go fix it? You clearly already know what the problem is.

---

### triage-agent

[RESOLVED] {"title":"Flaky CI: task ordering tests fail intermittently due to likely non-deterministic sort","problem":"Five tests added in a recent task ordering PR (merged ~4 days ago) fail intermittently in CI but pass consistently on the developer's local machine. The tests assert on the order tasks are returned after sorting. Failures block releases.","root_cause_hypothesis":"Non-deterministic sort order. In Go, the standard sort (sort.Slice, sort.Sort) is not stable — elements comparing as equal can appear in any order. If the task ordering tests assert on exact output order for items with equal sort keys, results will vary across runs. Go's map iteration order is also randomized, which could contribute if tasks are collected from a map before sorting. The behavioral difference between local (macOS) and CI (Ubuntu) is consistent with platform-dependent memory layout or scheduling differences affecting an unstable sort.","reproduction_steps":["Identify the 5 task ordering tests added in the PR merged approximately 4 days ago","Run those tests repeatedly (e.g., `go test -count=100 ./path/to/task/ordering/...`) to reproduce the intermittent failure","Inspect the test assertions: check whether they assert on exact slice order for items that may have equal sort keys"],"environment":"CI: GitHub Actions on Ubuntu, Go 1.22. Local (reporter): macOS, Go 1.22.","severity":"high","impact":"Blocks releases for the team. Developers are wasting time re-running the CI pipeline hoping for green builds.","recommended_fix":"1. Find the 5 new tests in the task ordering module from the recent PR. 2. Check whether the sort used is stable (`sort.SliceStable`) or unstable (`sort.Slice`). If unstable, either switch to a stable sort or add a tiebreaker (e.g., sort by ID as a secondary key). 3. Update test assertions to either use a stable sort, assert only on the relative order of items with distinct sort keys, or normalize equal-key items before comparison. 4. Run `go test -count=100` to confirm flakiness is resolved.","proposed_test_case":"Create a test with multiple tasks sharing the same sort key (e.g., same priority and due date) and verify that the sort output is deterministic across 100+ runs, asserting either on stable sort order or on order-independent equality for equal-key groups.","information_gaps":["Exact test names and file paths (available in CI logs — reporter declined to provide)","Exact CI failure output and error messages","Whether tests are run in parallel in CI (reporter unsure)","Exact PR reference for the task ordering change"]}

---
