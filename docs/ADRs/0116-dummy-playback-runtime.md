---
title: "116. Dummy-playback runtime for multi-agent e2e scenarios"
status: Accepted
relates_to:
  - testing-agents
  - agent-infrastructure
topics:
  - e2e
  - testing
  - behaviour-tests
  - runtime
---

# 116. Dummy-playback runtime for multi-agent e2e scenarios

Date: 2026-09-14

## Status

Accepted

## Context

[ADR 0066](0066-behaviour-tests-with-gherkin-and-drivers.md) introduced behaviour tests with a **dummy runtime** that executes scripted operations inside the sandbox to verify platform infrastructure (dispatch routing, sandbox policy, post-scripts). The dummy runtime handles a single agent invocation per scenario — it runs one script of operations and exits.

Real fullsend scenarios involve multiple agents acting in sequence: triage labels an issue, code creates a PR, review examines it, fix pushes a correction. Testing these multi-agent flows requires each dispatch to produce a distinct, predetermined result — without calling an LLM. The dummy runtime cannot do this because it has no concept of ordered results or state that persists across independent sandbox invocations within one scenario.

## Options

**Extend the dummy runtime with playlist support.** This would add playlist semantics to the existing dummy runtime. Rejected because the two runtimes answer different questions: dummy runs sandbox probes (assert env vars exist, files are reachable, branches can be checked out) and reports pass/fail; playback writes canned agent output so the post-agent pipeline (PR creation, review posting) can be exercised. A single-entry playlist is not equivalent to a dummy script — sandbox assertions cannot be expressed as playlist entries. The two runtimes share infrastructure boilerplate (bootstrap, cleanup, transcript stubs) that can be extracted into a common base, but the core Run logic is genuinely distinct.

**Mock the forge API instead of running real workflows.** This would avoid the need for a mint and real CI entirely. Rejected because the value of behaviour tests is exercising the real dispatch → mint → sandbox → post-agent pipeline; mocking the forge removes exactly the integration surface these tests exist to cover.

**Record and replay HTTP traffic.** Capture real agent runs and replay the HTTP interactions. Rejected because recorded traffic is brittle across API changes, contains sensitive tokens, and still requires real infrastructure to produce the initial recording.

## Decision

Add a **dummy-playback** runtime that replays canned agent results from an ordered playlist committed to the test repository at `.fullsend/results/playlist.yaml`.

Each behaviour scenario commits a playlist with one entry per agent invocation (playlist provisioning will be handled by the Gherkin step definitions; not yet implemented). The playlist tracks a `current` index (1-based). On each invocation the runtime:

1. Reads `result.json` from the entry directory and writes it to `output/agent-result.json`.
2. Copies companion files to the workspace: code changes from `repo/` into the repository, other files to the workspace root.
3. If the entry includes code changes, commits them — to a new feature branch for code entries (simulating a new PR) or to the current branch for fix entries (pushing corrections to the existing PR).
4. If a tracking comment locator (`.fullsend/playback-comment-url`) is present, updates the **tracking comment** on a dedicated issue via the forge API to advance the playlist index (see [Playback comment tracking](../contributing/runtime-implementation.md#playback-comment-tracking) for the full contract). The tracking comment is the durable state that survives sandbox teardown between independent invocations — the next invocation reads it to discover the current position. Without the locator, the runtime falls back to the local playlist index.

Forge-specific result overrides (e.g. GitLab vs GitHub response shapes) will be handled at commit time by the Gherkin step, not at runtime — files under a `<forge>/` subdirectory in the entry will replace base files before the playlist is committed. This overlay mechanism is not yet implemented.

## Consequences

- Multi-agent e2e scenarios (triage → code → review → fix) can run against real CI with deterministic, repeatable results.
- No LLM inference cost or non-determinism in the behaviour test suite.
- The dummy runtime remains unchanged — it continues to serve single-agent sandbox verification scenarios as defined in [ADR 0066](0066-behaviour-tests-with-gherkin-and-drivers.md).
- Adding a new agent flow requires authoring canned result JSON files and a Gherkin scenario, not recording or mocking.
- Scenarios are portable across runtimes: substituting a real runtime runs the same pipeline end-to-end with live inference, and the Gherkin assertions (PR created, label applied, comment posted) should still hold.
- Playlist state coordination via tracking comments adds a dependency on forge API availability between sandbox invocations.
