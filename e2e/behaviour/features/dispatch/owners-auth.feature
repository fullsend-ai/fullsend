Feature: OWNERS file authorization for bash routing

  Verify that the OWNERS-file authorization path fires when
  authorization.owners_file is enabled in config.yaml. Scenarios
  trigger via issues.opened, which unconditionally calls
  is_event_actor_authorized and exercises has_repo_permission.

  Bot scenarios confirm the OWNERS code path is reached (via audit
  log); the bot has collaborator access so the API fallback would
  also grant. The outsider identity (TEST_ACTOR_OUTSIDER_PAT) has
  no collaborator access, so authorization succeeds only through
  OWNERS — the denial scenario verifies a reviewer cannot escalate
  to write-level access.

  Background:
    Given the enrolled test repository

  Scenario: Triage dispatches via OWNERS approver path when enabled
    Given an OWNERS file listing the outsider as an approver
    And OWNERS authorization is enabled
    And a dummy agent that would:
      | description          | op            | args                                               |
      | Prove execution      | write_fixture | output/owners-ok.json, fixtures/dispatch/ok.json   |
    When the outsider opens an issue for OWNERS auth testing
    Then the triage workflow completes successfully
    And the agent will succeed to Prove execution
    And the triage workflow logs contain "OWNERS file resolved user"
    And the triage workflow logs contain "as approver (requested:"

  Scenario: OWNERS alias resolves to grant access
    Given an OWNERS file with alias "test-team" as approver
    And an OWNERS_ALIASES file mapping "test-team" to the outsider
    And OWNERS authorization is enabled
    And a dummy agent that would:
      | description          | op            | args                                                  |
      | Prove execution      | write_fixture | output/owners-alias-ok.json, fixtures/dispatch/ok.json |
    When the outsider opens an issue for OWNERS auth testing
    Then the triage workflow completes successfully
    And the agent will succeed to Prove execution
    And the triage workflow logs contain "OWNERS file resolved user"
    And the triage workflow logs contain "as approver (requested:"

  Scenario: OWNERS reviewer can triage
    Given an OWNERS file listing the outsider as a reviewer only
    And OWNERS authorization is enabled
    And a dummy agent that would:
      | description          | op            | args                                               |
      | Prove execution      | write_fixture | output/owners-rev-ok.json, fixtures/dispatch/ok.json |
    When the outsider opens an issue for OWNERS auth testing
    Then the triage workflow completes successfully
    And the agent will succeed to Prove execution
    And the triage workflow logs contain "OWNERS file resolved user"
    And the triage workflow logs contain "as reviewer (requested:"

  Scenario: OWNERS reviewer is denied write-level access
    Given an OWNERS file listing the outsider as a reviewer
    And OWNERS authorization is enabled
    And an issue
    When the outsider posts "/fs-code" on the issue
    Then the dispatch run does not authorize via OWNERS

  Scenario: Unlisted collaborator falls through to API authorization
    Given an OWNERS file listing the outsider as a reviewer only
    And OWNERS authorization is enabled
    And a dummy agent that would:
      | description          | op            | args                                                      |
      | Prove execution      | write_fixture | output/owners-fallback-ok.json, fixtures/dispatch/ok.json |
    When the bot opens an issue for OWNERS auth testing
    Then the triage workflow completes successfully
    And the agent will succeed to Prove execution
    And the triage workflow logs do not contain "OWNERS file resolved user"

  Scenario: Triage dispatches without OWNERS path when not opted in
    Given an OWNERS file listing the bot as an approver
    And a dummy agent that would:
      | description          | op            | args                                               |
      | Prove execution      | write_fixture | output/owners-off-ok.json, fixtures/dispatch/ok.json |
    When the bot opens an issue for OWNERS auth testing
    Then the triage workflow completes successfully
    And the agent will succeed to Prove execution
    And the triage workflow logs do not contain "OWNERS file resolved user"
