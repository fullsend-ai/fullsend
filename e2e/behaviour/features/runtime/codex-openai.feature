# GPT on the codex runtime through OpenAI Workload Identity Federation
# (#6920, ADR 0092). Codex has no Vertex path, so unlike pi — whose
# runtime-pi scenario runs on every behaviour job — this is the *only*
# behaviour coverage codex has, and it is gated. Until an OpenAI
# organization is mapped to the pool repositories, codex has no default
# behaviour-test coverage at all; its evidence is unit tests, recorded
# fixtures and local smoke runs.
#
# What the scenario proves that the unit tests cannot: the repo's
# `runtime: codex` reaches backend selection, the runner exchanges the
# job's OIDC token and carries it in a run-scoped OpenShell provider, and
# codex reaches api.openai.com holding nothing but the gateway's
# placeholder. A failed exchange or a rejected placeholder fails the
# workflow, so a successful run is the assertion.
#
# Gated on the `runtime-codex-openai` capability, which is NOT declared by
# default: it needs an OpenAI organization with a Workload Identity
# Provider and a service-account mapping for the pool repositories, plus
# the three FULLSEND_OPENAI_* repository variables on them (see
# docs/guides/infrastructure/openai-workload-identity.md). Enable with
# BEHAVIOUR_CAPABILITIES=runtime-pi,runtime-codex-openai (costs one small
# gpt-5.6-luna run).
Feature: codex runtime runs an agent on OpenAI without a credential in the sandbox

  @requires:capability:runtime-codex-openai
  Scenario: OpenAI WIF run selects codex, calls tools through the hook adapter, and reports metrics
    Given the enrolled test repository
    And the repository runtime is "codex"
    And a custom harness "codex-openai-smoke" with:
      """
      agent: agents/codex-openai-smoke.md
      role: triage
      slug: fullsend-ai-codex-openai-smoke
      model: openai/gpt-5.6-luna
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      # The fleet's base policy (layered into .fullsend/policies/ by the
      # workflow): it carries no network rules of its own, so the only
      # api.openai.com route is the provider's inspected one. Without it
      # the image's default policy also allows api.openai.com as a raw
      # tunnel and OpenShell 0.0.110+ refuses to inject the credential.
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-codex-openai-smoke"
      # No host_files and no GCP env: the OpenAI credential never enters
      # the sandbox. The runner resolves it from the FULLSEND_OPENAI_*
      # variables the reusable workflow passes in, imports the
      # fullsend-openai profile it ships itself (allowing only
      # POST /v1/responses on api.openai.com), creates a run-scoped
      # provider and attaches it.
      providers:
        - providers/openai.yaml
      """
    And a codex agent "codex-openai-smoke" defined as:
      """
      ---
      name: codex-openai-smoke
      description: Behaviour smoke agent for GPT on the codex runtime.
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
    When the issue is labeled "ready-for-codex-openai-smoke"
    Then the harness "codex-openai-smoke" workflow completes successfully
    And the run selected the "codex" runtime
    And the codex output stream records at least one tool call
    And the run metrics report tokens
