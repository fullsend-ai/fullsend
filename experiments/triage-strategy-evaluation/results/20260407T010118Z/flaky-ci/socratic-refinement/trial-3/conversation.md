# Conversation: flaky-ci x socratic-refinement

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI is really frustrating, especially when it's blocking releases.

To start getting to the bottom of this: can you tell me more about what was happening around the time these failures started a few days ago? Were there any notable changes — new features being merged, dependency updates, infrastructure or CI configuration changes — or did it seem to come out of nowhere?

---

### reporter

Yeah, a PR got merged about 4 days ago that added some new tests. I guess that's probably when it started. But the tests pass fine on my machine so I don't see how that's the problem. Can we just get this fixed? It's blocking everything.

---

### triage-agent

That's a really useful lead — the timing lines up well with that PR.

I'd like to understand the failures a bit better: when tests fail in CI, are the ones failing the *newly added* tests from that PR, or are they *existing* tests that used to pass reliably? And is it the same tests each time, or a different subset on each run?

If you have a couple of recent failed CI run logs handy, that would also help me see the pattern — but even just your recollection of which tests tend to show up in the failures would be a great start.

---

### reporter

Yeah it's the new tests that fail. The old tests are fine. I can see the test names in the CI logs when they fail — they're all in the task ordering module. It's like 5 tests that were added in that PR. Sometimes they all pass, sometimes some of them fail. Look, I've already verified they pass locally, so something is weird with CI. Can someone just look at this?

---

### triage-agent

That narrows it down a lot — five new tests in the task ordering module, intermittently failing only in CI. That pattern almost always comes down to something being different about *how* the tests run in CI versus on your machine.

Can you tell me a bit about what those task ordering tests actually do? Specifically, do they set up their own test data, or do they rely on data that other tests create or a shared database state? And on the CI side — do you know if your CI runs tests in parallel or in a different order than when you run them locally?

That should help me figure out whether this is a test isolation issue or something else entirely.

---

### reporter

They create some tasks and then check that they come back in the right order after sorting. They set up their own data, it's not a shared state thing. I don't know about parallel vs not in CI — I didn't set up the CI config. Look, they pass locally every single time, never fail. Something is different about the CI environment. We're running Go 1.22 both places so it's not a version thing. Can someone just look at the actual test code instead of asking me 20 questions?

---

### triage-agent

[RESOLVED] {"title":"New task ordering tests fail intermittently in CI due to likely non-deterministic sort","problem":"Five tests added ~4 days ago in the task ordering module pass consistently on the developer's local machine but fail intermittently in CI. All failures are confined to these new tests; existing tests are unaffected.","root_cause_hypothesis":"The tests assert a specific element order after sorting, but the sort is non-deterministic for elements with equal sort keys. Go's sort.Slice uses an unstable sort algorithm, so items with equal keys can appear in any order between runs. The local machine likely produces a consistent order by coincidence (same hardware, memory layout, no parallelism), while CI exposes the non-determinism through parallel execution, the race detector, or different system characteristics.","reproduction_steps":["Identify the 5 new tests in the task ordering module from the merged PR (~4 days ago)","Run them repeatedly with: go test -count=100 -shuffle=on ./path/to/task/ordering/...","If that doesn't reproduce, try with the race detector: go test -race -count=100 -shuffle=on ./path/to/task/ordering/...","Check if CI runs with -race or -parallel flags that differ from local defaults"],"environment":"Go 1.22, CI environment (specific CI system unknown). Local development machine passes consistently.","severity":"high","impact":"Blocking releases for the team. Flaky CI erodes confidence in the test suite and slows all contributors.","recommended_fix":"1. Inspect the sort call in the task ordering module — if using sort.Slice, check whether the 'less' function fully disambiguates all elements (i.e., has a tiebreaker on a unique field like ID or created_at). 2. If elements can have equal sort keys, either add a stable tiebreaker to the sort, use sort.SliceStable, or update test assertions to only check ordering of elements with distinct keys (e.g., assert relative order only where sort keys differ). 3. Check CI configuration for -race, -parallel, or -shuffle flags that differ from local defaults.","proposed_test_case":"Create a test that generates multiple tasks with identical sort keys (e.g., same priority and due date) and verifies that the sort output is deterministic by running it in a loop (count=100+). The test should either confirm the tiebreaker produces a unique order or assert only the properties that the sort guarantees (e.g., group ordering rather than exact position of equal elements).","information_gaps":["Exact CI system and configuration (parallel flags, race detector, shuffle mode)","Whether the sort uses sort.Slice vs sort.SliceStable","Whether any test tasks share identical sort key values","Exact test names and assertion style (strict order vs. partial order)"]}

---
