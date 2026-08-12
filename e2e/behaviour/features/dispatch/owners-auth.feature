Feature: OWNERS file authorization for bash routing

  Verify that the OWNERS-file authorization path fires when
  authorization.owners_file is enabled in config.yaml. Scenarios
  trigger via issues.opened, which unconditionally calls
  is_event_actor_authorized and exercises has_repo_permission.

  The e2e bot already has collaborator access, so these scenarios
  confirm the OWNERS code path is reached (via audit log) rather
  than testing the API-fallback denial path — that requires a
  restricted identity without collaborator access (#6072).

  Background:
    Given the enrolled test repository

  Scenario: Triage dispatches via OWNERS approver path when enabled
    Given an OWNERS file listing the bot as an approver
    And OWNERS authorization is enabled
    And a dummy agent that would:
      | description          | op            | args                                               |
      | Prove execution      | write_fixture | output/owners-ok.json, fixtures/dispatch/ok.json   |
    When an issue is opened for OWNERS auth testing
    Then the triage workflow completes successfully
    And the agent will succeed to Prove execution
    And the triage workflow logs contain "authorized via OWNERS file (approver"

  Scenario: OWNERS alias resolves to grant access
    Given an OWNERS file with alias "test-team" as approver
    And an OWNERS_ALIASES file mapping "test-team" to the bot
    And OWNERS authorization is enabled
    And a dummy agent that would:
      | description          | op            | args                                                  |
      | Prove execution      | write_fixture | output/owners-alias-ok.json, fixtures/dispatch/ok.json |
    When an issue is opened for OWNERS auth testing
    Then the triage workflow completes successfully
    And the agent will succeed to Prove execution
    And the triage workflow logs contain "authorized via OWNERS file (approver"

  Scenario: OWNERS reviewer can triage
    Given an OWNERS file listing the bot as a reviewer only
    And OWNERS authorization is enabled
    And a dummy agent that would:
      | description          | op            | args                                               |
      | Prove execution      | write_fixture | output/owners-rev-ok.json, fixtures/dispatch/ok.json |
    When an issue is opened for OWNERS auth testing
    Then the triage workflow completes successfully
    And the agent will succeed to Prove execution
    And the triage workflow logs contain "authorized via OWNERS file (reviewer"

  Scenario: OWNERS reviewer is denied write-level access
    Given an OWNERS file listing the outsider as a reviewer
    And OWNERS authorization is enabled
    And an issue
    When the outsider posts "/fs-code" on the issue
    Then the dispatch run does not authorize via OWNERS

  Scenario: Triage dispatches without OWNERS path when not opted in
    Given an OWNERS file listing the bot as an approver
    And a dummy agent that would:
      | description          | op            | args                                               |
      | Prove execution      | write_fixture | output/owners-off-ok.json, fixtures/dispatch/ok.json |
    When an issue is opened for OWNERS auth testing
    Then the triage workflow completes successfully
    And the agent will succeed to Prove execution
    And the triage workflow logs do not contain "authorized via OWNERS file"
