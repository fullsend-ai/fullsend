@skip:gitlab
Feature: A dispatch run with skipped jobs reads as complete logs

  The GitHub Actions dispatch workflow carries a job per stage (triage,
  code, review, fix, retro, prioritize), each gated on the route's stage
  output, and runs every harness in the single harness-run matrix job,
  so a run routed to one stage leaves the other stages' jobs skipped: a
  job that never ran has no log to fetch. The GitHub client leaves the
  skipped job's log unrequested and lists the job with a header only
  ("[completed/skipped]"), so the behaviour-test log reader sees a
  complete snapshot with no fetch-failure note and log checks on the run
  pass (AISDLC-652; the GitHub half landed with #9107). Without the
  skip, the skipped job's log request reads as a fetch that failed, the
  snapshot stays incomplete, and the absence check below fails on the
  incomplete snapshot instead of passing.

  GitLab dispatch runs each harness in its own single-job pipeline
  (createAgentPipeline), so no e2e scenario can observe a skipped job
  there; the GitLab client's skip of a skipped job's trace — with its
  "=== Job N (name) [skipped] ===" header — is pinned by the unit tests
  TestGetWorkflowRunLogs_SkippedJobWritesHeader and
  TestGetWorkflowRunLogs_AllJobsSkipped_HeaderOnlyLogs
  (internal/forge/gitlab/ci_logs_notes_test.go).

  Scenario: a dispatch run with skipped jobs yields complete logs
    Given the enrolled test repository
    And a custom harness "skip-listed" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-skip-listed
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-skip-listed"
      """
    And a dummy agent that would:
      | description     | op            | args                                               |
      | Prove execution | write_fixture | output/dispatch-ok.json, fixtures/dispatch/ok.json |
    And an issue
    When the issue is labeled "ready-for-skip-listed"
    Then the harness "skip-listed" workflow completes successfully
    And the harness "skip-listed" workflow logs contain "[completed/skipped] ==="
    And the harness "skip-listed" workflow logs do not contain "error fetching trace:"
    And the agent will succeed to Prove execution
