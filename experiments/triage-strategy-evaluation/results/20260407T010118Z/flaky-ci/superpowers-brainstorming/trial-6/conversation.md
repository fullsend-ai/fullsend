# Conversation: flaky-ci x superpowers-brainstorming

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI is really frustrating, especially when it blocks releases.

To help narrow this down: what kind of failures are you seeing when tests fail in CI? For example:

1. **Timeouts** — tests hang and eventually get killed
2. **Assertion failures** — tests produce wrong results (e.g., unexpected values, missing data)
3. **Connection/resource errors** — failures connecting to databases, APIs, or other services
4. **Something else**

If you have a recent failed CI run you could link or paste the error output from, that would be especially helpful.

---

### reporter

It's assertion failures. The tests expect tasks in a certain order and they come back in a different order. Like I said, it works fine on my machine every single time. Can you just look at the CI logs? The failing test names are right there.

---

### triage-agent

[RESOLVED] {"title":"Flaky CI: task-ordering assertions fail due to non-deterministic query results","problem":"Tests that assert on the order of returned tasks pass locally but fail intermittently in CI. The failures are assertion errors where tasks appear in an unexpected order, not timeouts or connection errors.","root_cause_hypothesis":"The application queries that return task lists do not specify an explicit ORDER BY clause. Locally, the database engine (or its configuration) happens to return rows in insertion order consistently, masking the bug. In CI, differences in the database engine, configuration, parallel test execution, or connection pooling cause the query planner to return rows in a different — but equally valid — order, exposing the non-determinism.","reproduction_steps":["Run the full test suite in CI (the failures are intermittent, so multiple runs may be needed)","Look for failing tests whose assertions compare ordered lists of tasks","Check the underlying queries for those endpoints/functions — they will likely lack an ORDER BY clause","Alternatively, run tests locally with a database configuration matching CI (e.g., PostgreSQL instead of SQLite, or with parallel connections)"],"environment":"CI environment (specific runner/database configuration not yet confirmed); passes consistently on the reporter's local machine","severity":"high","impact":"Blocking releases for the team. Every CI run is a coin flip, eroding confidence in the test suite and slowing development velocity.","recommended_fix":"1. Identify all queries that return task lists consumed by the failing tests. Add explicit ORDER BY clauses (e.g., by created_at, priority, or id) to guarantee deterministic ordering. 2. Alternatively, if the test intent is to verify the *set* of returned tasks rather than their order, update assertions to be order-independent (e.g., sort both lists before comparing, or use set-based matchers). 3. Audit other list-returning queries for the same pattern to prevent future flakiness.","proposed_test_case":"Add a test that inserts tasks with identical timestamps (or in reverse order) and asserts the query returns them in the expected sort order. This test should fail before the ORDER BY fix and pass after, even on a local database.","information_gaps":["Exact test names and query code (available in CI logs and source — no reporter input needed)","Whether local and CI use different database engines (e.g., SQLite vs. PostgreSQL)","Whether CI runs tests in parallel, which could compound the ordering issue"]}

---
