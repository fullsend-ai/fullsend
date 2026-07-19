Feature: Fork PR dispatch

  Background:
    Given the enrolled test repository
    And a fork "test-repo-fork" of the enrolled test repository

  Scenario: Fork PR opened dispatches pull_request_target harness
    Given a custom harness "fork-pr-ping" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-pr-ping
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "change_proposal"
        && event.transition.kind == "opened"
      """
    And a dummy agent that would:
      | description            | op           | args                                                              |
      | Fork PR payload        | assert_json  | .fullsend/dispatch/event-payload.json,pull_request.head.repo.fork |
      | Prove fork execution   | write_fixture| output/dispatch-fork-ok.json, fixtures/dispatch/ok.json           |
    When a fork pull request is opened
    Then the harness "fork-pr-ping" workflow completes successfully
    And the agent will succeed to Prove fork execution
    And the harness "fork-pr-ping" was dispatched exactly 1 time

  Scenario: Fork PR synchronize dispatches harness
    Given a custom harness "fork-pr-sync" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-pr-sync
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "change_proposal"
        && event.transition.kind == "synchronize"
      """
    And a dummy agent that would:
      | description              | op           | args                                                              |
      | Sync PR payload          | assert_json  | .fullsend/dispatch/event-payload.json,pull_request.head.repo.fork |
      | Prove sync execution     | write_fixture| output/dispatch-sync-ok.json, fixtures/dispatch/ok.json           |
    When a fork pull request is opened
    And a commit is pushed to the fork pull request
    Then the harness "fork-pr-sync" workflow completes successfully
    And the agent will succeed to Prove sync execution
    And the harness "fork-pr-sync" was dispatched exactly 1 time

  Scenario: Fork PR does not trigger issue-only harness
    Given a custom harness "issue-ping" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-issue-ping
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-ping"
      """
    When a fork pull request is opened
    Then the harness "issue-ping" agent did not run
