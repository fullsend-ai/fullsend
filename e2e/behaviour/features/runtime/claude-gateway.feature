# Claude Code through a self-hosted inference gateway (#8294, ADR 0137). The
# runner presents the job's forge OIDC token (audience = the gateway's)
# through a run-scoped OpenShell provider; Claude Code reaches the gateway at
# the runner-owned ANTHROPIC_BASE_URL with an apiKeyHelper that re-reads the
# runner-seeded token file, and the gateway holds the upstream credential. A
# refused token, an unreachable gateway or a fallback to another route
# (Vertex included) fails the workflow, so a successful run is the assertion.
#
# Gated on the `runtime-claude-gateway` capability, which is NOT declared by
# default: it needs the durable test gateway (#8286) with one real upstream
# model. The gateway location comes from E2E_INFERENCE_GATEWAY_URL and
# E2E_INFERENCE_GATEWAY_AUDIENCE (default fullsend-e2e-gateway) and is never
# committed here; the scenario skips when the URL is unset. Enable with
# BEHAVIOUR_CAPABILITIES=runtime-pi,runtime-claude-gateway, against a sandbox
# image built from main (the published :latest may lag it).
#
# The "test inference gateway" step also commits pi's model list; Claude Code
# needs no list and ignores it. This runs in the oidc mode only: real
# runtimes run the api-key mode outside CI (#8280 part 4 covers the key path
# with the dummy runtime and an echo-only key).
Feature: Claude Code runs an agent through an inference gateway without a credential in the sandbox

  @requires:capability:runtime-claude-gateway
  Scenario: gateway run selects Claude Code and reports metrics
    Given the enrolled test repository
    And the test inference gateway is configured for the repository
    And the repository runtime is "claude"
    And a custom harness "claude-gateway-smoke" with:
      """
      agent: agents/claude-gateway-smoke.md
      role: triage
      slug: fullsend-ai-claude-gateway-smoke
      model: gateway/claude-haiku-5-5
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      # A policy with no network rules of its own, so the only route to the
      # gateway host is the inspected one from the Claude Code gateway
      # profile the runner renders for the configured block.
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-claude-gateway-smoke"
      # No host_files, no provider files and no credential env: the runner
      # attaches the gateway provider and profile itself because the model
      # carries the gateway/ prefix and the repository has an
      # inference.gateway block, and it skips the Vertex preflight.
      """
    And a claude agent "claude-gateway-smoke" defined as:
      """
      ---
      name: claude-gateway-smoke
      description: Behaviour smoke agent for Claude Code through an inference gateway.
      tools: Bash, Write
      ---
      You are an unattended smoke-test agent. Do exactly the following, in
      order, then stop. Do not ask questions, do not explain, do not read or
      modify any other file.

      1. Using the Bash tool, run: ls .
      2. Using the Write tool, create the file /sandbox/workspace/output/agent-result.json
         with exactly this content and nothing else:

      {{fixture:fixtures/triage/sufficient.json}}
      """
    And an issue
    When the issue is labeled "ready-for-claude-gateway-smoke"
    Then the harness "claude-gateway-smoke" workflow completes successfully
    And the run selected the "claude" runtime
    And the run metrics report tokens
