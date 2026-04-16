# Conversation: flaky-ci x omc-deep-interview

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI is really frustrating, especially when it's blocking releases.

To start narrowing this down: could you share the error output from a recent failed CI run? Specifically, which tests are failing and what the failure messages look like. Even a couple of examples would help — I'm trying to understand whether it's always the same tests failing or truly different ones each time.

---

### reporter

I don't have the logs in front of me right now but it's the ordering tests that fail. The ones from that PR we merged last week. They don't fail every time which is what's so annoying — I've been just re-running the pipeline and sometimes it goes green. Look, they pass fine on my machine every single time, so something is wrong with CI.

---

### triage-agent

That's helpful context — knowing it's the ordering tests from last week's PR gives us a good starting point.

When you get a chance, could you grab the failure output from one of those failed CI runs? I'm particularly interested in whether the tests are failing with assertion errors (e.g., unexpected ordering of results) or something else like timeouts or connection errors. Even a quick copy-paste of the error for one or two failing tests would really help narrow down whether this is a test isolation issue, a race condition, or something environment-specific.

---

### reporter

Yeah they're assertion errors. The expected order doesn't match the actual order. Like I said they pass every time on my machine so I don't really see what more the logs are going to tell you. Can we just figure out why CI is different?

---

### triage-agent

Fair point — let's focus on the difference between CI and your machine rather than the logs.

Do the ordering tests from that PR sort results explicitly (e.g., an `ORDER BY` clause or a `.sort()` call), or do they assert on the order that results come back naturally? I'm asking because some databases and drivers return rows in a consistent-looking order on a single developer machine but shuffle them in CI — especially if CI uses a different database engine, a fresh database each run, or runs tests in parallel.

Also, quick check: does your CI run tests in parallel or with any concurrency, where locally you might be running them sequentially?

---

### reporter

I don't know the exact implementation details off the top of my head, I didn't write the tests — a teammate did in that PR. But I'm pretty sure they just check that the tasks come back in the right order after sorting. I don't think there's any database involved, it's all in-memory stuff. And yeah CI might run things in parallel, I'm not sure about that. But look, it's the same Go version on both, so why would the ordering be different? That's the part I don't get.

---

### triage-agent

Good question — same Go version doesn't guarantee identical behavior if the test runner is invoked differently. A couple of things that commonly cause exactly this pattern:

Could you check how your CI pipeline runs the tests? Specifically, does the CI config (e.g., your pipeline YAML or Makefile target) pass any flags like `-shuffle`, `-race`, `-count`, or `-parallel` that you might not be using when you run `go test` locally? Even something like `go test ./...` vs. `go test ./pkg/ordering/...` can change parallelism defaults.

If you can share the relevant line from your CI config and the command you run locally, that would help us pinpoint the difference.

---

### reporter

I just run `go test ./...` locally, nothing special. I think we have some dev config that sets GOFLAGS or something but I haven't touched it. I don't know what the CI config looks like off the top of my head, I'd have to go dig through the pipeline yaml. Can't someone on the team just look at it? I'm kind of swamped right now trying to work around these failures.

---

### triage-agent

Totally understand you're swamped — I think we're close to cracking this and I want to minimize the back-and-forth.

You mentioned a dev config that sets `GOFLAGS` or something similar. Could you quickly check what that's set to? For example, run `echo $GOFLAGS` in the terminal where you normally run tests, or check your `.envrc` / `Makefile` / `direnv` setup. If it includes something like `-count=1` (which disables test caching and can mask ordering issues) or notably does *not* include `-shuffle=on`, that could explain exactly why your local runs are stable while CI isn't.

That one value might be all we need to close this out — and if not, I'd suggest looping in whoever owns the CI config to check the test invocation there.

---

### reporter

Fine, I checked. It's set to `-race` and there's something about a hash seed being fixed, I don't really remember setting that up, it was just part of the dev setup. Look I don't know what half of that stuff does, I just know my tests pass locally and they don't in CI. Can we just fix it?

---

### triage-agent

[RESOLVED] {"title":"Ordering tests fail intermittently in CI due to non-deterministic map iteration (fixed hash seed locally, randomized in CI)","problem":"Tests added in a recent PR that verify task ordering fail with assertion errors intermittently in CI but pass consistently on developer machines. The expected order of results does not match the actual order returned.","root_cause_hypothesis":"The ordering tests rely on the natural iteration order of a Go map or similar unordered data structure. The local dev environment has a fixed hash seed (confirmed via GOFLAGS/env config), which makes map iteration order deterministic and repeatable locally. CI does not fix the hash seed, so Go's intentional map iteration randomization causes the order to vary between runs. The tests likely either (a) assert on results without explicitly sorting them first, or (b) sort by a non-unique key without a stable tiebreaker, exposing the underlying nondeterminism.","reproduction_steps":["Identify the ordering tests from the recently merged PR (likely in a package related to task ordering/sorting)","Run the tests with a randomized hash seed: `GOFLAGS='' go test -count=100 -shuffle=on ./path/to/ordering/tests/...`","Observe intermittent assertion failures where actual result order differs from expected","Compare to running with the dev environment's fixed hash seed, where tests pass consistently"],"environment":"Go (same version locally and CI). Local dev setup includes GOFLAGS with -race and a fixed hash seed. CI environment does not fix the hash seed. CI may also run tests in parallel or with -shuffle, further exposing the nondeterminism.","severity":"high","impact":"Blocking releases. The team is working around failures by re-running the CI pipeline until tests happen to pass, which wastes CI resources and delays deployments. All team members are affected.","recommended_fix":"1. Locate the ordering tests from the recent PR and examine what data structure holds results before assertion. 2. If results come from map iteration or any unordered source, add an explicit sort (e.g., `sort.Slice()`) on deterministic keys before asserting order. 3. If sorting by a field that can have duplicates, add a tiebreaker (e.g., sort by priority then by ID). 4. Remove or stop relying on the fixed hash seed in local dev config — it masks real nondeterminism bugs. 5. Consider adding `-shuffle=on` to the local GOFLAGS so developers catch these issues before CI does.","proposed_test_case":"Run the ordering tests with `-count=50 -shuffle=on` and no fixed hash seed. All 50 runs should pass consistently. Additionally, add a test case that creates tasks with identical sort keys to verify the tiebreaker produces stable ordering.","information_gaps":["Exact test file and function names from the merged PR (team can identify from recent PR history)","Exact CI pipeline config and test invocation command (team should verify CI does not fix the hash seed)","The specific fixed hash seed env var name in the dev setup (reporter mentioned it but wasn't sure of details)","Whether CI also uses -shuffle=on or -parallel flags that compound the issue"]}

---
