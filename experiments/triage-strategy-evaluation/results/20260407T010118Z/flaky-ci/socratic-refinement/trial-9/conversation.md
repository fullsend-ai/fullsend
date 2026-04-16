# Conversation: flaky-ci x socratic-refinement

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI is really frustrating, especially when it's blocking releases.

To help me understand the bigger picture: can you walk me through what was happening around the time these failures started? For instance, were there any recent changes to the test suite itself, the CI configuration, dependencies, or the codebase that landed around "a few days ago"? Even things that seem unrelated could be helpful — sometimes the trigger isn't obvious.

---

### reporter

Yeah, we merged a PR about 4 days ago that added some new tests for the task ordering stuff. But those tests pass fine locally, so I don't see how that's the problem. Can we just figure out what's wrong with the CI environment? It's been 4 days and we're stuck.

---

### triage-agent

That timing is really interesting — the PR landing 4 days ago lines up exactly with when the failures started, so it's worth digging into even if those new tests pass locally.

Here's what would help me connect the dots: when the CI fails, which tests are actually failing — is it the new task ordering tests, other pre-existing tests, or a mix? And could you share what the failure output looks like for one or two of them? (Even a truncated snippet would help.) I'm curious whether the errors point to wrong results, timeouts, or something else entirely.

---

### reporter

It's the new ordering tests that fail. The names show up clearly in the CI logs when they fail. They assert that tasks come back in a certain order after sorting. Look, I don't have the logs in front of me right now but it's always those same 5 new tests, sometimes they pass sometimes they don't. The old tests are fine. Can someone just look at the CI setup? Something is obviously different there.

---

### triage-agent

[RESOLVED] {"title":"New task ordering tests fail intermittently in CI due to likely non-deterministic sort order","problem":"Five new tests added for task ordering functionality pass consistently on the developer's local machine but fail intermittently in CI. The failures started exactly when the PR introducing these tests was merged (~4 days ago). All pre-existing tests continue to pass.","root_cause_hypothesis":"The tests assert an exact ordering of tasks after sorting, but the sort is either unstable or there are tasks with identical sort keys (ties). The tie-breaking order is non-deterministic and happens to be consistent locally but varies in CI — likely due to differences in database query ordering, language runtime sort stability guarantees, hash randomization seeds, or timing-dependent factors like parallel execution.","reproduction_steps":["Identify the 5 new task ordering tests added in the PR merged ~4 days ago","Run the test suite repeatedly (e.g., 20+ iterations) — failures may not reproduce on every run even locally","Inspect the sort keys of the test fixture data for ties or identical values","Compare the CI runtime environment (database, language version, OS) to local for sort stability differences"],"environment":"CI environment (specific details not provided); failures do not reproduce consistently on the reporter's local machine","severity":"high","impact":"Blocking all releases for the team for the past 4 days. The flaky tests cause CI failures that prevent merging and deploying.","recommended_fix":"1. Examine the 5 new ordering tests and identify what exact order they assert. 2. Check the sorting logic for tasks with equal sort keys (ties). 3. Either add a deterministic tiebreaker to the sort (e.g., sort by priority then by ID) or relax the test assertions to only verify relative ordering of items with distinct sort keys. 4. If the sort is expected to be stable, ensure the underlying sort implementation (language/DB) guarantees stability in both local and CI environments.","proposed_test_case":"Create a test with multiple tasks sharing identical sort keys and verify that the sort produces a deterministic result regardless of input order. Run the test in a loop (50+ iterations) to confirm no flakiness.","information_gaps":["Exact CI environment details (OS, language runtime version, database)","Actual failure log output showing the assertion mismatches","Whether the sort key has known duplicates in test fixtures"]}

---
