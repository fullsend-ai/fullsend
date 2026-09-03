Feature: Fork PR bash routing smoke

  Verify that a fork pull request dispatches through the bash routing
  path (reusable-dispatch.yml route job) on a per-repo installation.
  This is a time-boxed guard until harness CEL cutover (#2902).

  Background:
    Given a test repository with fullsend installed
    And a fork "fork" of the test repository

  Scenario: Fork PR labeled ready-for-review dispatches review via bash routing
    Given a dummy agent that would:
      | description          | op            | args                                                       |
      | Prove bash routing   | write_fixture | output/bash-routing-ok.json, fixtures/dispatch/ok.json     |
      | Emit review JSON     | write_fixture | output/agent-result.json, fixtures/review/comment.json     |
    When a fork pull request is opened
    And the fork pull request is labeled "ready-for-review"
    Then the harness "review" workflow completes successfully
    And the agent will succeed to Prove bash routing
    And the agent will succeed to Emit review JSON
