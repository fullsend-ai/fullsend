# Review routing by an explicit config trigger (#8726). A review entry in
# .fullsend/config.yaml with agents[].trigger moves the decision of whether the
# Review job starts from the route job's bash rules to that CEL trigger
# (`fullsend dispatch --route review`). Here an on-demand trigger turns off the
# automatic review when a pull request is opened and keeps /fs-review. The
# write actor (TEST_ACTOR_WRITE_PAT) opens the pull request and posts the
# comment, so both events come from an authorized human: bash routing would
# review the opened pull request, so no review on open shows the trigger
# decided. The e2e installation identity is a bot, which the CEL path skips
# for comments and does not yet authorize for pull requests (#8329).
Feature: Review routing by a config trigger

  Scenario: an on-demand review trigger skips the automatic review and keeps /fs-review
    Given the enrolled test repository
    And the repository agents are configured with:
      """
      review:
        trigger: >
          event.transition.kind == "comment_added"
          && has(event.entity.linked_change_proposal)
          && has(event.transition.comment.command)
          && event.transition.comment.command == "/fs-review"
          && event.actor.kind == "human"
      """
    And a dummy agent that would:
      | description      | op            | args                                                   |
      | Emit review JSON | write_fixture | output/agent-result.json, fixtures/review/comment.json |
    When the write actor opens a pull request
    Then the harness "review" agent did not run
    And the harness "review" was dispatched exactly 0 times
    When the write actor posts "/fs-review" on the pull request
    Then the harness "review" workflow completes successfully
    And the agent will succeed to Emit review JSON
    And the harness "review" was dispatched exactly 1 time
