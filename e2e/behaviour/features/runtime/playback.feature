@playback
Feature: Playback runtime serves canned results
  # Replays canned agent results without LLM inference.
  # Tests the harness infrastructure (dispatch, pre-scripts, post-scripts,
  # label application) using an ordered playlist of result directories.

  Scenario: Playback pipeline
    Given an installed test repository
    And a triage agent that returns "triage/bug"
    And a code agent that returns "code/bug-fix"
    And a review agent that returns "review/request-changes"
    And a fix agent that returns "fix/success"
    And a review agent that returns "review/approve"
    When an issue "concurrent-map-crash" is created
    Then the triage agent is triggered
    And the triage agent completes successfully
    And the issue is labeled with "ready-to-code"
    Then the code agent is triggered
    And the code agent completes successfully
    And a pull request exists
    Then the review agent is triggered
    And the review agent completes successfully
    And the review agent submitted "changes_requested"
    Then the fix agent is triggered
    And the fix agent completes successfully
    Then the review agent is triggered
    And the review agent completes successfully
    And the review agent submitted "approved"
