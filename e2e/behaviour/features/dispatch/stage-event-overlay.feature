@skip:gitlab
Feature: A built-in stage job's run resolves event-keyed overlays

  The reusable-dispatch.yml route job embeds the routed event's complete
  normalized form in the event payload, and every built-in stage job
  writes it to .fullsend/dispatch/event-payload.json before the agent
  action, so the stage job's `fullsend run` loads the harness with the
  event the label dispatch produced (extractNormalizedEventFromDispatch
  in internal/cli/run.go reads the file back as the overlay `event`).

  The scenario replaces the repository's triage agents: entry with a
  local harness carrying two overlays: one guarded on the trusted event
  fields of the label that routed the stage, one guarded on the event
  being absent. The harness has no trigger, so harness dispatch never
  matrix-dispatches it; the ready-for-triage label routes the bash
  router to the built-in Triage stage job, whose `fullsend run` loads
  this harness through the agents: entry. The sandbox env pins which
  overlay applied: with the event present the first overlay's
  assert_env succeeds and the second (no-event) one is unset, so its
  assert_env fails as expected; without an event the first overlay's
  guard errors into a non-match and the second applies, failing the
  scenario both ways.

  Scenario: The stage job's run sees the routed event
    Given the enrolled test repository
    And a custom harness "triage" with:
      """
      agent: agents/triage.md
      role: triage
      slug: fullsend-ai-stage-event-overlay
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      overlays:
        - when: 'event.entity.kind == "work_item" && event.transition.kind == "label_changed" && event.transition.label.name == "ready-for-triage"'
          env:
            sandbox:
              EVENT_OVERLAY_APPLIED: "1"
        - when: '!has(event.source)'
          env:
            sandbox:
              EVENT_OVERLAY_MISSING: "1"
      """
    And a dummy agent that would:
      | description             | op            | args                                                      | expect |
      | Event overlay applied   | assert_env    | EVENT_OVERLAY_APPLIED                                     |        |
      | No-event overlay unset  | assert_env    | EVENT_OVERLAY_MISSING                                     | fail   |
      | Emit triage JSON        | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json |        |
    And an issue
    When the issue is labeled "ready-for-triage"
    Then the triage workflow completes successfully
    And the run selected the "dummy" runtime
    And the agent will succeed to Event overlay applied
    And the agent will fail to No-event overlay unset
    And the agent will succeed to Emit triage JSON
