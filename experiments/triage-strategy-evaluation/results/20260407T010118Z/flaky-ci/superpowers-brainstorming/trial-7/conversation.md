# Conversation: flaky-ci x superpowers-brainstorming

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI is really frustrating, especially when it's blocking releases.

To narrow this down quickly: can you think of anything that changed around the time the failures started a few days ago?

1. **A dependency or runtime update** (e.g., Node/Python version bump, package update, new lockfile)
2. **New or modified tests** were added around that time
3. **CI infrastructure change** (new runner image, config change, different CI provider)
4. **A code change** that touched shared state, database setup, or async logic
5. **Nothing obvious** — it seemed to start on its own

Also, are the failing tests always the *same* tests, or a different random subset each run?

---

### reporter

Yeah, a PR got merged a few days ago that added some new tests. Those are the ones that fail. But they pass on my machine every single time, so I don't see how they're the problem. It's the same tests that fail but not every run — sometimes they pass in CI too, which is what makes this so annoying. Can we just get this fixed? It's been blocking us for days.

---

### triage-agent

Thanks — that's really helpful. Knowing it's the new tests from that PR narrows things down a lot.

What do those new tests involve? Specifically:

1. **Database operations** — creating/reading/updating records, relying on specific seed data or cleanup
2. **Async or timed operations** — waiting for events, timeouts, scheduled tasks, polling
3. **External services or network calls** — APIs, file system operations, or anything outside the process
4. **Shared in-memory state** — global variables, caches, singletons that other tests might also touch
5. **Something else** — happy to hear what they cover

This will help me figure out why CI's environment is tripping them up while your local setup doesn't.

---

### reporter

They're testing task ordering. Like, you put tasks in and they should come back in the right order after sorting. It's not database stuff or network calls or anything complicated — it's straightforward ordering logic. That's why I don't get why it fails. The tests are dead simple and they pass every time on my machine. I've run them like 50 times locally, zero failures.

---

### triage-agent

I think I know what's going on. This is a really common pattern with sorting tests.

When your tests create tasks and check their order after sorting — do any of the tasks in your test data share the **same value** for whatever you're sorting by? For example, two tasks with the same priority, same due date, or same creation timestamp?

What I suspect is happening: your sort is correct, but when two tasks are "equal" by the sort criteria, their relative order isn't guaranteed (this is called an *unstable sort*). Locally it happens to come back in one consistent order, but in CI the underlying runtime or data setup is just different enough that equal items land in a different order — and your assertions fail.

Does that sound like it could match what you're seeing?

---

### reporter

I mean... maybe? I'm not sure if any of them have the same sort values off the top of my head. But yeah, the tests check that the tasks come back in insertion order after sorting, and they always do on my machine. Every single time. So if it's an ordering thing, why does it work locally and not in CI? That's what I don't get. We're running the same Go version on both.

---

### triage-agent

[RESOLVED] {"title":"Flaky CI: new task-ordering tests assume stable sort behavior","problem":"A recently merged PR added tests for task sorting/ordering logic. These tests pass consistently on the author's local machine but fail intermittently in CI. The tests assert that tasks come back in insertion order after sorting. Both environments run the same Go version.","root_cause_hypothesis":"The tests rely on Go's sort.Slice, which is an unstable sort (it does not preserve the original order of equal elements). The test data likely contains tasks with identical sort keys (e.g., same priority or due date). Locally, the underlying memory layout or allocation pattern happens to preserve insertion order for equal elements, making the tests appear stable. In CI, different runner hardware, memory pressure, or goroutine scheduling produces a different — but equally valid — ordering of equal elements, causing assertion failures.","reproduction_steps":["Identify the PR that added the new task-ordering tests","Examine the test data for tasks that share the same value for the sort key (priority, due date, etc.)","Run the tests repeatedly (e.g., `go test -count=100 ./...`) — on most machines this will eventually surface the failure","Alternatively, reverse the insertion order of equal-key tasks in the test data and run once — the assertions should fail immediately if this is the cause"],"environment":"Go (same version locally and in CI). CI runner specifics not identified but not needed — the issue is in the sort contract, not the environment.","severity":"high","impact":"Blocking all releases. The entire team is unable to merge and ship because CI is unreliable. The flakiness also erodes trust in the test suite.","recommended_fix":"Two options depending on intent: (1) If the application requires a stable sort, switch from sort.Slice to sort.SliceStable (or slices.SortStableFunc). (2) If stable ordering of equal elements is not a product requirement, fix the tests — either add a tiebreaker field (e.g., sort by priority then by ID) so no two tasks are 'equal', or relax the assertions to only verify the sort-key ordering without asserting the relative order of equal-key tasks.","proposed_test_case":"Create test data with at least two tasks that share the same sort key value. Assert only that the sort-key order is correct (task with higher priority comes first, etc.) without asserting anything about the relative order of same-key tasks. Run with -count=100 to confirm no flakiness.","information_gaps":["Exact sort key field(s) used and which specific test data entries share values (developer can read the test code directly)","Whether the application's product requirements demand stable ordering of equal-priority tasks (this would affect fix direction but the developer/PM can decide)"]}

---
