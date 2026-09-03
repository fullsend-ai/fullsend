Feature: Fork PR dispatch

  Background:
    Given a test repository with fullsend installed
    And a fork "fork" of the test repository

  Scenario: Fork PR label dispatches harness
    Given a custom harness "fork-pr-ping" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-pr-ping
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "change_proposal"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-fork-ping"
      """
    And a custom harness "fork-issue-ping" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-issue-ping
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-fork-ping"
      """
    And a disabled custom harness "fork-pr-killed" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-pr-killed
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "change_proposal"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-fork-ping"
      """
    And a custom harness "fork-pr-nofork" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-pr-nofork
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "change_proposal"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-fork-ping"
        && !event.state.change_proposal.is_fork
      """
    And a dummy agent that would:
      | description            | op           | args                                                              |
      | Fork PR payload        | assert_json  | .fullsend/dispatch/event-payload.json,pull_request.head.repo.fork |
      | Prove fork execution   | write_fixture| output/dispatch-fork-ok.json, fixtures/dispatch/ok.json           |
    When a fork pull request is opened
    And the fork pull request is labeled "ready-for-fork-ping"
    Then the harness "fork-pr-ping" workflow completes successfully
    And the agent will succeed to Prove fork execution
    And the harness "fork-pr-ping" was dispatched exactly 1 time
    And the harness "fork-issue-ping" agent did not run
    And the harness "fork-pr-killed" agent did not run
    And the harness "fork-pr-nofork" agent did not run

  Scenario: Fork PR kill switch blocks all harnesses
    Given a custom harness "fork-pr-killswitch" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-pr-killswitch
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "change_proposal"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-fork-killswitch"
      """
    And the kill switch is active
    When a fork pull request is opened
    And the fork pull request is labeled "ready-for-fork-killswitch"
    Then the harness "fork-pr-killswitch" agent did not run

  Scenario: Fork PR sync + label dispatches harness
    Given a custom harness "fork-pr-sync" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-pr-sync
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "change_proposal"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-fork-sync"
      """
    And a dummy agent that would:
      | description              | op           | args                                                              |
      | Sync PR payload          | assert_json  | .fullsend/dispatch/event-payload.json,pull_request.head.repo.fork |
      | Prove sync execution     | write_fixture| output/dispatch-sync-ok.json, fixtures/dispatch/ok.json           |
    When a fork pull request is opened
    And a commit is pushed to the fork pull request
    And the fork pull request is labeled "ready-for-fork-sync"
    Then the harness "fork-pr-sync" workflow completes successfully
    And the agent will succeed to Prove sync execution
    And the harness "fork-pr-sync" was dispatched exactly 1 time
