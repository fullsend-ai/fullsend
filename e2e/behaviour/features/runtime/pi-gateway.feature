# pi through a self-hosted inference gateway (#8280 Tier B, ADR 0137). The
# runner presents the job's forge OIDC token (audience = the gateway's)
# through a run-scoped OpenShell provider; pi reaches the gateway through
# the pi-inference-gateway extension's `gateway` provider id, and the
# gateway holds the upstream credential. A refused token, an unreachable
# gateway or a fallback to another route fails the workflow, so a
# successful run is the assertion.
#
# Gated on the `runtime-pi-gateway` capability, which is NOT declared by
# default: it needs the durable test gateway (#8286) with one real upstream
# model. The gateway location comes from E2E_INFERENCE_GATEWAY_URL and
# E2E_INFERENCE_GATEWAY_AUDIENCE (default fullsend-e2e-gateway) and is
# never committed here; the scenario skips when the URL is unset. Enable
# with BEHAVIOUR_CAPABILITIES=runtime-pi,runtime-pi-gateway.
#
# The "test inference gateway" step also commits the model list pi needs
# (it runs offline): claude-haiku-5-5 on anthropic-messages. Some
# projects' org policy refuses strict tool schemas on partner models, so
# that entry sets compat supportsStrictTools: false.
Feature: pi runtime runs an agent through an inference gateway without a credential in the sandbox

  @requires:capability:runtime-pi-gateway
  Scenario: gateway run selects pi, calls tools through the hook adapter, and reports metrics
    Given the enrolled test repository
    And the test inference gateway is configured for the repository
    And the repository runtime is "pi"
    And a custom harness "pi-gateway-smoke" with:
      """
      agent: agents/pi-gateway-smoke.md
      role: triage
      slug: fullsend-ai-pi-gateway-smoke
      model: gateway/claude-haiku-5-5
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      # A policy with no network rules of its own, so the only route to the
      # gateway host is the inspected one from the gateway profile the
      # runner renders for the configured block (node only).
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-pi-gateway-smoke"
      # No host_files, no provider files and no credential env: the runner
      # attaches the gateway provider and profile itself because the model
      # carries the gateway/ prefix and the repository has an
      # inference.gateway block.
      """
    And a pi agent "pi-gateway-smoke" defined as:
      """
      ---
      name: pi-gateway-smoke
      description: Behaviour smoke agent for pi through an inference gateway.
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
    When the issue is labeled "ready-for-pi-gateway-smoke"
    Then the harness "pi-gateway-smoke" workflow completes successfully
    And the run selected the "pi" runtime
    And the pi session transcript records at least one tool call
    And the run metrics report tokens
