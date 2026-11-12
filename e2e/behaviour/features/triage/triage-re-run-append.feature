# Sticky comments were removed (#9334): every agent run appends its
# result as a new comment at the end of the thread instead of editing
# the comment the first run created. This scenario re-runs triage on the
# same issue (first on the ready-for-triage label, then on a /fs-triage
# comment) and asserts the second result is a new comment behind the
# first, so a regression to edit-in-place fails the comment count.
# @skip:gitlab because the GitLab posting door for triage lives in the
# agents repo's post-triage.sh, whose GitLab notes path still edits in
# place until the companion change lands there; the GitHub door is the
# one this repository owns.
@skip:gitlab
Feature: Re-running triage appends a new comment

  Scenario: A second triage run posts a new comment and leaves the first unchanged
    Given the enrolled test repository
    And a dummy agent that would:
      | description      | op            | args                                                      |
      | Emit triage JSON | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json |
    And an issue
    When the issue is labeled "ready-for-triage"
    Then the triage agent completes successfully
    And the issue has label "ready-to-code"
    And the issue has 1 comment containing "<!-- fullsend:triage-agent -->"
    When a dummy agent that would:
      | description      | op            | args                                                              |
      | Emit triage JSON | write_fixture | output/agent-result.json, fixtures/triage/sufficient-recheck.json |
    And the write actor posts "/fs-triage" on the issue
    Then the triage agent completes successfully
    And the issue has 2 comments containing "<!-- fullsend:triage-agent -->"
    And the last comment on the issue contains "Re-triage on the second run"
    And a comment on the issue other than the last contains "This issue is ready for implementation."
