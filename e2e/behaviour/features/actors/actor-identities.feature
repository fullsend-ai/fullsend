Feature: Human-like test actor identities

  Smoke coverage for the human-like test actors. Each actor acts through
  its own PAT-authenticated SCM driver: the write actor
  (TEST_ACTOR_WRITE_PAT), the triage actor (TEST_ACTOR_TRIAGE_PAT), and
  the outsider (TEST_ACTOR_OUTSIDER_PAT). A single scenario has each
  actor post a plain comment on one issue and checks the comment is
  authored by that actor's login rather than the e2e installation
  identity. Plain comments carry no slash command, so they dispatch no
  agent runs.

  Background:
    Given the enrolled test repository

  Scenario: Write, triage, and outsider PAT identities are usable
    Given an issue
    When the write actor posts "behaviour actor identity smoke: write actor" on the issue
    And the triage actor posts "behaviour actor identity smoke: triage actor" on the issue
    And the outsider posts "behaviour actor identity smoke: outsider" on the issue
    Then the comment "behaviour actor identity smoke: write actor" on the issue is authored by the write actor
    And the comment "behaviour actor identity smoke: triage actor" on the issue is authored by the triage actor
    And the comment "behaviour actor identity smoke: outsider" on the issue is authored by the outsider
