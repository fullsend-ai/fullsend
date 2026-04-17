# Conversation: flaky-ci x superpowers-brainstorming

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI is really frustrating, especially when it blocks releases.

To help narrow this down: what do the failing tests have in common, if anything? For example:

1. **Timing/concurrency** — tests that involve timeouts, async operations, or parallel execution
2. **External dependencies** — tests that hit a database, API, or file system
3. **Test ordering** — different tests fail each time, suggesting shared state between tests
4. **Resource limits** — failures look like out-of-memory, disk space, or connection pool exhaustion
5. **Something else or not sure** — the failures seem truly random with no pattern

Even a rough sense of which category fits best would help a lot.

---

### reporter

Look, I don't know exactly what category it falls into. It's the task ordering stuff — the tests that got added last week. They just fail sometimes in CI and I can't reproduce it locally. Can someone just look at the CI logs? The failing test names are right there.

---

### triage-agent

Thanks — that's really helpful. Here's my working theory:

The task ordering tests added last week likely have **shared state** (e.g., they create/reorder tasks without fully isolating from other tests). Locally your tests probably run in a fixed order, so the shared state happens to work out. In CI, tests may run in a **different order or in parallel**, exposing the dependency.

Does that sound plausible? And could you share a link to a recent failing CI run so a developer can confirm which specific tests are failing and check for the shared-state pattern? That should be enough to get this unblocked.

---

### reporter

I mean, maybe? But it's not like different tests fail each time — it's always the same 5 tests that were added in that PR last week. They either all pass or some of them fail. And they pass every single time on my machine. I don't have a link handy but the test names are obvious in the CI output, they're all in the task ordering module. Can someone from the team just pull up the last few CI runs and look?

---

### triage-agent

[RESOLVED] {"title":"Task ordering tests from last week's PR are flaky in CI (pass locally)","problem":"Five tests in the task ordering module, all introduced in a PR last week, fail intermittently in CI while passing consistently on the reporter's local machine. The failures are not random across the suite — it is always the same 5 tests, and on any given run they either all pass or a subset fails. This is blocking releases.","root_cause_hypothesis":"The task ordering tests likely depend on something that differs between the local environment and CI — most probably timing or execution speed. Since it's always the same 5 tests (not different tests each run), this is not a test-ordering/shared-state issue. The consistent-locally, flaky-in-CI pattern strongly suggests race conditions or timing-sensitive assertions that happen to always win locally but sometimes lose on CI runners with different performance characteristics or resource contention.","reproduction_steps":["Pull up the last 3-5 CI runs for the main branch or recent PRs","Identify the 5 failing tests in the task ordering module","Examine those tests for timing-sensitive patterns: sleeps, polling loops, async awaits with timeouts, ordering assumptions on concurrent operations","Attempt to reproduce by running those tests under resource pressure (e.g., CPU throttling, parallel test execution, or reduced timeouts)"],"environment":"CI environment (specific runner details to be confirmed from CI config). Failures do not reproduce on the reporter's local machine.","severity":"high","impact":"Blocking releases for the team. Every CI run is a coin flip, eroding confidence in the test suite.","recommended_fix":"1. Pull up recent CI logs and identify the exact 5 failing tests. 2. Review the test code from last week's PR for race conditions, timing assumptions, or missing awaits. 3. Look for non-deterministic ordering assumptions (e.g., asserting array order on results that aren't guaranteed to be sorted). 4. If the tests create/reorder tasks concurrently, ensure proper synchronization or deterministic assertions. 5. Consider running the tests with a CI-like configuration locally (parallel execution, resource limits) to reproduce.","proposed_test_case":"After fixing the flaky tests, run them in a loop (e.g., 50-100 iterations) under parallel execution to confirm the flakiness is resolved. The tests should pass consistently under CI-equivalent conditions.","information_gaps":["Exact names of the 5 failing tests (available in CI logs)","CI runner configuration and parallelism settings","Whether the PR that introduced these tests also changed any application code or just added tests","Specific error messages or assertion failures from the CI logs"]}

---
