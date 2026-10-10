# Inference gateway route in the api-key mode under the dummy runtime
# (#8280, ADR 0138). The runner reads FULLSEND_INFERENCE_GATEWAY_API_KEY,
# puts it behind the same run-scoped OpenShell provider as the oidc mode,
# and the proxy swaps the placeholder for the key on the way to the
# gateway. The gateway checks the key, then calls its upstream with its
# own credential, so the key never travels further.
#
# Gated on the `inference-gateway` and `inference-gateway-api-key`
# capabilities, which are NOT declared by default. It needs the durable
# test gateway (#8286) with a test key authorised for the `echo` model
# only, and that key provisioned two ways:
#   - as the pool repositories' FULLSEND_INFERENCE_GATEWAY_API_KEY secret,
#     passed through to the runner by the reusable workflow;
#   - as E2E_INFERENCE_GATEWAY_TEST_KEY in the suite's environment, used
#     only to check the key never reaches the upstream (never committed,
#     redacted from the suite's logs).
# The scenario skips when E2E_INFERENCE_GATEWAY_URL or
# E2E_INFERENCE_GATEWAY_TEST_KEY is unset. The gateway answers a model the
# key is not authorised for with 403.
Feature: inference gateway route in the api-key mode under the dummy runtime

  @requires:capability:inference-gateway @requires:capability:inference-gateway-api-key
  Scenario: the proxy injects the gateway key and the gateway authorises only its model
    Given the enrolled test repository
    And the test inference gateway is configured for the repository with an API key
    And a custom harness "gateway-key-probe" with:
      """
      agent: agents/gateway-key-probe.md
      role: triage
      slug: fullsend-ai-gateway-key-probe
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      # No network rules of its own, so the only route to the gateway host
      # is the inspected one from the gateway profile the runner renders.
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-gateway-key-probe"
      """
    And a dummy agent that would:
      | description          | op             | args                                                                                                                                  |
      | See the placeholder  | assert_env     | INFERENCE_GATEWAY_API_KEY                                                                                                             |
      | Call the echo model  | http_probe     | POST <gateway>/v1/chat/completions INFERENCE_GATEWAY_API_KEY {"model":"echo","messages":[{"role":"user","content":"ping"}]}            |
      | Call a real model    | http_probe     | POST <gateway>/v1/chat/completions INFERENCE_GATEWAY_API_KEY {"model":"claude-haiku-5-5","messages":[{"role":"user","content":"ping"}]} |
      | Emit triage JSON     | write_fixture  | output/agent-result.json, fixtures/triage/sufficient.json                                                                             |
    And an issue
    When the issue is labeled "ready-for-gateway-key-probe"
    Then the harness "gateway-key-probe" workflow completes successfully
    And the agent will succeed to See the placeholder
    And the agent's probe "Call the echo model" returned HTTP 200
    # Custody: the stub upstream echoes the headers it received. The
    # gateway strips the caller's key and sends its own stub credential.
    And the agent's probe "Call the echo model" response does not contain the test inference gateway API key
    And the agent will fail to Call a real model
    And the agent's probe "Call a real model" returned HTTP 403
    And the harness workflow logs show the inference gateway provider was cleaned up
