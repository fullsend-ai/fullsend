Feature: Harness CEL dispatch

  Background:
    Given the enrolled test repository

  Scenario: Issue label dispatches issue-only harness
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
    And a dummy agent that would:
      | description              | op           | args                                               |
      | Issue URL set            | assert_env   | GITHUB_ISSUE_URL                                   |
      | Repo name set            | assert_env   | REPO_FULL_NAME                                     |
      | GH token set             | assert_env   | GH_TOKEN                                           |
      | Event payload file       | assert_file  | .fullsend/dispatch/event-payload.json              |
      | Payload has issue number | assert_json  | .fullsend/dispatch/event-payload.json,issue.number |
      | Prove execution          | write_fixture| output/dispatch-ok.json, fixtures/dispatch/ok.json |
    And an issue
    When the issue is labeled "wrong-label"
    Then the harness "issue-ping" agent did not run
    When the issue is labeled "ready-for-ping"
    Then the harness "issue-ping" workflow completes successfully
    And the agent will succeed to Prove execution
    And the harness "issue-ping" was dispatched exactly 1 time

  Scenario: PR does not trigger issue-only harness
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
    When a pull request is opened
    Then the harness "issue-ping" agent did not run

  Scenario: PR label dispatches PR-only harness
    Given a custom harness "pr-ping" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-pr-ping
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "change_proposal"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-pr-ping"
      """
    And a dummy agent that would:
      | description             | op           | args                                                    |
      | PR payload present      | assert_json  | .fullsend/dispatch/event-payload.json,pull_request.number |
      | Prove PR execution      | write_fixture| output/dispatch-pr-ok.json, fixtures/dispatch/ok.json    |
    When a pull request is opened
    And the pull request is labeled "ready-for-pr-ping"
    Then the harness "pr-ping" workflow completes successfully
    And the agent will succeed to Prove PR execution

  Scenario: Disabled harness is not dispatched while enabled one triggers
    Given a custom harness "enabled-ping" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-enabled-ping
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-enabled-test"
      """
    And a disabled custom harness "disabled-ping" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-disabled-ping
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-enabled-test"
      """
    And a dummy agent that would:
      | description         | op           | args                                                         |
      | Prove execution     | write_fixture| output/dispatch-enabled-ok.json, fixtures/dispatch/ok.json   |
    And an issue
    When the issue is labeled "ready-for-enabled-test"
    Then the harness "enabled-ping" workflow completes successfully
    And the agent will succeed to Prove execution
    And the harness "enabled-ping" was dispatched exactly 1 time
    And the harness "disabled-ping" agent did not run

  Scenario: PR review dispatches review-only harness
    Given a custom harness "review-ping" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-review-ping
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      trigger: |
        event.entity.kind == "change_proposal"
        && event.transition.kind == "review_submitted"
        && event.transition.review.state == "commented"
      """
    And a dummy agent that would:
      | description              | op           | args                                                    |
      | Review payload present   | assert_json  | .fullsend/dispatch/event-payload.json,pull_request.number |
      | Prove review execution   | write_fixture| output/dispatch-review-ok.json, fixtures/dispatch/ok.json |
    When a pull request is opened
    And a review comment is submitted on the pull request
    Then the harness "review-ping" workflow completes successfully
    And the agent will succeed to Prove review execution
