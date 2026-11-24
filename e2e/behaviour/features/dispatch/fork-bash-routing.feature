# GitHub only: fork pull request dispatch follows the GitHub shim (#9221).
@skip:gitlab
Feature: Fork PR bash routing smoke

  Verify the fork pull request path through bash routing (the
  reusable-dispatch.yml route job) on a per-repo installation. The shim
  runs on pull_request and skips fork pull requests (#9221); a fork pull
  request reaches it through the fork PR stub (fullsend-fork-pr.yaml) and
  the shim's workflow_run trigger, or through a maintainer's /fs-review.
  This is a time-boxed guard until harness CEL cutover (#2902). It owns
  bash routing only; actor authorization is owned by
  platform/actor-authorization.feature, and the workflow_run guards (stale
  head SHA, unauthorized author, label window, closer permission) by the
  route job's unit tests.

  Scenario: Fork PR is reviewed only after a maintainer comments /fs-review
    # An outsider's fork PR: its author may not start a review, so only
    # the maintainer's comment does, whether or not the forge sends the
    # stub's workflow_run.
    Given the enrolled test repository
    And a fork "test-repo-fork" of the enrolled test repository owned by the outsider
    And a dummy agent that would:
      | description          | op            | args                                                       |
      | Prove bash routing   | write_fixture | output/bash-routing-ok.json, fixtures/dispatch/ok.json     |
      | Emit review JSON     | write_fixture | output/agent-result.json, fixtures/review/comment.json     |
    When a fork pull request is opened by the outsider
    And the write actor posts "/fs-review" on the fork pull request
    Then the harness "review" workflow completes successfully
    And the agent will succeed to Prove bash routing
    And the agent will succeed to Emit review JSON

  # Needs GitHub's workflow_run event, which the GitHub emulator does not
  # send; declare the capability where the forge does (live GitHub).
  @requires:capability:fork-pr-workflow-run
  Scenario: Fork PR from an authorized author is reviewed without a comment
    Given the enrolled test repository
    And a fork "test-repo-fork" of the enrolled test repository
    And a dummy agent that would:
      | description          | op            | args                                                       |
      | Prove bash routing   | write_fixture | output/bash-routing-ok.json, fixtures/dispatch/ok.json     |
      | Emit review JSON     | write_fixture | output/agent-result.json, fixtures/review/comment.json     |
    When a fork pull request is opened
    Then the harness "review" workflow completes successfully
    And the agent will succeed to Prove bash routing
    And the agent will succeed to Emit review JSON
