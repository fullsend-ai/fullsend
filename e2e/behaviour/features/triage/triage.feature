Feature: Manual triage via slash command

  Scenario: Triage applies ready-to-code on sufficient issue
    Given the enrolled test repository
    And a dummy agent that would:
      | description      | op            | args                                                      |
      | Emit triage JSON | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json |
    And an issue with title "Login fails" and body containing "steps to reproduce"
    When a member comments "/fs-triage" on the issue
    Then the triage workflow completes successfully
    And the agent will succeed to Emit triage JSON
    And the issue has label "ready-to-code"

  Scenario: Sandbox blocks disallowed outbound URL
    Given the enrolled test repository
    And a dummy agent that would:
      | description    | op      | args                                |
      | Search for foo | url_get | https://www.google.com/search?q=foo |
    And an issue
    When a member comments "/fs-triage" on the issue
    Then the triage workflow completes successfully
    And the agent will fail to Search for foo
