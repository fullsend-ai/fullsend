Feature: Jira dispatch transport bounds

  The GitLab dispatch transport bounds what an agent run sees: state.labels
  past a 1024-byte JSON prefix are dropped before trigger matching and
  before encoding, and an oversized /fs-* command is refused outright
  rather than dispatched shortened. These scenarios run the real Jira
  poller against a local mock Jira server with the production CEL trigger
  matching, dispatch through the real GitLab dispatch door
  (poll.Poller.DispatchJiraEvent, including its transport budget), and
  stand at the GitLab API boundary: a fake API records the agent pipelines
  it is asked to create.

  Background:
    Given a mock Jira server
    And a local harness "code" with role "coder" and trigger:
      """
      event.source.system == "jira"
      && event.entity.kind == "work_item"
      && event.transition.kind == "label_changed"
      && event.transition.label.action == "added"
      && "ready-to-code" in event.state.labels
      """
    And a local harness "triage" with role "triage" and trigger:
      """
      event.source.system == "jira"
      && event.entity.kind == "work_item"
      && event.transition.kind == "comment_added"
      && has(event.transition.comment.command)
      && event.transition.comment.command == "/fs-triage"
      && event.actor.kind == "human"
      """
    And the Jira poller dispatches to the GitLab project "group/sub/project"

  Scenario: A trigger keyed on a label past the transport bound does not fire
    Given a Jira issue "PROJ-501" with labels "backlog"
    And 30 pad labels of 40 characters on Jira issue "PROJ-501"
    And the label "ready-to-code" is added to Jira issue "PROJ-501"
    And a Jira issue "PROJ-503" with labels "backlog"
    And 20 pad labels of 40 characters on Jira issue "PROJ-503"
    And the label "ready-to-code" is added to Jira issue "PROJ-503"
    And a Jira issue "PROJ-502" with labels "backlog"
    And the label "ready-to-code" is added to Jira issue "PROJ-502"
    When the Jira GitLab poller runs
    Then no agent pipeline was created for issue "PROJ-501"
    And an agent pipeline was created for issue "PROJ-503"
    And an agent pipeline was created for issue "PROJ-502"

  Scenario: An oversized /fs-* command produces no run
    Given a Jira issue "PROJ-601" with labels "bug"
    And an oversized "/fs-triage" command comment is added to Jira issue "PROJ-601"
    And a Jira issue "PROJ-602" with labels "bug"
    And a comment "/fs-triage check acceptance criteria" is added to Jira issue "PROJ-602"
    When the Jira GitLab poller runs
    Then no agent pipeline was created for issue "PROJ-601"
    And an agent pipeline was created for issue "PROJ-602"
