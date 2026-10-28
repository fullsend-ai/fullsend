Feature: Platform actor authorization

  Human actors by permission level (ADR 0054). Bot actors are in
  bot-actor-authorization.feature.

  Background:
    Given the enrolled test repository
    And a custom harness "do-needs-triage" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-do-needs-triage
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.transition.kind == "comment_added"
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-do"
      """
    And a custom harness "do-needs-write" with:
      """
      agent: agents/triage.md
      role: coder
      slug: fullsend-ai-do-needs-write
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.transition.kind == "comment_added"
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-do"
      """
    And a custom harness "do-on-label" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-do-on-label
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.transition.kind == "label_changed"
        && event.transition.label.name == "do-it"
      """
    And a dummy agent that would:
      | description | op            | args                                              |
      | Prove run   | write_fixture | output/actor-auth-ok.json, fixtures/dispatch/ok.json |

  Scenario Outline: <actor> comments "/fs-do" and labels the issue
    Given the label "do-it" exists
    And an issue
    When the <actor> posts "/fs-do" on the issue
    And the <actor> labels the issue "do-it"
    Then the harness "do-needs-triage" workflow completes successfully
    And the harness "do-on-label" workflow completes successfully
    And the harness "do-needs-triage" was dispatched exactly <needs triage> times
    And the harness "do-needs-write" was dispatched exactly <needs write> times
    And the harness "do-on-label" was dispatched exactly <on label> times

    Examples:
      | actor        | needs triage | needs write | on label |
      | write actor  | 1            | 1           | 1        |
      | triage actor | 1            | 0           | 1        |

  Scenario: outsider comments "/fs-do" on the issue
    Given an issue
    When the outsider posts "/fs-do" on the issue
    Then the harness dispatch completes with an empty matrix
    And the harness "do-needs-triage" was dispatched exactly 0 times
    And the harness "do-needs-write" was dispatched exactly 0 times
    When the write actor posts "/fs-do" on the issue
    Then the harness "do-needs-triage" workflow completes successfully

  Scenario: outsider opens a pull request from a fork
    Given a fork "test-repo-fork" of the enrolled test repository owned by the outsider
    And a custom harness "fork-auth-probe" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-auth-probe
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "change_proposal"
        && (event.transition.kind == "opened"
            || (event.transition.kind == "label_changed"
                && event.transition.label.name == "ready-for-fork-auth"))
      """
    When a fork pull request is opened by the outsider
    Then the harness dispatch completes with an empty matrix
    And the harness "fork-auth-probe" agent did not run
    And the harness "fork-auth-probe" was dispatched exactly 0 times
    When the fork pull request is labeled "ready-for-fork-auth"
    Then the harness "fork-auth-probe" workflow completes successfully
    And the agent will succeed to Prove run
