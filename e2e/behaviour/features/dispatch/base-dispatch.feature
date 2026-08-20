Feature: Base-composed harness dispatch

  Background:
    Given the enrolled test repository
    And a harness-hosting repository "base-harness-host"

  Scenario: Base-composed harnesses dispatch with local and remote base variants
    # Local base harness (shared config, no trigger — not dispatched itself)
    Given a custom harness "local-base" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-local-base
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      """
    # Variant 1: local child with local base
    And a custom harness "local-child" with base "local-base" and:
      """
      slug: fullsend-ai-local-child
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-base-test"
      """
    # Remote base harness (committed to hosting repo, not registered as agent)
    And a URL-sourced base harness "remote-base" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-remote-base
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      """
    # Variant 2: local child with remote (URL) base
    And a custom harness "remote-base-child" with URL base "remote-base" and:
      """
      slug: fullsend-ai-remote-base-child
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-base-test"
      """
    # Variant 3: remote child with remote (URL) base
    And a URL-sourced custom harness "remote-child" with URL base "remote-base" and:
      """
      slug: fullsend-ai-remote-child
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-base-test"
      """
    And a dummy agent that would:
      | description          | op            | args                                                    |
      | Prove base execution | write_fixture | output/dispatch-base-ok.json, fixtures/dispatch/ok.json |
    And an issue
    When the issue is labeled "ready-for-base-test"
    Then the harness "local-child" workflow completes successfully
    And the agent will succeed to Prove base execution
    And the harness "remote-base-child" workflow completes successfully
    And the harness "remote-child" workflow completes successfully
