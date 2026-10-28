@skip:gitlab
Feature: Bot actor authorization

  Bot actors by registered role (ADR 0107). Human actors are in
  actor-authorization.feature. The matrices are separate files on purpose:
  bot rows need role-bot workflows pushed to the leased repository and
  harnesses that trigger on event.actor.bot_role, and the bot-role lookup is
  GitHub-only, hence @skip:gitlab, while the human matrix is not.

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
        ((event.transition.kind == "comment_added"
          && has(event.transition.comment.command)
          && event.transition.comment.command == "/fs-do")
         || event.transition.kind == "review_submitted")
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
        ((event.transition.kind == "comment_added"
          && has(event.transition.comment.command)
          && event.transition.comment.command == "/fs-do")
         || event.transition.kind == "review_submitted")
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
        ((event.transition.kind == "comment_added"
          && has(event.transition.comment.command)
          && event.transition.comment.command == "/fs-do")
         || event.transition.kind == "review_submitted")
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
    And a custom harness "on-review" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-on-review
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.transition.kind == "review_submitted"
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

  # A pull request review is a bot transition the shim forwards to
  # `fullsend dispatch`, so it reaches the dispatch-time bot-role lookup. A
  # registered role bot is recognized and dispatches the harness of its own
  # role, and the unrestricted "on-review" harness, in every row.
  Scenario Outline: registered <actor> submits a review comment on a pull request
    Given a pull request is opened
    When the <actor> submits a review comment on the pull request
    Then the harness "<harness>" workflow completes successfully
    And the harness "on-review" workflow completes successfully
    And the harness "needs-review-bot" was dispatched exactly <needs-review-bot> times
    And the harness "needs-coder-bot" was dispatched exactly <needs-coder-bot> times
    And the harness "needs-triage-bot" was dispatched exactly <needs-triage-bot> times
    And the harness "on-review" was dispatched exactly 1 times

    Examples:
      | actor      | harness          | needs-review-bot | needs-coder-bot | needs-triage-bot |
      | review bot | needs-review-bot | 1                | 0               | 0                |
      | coder bot  | needs-coder-bot  | 0                | 1               | 0                |
      | triage bot | needs-triage-bot | 0                | 0               | 1                |

  # The e2e bot has no registered role, so the lookup denies its review
  # before CEL: even "on-review", whose trigger matches every review,
  # dispatches nothing. The write actor's review is the positive control, so
  # the denial cannot pass because the harnesses never loaded or the trigger
  # never matched. A failed lookup is denied the same way; it cannot be
  # induced against the deployed mint, so the unit tests of
  # `applyBotRoleLookup` and the gate cover it.
  Scenario: unregistered e2e bot submits a review comment on a pull request
    Given a pull request is opened
    When the e2e bot submits a review comment on the pull request
    Then the harness dispatch completes with an empty matrix
    And the harness "on-review" was dispatched exactly 0 times
    And the harness "needs-review-bot" was dispatched exactly 0 times
    And the harness "needs-coder-bot" was dispatched exactly 0 times
    And the harness "needs-triage-bot" was dispatched exactly 0 times
    When the write actor submits a review comment on the pull request
    Then the harness "on-review" workflow completes successfully
    And the harness "on-review" was dispatched exactly 1 times
    And the harness "needs-review-bot" was dispatched exactly 0 times
