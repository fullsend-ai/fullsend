# Per-agent runtime selection through agents: entries in .fullsend/config.yaml
# (#6581, ADR 0091). The repo-wide `runtime:` is flipped to a real runtime for
# this scenario and the agents the scenario can dispatch are pinned back to
# dummy on their own agents: entries, so a passing run proves the per-agent
# entry beat the repo-wide key without any inference: the runner records which config entry
# decided in metrics.json (`runtime_source` ends with `agents.triage`).
#
# `code` is pinned too because triage hands off with `ready-to-code`, which
# dispatches the code stage on the same repo; without its own entry that run
# would start on the repo-wide runtime for real.
Feature: Per-agent runtime and model on agents: entries in config.yaml

  Scenario: an agents: entry selects the runtime for one agent over the repo-wide key
    Given the enrolled test repository
    And the repository runtime is "claude"
    And the repository agents are configured with:
      """
      triage:
        runtime: dummy
      code:
        runtime: dummy
      """
    And a dummy agent that would:
      | description      | op            | args                                                      |
      | Emit triage JSON | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json |
    And an issue
    When the issue is labeled "ready-for-triage"
    Then the triage workflow completes successfully
    And the run selected the "dummy" runtime from "agents.triage"
    And the agent will succeed to Emit triage JSON
    And the issue has label "ready-to-code"

  # Real-runtime half: the repo stays on the install default (dummy) and a
  # single custom agent is put on pi with its own model through
  # agents: entries. The harness itself says `model: opus`; the entry says
  # `haiku`, so a passing run proves both the runtime and the model of one
  # agent came from config.yaml, not from the harness or the repo-wide key.
  # Gated like pi.feature: one small haiku run on Vertex per suite run.
  #
  # The same run covers file-level skill overrides on a real runtime (#6158)
  # at no extra model cost: the harness mounts the shipped pr-review skill
  # from fullsend-ai/agents (pinned as in skill-overrides.feature) with
  # sub-agents/user-experience.md removed, and the agent has the Agent tool,
  # so pi registers the staged sub-agents as personas before the model
  # starts. The runner logs one `subagents: <name> → <model>` line per
  # registered persona: correctness must be among them, user-experience must
  # not. The agents: entry pins `subagents: {correctness: haiku}` so the
  # correctness line does not hang on its frontmatter model resolving in
  # this run, and a correctness file that failed to register fails the run
  # instead of only dropping the line. pi prints these lines as one block at
  # bootstrap; the step reads only that block, requires an ordinary log line
  # on both sides of it so it was not cut off, and fails if it is missing,
  # so truncation elsewhere in the job log does not matter. Skill directories
  # are fetched by github.com tree URL, which the default allowlist does not
  # cover, so the repository allowlist gains it.
  @requires:capability:runtime-pi
  Scenario: an agents: entry puts one custom agent on pi with its own model
    Given the enrolled test repository
    And the repository allows remote resources from "https://github.com/fullsend-ai/agents/"
    And a custom harness "pi-override" with:
      """
      agent: agents/pi-override.md
      role: triage
      slug: fullsend-ai-pi-override
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-pi-override"
      # Vertex reaches the sandbox the way the fleet harnesses wire it:
      # egress is granted by the vertex-ai provider (ADR-0065; the per-repo
      # scaffold ships these provider/profile files), credentials arrive as
      # host files, and the project/region env is inlined here because the
      # scaffold ships no gcp-vertex.env. ${VAR} expands from the runner
      # environment set by setup-gcp.
      openshell:
        profiles:
          - profiles/fullsend-vertex-ai.yaml
      providers:
        - providers/vertex-ai.yaml
      host_files:
        - src: ${GOOGLE_APPLICATION_CREDENTIALS}
          dest: /tmp/.gcp-credentials.json
        - src: ${GCP_OIDC_TOKEN_FILE}
          dest: /sandbox/workspace/.gcp-oidc-token
          optional: true
      env:
        sandbox:
          ANTHROPIC_VERTEX_PROJECT_ID: ${ANTHROPIC_VERTEX_PROJECT_ID}
          GOOGLE_CLOUD_PROJECT: ${ANTHROPIC_VERTEX_PROJECT_ID}
          CLOUD_ML_REGION: ${CLOUD_ML_REGION}
          GOOGLE_APPLICATION_CREDENTIALS: /tmp/.gcp-credentials.json
      allowed_remote_resources:
        - https://github.com/fullsend-ai/agents/
      skills:
        - https://github.com/fullsend-ai/agents/tree/4a6645268c6c8063c15ce5cb1b88a4f3930c74da/skills/pr-review#sha256=d0a639e6870c3e204db83855cdf1e4e10e6eb3ecfaee1695bbf8630843cb5c6c:
            sub-agents/user-experience.md: null
      """
    And a pi agent "pi-override" defined as:
      """
      ---
      name: pi-override
      description: Behaviour agent proving a per-agent config entry selects pi and a model for one agent.
      tools: Bash(ls), Write, Agent
      ---
      You are an unattended smoke-test agent. Do exactly the following, in
      order, then stop. Do not ask questions, do not explain, do not read or
      modify any other file.

      1. Using the bash tool, run: ls .
      2. Using the write tool, create the file /sandbox/workspace/output/agent-result.json
         with exactly this content and nothing else:

      {{fixture:fixtures/triage/sufficient.json}}
      """
    And the repository agents are configured with:
      """
      pi-override:
        runtime: pi
        model: haiku
        subagents:
          correctness: haiku
      """
    And an issue
    When the issue is labeled "ready-for-pi-override"
    Then the harness "pi-override" workflow completes successfully
    And the run selected the "pi" runtime from "agents.pi-override"
    And the run requested model "haiku" from "agents.pi-override" and the provider reported a "haiku" model
    And the pi session transcript records at least one tool call
    And the run metrics report tokens
    And the harness "pi-override" workflow logs register persona "correctness" and not "user-experience"
