# Conversation: flaky-ci x socratic-refinement

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI is really frustrating, especially when it's blocking releases.

To help me understand the context: you mentioned this started happening over the past few days. Can you walk me through what was going on in the project around that time? For instance, were there any notable changes — new dependencies, infrastructure updates, changes to the test suite itself, or anything else that shifted around when the failures began appearing?

---

### reporter

Look, I already said it's been failing for a few days. A coworker merged a PR that added some new tests like 4 days ago, and that's around when it started. But the tests pass on my machine so I don't see how that's the problem. Can someone just look at the CI logs? The failing test names are right there.

---

### triage-agent

I hear you — I don't want to retread ground you've already covered. One thing that would really help me point this in the right direction: are the tests that fail in CI the *new* ones from your coworker's PR, or are they *existing* tests that used to pass reliably? (Or a mix of both?)

That distinction matters a lot for narrowing this down — it tells us whether we're looking at a problem with the new tests themselves or whether they've introduced something that's destabilizing the rest of the suite.

---

### reporter

It's the new ones. The 5 tests that got added in that PR are the ones failing. The old tests are fine. But like I said, they pass locally so I don't get what the deal is.

---

### triage-agent

That's a really helpful detail — knowing it's specifically the new tests narrows things down a lot.

You mentioned the failing test names are right there in the CI logs. When those 5 tests fail, what does the failure actually look like? I'm curious about the shape of it — are they timing out, getting unexpected values, failing to connect to something, or something else entirely? And does it tend to be all 5 that fail together, or is it a random subset each time?

That pattern will tell us a lot about what's going on differently in CI.

---

### reporter

They're getting wrong values — like the tasks come back in the wrong order. It's not timeouts or connection stuff. And it's random which ones fail, sometimes 2 of them, sometimes all 5. It's never the same combination twice which is what makes this so annoying. Can we please just fix this? It's been blocking us for days.

---

### triage-agent

[RESOLVED] {"title":"5 new tests flaky in CI due to non-deterministic task ordering","problem":"Five tests added in a recent PR fail intermittently in CI with wrong return order for tasks, while passing consistently on local machines. A random subset of the 5 fails each run, and the failing combination is never the same twice. This has been blocking releases for ~4 days.","root_cause_hypothesis":"The new tests assert on the order of returned tasks, but the underlying query or data access does not guarantee a deterministic order (likely missing an ORDER BY clause or relying on insertion order). The local development environment happens to return rows in a consistent order (common with SQLite or single-connection setups), masking the bug. CI uses a different database engine, connection pooling, or parallel test execution that surfaces the non-determinism.","reproduction_steps":["Identify the 5 tests added in the coworker's PR (~4 days ago)","Examine what queries or API calls those tests make to retrieve tasks","Run the test suite repeatedly in CI (or locally against the CI database configuration) to observe intermittent ordering failures","Check whether the tests assert on array order of results without the underlying query specifying ORDER BY"],"environment":"CI pipeline (specific CI environment and database engine not yet confirmed); passes on local developer machines","severity":"high","impact":"Blocking all releases for the team for ~4 days. All team members affected. No production bug, but development velocity is halted.","recommended_fix":"1. Inspect the queries exercised by the 5 failing tests for missing ORDER BY clauses — add explicit ordering where the test expectations depend on order. 2. Alternatively, if the tests don't actually care about order, change the assertions to be order-independent (e.g., sort both expected and actual before comparing, or use set-equality assertions). 3. Verify that local dev and CI use the same database engine and configuration to prevent similar masking in the future.","proposed_test_case":"Run the 5 tests 20+ times in a loop in CI (or against the CI database config locally) and confirm zero failures after the fix. Additionally, add a test that explicitly inserts tasks in reverse order and verifies the query returns them in the expected sorted order.","information_gaps":["Exact database engine used locally vs. in CI (e.g., SQLite vs. PostgreSQL)","Whether CI runs tests in parallel, which could compound the non-determinism","The specific sort key the tests expect (timestamp, ID, name, etc.)"]}

---
