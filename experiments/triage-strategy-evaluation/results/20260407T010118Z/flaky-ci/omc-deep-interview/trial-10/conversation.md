# Conversation: flaky-ci x omc-deep-interview

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI is really frustrating, especially when it blocks releases.

To start narrowing this down: when the failures happen, which tests are failing? Are they concentrated in a particular area of the test suite (e.g., database tests, API tests, UI tests), or truly scattered? And what do the failure messages or stack traces look like — are you seeing timeouts, assertion errors, connection failures, or something else?

---

### reporter

Look, I don't remember the exact test names off the top of my head but they're the ordering tests — the ones from that PR we merged last week. They're assertion failures, not timeouts or anything like that. The order of results comes back wrong sometimes. It's really annoying because they pass every single time on my machine.

---

### triage-agent

That's really helpful — non-deterministic ordering in results is a classic intermittent failure pattern.

Can you tell me how your CI environment differs from your local setup? Specifically: are you using the same database engine in both (e.g., SQLite locally vs. PostgreSQL in CI), and does CI run tests in parallel or in a different order than you do locally?

---

### reporter

It's not a database thing, we're using the same stack. Both are running Go 1.22. I'm on macOS and CI is Ubuntu but that shouldn't matter right? I don't think tests run in parallel differently either. I just don't get why it works perfectly fine on my machine every time but CI is a coin flip.

---

### triage-agent

That's a useful detail — same database, same Go version. The fact that results come back in the wrong order *sometimes* is a strong clue.

Could you take a look at the ordering tests from that recent PR and check: are they comparing results against a list in an expected order? And does the underlying query (or API call) that fetches those results include an explicit `ORDER BY` clause, or is it relying on the database to return rows in insertion order? Sometimes databases return rows in a consistent order on one OS or dataset size but not another, which would explain why macOS always works but Ubuntu CI doesn't.

---

### reporter

I already told you it's not a database issue. The tests check that tasks come back in the right order after sorting them. They're not querying a database, they're testing the task ordering logic in Go. Yeah they compare against an expected order — that's the whole point of an ordering test. It works on my machine every single time so the logic is clearly correct. Can we focus on why CI is broken instead of going in circles?

---

### triage-agent

You're right, let's move past the database angle — apologies for belaboring that.

Here's what's jumping out at me: in Go, `sort.Slice` and `slices.Sort` are **not stable sorts**, meaning if two tasks have the same value for whatever field you're sorting by (e.g., two tasks with the same priority or same timestamp), their relative order is undefined and can change between runs. It might happen to be consistent on your Mac but shuffle differently on CI.

Could you check the ordering tests from that PR and confirm: do any of the test cases include tasks that would be considered "equal" by the sort comparator (same priority, same date, etc.)? And is the code using `sort.Slice` / `slices.Sort` rather than `sort.SliceStable` / `slices.SortStableFunc`?

---

### reporter

I mean, maybe? I haven't dug into the sort implementation that closely because again, it works perfectly on my machine. Every. Single. Time. If the sort was broken it wouldn't pass locally either. Something about CI is different and that's what we need to figure out. I know we have a dev config that sets some Go flags locally — maybe that's relevant? I don't know. I just need this fixed, we've been blocked for 4 days now.

---

### triage-agent

That dev config with Go flags is probably the key piece here — it could easily explain why your machine always passes while CI doesn't. Could you share what flags that config sets? For example, anything related to `-shuffle`, `-count`, `-race`, or test ordering? If you're not sure where it lives, check for a `Makefile`, `.env`, or shell alias that wraps `go test`. Knowing exactly how your local test invocation differs from CI's would let us pinpoint the discrepancy fast.

---

### reporter

I think it sets GOFLAGS with -race and some seed thing? I don't remember exactly, I didn't set it up. It's in the dev config somewhere. CI doesn't use that config as far as I know — it just runs the tests normally. Can you just tell me what to fix? We've been going back and forth for a while now.

---

### triage-agent

[RESOLVED] {"title":"Flaky ordering tests from recent PR: unstable sort produces non-deterministic results in CI","problem":"Ordering tests introduced in a recent PR fail intermittently in CI (Ubuntu) with assertion errors — the task ordering comes back wrong. Tests pass consistently on the developer's local machine (macOS). This has been blocking releases for 4 days.","root_cause_hypothesis":"The task ordering code most likely uses Go's `sort.Slice` or `slices.Sort`, which are unstable sorts. When two tasks have equal sort keys (same priority, timestamp, etc.), their relative order is undefined and varies between runs. The developer's local environment has a dev config that sets GOFLAGS including a fixed shuffle seed (e.g., `-shuffle=12345`), which forces a deterministic test execution order that happens to mask the instability. CI runs without this config, exposing the non-determinism.","reproduction_steps":["Identify the ordering tests from the recently merged PR","Run them without the local dev config: `go test -count=10 -shuffle=on ./path/to/ordering/tests/...`","Observe intermittent assertion failures where tasks with equal sort keys appear in different orders across runs","Compare with running under the dev config's GOFLAGS to confirm the config masks the issue"],"environment":"CI: Ubuntu, Go 1.22, no special GOFLAGS. Local: macOS, Go 1.22, dev config sets GOFLAGS with -race and a seed value (exact contents not confirmed).","severity":"high","impact":"Blocking all releases for 4+ days. The entire team is affected. No workaround other than re-running CI until tests happen to pass.","recommended_fix":"1. Switch the sort from `sort.Slice`/`slices.Sort` to `sort.SliceStable`/`slices.SortStableFunc`, OR add a tiebreaker field (e.g., task ID) to the sort comparator so no two elements are ever equal. 2. Fix the tests to either assert stable ordering only on the distinguishing fields, or sort the expected and actual results by a unique key before comparing. 3. Audit the dev config — if it sets a fixed `-shuffle` seed, either remove it or replicate it in CI so local and CI behavior match. 4. Consider adding `-shuffle=on` to CI to proactively catch ordering assumptions in future tests.","proposed_test_case":"Create a test with multiple tasks sharing identical sort keys (e.g., same priority and timestamp) and verify that the sort output is deterministic by running with `-count=100`. The test should either assert on a stable sort or assert only on the primary ordering invariant (e.g., 'all priority-1 tasks appear before priority-2') without requiring a specific order among equal-priority tasks.","information_gaps":["Exact contents of the dev config GOFLAGS (specifically the seed value and other flags)","Exact test names and file paths for the failing ordering tests","Which sort function is used in the implementation (sort.Slice vs slices.Sort vs other)","Which fields the sort comparator uses and whether any test cases have equal values for those fields","Exact CI configuration (how go test is invoked)"]}

---
