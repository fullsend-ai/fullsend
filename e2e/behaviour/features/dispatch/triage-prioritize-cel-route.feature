# Triage and prioritize routing by an explicit config trigger (#8726, #2896,
# #2901). An entry in .fullsend/config.yaml with agents[].trigger moves the
# decision of whether the Triage or Prioritize job starts from the route
# job's bash rules to that CEL trigger (`fullsend dispatch --route <agent>`).
# The write actor (TEST_ACTOR_WRITE_PAT) posts the comments: the e2e
# installation identity's comments are bot comments, which never start a
# stage.
Feature: Triage and prioritize routing by a config trigger

  Background:
    Given the enrolled test repository

  Scenario: an on-demand triage trigger skips the ready-for-triage label and keeps /fs-triage
    Given the repository agents are configured with:
      """
      triage:
        trigger: >
          event.entity.kind == "work_item"
          && event.transition.kind == "comment_added"
          && has(event.transition.comment.command)
          && event.transition.comment.command == "/fs-triage"
          && event.actor.kind == "human"
      """
    And a dummy agent that would:
      | description            | op            | args                                                            |
      | Emit sufficient triage | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json      |
    And an issue
    When the issue is labeled "ready-for-triage"
    Then the harness "triage" agent did not run
    And the harness "triage" was dispatched exactly 0 times
    When the write actor posts "/fs-triage" on the issue
    Then the harness "triage" workflow completes successfully
    And the agent will succeed to Emit sufficient triage
    And the harness "triage" was dispatched exactly 1 time

  Scenario: a prioritize trigger that never matches turns /fs-prioritize off
    Given the repository agents are configured with:
      """
      prioritize:
        trigger: "false"
      """
    And an issue
    When the write actor posts "/fs-prioritize" on the issue
    Then the harness "prioritize" agent did not run
    And the harness "prioritize" was dispatched exactly 0 times
