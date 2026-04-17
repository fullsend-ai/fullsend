# Conversation: flaky-ci x socratic-refinement

## Issue

**Tests keep failing in CI but pass on my machine**

Our CI pipeline has been randomly failing for the past few days. The test suite passes fine when I run it locally. The failures seem random — sometimes all tests pass, sometimes a handful fail. It's blocking our releases and we can't figure out what's going on.

---

## Comments

### triage-agent

Thanks for reporting this — flaky CI is really frustrating, especially when it's blocking releases.

To help me understand the bigger picture: can you walk me through what was happening around the time this started? You mentioned it's been going on for a few days — did anything change around then? A new dependency, a CI configuration update, a merge that touched test infrastructure? Even changes that seem unrelated could be helpful.

Also, when the failures do happen, what do they look like? For example, are you seeing timeouts, assertion errors, connection failures, or something else entirely?

---

### reporter

Look, I don't remember every little thing that changed. A PR got merged a few days ago that added some new tests, maybe that's related, I don't know. The failures are assertion errors — the tests expect a certain order of tasks and sometimes they come back in a different order. It works fine on my machine every single time. Can someone just look at the CI logs? The failing test names are right there.

---

### triage-agent

That ordering detail is really helpful — that narrows things down a lot. Tests expecting a specific order but getting a different one is a classic pattern with a few known causes.

One thing that would help me point a developer in the right direction: are the tests that fail the *new* ones from that PR, or are they *existing* tests that started failing after the PR was merged? (If you're not sure, even a rough sense — like whether the failing test names look familiar or unfamiliar — would help.)

I ask because the fix is different depending on the answer: if the new tests are the ones failing, they likely need to stop assuming a specific order. If existing tests broke, the new PR may have changed how tasks are stored or retrieved in a way that affects ordering.

---

### reporter

Yeah it's the new tests that are failing. The ones from that PR. The old tests never had this problem. Like I said, they pass every time on my machine so I don't see how they're wrong, but whatever. Can we just get this fixed?

---

### triage-agent

[RESOLVED] {"title":"New tests from recent PR fail intermittently in CI due to non-deterministic task ordering","problem":"Tests added in a recent PR assert that tasks are returned in a specific order. In CI, the order is sometimes different, causing assertion failures. Locally, the tests always pass — likely because the local environment happens to return tasks in a consistent order.","root_cause_hypothesis":"The new tests assume deterministic ordering of task results, but the underlying query or data source does not guarantee order. The local environment (e.g., SQLite with implicit insertion-order, or single-threaded execution) masks this by coincidentally returning results in the expected order, while CI (e.g., PostgreSQL, parallel workers, or different data seeding) exposes the non-determinism.","reproduction_steps":["Identify the failing test names from CI logs (reporter confirms they are visible there)","Examine the assertions in those tests — look for exact-order comparisons of task lists","Check the query or API call that produces the task list — confirm it lacks an explicit ORDER BY or sort","Run the tests repeatedly in CI or with a shuffled/randomized DB to reproduce locally"],"environment":"CI pipeline (specific runner/DB configuration unknown but differs from local). Local environment passes consistently, suggesting it implicitly preserves insertion order.","severity":"high","impact":"Blocking releases for the team. CI pipeline is unreliable, forcing re-runs and eroding confidence in the test suite.","recommended_fix":"Two-pronged fix: (1) If the application should return tasks in a defined order, add an explicit ORDER BY / sort to the relevant query or API endpoint, and the tests are correct to assert order. (2) If the order is not meaningful, update the new tests to use order-independent assertions (e.g., sort both lists before comparing, or use set-based comparison). Also review whether the PR that introduced the tests intended a specific ordering contract — if so, the production code needs the fix, not the tests.","proposed_test_case":"A test that inserts tasks in a randomized order (or runs with a database that does not preserve insertion order) and verifies the API returns them in the expected sorted order — confirming the ordering contract is enforced by the application, not by accident.","information_gaps":["Exact CI environment (database engine, test parallelism settings) vs. local — would confirm the mechanism but not change the fix","Specific PR number and failing test names — developer can find these in CI logs and git history","Whether the intended behavior is ordered or unordered task retrieval — developer should determine this from product requirements before choosing fix direction"]}

---
