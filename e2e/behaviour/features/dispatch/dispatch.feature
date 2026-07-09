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
    When the issue is labeled "ready-for-ping"
    Then the harness "issue-ping" workflow completes successfully
    And the agent will succeed to Prove execution

  Scenario: Wrong label does not trigger issue harness
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
    And an issue
    When the issue is labeled "wrong-label"
    Then the harness "issue-ping" agent did not run

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
