# Review routing by an explicit config trigger (#8726). A review entry in
# .fullsend/config.yaml with agents[].trigger moves the decision of whether the
# Review job starts from the route job's bash rules to that CEL trigger
# (`fullsend dispatch --route review`). Here an on-demand trigger turns off the
# automatic review when a pull request is opened and keeps /fs-review. The
# write actor (TEST_ACTOR_WRITE_PAT) posts the comment: the e2e installation
# identity's comments are bot comments, which never start a review. The pull
# request is opened by the e2e installation identity, which bash routing
# would review; that no review starts shows CEL took over the decision (the
# trigger does not match, and a bot without a registered role is not
# authorized on the CEL path either, #8329).
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
    When a pull request is opened
    Then the harness "review" agent did not run
    And the harness "review" was dispatched exactly 0 times
    When the write actor posts "/fs-review" on the pull request
    Then the harness "review" workflow completes successfully
    And the agent will succeed to Emit review JSON
    And the harness "review" was dispatched exactly 1 time
