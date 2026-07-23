# Behaviour tests request triage via the ready-for-triage label (issues event).
# The per-repo shim ignores /fs-triage issue_comment events from bot users; CI
# uses minted e2e installation tokens, so label dispatch is the supported path.
Feature: Manual triage via ready-for-triage label

  Scenario: Triage applies ready-to-code and sandbox blocks disallowed URL
    Given the enrolled test repository
    And a dummy agent that would:
      | description      | op            | args                                                      |
      | Search for foo   | url_get       | https://www.google.com/search?q=foo                       |
      | Emit triage JSON | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json |
    And an issue
    When the issue is labeled "ready-for-triage"
    Then the triage workflow completes successfully
    And the agent will fail to Search for foo
    And the agent will succeed to Emit triage JSON
    And the issue has label "ready-to-code"
