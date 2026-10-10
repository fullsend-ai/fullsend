# Inference gateway route under the dummy runtime (#8280 Tier A, ADR 0137).
# The runner fetches the job's forge OIDC token with the gateway audience,
# seeds it into a run-scoped OpenShell provider as a placeholder, and the
# proxy swaps the placeholder for the real token on the way to the gateway.
# The dummy runtime's http_probe op sends `Authorization: Bearer $<env>`
# through node (the binary the gateway profile allows) and records the
# HTTP status and the first 4 KiB of the response body.
#
# Gated on the `inference-gateway` capability, which is NOT declared by
# default. It needs the durable test gateway (#8286): agentgateway with
# strict jwtAuth for the pool repositories, serving `echo` (authorised for
# the pool) and `echo-denied` (authorised only for another repository) in
# front of a header-echo stub upstream with a non-secret stub key.
#
# The gateway location is never committed here: the "test inference
# gateway" step reads E2E_INFERENCE_GATEWAY_URL and
# E2E_INFERENCE_GATEWAY_AUDIENCE (default fullsend-e2e-gateway), commits
# them as the repository's inference.gateway block, and skips the scenario
# when the URL is unset. `<gateway>` in http_probe args expands to the URL.
#
# http_probe args: METHOD URL HEADER_ENV [BODY].
#
# The placeholder variable is the gateway provider's credential key,
# INFERENCE_GATEWAY_API_KEY (profiles/fullsend-inference-gateway.yaml).
# assert_not_jwt checks that a variable does not hold a JWT and, when it
# names a file, that the file is readable and does not hold one either.
# The dummy runtime does not implement the gateway route runtime, so it
# prepares no token file and exports no INFERENCE_GATEWAY_TOKEN_FILE: only
# the placeholder variable can be asserted here. wait sleeps between ops, so the re-seed scenario outlasts one
# token lifetime (exp - iat). "the harness workflow logs show the inference
# gateway provider was cleaned up" checks the run-scoped provider's removal
# in the completed harness run's logs.
Feature: inference gateway route under the dummy runtime

  Background:
    Given the enrolled test repository
    And the test inference gateway is configured for the repository
    And a custom harness "gateway-probe" with:
      """
      agent: agents/gateway-probe.md
      role: triage
      slug: fullsend-ai-gateway-probe
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      # No network rules of its own, so the only route to the gateway host
      # is the inspected one from the gateway profile the runner renders
      # for the configured block (node only, POST /v1/chat/completions).
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-gateway-probe"
      """

  @requires:capability:inference-gateway
  Scenario: the sandbox holds only the placeholder
    Given a dummy agent that would:
      | description             | op            | args                                                      |
      | See the token file path | assert_env    | INFERENCE_GATEWAY_TOKEN_FILE                              |
      | See the placeholder     | assert_env    | INFERENCE_GATEWAY_API_KEY                                  |
      | Placeholder is no JWT   | assert_not_jwt | INFERENCE_GATEWAY_API_KEY                                |
      | Emit triage JSON        | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json |
    And an issue
    When the issue is labeled "ready-for-gateway-probe"
    Then the harness "gateway-probe" workflow completes successfully
    And the agent will succeed to See the token file path
    And the agent will succeed to See the placeholder
    And the agent will succeed to Placeholder is no JWT
    And the agent will succeed to Emit triage JSON

  @requires:capability:inference-gateway
  Scenario: the proxy injects the credential and the gateway accepts it
    Given a dummy agent that would:
      | description      | op            | args                                                                                                                       |
      | Call the gateway | http_probe    | POST <gateway>/v1/chat/completions INFERENCE_GATEWAY_API_KEY {"model":"echo","messages":[{"role":"user","content":"ping"}]} |
      | Emit triage JSON | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json                                                                  |
    And an issue
    When the issue is labeled "ready-for-gateway-probe"
    Then the harness "gateway-probe" workflow completes successfully
    And the agent will succeed to Call the gateway
    And the agent's probe "Call the gateway" returned HTTP 200
    # Custody: the stub upstream echoes the headers it received. The
    # gateway strips the caller's token and adds its own stub key, so the
    # forge OIDC token (a JWT, "eyJ...") never reaches the upstream.
    And the agent's probe "Call the gateway" response contains "x-api-key"
    And the agent's probe "Call the gateway" response does not contain "eyJ"
    And the harness workflow logs show the inference gateway provider was cleaned up

  @requires:capability:inference-gateway
  Scenario: egress is scoped to the gateway's inference path
    Given a dummy agent that would:
      | description               | op            | args                                                      |
      | List models on gateway    | http_probe    | GET <gateway>/v1/models INFERENCE_GATEWAY_API_KEY          |
      | Reach another host        | http_probe    | GET https://example.com/ INFERENCE_GATEWAY_API_KEY         |
      | Emit triage JSON          | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json |
    And an issue
    When the issue is labeled "ready-for-gateway-probe"
    Then the harness "gateway-probe" workflow completes successfully
    And the agent will fail to List models on gateway
    And the agent will fail to Reach another host
    And the agent will succeed to Emit triage JSON

  @requires:capability:inference-gateway
  Scenario: a model authorised only for another repository is refused
    Given a dummy agent that would:
      | description        | op            | args                                                                                                                              |
      | Call allowed model | http_probe    | POST <gateway>/v1/chat/completions INFERENCE_GATEWAY_API_KEY {"model":"echo","messages":[{"role":"user","content":"ping"}]}        |
      | Call denied model  | http_probe    | POST <gateway>/v1/chat/completions INFERENCE_GATEWAY_API_KEY {"model":"echo-denied","messages":[{"role":"user","content":"ping"}]} |
      | Emit triage JSON   | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json                                                                         |
    And an issue
    When the issue is labeled "ready-for-gateway-probe"
    Then the harness "gateway-probe" workflow completes successfully
    And the agent's probe "Call allowed model" returned HTTP 200
    And the agent will fail to Call denied model
    And the agent's probe "Call denied model" returned HTTP 403

  # Re-seed: the probe must still succeed after one token lifetime. The wait
  # outlasts a 300 s GitHub OIDC token. Because it holds the sandbox for
  # more than five minutes, it is also gated on its own capability, which
  # a maintainer declares when the CI budget allows.
  @requires:capability:inference-gateway @requires:capability:inference-gateway-reseed
  Scenario: the credential is re-seeded after the token expires
    Given a dummy agent that would:
      | description       | op            | args                                                                                                                       |
      | Call before wait  | http_probe    | POST <gateway>/v1/chat/completions INFERENCE_GATEWAY_API_KEY {"model":"echo","messages":[{"role":"user","content":"ping"}]} |
      | Outlast the token | wait          | 330                                                                                                                        |
      | Call after expiry | http_probe    | POST <gateway>/v1/chat/completions INFERENCE_GATEWAY_API_KEY {"model":"echo","messages":[{"role":"user","content":"ping"}]} |
      | Emit triage JSON  | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json                                                                  |
    And an issue
    When the issue is labeled "ready-for-gateway-probe"
    Then the harness "gateway-probe" workflow completes successfully
    And the agent's probe "Call before wait" returned HTTP 200
    And the agent's probe "Call after expiry" returned HTTP 200
