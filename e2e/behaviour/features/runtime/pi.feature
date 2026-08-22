# Runtime-specific coverage for pi (#6464). The rest of the suite runs every
# stage under the dummy runtime, which proves dispatch, harness loading and
# the sandbox boundary but never exercises a real runtime's Bootstrap/Run,
# the sandbox hook adapter, the Vertex credential path, or the stream
# parser. This scenario does, on the same leased per-repo install, by
# selecting pi for one run of a minimal agent whose tool use is deliberate
# (the custom-harness step only commits a placeholder agent, which would
# give a real model no task).
#
# Gated on the `runtime-pi` capability: the harness pins
# fullsend-sandbox:latest, which only carries the pinned pi CLI once the
# image change is published from main. Enable with
# BEHAVIOUR_CAPABILITIES=runtime-pi (costs one small haiku run on Vertex).
Feature: pi runtime runs an agent unattended

  @requires:capability:runtime-pi
  Scenario: pi run selects the runtime, calls tools through the hook adapter, and reports metrics
    Given the enrolled test repository
    And the repository runtime is "pi"
    And a custom harness "pi-smoke" with:
      """
      agent: agents/pi-smoke.md
      role: triage
      slug: fullsend-ai-pi-smoke
      model: haiku
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-pi-smoke"
      """
    And a pi agent "pi-smoke" defined as:
      """
      ---
      name: pi-smoke
      description: Behaviour smoke agent for the pi runtime.
      tools: Bash(ls), Write
      ---
      You are an unattended smoke-test agent. Do exactly the following, in
      order, then stop. Do not ask questions, do not explain, do not read or
      modify any other file.

      1. Using the bash tool, run: ls .
      2. Using the write tool, create the file /sandbox/workspace/output/agent-result.json
         with exactly this content and nothing else:

      {{fixture:fixtures/triage/sufficient.json}}
      """
    And an issue
    When the issue is labeled "ready-for-pi-smoke"
    Then the harness "pi-smoke" workflow completes successfully
    And the run selected the "pi" runtime
    And the pi session transcript records at least one tool call
    And the run metrics report tokens
