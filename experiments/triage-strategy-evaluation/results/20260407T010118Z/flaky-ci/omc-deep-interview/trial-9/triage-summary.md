# Triage Summary

**Title:** Flaky CI: task ordering tests fail intermittently due to non-deterministic map iteration

## Problem
Task ordering tests introduced in a PR merged approximately 4 days ago fail intermittently in CI with assertion errors, while passing consistently on the developer's local machine. The failures are blocking releases.

## Root Cause Hypothesis
The new tests collect tasks from a map (hash map) and then assert exact ordering. Map iteration order is not guaranteed and varies across runs and environments. The local development runtime likely stabilizes map iteration order (e.g., due to consistent memory layout, Go map seed, or JVM internals), while the CI environment does not, causing intermittent assertion failures when tasks come back in a different order.

## Reproduction Steps
  1. Identify the PR merged ~4 days ago that introduced the task ordering tests
  2. Locate the test(s) that pull tasks from a map and assert exact ordering
  3. Run the test suite repeatedly (e.g., 50-100 iterations) — the failure should eventually appear, especially with different random seeds or in a CI-like environment
  4. Alternatively, inspect the test code for map iteration without explicit sorting before order assertions

## Environment
CI environment (specific runner not confirmed). Failure does not reproduce locally. Tests use in-memory data structures, no database involved. Parallelism is not the cause (verified with -parallel 1 locally).

## Severity: high

## Impact
Blocking releases for the team. Failures are intermittent, eroding CI trust and causing developer frustration. All team members shipping through this pipeline are affected.

## Recommended Fix
In the test code from the recent PR, add an explicit deterministic sort (e.g., by task ID, name, or creation timestamp) after collecting tasks from the map and before asserting ordering. Alternatively, if the product behavior itself should guarantee an order, the fix belongs in the production code — ensure the task retrieval method returns a sorted collection rather than raw map values. If exact order is not part of the contract, switch assertions to order-independent checks (e.g., assert set equality instead of sequence equality).

## Proposed Test Case
Run the corrected ordering tests 100 times in a loop (or use a test repetition flag like `go test -count=100` or equivalent). All runs should pass consistently. Additionally, verify that the test correctly fails when the ordering is actually wrong (not just non-deterministic).

## Information Gaps
- Exact test file and test function names (discoverable from the PR merged ~4 days ago)
- Specific map type and language runtime (Go, Java, Python, etc.) — affects exact fix syntax but not fix direction
- Whether the ordering is a product requirement or just a test artifact — determines whether the fix goes in test code or production code
