Feature: A fetch-capable harness outside the config allowlist fails the run

  A harness whose runtime fetch service can start (an explicit
  allow_runtime_fetch, URL-sourced skill/plugin directories, or a bare
  allowed_remote_resources declaration) may only declare fetch prefixes
  the repository's config-level allowed_remote_resources covers. Harness
  dispatch does not run that subset check, so the harness is dispatched;
  the run job's fullsend run refuses it (#9291) and the gate error
  reaches the run's logs. The no-repository-config shape of the same
  gate cannot be expressed here: dispatch itself reads config.yaml to
  enumerate agents, so a repo without one never reaches a run — that
  shape is pinned by the unit tests (TestRunAgent_FetchOnlyHarness
  NoOrgConfigErrors and its lock twin).

  Background:
    Given the enrolled test repository

  # Not declared in BEHAVIOUR_CAPABILITIES until this scenario has passed
  # one live run. A dev run (an authorized PR or the merge queue) installs
  # the PR head's fullsend binary; a stage run (push to main) takes main's.
  # Run make behaviour-test on demand with fetch-allowlist-gate added to
  # BEHAVIOUR_CAPABILITIES, on dev against the fix or on stage once it is
  # on main; when that passes, declare it here.
  @requires:capability:fetch-allowlist-gate
  Scenario: A fetch-capable harness with a prefix outside the config allowlist fails the run
    # Harness dispatch composes the harness without the subset check, so
    # the workflow is dispatched; the run job loads it, the fetch-service
    # gate refuses the harness before the agent runs, and the run's logs
    # carry the gate error.
    Given a custom harness "fetch-not-allowed" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-fetch-not-allowed
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      allow_runtime_fetch: true
      allowed_remote_resources:
        - "https://behaviour-not-allowed.example.com/"
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-fetch-not-allowed"
      """
    And a dummy agent that would:
      | description     | op            | args                                               |
      | Prove execution | write_fixture | output/dispatch-ok.json, fixtures/dispatch/ok.json |
    And an issue
    When the issue is labeled "ready-for-fetch-not-allowed"
    Then the harness "fetch-not-allowed" workflow fails with "is not covered by the org allowlist" in its logs
