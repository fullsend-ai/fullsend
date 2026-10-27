# Runtime-specific coverage for OpenCode (#1260). Like pi.feature, this runs
# one real model call through the runtime's Bootstrap/Run, the Vertex
# credential path and the stream parser, on the leased per-repo install.
#
# The trusted OPENCODE_CONFIG_CONTENT policy gives bash a pattern map, so the
# run also proves an agent that declares Bash keeps the trusted pattern rules
# rather than losing bash to its own "*": "deny".
#
# OpenCode requires security.enabled: true; see docs/runtimes/opencode.md.
#
# Gated on the `runtime-opencode` capability, which the Makefile does not
# declare: fullsend-sandbox:latest carries OpenCode only once the image change
# is published from main. Enable with BEHAVIOUR_CAPABILITIES=runtime-opencode
# (costs one small haiku run on Vertex).
Feature: OpenCode runtime runs an agent unattended

  Scenario: opencode rejects disabled sandbox security before sandbox creation
    Given the enrolled test repository
    And the repository runtime is "opencode"
    And a custom harness "opencode-security-disabled" with:
      """
      agent: agents/opencode-smoke.md
      role: triage
      slug: fullsend-ai-opencode-security-disabled
      model: haiku
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-opencode-security-disabled"
      security:
        enabled: false
      """
    And an issue
    When the issue is labeled "ready-for-opencode-security-disabled"
    Then the harness "opencode-security-disabled" workflow fails reporting "OpenCode requires security.enabled: true"

  @requires:capability:runtime-opencode
  Scenario: opencode run selects the runtime, calls a pattern-allowed tool, and reports metrics
    Given the enrolled test repository
    And the repository runtime is "opencode"
    And a custom harness "opencode-smoke" with:
      """
      agent: agents/opencode-smoke.md
      role: triage
      slug: fullsend-ai-opencode-smoke
      model: haiku
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-opencode-smoke"
      openshell:
        profiles:
          - profiles/fullsend-vertex-ai.yaml
      providers:
        - providers/vertex-ai.yaml
      security:
        enabled: true
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
          OPENCODE_CONFIG_CONTENT: '{"provider":{"google-vertex-anthropic":{"id":"google-vertex-anthropic"}},"permission":{"*":"deny","read":"allow","glob":"allow","grep":"allow","list":"allow","bash":{"*":"deny","ls *":"allow","ls":"allow","cat *":"allow"}}}'
      """
    And an opencode agent "opencode-smoke" defined as:
      """
      ---
      name: opencode-smoke
      description: Behaviour smoke agent for the OpenCode runtime.
      tools: Bash
      ---
      You are an unattended smoke-test agent. Do exactly the following, in
      order, then stop. Do not ask questions, do not explain, do not read or
      modify any other file.

      1. Using the bash tool, run: ls .
      2. Using the bash tool, write the file "$FULLSEND_OUTPUT_DIR/agent-result.json"
         with `cat > "$FULLSEND_OUTPUT_DIR/agent-result.json" <<'EOF'`, containing
         exactly this content and nothing else:

      {{fixture:fixtures/triage/sufficient.json}}
      """
    And an issue
    When the issue is labeled "ready-for-opencode-smoke"
    Then the harness "opencode-smoke" workflow completes successfully
    And the run selected the "opencode" runtime
    And the run used model "claude-haiku-5-5@default" without a fallback
    And the run metrics report tokens
