# Conversation: flaky-ci x omc-deep-interview

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — intermittent CI failures are frustrating, especially when they block releases.

Could you share what the actual failure output looks like? Specifically: which tests are failing (is it always the same ones, or different tests each run?), and what do the error messages or stack traces say? A link to a recent failed CI run would be ideal if you have one.

---

### reporter

It's always the same tests that fail — they're in the task ordering module. The ones from that PR we merged last week. I don't have a link handy but the test names are right there in the CI logs. They just randomly pass or fail, it's incredibly frustrating. Can we just get someone to look at this?

---

### triage-agent

Totally understand the urgency — let's get this sorted out.

When those task ordering tests fail, what kind of failure is it? For example, are you seeing assertion errors where items come back in the wrong order, timeouts, or something else entirely? Even a rough description of the error message (like "expected [A, B, C] but got [B, A, C]") would help narrow this down.

Also — do those tests rely on any shared state like a database, cache, or temp files, or are they purely in-memory unit tests?

---

### reporter

Yeah it's the ordering — the tests expect tasks in a certain order and they come back jumbled. Like exactly what you said, expected one order, got another. They're just unit tests, no database or anything like that. Look, I've already spent way too long staring at this. They pass every single time on my machine. Every time. It's only CI that has the problem.

---

### triage-agent

That's really helpful — ordering assertion failures on the same tests every time is a strong signal. The fact that they always pass locally but randomly fail in CI usually comes down to an environmental difference.

A couple of things to check: does your CI run tests in parallel (e.g. with pytest-xdist or a similar runner), or in a randomized order? And what does the CI environment look like compared to your local setup — same OS, same language/runtime version?

Alternately, have you tried running just those task ordering tests in a tight loop locally (say, 50–100 times)? Sometimes non-determinism only shows up at volume. Something like `for i in $(seq 100); do pytest tests/test_task_ordering.py || break; done` can flush out a flaky test that seems to always pass on a single run.

---

### reporter

It's Go, not Python — `go test`, not pytest. CI runs on Ubuntu, I'm on macOS, both Go 1.22. I haven't tried running them in a loop because they literally pass every single time locally, I don't see how running them 100 more times is going to change that. Can someone just look at the actual tests? They were added like 4 days ago in that task ordering PR and everything was fine before that.

---

### triage-agent

Heard — I know you want eyes on the code rather than more questions, so this is the one thing that would let us point a developer at the exact problem:

In that task ordering PR, does the code collect or group tasks using a Go map (like `map[string][]Task` or similar) before returning them in order? Map iteration order in Go is intentionally randomized by the runtime, so code that iterates a map to build results will produce consistent-looking output on one machine but scrambled output on another — which matches your symptoms exactly.

If you're not sure, even just the file name from that PR would help. A developer could confirm in about 30 seconds by checking for map iteration in the return path.

---

### reporter

Yeah I mean, I didn't write the code myself but that sounds like it could be the thing. I know it's doing some kind of sorting or grouping of tasks. Look, can you just assign someone to look at the PR? It's the task ordering tests that were added about 4 days ago, there's only like 5 new tests. It shouldn't take long for someone who knows the codebase to spot the issue. I just need CI to stop being broken.

---

### triage-agent

[RESOLVED] {"title":"Task ordering tests fail intermittently in CI due to likely Go map iteration non-determinism","problem":"Unit tests in the task ordering module, added approximately 4 days ago, produce intermittent assertion failures in CI (Ubuntu) where tasks come back in an unexpected order. The same tests pass consistently on the author's local machine (macOS). The failures are blocking releases.","root_cause_hypothesis":"The task ordering code almost certainly iterates over a Go map (e.g. map[string][]Task or similar) to build its result set without explicitly sorting afterward. Go randomizes map iteration order at runtime, but the degree of observable randomness can vary across OS, hardware, and runtime scheduling. This explains why results appear stable on one developer's macOS machine but scramble in CI on Ubuntu.","reproduction_steps":["Check out the task ordering PR merged approximately 4 days ago","Run the task ordering tests in a loop: go test -count=100 -run TestTaskOrdering ./path/to/task/ordering/...","If that doesn't reproduce locally, run with -race flag or on an Ubuntu environment matching CI","Alternatively, inspect the code path for map iteration feeding into the returned slice — the bug will be visible by reading the code"],"environment":"CI: Ubuntu, Go 1.22. Local (reporter): macOS, Go 1.22. Test runner: go test (standard).","severity":"high","impact":"Blocking all releases for the team. Every CI run has a chance of spurious failure, eroding confidence in the test suite and slowing the entire development workflow.","recommended_fix":"Find the map iteration in the task ordering return path (likely in the grouping/sorting logic added in the recent PR). After collecting results from the map, sort the output slice explicitly using sort.Slice or slices.SortFunc before returning. This ensures deterministic ordering regardless of map iteration order. Verify by running go test -count=100 on the affected tests.","proposed_test_case":"Run the existing task ordering tests with -count=1000 to confirm they pass deterministically after the fix. Optionally add a test that explicitly constructs a map with multiple keys, iterates it 100 times, and asserts the output order is stable after the sort step.","information_gaps":["Exact PR number or branch name (easily found by searching recent merges in the task ordering module)","Exact file path of the affected tests and production code (discoverable from the PR)","Whether CI uses any test parallelism flags like -parallel beyond Go's defaults (unlikely to be the primary cause but worth confirming)"]}

---
