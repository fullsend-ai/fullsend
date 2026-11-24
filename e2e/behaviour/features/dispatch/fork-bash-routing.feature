# GitHub only: fork pull request dispatch follows the GitHub shim (#9221).
@skip:gitlab
Feature: Fork PR bash routing smoke

  Verify the fork pull request path through bash routing (the
  reusable-dispatch.yml route job) on a per-repo installation. The shim
  runs on pull_request and skips fork pull requests (#9221), so a fork
  pull request is reviewed only when a maintainer comments /fs-review.
  This is a time-boxed guard until harness CEL cutover (#2902). It owns
  bash routing only; actor authorization is owned by
  platform/actor-authorization.feature.

  Background:
    Given the enrolled test repository
    And a fork "test-repo-fork" of the enrolled test repository

  Scenario: Fork PR is reviewed only after a maintainer comments /fs-review
    Given a dummy agent that would:
      | description          | op            | args                                                       |
      | Prove bash routing   | write_fixture | output/bash-routing-ok.json, fixtures/dispatch/ok.json     |
      | Emit review JSON     | write_fixture | output/agent-result.json, fixtures/review/comment.json     |
    When a fork pull request is opened
    And the write actor posts "/fs-review" on the fork pull request
    Then the harness "review" workflow completes successfully
    And the agent will succeed to Prove bash routing
    And the agent will succeed to Emit review JSON
