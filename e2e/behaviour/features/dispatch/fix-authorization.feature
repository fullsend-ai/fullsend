@skip:gitlab
Feature: Fix dispatch honours actor roles and the review bot identity

  The bash route in reusable-dispatch.yml starts the built-in fix stage
  from who acted. /fs-fix needs a write actor: the triage actor
  (TEST_ACTOR_TRIAGE_PAT) is refused. The fix-probe harness matches only
  a triage actor's /fs-fix, so its success shows that comment's run was
  evaluated. A changes-requested review starts fix only from the
  registered review bot: the e2e App's review is refused, then the dummy
  review agent requests changes as the review bot and fix starts. Only
  the fix dispatch is counted, because fix fails on the review fixture.

  Background:
    Given the enrolled test repository

  Scenario: /fs-fix starts fix for the write actor but not the triage actor
    Given a custom harness "fix-probe" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fix-probe
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.transition.kind == "comment_added"
        && has(event.entity.linked_change_proposal)
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-fix"
        && event.actor.role == "triage"
      """
    And a dummy agent that would:
      | description      | op            | args                                                   |
      | Emit review JSON | write_fixture | output/agent-result.json, fixtures/review/comment.json |
      | Prove probe      | write_fixture | output/fix-probe-ok.json, fixtures/dispatch/ok.json    |
    When the write actor opens a pull request
    And the write actor posts "/fs-fix" on the pull request
    # Settles 90 s so the comment's run exists before it is counted.
    Then the harness "fix-probe" agent did not run
    And the harness "fix" was dispatched exactly 1 time
    When the triage actor posts "/fs-fix" on the pull request
    Then the harness "fix-probe" workflow completes successfully
    And the agent will succeed to Prove probe
    And the harness "fix" agent did not run
    And the harness "fix" was dispatched exactly 0 times

  Scenario: only the registered review bot's changes-requested review starts fix
    Given a dummy agent that would:
      | description      | op            | args                                                   |
      | Emit review JSON | write_fixture | output/agent-result.json, fixtures/review/comment.json |
    When the write actor opens a pull request
    Then the harness "review" workflow completes successfully
    When the pull request is labeled "fullsend-fix"
    Then the harness dispatch completes with an empty matrix
    When a changes-requested review is submitted on the pull request
    Then the harness dispatch completes with an empty matrix
    And the harness "fix" was dispatched exactly 0 times
    Given a dummy agent that would:
      | description          | op            | args                                                           |
      | Emit request-changes | write_fixture | output/agent-result.json, fixtures/review/request-changes.json |
    When the write actor posts "/fs-review" on the pull request
    Then the harness "review" workflow completes successfully
    And the harness "review" was dispatched exactly 1 time
    And the fix agent is triggered
    And the harness "fix" was dispatched exactly 1 time
