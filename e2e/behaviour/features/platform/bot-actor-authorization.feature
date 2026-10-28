@skip:gitlab
Feature: Bot actor authorization

  Background:
    Given the enrolled test repository
    And a custom harness "needs-review-bot" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-needs-review-bot
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.transition.kind == "comment_added"
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-do"
        && has(event.actor.bot_role)
        && event.actor.bot_role == "review"
      """
    And a custom harness "needs-coder-bot" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-needs-coder-bot
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.transition.kind == "comment_added"
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-do"
        && has(event.actor.bot_role)
        && event.actor.bot_role == "coder"
      """
    And a custom harness "needs-triage-bot" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-needs-triage-bot
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.transition.kind == "comment_added"
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-do"
        && has(event.actor.bot_role)
        && event.actor.bot_role == "triage"
      """
    And a custom harness "on-label" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-on-label
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.transition.kind == "label_changed"
        && event.transition.label.name == "do-it"
      """
    And a dummy agent that would:
      | description | op            | args                                               |
      | Prove run   | write_fixture | output/bot-auth-ok.json, fixtures/dispatch/ok.json |

  # The dispatch shim drops every issue comment from a bot, so no bot's
  # "/fs-do" reaches a harness. Labels are not dropped.
  Scenario Outline: <actor> posts "/fs-do" and labels the issue "do-it"
    Given the label "do-it" exists
    And an issue
    When the <actor> posts "/fs-do" on the issue
    And the <actor> labels the issue "do-it"
    Then the harness "on-label" workflow completes successfully
    And the harness "needs-review-bot" was dispatched exactly <needs-review-bot> times
    And the harness "needs-coder-bot" was dispatched exactly <needs-coder-bot> times
    And the harness "needs-triage-bot" was dispatched exactly <needs-triage-bot> times
    And the harness "on-label" was dispatched exactly <on-label> times

    Examples:
      | actor       | needs-review-bot | needs-coder-bot | needs-triage-bot | on-label |
      | write actor | 0                | 0               | 0                | 1        |
      | review bot  | 0                | 0               | 0                | 1        |
      | coder bot   | 0                | 0               | 0                | 1        |
      | triage bot  | 0                | 0               | 0                | 1        |
      | e2e bot     | 0                | 0               | 0                | 1        |
