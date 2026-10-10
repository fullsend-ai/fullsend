# codex through a self-hosted inference gateway (#8295, ADR 0137). The
# runner presents the job's forge OIDC token (audience = the gateway's)
# through a run-scoped OpenShell provider; codex reaches the gateway's
# POST /v1/responses through the runner-owned fullsend-gateway provider
# rendered into its digest-guarded config.toml, and the gateway holds the
# upstream credential. A refused token, an unreachable gateway or a
# fallback to another route fails the workflow, so a successful run is the
# assertion.
#
# Gated on the `runtime-codex-gateway` capability, which is NOT declared by
# default. codex speaks the Responses API only, and fullsend renders no
# model catalog for it, so the model is pinned here; the inference.gateway model list the shared
# step commits for pi is not read on codex. The test gateway serves
# gpt-oss-120b on Responses. The gateway location comes from
# E2E_INFERENCE_GATEWAY_URL and E2E_INFERENCE_GATEWAY_AUDIENCE (default
# fullsend-e2e-gateway) and is never committed here; the scenario skips when
# the URL is unset. Enable with
# BEHAVIOUR_CAPABILITIES=runtime-pi,runtime-codex-gateway.
#
# The test gateway delivers gpt-oss-120b's usage after the finish chunk,
# so codex records 0 tokens for it; the token step exempts that model and
# requires that it answered instead.
Feature: codex runtime runs an agent through an inference gateway without a credential in the sandbox

  @requires:capability:runtime-codex-gateway
  Scenario: gateway run selects codex, calls tools through the hook adapter, and reports metrics
    Given the enrolled test repository
    And the test inference gateway is configured for the repository
    And the repository runtime is "codex"
    And a custom harness "codex-gateway-smoke" with:
      """
      agent: agents/codex-gateway-smoke.md
      role: triage
      slug: fullsend-ai-codex-gateway-smoke
      model: gateway/gpt-oss-120b
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      # A policy with no network rules of its own, so the only route to the
      # gateway host is the inspected one from the gateway profile the
      # runner renders for the configured block.
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-codex-gateway-smoke"
      # No providers, no host_files and no credential env: the runner
      # attaches the gateway provider and profile itself because the model
      # carries the gateway/ prefix and the repository has an
      # inference.gateway block, and a gateway-only codex run needs no
      # openai provider.
      """
    And a codex agent "codex-gateway-smoke" defined as:
      """
      ---
      name: codex-gateway-smoke
      description: Behaviour smoke agent for codex through an inference gateway.
      tools: Bash(ls), Write
      ---
      You are an unattended smoke-test agent. Do exactly the following, in
      order, then stop. Do not ask questions, do not explain, do not read or
      modify any other file.

      1. Using the shell, run: ls .
      2. Create the file /sandbox/workspace/output/agent-result.json with
         exactly this content and nothing else:

      {{fixture:fixtures/triage/sufficient.json}}
      """
    And an issue
    When the issue is labeled "ready-for-codex-gateway-smoke"
    Then the harness "codex-gateway-smoke" workflow completes successfully
    And the run selected the "codex" runtime
    And the codex output stream records at least one tool call
    And the run metrics report tokens unless the requested model is "gateway/gpt-oss-120b"
