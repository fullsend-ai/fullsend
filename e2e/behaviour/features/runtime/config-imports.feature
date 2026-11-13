# A config import is loaded from an immutable URL and composes with the local
# config. The imported per-agent runtime keeps both dispatched stages on the
# dummy backend while the local repo-wide runtime is set to claude.
Feature: Pinned config imports affect runtime selection

  Scenario: an imported config selects the runtime for triage
    Given the enrolled test repository
    And the repository imports config from "https://raw.githubusercontent.com/fullsend-ai/fullsend/4a0d091a5a6e3afe15de41c731c79ad7685563f6/e2e/behaviour/fixtures/config-imports/triage-runtime.yaml#sha256=ba819afc5b53b87b687009a707f44e3d0b392fd9deb1eca2078b9836363164c8"
    And the repository runtime is "claude"
    And a dummy agent that would:
      | description      | op            | args                                                      |
      | Emit triage JSON | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json |
    And an issue
    When the issue is labeled "ready-for-triage"
    Then the triage workflow completes successfully
    And the run selected the "dummy" runtime from "agents.triage"
    And the agent will succeed to Emit triage JSON
    And the issue has label "ready-to-code"
