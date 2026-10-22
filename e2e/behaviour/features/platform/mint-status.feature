@skip:gitlab
Feature: CLI mint status with GitHub user-token auth

  An operator who knows only the mint address and has an authorized
  GitHub account gets the mint's status through the CLI:
  `fullsend mint status --mint-url <mint address>`. The suite's own mint
  is the target: the preview mint of the numbered halfsend org on dev,
  the STAGE mint on stage. Both are deployed with
  --status-auth=oidc,github --status-github-group=ORG/mint-status, and
  the write actor (TEST_ACTOR_WRITE_PAT) is the only member of that team.

  The CLI runs as an operator would: no --project, no GCP project or
  credentials, no GitHub Actions OIDC variables and no gh login, with
  the credential in GH_TOKEN for the CLI's normal GitHub credential
  discovery. The status facts are read from the CLI's output; nothing
  calls the mint's status endpoint directly.

  A failure is reported as authentication, transport, deployment or
  assertion. The controls show that each class is told apart and that
  none of them passes as a success: an invalid credential is rejected,
  an address nothing listens on fails in transport, and an address on
  the suite mint that is not its API fails as a deployment. The
  credential never appears in the CLI output or the test artifacts.

  Scenario: The write actor gets mint status through the CLI
    Given the suite mint address
    When the operator runs mint status with the write actor credential
    Then the mint status outcome is "ok"
    And mint status reports the suite mint's status facts
    And the credential is absent from the mint status output and artifacts

  Scenario Outline: Mint status with the <credential> for the <mint address> fails as <outcome>
    Given the <mint address>
    When the operator runs mint status with the <credential>
    Then the mint status outcome is "<outcome>"
    And the credential is absent from the mint status output and artifacts

    Examples:
      | mint address                            | credential             | outcome        |
      | suite mint address                      | invalid credential     | authentication |
      | unreachable mint address                | write actor credential | transport      |
      | suite mint address with a non-mint path | write actor credential | deployment     |
