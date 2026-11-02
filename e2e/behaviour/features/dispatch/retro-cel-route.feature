# Retro routing by an explicit config trigger (#8726). A retro entry in
# .fullsend/config.yaml with agents[].trigger moves the decision of whether the
# Retro job starts from the route job's bash rules to that CEL trigger
# (`fullsend dispatch --route retro`). The first scenario closes as the write
# actor to prove an authorized close can still skip on a trigger mismatch. The
# second closes as the e2e installation bot, which has no collaborator write
# access. The write actor (TEST_ACTOR_WRITE_PAT) also posts the comment: the
# installation identity's comments are bot comments, which never start retro.
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
    When the write actor closes the pull request
    Then the harness "retro" agent did not run
    And the harness "retro" was dispatched exactly 0 times
    When the write actor posts "/fs-retro" on the pull request
    Then the harness "retro" workflow completes successfully
    And the agent will succeed to Emit retro JSON
    And the harness "retro" was dispatched exactly 1 time

  Scenario: a close trigger skips retro when a bot without write access closes the pull request
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
    Then the harness "retro" agent did not run
    And the harness "retro" was dispatched exactly 0 times
