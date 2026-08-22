Feature: Sandbox security hooks are loaded via --settings

  Security hooks (SSRF, canary, secret redaction, etc.) are installed under
  the runner-owned claude-config/ directory and wired via the --settings flag
  so Claude Code loads them regardless of its working directory. This scenario
  verifies that at least one blocking PreToolUse hook fires end-to-end —
  catching the "silently not loaded" class of regression where hook wiring
  exists but the CLI never reads it.

  Scenario: SSRF PreToolUse hook blocks a disallowed URL
    Given the enrolled test repository
    And a custom harness "hooks-smoke" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-hooks-smoke
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-hooks-smoke"
      """
    And a dummy agent that would:
      | description              | op            | args                                                      |
      | Fetch metadata endpoint  | url_get       | http://169.254.169.254/latest/meta-data/                  |
      | Emit triage JSON         | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json |
    And an issue
    When the issue is labeled "ready-for-hooks-smoke"
    Then the harness "hooks-smoke" workflow completes successfully
    And the agent will fail to Fetch metadata endpoint
    And the agent will succeed to Emit triage JSON
