@playback
Feature: Playback runtime serves canned results
  # Replays canned agent results without LLM inference, exercising the
  # dummy-playback pipeline (dispatch, pre-script, post-script, labeling,
  # status comments) against a real per-repo install instead of invoking
  # an LLM: triage marks the issue ready-to-code, code opens a pull
  # request, review requests changes, fix addresses them, and a second
  # review approves.
  #
  # Issue creation uses the e2e suite's own minted GitHub App installation
  # token, so the issue is bot-authored. fullsend.yaml's "issues: opened"
  # route only dispatches triage for an actor holding collaborator
  # write/triage permission, which the e2e installation bot is never
  # granted — so issue-open cannot be this pipeline's trigger. Labeling
  # the issue "ready-for-triage" afterward is the sole working trigger: a
  # label-added event is dispatched unconditionally regardless of actor
  # identity (#7957 review).
  #
  # Successful jobs, an open PR, and submitted reviews alone don't establish
  # that the code or fix stage actually published the expected content —
  # final approval here is canned, so an incorrect fix could still push some
  # commit, trigger review, and satisfy every other assertion while leaving
  # the code stage's TOCTOU bug in main.go. "the published repository matches
  # fixture" closes that gap by comparing the pull request's own commit
  # (pinned at that point in the scenario, not re-resolved later) against the
  # result fixture's repo/ tree, right after each stage that is supposed to
  # have published it (#7957 review).

  Scenario: Playback pipeline
    Given the enrolled test repository
    And a triage agent that returns "triage/bug"
    And a code agent that returns "code/bug-fix"
    And a review agent that returns "review/request-changes"
    And a fix agent that returns "fix/success"
    And a review agent that returns "review/approve"
    When an issue "concurrent-map-crash" is created
    And the issue is labeled "ready-for-triage"
    Then the triage agent is triggered
    And the triage agent completes successfully
    And the issue has label "ready-to-code"
    Then the code agent is triggered
    And the code agent completes successfully
    And a pull request exists
    And the published repository matches fixture "code/bug-fix"
    Then the review agent is triggered
    And the review agent completes successfully
    And the review agent submitted "changes_requested"
    Then the fix agent is triggered
    And the fix agent completes successfully
    And the published repository matches fixture "fix/success"
    Then the review agent is triggered
    And the review agent completes successfully
    And the review agent submitted "approved"
