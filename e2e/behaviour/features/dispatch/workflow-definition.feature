# A harness that runs a workflow from a workflow definition (ADR 0130):
# the harness workflow: field pins a definition and names one of its
# workflows, and the runner delivers the definition as a plugin and starts
# /<namespace>:<workflow> <args>.
#
# The definition is committed into the leased repository under
# pipelines/sample-pipeline/ and used as a path source, which resolves in
# the repository that holds the harness. It goes in a
# sub-directory, not at `source: .`: the leased repository's root is not a
# plugin (it has no .claude-plugin/plugin.json and no workflows/), and `.`
# would ship the whole repository to the sandbox as the definition.
#
# The repository stays on the install runtime (dummy), so no model runs and
# the workflow script never executes: the run proves the plan-time path end
# to end (the source resolves, the definition is read from the checkout, checked as a plugin with workflows/triage-fanout.js, hashed and
# delivered, and the agent's tools: names Workflow) and the runner records
# the command it started in metrics.json. The custom harness carries no
# post-script, so the issue labels are not asserted here.
Feature: Workflow definitions run from a harness

  Scenario: a harness starts a workflow from a definition in the repository
    Given the enrolled test repository
    And the workflow definition "sample-pipeline" is committed at "pipelines/sample-pipeline"
    And a custom harness "workflow-triage" with:
      """
      agent: agents/workflow-triage.md
      role: triage
      slug: fullsend-ai-workflow-triage
      model: opus
      image: ghcr.io/fullsend-ai/fullsend-sandbox:latest
      policy: policies/base.yaml
      trigger: |
        event.entity.kind == "work_item"
        && event.transition.kind == "label_changed"
        && event.transition.label.name == "ready-for-workflow-triage"
      workflow:
        source: pipelines/sample-pipeline
        name: triage-fanout
        args: "issue 1"
      """
    And an agent "workflow-triage" defined as:
      """
      ---
      name: workflow-triage
      description: Behaviour agent that starts the triage-fanout workflow.
      tools: Read, Bash, Workflow
      ---
      Start the workflow named in your prompt and report its result.
      """
    And a dummy agent that would:
      | description      | op            | args                                                      |
      | Emit triage JSON | write_fixture | output/agent-result.json, fixtures/triage/sufficient.json |
    And an issue
    When the issue is labeled "ready-for-workflow-triage"
    Then the harness "workflow-triage" workflow completes successfully
    And the agent will succeed to Emit triage JSON
    And the run selected the "dummy" runtime
    And the run started the workflow "/sample-pipeline:triage-fanout issue 1" from "pipelines/sample-pipeline"
