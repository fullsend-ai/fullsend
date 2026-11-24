# GitHub only: fork pull request dispatch follows the GitHub shim (#9221).
@skip:gitlab
Feature: Fork PR dispatch

  Owns fork pull request CEL matching (is_fork, linked change proposal, disabled
  harnesses), the kill switch and synchronize handling for harness
  dispatch. The shim runs on pull_request and skips fork pull requests
  (#9221): fork pull request events never dispatch, so every scenario
  dispatches through a maintainer's slash command on the fork pull request.
  Which actors may trigger a harness on a fork pull request is owned by
  platform/actor-authorization.feature.

  Background:
    Given the enrolled test repository
    And a fork "test-repo-fork" of the enrolled test repository

  Scenario: Fork PR dispatches a harness only from a maintainer comment
    Given a custom harness "fork-pr-ping" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-pr-ping
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        (event.entity.kind == "change_proposal"
          && event.transition.kind == "label_changed"
          && event.transition.label.name == "ready-for-fork-ping")
        || (event.transition.kind == "comment_added"
          && has(event.transition.comment.command)
          && event.transition.comment.command == "/fs-fork-ping"
          && has(event.entity.linked_change_proposal)
          && event.state.change_proposal.is_fork)
      """
    And a custom harness "fork-issue-ping" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-issue-ping
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.transition.kind == "comment_added"
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-fork-ping"
        && !has(event.entity.linked_change_proposal)
      """
    And a disabled custom harness "fork-pr-killed" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-pr-killed
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.transition.kind == "comment_added"
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-fork-ping"
        && has(event.entity.linked_change_proposal)
      """
    And a custom harness "fork-pr-nofork" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-pr-nofork
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.transition.kind == "comment_added"
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-fork-ping"
        && has(event.entity.linked_change_proposal)
        && !event.state.change_proposal.is_fork
      """
    And a dummy agent that would:
      | description            | op           | args                                                              |
      | Prove fork execution   | write_fixture| output/dispatch-fork-ok.json, fixtures/dispatch/ok.json           |
    When a fork pull request is opened
    And the fork pull request is labeled "ready-for-fork-ping"
    # The label branch of fork-pr-ping matches this event; the shim skips it.
    Then the harness "fork-pr-ping" agent did not run
    When the write actor posts "/fs-fork-ping" on the fork pull request
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
      policy: policies/base.yaml
      trigger: |
        event.transition.kind == "comment_added"
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-fork-killswitch"
        && has(event.entity.linked_change_proposal)
      """
    And the kill switch is active
    When a fork pull request is opened
    And the write actor posts "/fs-fork-killswitch" on the fork pull request
    Then the harness "fork-pr-killswitch" agent did not run

  Scenario: Fork PR kill switch in config.base.yaml blocks all harnesses
    Given a custom harness "fork-pr-killswitch-base" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-pr-killswitch-base
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.transition.kind == "comment_added"
        && has(event.transition.comment.command)
        && event.transition.comment.command == "/fs-fork-killswitch-base"
        && has(event.entity.linked_change_proposal)
      """
    And the kill switch is active in config.base.yaml
    When a fork pull request is opened
    And the write actor posts "/fs-fork-killswitch-base" on the fork pull request
    Then the harness "fork-pr-killswitch-base" agent did not run

  Scenario: Fork PR push does not dispatch; a maintainer comment does
    Given a custom harness "fork-pr-sync" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fork-pr-sync
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        (event.entity.kind == "change_proposal"
          && event.transition.kind == "synchronized")
        || (event.transition.kind == "comment_added"
          && has(event.transition.comment.command)
          && event.transition.comment.command == "/fs-fork-sync"
          && has(event.entity.linked_change_proposal))
      """
    And a dummy agent that would:
      | description              | op           | args                                                              |
      | Prove sync execution     | write_fixture| output/dispatch-sync-ok.json, fixtures/dispatch/ok.json           |
    When a fork pull request is opened
    And a commit is pushed to the fork pull request
    Then the harness "fork-pr-sync" agent did not run
    When the write actor posts "/fs-fork-sync" on the fork pull request
    Then the harness "fork-pr-sync" workflow completes successfully
    And the agent will succeed to Prove sync execution
    And the harness "fork-pr-sync" was dispatched exactly 1 time
