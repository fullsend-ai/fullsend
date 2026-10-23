Feature: Platform trigger authorization

  Harness-dispatch authorization (ADR 0054) on platform trigger paths.
  The dispatch gate requires the harness's role-derived threshold for
  most transitions, but authorizes label-added events without one. So the
  denial is exercised through the pull request opened transition,
  performed by the outsider (TEST_ACTOR_OUTSIDER_PAT, no collaborator
  access), on a pull request from the outsider's own fork.

  A label added afterwards by the e2e identity matches the same
  trigger and must dispatch. It is the positive control: it proves the
  harness is registered and its trigger matches this pull request, so
  the denial assertion cannot pass because the harness never loaded.
  The denial assertions run before the label is added, so a leaked
  opened dispatch cannot be cancelled by the label run's concurrency
  group and slip past the count. The first of them waits for the
  opened event's harness run and requires its Harness dispatch job to
  succeed with an empty matrix, because the other two also pass when
  that job errors or the run was never created.

  Background:
    Given the enrolled test repository
    And a fork "test-repo-fork" of the enrolled test repository owned by the outsider

  Scenario: Fork PR opened by an unauthorized actor dispatches nothing
    Given a custom harness "fork-auth-probe" with:
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
    And a dummy agent that would:
      | description     | op            | args                                                         |
      | Prove execution | write_fixture | output/dispatch-fork-auth-ok.json, fixtures/dispatch/ok.json |
    When a fork pull request is opened by the outsider
    Then the harness dispatch completes with an empty matrix
    And the harness "fork-auth-probe" agent did not run
    And the harness "fork-auth-probe" was dispatched exactly 0 times
    When the fork pull request is labeled "ready-for-fork-auth"
    Then the harness "fork-auth-probe" workflow completes successfully
    And the agent will succeed to Prove execution
