# Inference gateway route under the dummy runtime (#8280 Tier A, ADR 0137).
# The runner fetches the job's forge OIDC token with the gateway audience,
# seeds it into a run-scoped OpenShell provider as a placeholder, and the
# proxy swaps the placeholder for the real token on the way to the gateway.
# The dummy runtime's http_probe op sends `Authorization: Bearer $<env>`
# through node (the binary the gateway profile allows) and records the
# HTTP status and the first 4 KiB of the response body.
#
# Gated on the `inference-gateway` capability, which `make behaviour-test`
# declares by default (it reaches no real model). It needs the durable test
# gateway (#8286): agentgateway with jwtAuth for the pool repositories,
# serving `echo` (authorised for the pool) and `echo-denied` (authorised
# only for another repository) in front of a header-echo stub upstream
# with a non-secret stub key.
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
# assert_not_jwt checks that a variable does not hold a JWT. The dummy
# runtime carries no gateway token file (that is pi's, re-seeded on each
# refresh; runtime/pi-gateway.feature covers it), and each op is its own
# sandbox exec, so a probe always sends the placeholder the sandbox hands
# out at that moment. wait sleeps between ops, so the rotation scenario
# outlasts one token lifetime (exp - iat). "the harness workflow logs show
# the inference gateway provider was cleaned up" checks the run-scoped
# provider's removal in the completed harness run's logs.
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

  # One harness run carries every check, so CI pays for a single repo
  # lease and workflow run: the sandbox sees only the placeholder, the
  # proxy injects the credential and the gateway accepts it, egress is
  # scoped to the gateway's inference path, and a model authorised only
  # for another repository is refused.
  @requires:capability:inference-gateway
  Scenario: the gateway route keeps the credential out of the sandbox and scoped to its model path
    Given a dummy agent that would:
      | description            | op             | args                                                                                                                              |
      | See the placeholder    | assert_env     | INFERENCE_GATEWAY_API_KEY                                                                                                         |
      | Placeholder is no JWT  | assert_not_jwt | INFERENCE_GATEWAY_API_KEY                                                                                                         |
      | Call the gateway       | http_probe     | POST <gateway>/v1/chat/completions INFERENCE_GATEWAY_API_KEY {"model":"echo","messages":[{"role":"user","content":"ping"}]}        |
      | Call denied model      | http_probe     | POST <gateway>/v1/chat/completions INFERENCE_GATEWAY_API_KEY {"model":"echo-denied","messages":[{"role":"user","content":"ping"}]} |
      | List models on gateway | http_probe     | GET <gateway>/v1/models INFERENCE_GATEWAY_API_KEY                                                                                 |
      | Reach another host     | http_probe     | GET https://example.com/ INFERENCE_GATEWAY_API_KEY                                                                                |
      | Emit triage JSON       | write_fixture  | output/agent-result.json, fixtures/triage/sufficient.json                                                                         |
    And an issue
    When the issue is labeled "ready-for-gateway-probe"
    Then the harness "gateway-probe" workflow completes successfully
    And the agent will succeed to See the placeholder
    And the agent will succeed to Placeholder is no JWT
    And the agent's probe "Call the gateway" returned HTTP 200
    # Custody: the stub upstream reports which credential it received:
    # STUB_KEY for the gateway's own stub key, "OTHER len=N" for anything
    # else. The gateway strips the caller's token and sends its stub key,
    # so the forge OIDC token (a JWT, "eyJ...") never reaches the upstream.
    And the agent's probe "Call the gateway" response contains "STUB_KEY"
    And the agent's probe "Call the gateway" response does not contain "OTHER len="
    And the agent's probe "Call the gateway" response does not contain "eyJ"
    And the agent will fail to Call denied model
    And the agent's probe "Call denied model" returned HTTP 403
    And the agent will fail to List models on gateway
    And the agent will fail to Reach another host
    And the agent will succeed to Emit triage JSON
    And the harness workflow logs show the inference gateway provider was cleaned up

  # Rotation: the probe must still succeed after one token lifetime, so the
  # runner refreshed the provider's token. The wait outlasts a 300 s GitHub
  # OIDC token. Each probe is a new exec carrying the current placeholder,
  # so this covers the provider refresh, not the token-file re-seed (pi's,
  # runtime/pi-gateway.feature). Because it holds the sandbox for more than
  # five minutes, it is also gated on its own capability, which a
  # maintainer declares when the CI budget allows.
  @requires:capability:inference-gateway @requires:capability:inference-gateway-reseed
  Scenario: the credential still resolves after the token rotates
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
