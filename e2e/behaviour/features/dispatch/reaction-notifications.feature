Feature: Emoji reaction status notifications

  Reaction notifications post emoji reactions on issues/PRs at start and
  completion, as a supplementary/alternative signal to status comments.
  Reactions do not generate GitHub notifications.

  Background:
    Given the enrolled test repository

  Scenario: Triage with reactions enabled posts completion reaction
    Given status notification reactions are enabled
    And a custom harness "reaction-ping" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-reaction-ping
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-reaction-test"
      """
    And a dummy agent that would:
      | description              | op           | args                                                      |
      | Emit triage JSON         | write_fixture| output/agent-result.json, fixtures/triage/sufficient.json |
    And an issue
    When the issue is labeled "ready-for-reaction-test"
    Then the harness "reaction-ping" workflow completes successfully
    And the agent will succeed to Emit triage JSON
    And the issue has a "+1" reaction
