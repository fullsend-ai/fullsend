@skip:gitlab
Feature: An overlay that references a runner secret reads only runtime.forge and config

  A harness overlay that references a runner secret (ADR 0136) may guard
  itself only on runtime.forge and config, so event content cannot steer
  a secret into a run. A comprehension variable named event makes CEL
  spell the global event as .event; the harness that reads event that
  way is refused when fullsend run loads it, and the run fails with the
  guard error. The same harness guarded on runtime.forge runs. Runner
  secrets are stored in FULLSEND_GITHUB_RUNNER_SECRETS, which exists on
  GitHub only; on GitLab no runner secret name is set and the guard has
  nothing to check.

  Background:
    Given the enrolled test repository
    And the repository has the runner secret "BEHAVIOUR_RUNNER_SECRET"

  # The refused scenario is gated on the runner-secret-overlay-guard
  # capability, declared in the Makefile's BEHAVIOUR_CAPABILITIES, so it
  # runs in the default behaviour suite; the gate stays so
  # BEHAVIOUR_CAPABILITIES= can still skip it on demand.
  @requires:capability:runner-secret-overlay-guard
  Scenario: An overlay that reads a shadowed .event is refused
    # Harness dispatch loads the harness without the runner secret names
    # (only the run job reads FULLSEND_GITHUB_RUNNER_SECRETS), so the
    # runner-secret check does not run there and the harness is
    # dispatched; the run job refuses it, and the error below reaches the
    # run's logs.
    Given a custom harness "secret-guard-refused" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-secret-guard-refused
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-secret-guard-refused"
      overlays:
        - when: '[0].exists(event, .event.source.system == "github")'
          env:
            runner:
              BEHAVIOUR_RUNNER_SECRET: "${BEHAVIOUR_RUNNER_SECRET}"
      """
    And a dummy agent that would:
      | description     | op            | args                                               |
      | Prove execution | write_fixture | output/dispatch-ok.json, fixtures/dispatch/ok.json |
    And an issue
    When the issue is labeled "ready-for-secret-guard-refused"
    Then the harness "secret-guard-refused" workflow fails with "overlays[0] references runner secret(s) BEHAVIOUR_RUNNER_SECRET, but its when: reads more than runtime.forge and config" in its logs

  Scenario: An overlay guarded on runtime.forge runs
    Given a custom harness "secret-guard-allowed" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-secret-guard-allowed
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-secret-guard-allowed"
      overlays:
        - when: 'runtime.forge == "github"'
          env:
            runner:
              BEHAVIOUR_RUNNER_SECRET: "${BEHAVIOUR_RUNNER_SECRET}"
      """
    And a dummy agent that would:
      | description     | op            | args                                               |
      | Prove execution | write_fixture | output/dispatch-ok.json, fixtures/dispatch/ok.json |
    And an issue
    When the issue is labeled "ready-for-secret-guard-allowed"
    Then the harness "secret-guard-allowed" workflow completes successfully
    And the harness "secret-guard-allowed" workflow logs contain "Runner secrets"
    And the agent will succeed to Prove execution
