# Conversation: flaky-ci x superpowers-brainstorming

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI is really frustrating, especially when it blocks releases.

To help narrow this down, what kind of failures are you seeing when tests fail in CI?

1. **Timeout failures** — tests hang and eventually get killed
2. **Assertion failures** — tests produce wrong results (e.g., unexpected values, missing data)
3. **Connection/resource errors** — failures talking to a database, API, or external service
4. **Order-dependent failures** — different tests fail each run, suggesting tests interfere with each other
5. **Something else** — happy to hear more detail

Also, if you have a link to a recent failed CI run, that would be really helpful.

---

### reporter

It's assertion failures. The tests expect certain values but get different ones. I don't have a link handy but the test names are right there in the CI logs. Can someone just look at it? This has been blocking us for days.

---

### triage-agent

Thanks for confirming — assertion failures with different values is a helpful clue.

Since the failures are random and different tests fail each run, the most common cause is tests interfering with each other through shared state. A quick question that would narrow this down a lot:

**Do your CI tests run in parallel (multiple test workers/processes), while you run them sequentially locally?**

1. **Yes** — CI uses parallel workers (e.g., `--workers=4`, `--parallel`, pytest-xdist) but I run them with a single process locally
2. **Not sure** — I haven't checked the CI config vs. my local setup
3. **No** — both CI and local run the same way

If you're not sure, the CI config file (e.g., `.github/workflows/*.yml`, `Jenkinsfile`, etc.) would have the test command — happy to help you check if you can share it.

---

### reporter

I don't think it's a parallelism thing. Look, it's the same tests that fail every time — the ones from that task ordering PR we merged last week. They're not interfering with other tests, they just sometimes get the wrong order back. Can someone just look at the CI logs? The failing test names are right there.

---

### triage-agent

[RESOLVED] {"title":"Non-deterministic task ordering causes flaky test failures in CI","problem":"Tests introduced in a recent task ordering PR produce intermittent assertion failures in CI. The tests expect a specific ordering of tasks but sometimes receive a different order. The tests pass locally (likely due to differences in database engine, dataset size, or execution timing) but fail randomly in CI.","root_cause_hypothesis":"The task ordering implementation uses a sort that is not fully deterministic — most likely a database query with an ORDER BY on a non-unique column (e.g., priority, created_at) without a tiebreaker (e.g., id). When multiple tasks share the same sort key value, the database returns them in arbitrary order, which varies across runs and environments.","reproduction_steps":["Identify the task ordering PR merged approximately one week ago","Run the failing tests repeatedly (e.g., in a loop of 20-50 iterations) to reproduce the non-deterministic ordering","Alternatively, inspect the ordering query/logic for sort stability — look for ORDER BY clauses on non-unique fields or in-memory sorts without tiebreakers"],"environment":"Failures occur in CI but not locally, suggesting the local environment happens to return a consistent order by coincidence (e.g., smaller dataset, different DB engine, or insertion-order bias in SQLite vs. PostgreSQL)","severity":"high","impact":"Blocking all releases due to flaky CI. The entire team is affected and the issue has persisted for several days.","recommended_fix":"Add a deterministic tiebreaker to the task ordering logic. If ordering by a non-unique field (e.g., priority or due_date), add a secondary sort on a unique field (e.g., ORDER BY priority, id). Update the corresponding tests to use data with distinct sort keys or to assert based on the full deterministic ordering.","proposed_test_case":"Create multiple tasks with identical values for the primary sort field (e.g., same priority). Verify that the returned order is consistent across repeated calls and matches the expected tiebreaker order (e.g., ascending by id).","information_gaps":["Exact PR and test names (available in CI logs and git history — team can locate without reporter)","Whether the backing database differs between CI and local (e.g., PostgreSQL vs. SQLite)","Whether the ordering is implemented at the database query level or in application code"]}

---
