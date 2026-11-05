# Retro routing by an explicit config trigger (#8726). A retro entry in
# .fullsend/config.yaml with agents[].trigger moves the decision of whether the
# Retro job starts from the route job's bash rules to that CEL trigger
# (`fullsend dispatch --route retro`). The e2e installation identity closes the
# pull request, so the closer is a bot with no collaborator role: only the
# lifecycle-close exception lets its close start a retro, as bash does for any
# closer. The write actor (TEST_ACTOR_WRITE_PAT) posts the comment: the e2e
# installation identity's comments are bot comments, which never start a retro.
# A review trigger of "false" keeps the opened pull request from starting a
# review this feature does not test.
Feature: Retro routing by a config trigger

  Scenario: an on-demand retro trigger skips the retro on close and keeps /fs-retro
    Given the enrolled test repository
    And the repository agents are configured with:
      """
      review:
        trigger: "false"
      retro:
        trigger: >
          event.transition.kind == "comment_added"
          && has(event.transition.comment.command)
          && event.transition.comment.command == "/fs-retro"
          && event.actor.kind == "human"
      """
    And a dummy agent that would:
      | description     | op            | args                                                  |
      | Emit retro JSON | write_fixture | output/agent-result.json, fixtures/retro/summary.json |
    And a pull request is opened
    When the pull request is closed
    Then the harness "retro" agent did not run
    And the harness "retro" was dispatched exactly 0 times
    When the write actor posts "/fs-retro" on the pull request
    Then the harness "retro" workflow completes successfully
    And the agent will succeed to Emit retro JSON
    And the harness "retro" was dispatched exactly 1 time

  Scenario: a close trigger starts a retro when a bot without a role closes the pull request
    Given the enrolled test repository
    And the repository agents are configured with:
      """
      review:
        trigger: "false"
      retro:
        trigger: >
          event.entity.kind == "change_proposal"
          && event.transition.kind in ["closed", "merged"]
      """
    And a dummy agent that would:
      | description     | op            | args                                                  |
      | Emit retro JSON | write_fixture | output/agent-result.json, fixtures/retro/summary.json |
    And a pull request is opened
    When the pull request is closed
    Then the harness "retro" workflow completes successfully
    And the agent will succeed to Emit retro JSON
    And the harness "retro" was dispatched exactly 1 time
