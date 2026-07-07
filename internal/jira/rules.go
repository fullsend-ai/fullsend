package jira

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Automation rule component types. These are internal Jira identifiers
// discovered by reverse-engineering existing rules via the Jira
// Automation REST API. Atlassian does not document these.
const (
	TriggerIssueCreated    = "jira.issue.event.trigger:created"
	TriggerCommentAdded    = "jira.issue.event.trigger:commented"
	ConditionCompareValues = "jira.comparator.condition"
	ActionSendWebRequest   = "jira.issue.outgoing.webhook"
)

// Rule names used for idempotency checks.
const (
	RuleNameAutoTriage = "fullsend: auto-triage on issue creation"
	RuleNameFsTriage   = "fullsend: /fs-triage command"
)

// RuleContext holds the identifiers needed to construct automation rules.
type RuleContext struct {
	Owner     string // GitHub repo owner
	Repo      string // GitHub repo name
	GitHubPAT string // GitHub PAT for webhook auth
	AccountID string // Jira account ID of the rule author
	CloudID   string // Jira Cloud ID
	ProjectID string // Jira numeric project ID
}

// projectARI returns the Atlassian Resource Identifier for the project.
func (rc RuleContext) projectARI() string {
	return fmt.Sprintf("ari:cloud:jira:%s:project/%s", rc.CloudID, rc.ProjectID)
}

func webhookURL(owner, repo string) string {
	return "https://api.github.com/repos/" + owner + "/" + repo + "/dispatches"
}

func webhookHeaders(githubPAT string) json.RawMessage {
	now := time.Now().UnixMilli()
	headers := []map[string]interface{}{
		{"id": fmt.Sprintf("_header_%d", now), "name": "Authorization", "value": "Bearer " + githubPAT, "headerSecure": false},
		{"id": fmt.Sprintf("_header_%d", now+1), "name": "Accept", "value": "application/vnd.github.v3+json", "headerSecure": false},
		{"id": fmt.Sprintf("_header_%d", now+2), "name": "Content-Type", "value": "application/json", "headerSecure": false},
	}
	data, _ := json.Marshal(headers)
	return data
}

func webhookAction(url, githubPAT, customBody string) RuleComponent {
	actionValue, _ := json.Marshal(map[string]interface{}{
		"url":                    url,
		"urlSecure":              false,
		"headers":                json.RawMessage(webhookHeaders(githubPAT)),
		"sendIssue":              false,
		"contentType":            "custom",
		"customBody":             customBody,
		"method":                 "POST",
		"responseEnabled":        false,
		"continueOnErrorEnabled": false,
	})
	return RuleComponent{
		Component:     "ACTION",
		Type:          ActionSendWebRequest,
		Value:         json.RawMessage(actionValue),
		SchemaVersion: 1,
	}
}

// basePayload returns a RulePayload with the required fields populated.
func basePayload(rc RuleContext, name string) RulePayload {
	return RulePayload{
		Name:  name,
		State: "ENABLED",
		Actor: &RuleActor{
			Type:  "ACCOUNT_ID",
			Actor: rc.AccountID,
		},
		AuthorAccountID:     rc.AccountID,
		CanOtherRuleTrigger: false,
		NotifyOnError:       "FIRSTERROR",
		WriteAccessType:     "OWNER_ONLY",
		RuleScopeARIs:       []string{rc.projectARI()},
	}
}

// AutoTriageRule builds a Jira Automation rule that triggers on work
// item creation and sends a webhook to the GitHub dispatch API.
func AutoTriageRule(rc RuleContext) CreateRuleRequest {
	url := webhookURL(rc.Owner, rc.Repo)

	triggerValue, _ := json.Marshal(map[string]interface{}{
		"eventKey":     "jira:issue_created",
		"issueEvent":   "issue_created",
		"eventFilters": []string{rc.projectARI()},
	})

	customBody := `{
  "event_type": "jira-issue-created",
  "client_payload": {
    "issue_key": "{{issue.key}}",
    "project_key": "{{issue.fields.project.key}}"
  }
}`

	payload := basePayload(rc, RuleNameAutoTriage)
	payload.Trigger = RuleComponent{
		Component:     "TRIGGER",
		Type:          TriggerIssueCreated,
		Value:         json.RawMessage(triggerValue),
		SchemaVersion: 1,
	}
	payload.Components = []RuleComponent{
		webhookAction(url, rc.GitHubPAT, customBody),
	}

	return CreateRuleRequest{
		Rule:        payload,
		Connections: []RuleConnection{},
	}
}

// SlashCommandRule builds a Jira Automation rule that triggers on
// comments containing a slash command and sends a webhook to the
// GitHub dispatch API.
func SlashCommandRule(rc RuleContext, command string) CreateRuleRequest {
	url := webhookURL(rc.Owner, rc.Repo)
	ruleName := "fullsend: " + command + " command"

	triggerValue, _ := json.Marshal(map[string]interface{}{
		"eventTypes":   []string{},
		"eventFilters": []string{rc.projectARI()},
	})

	conditionValue, _ := json.Marshal(map[string]interface{}{
		"first":    "{{comment.body}}",
		"second":   command,
		"operator": "CONTAINS",
	})

	customBody := fmt.Sprintf(`{
  "event_type": "jira-command",
  "client_payload": {
    "issue_key": "{{issue.key}}",
    "project_key": "{{issue.fields.project.key}}",
    "command": "%s"
  }
}`, command)

	payload := basePayload(rc, ruleName)
	payload.Trigger = RuleComponent{
		Component:     "TRIGGER",
		Type:          TriggerCommentAdded,
		Value:         json.RawMessage(triggerValue),
		SchemaVersion: 1,
	}
	payload.Components = []RuleComponent{
		{
			Component:     "CONDITION",
			Type:          ConditionCompareValues,
			Value:         json.RawMessage(conditionValue),
			SchemaVersion: 1,
		},
		webhookAction(url, rc.GitHubPAT, customBody),
	}

	return CreateRuleRequest{
		Rule:        payload,
		Connections: []RuleConnection{},
	}
}

// ManualRuleInstructions returns human-readable instructions for
// creating automation rules manually in the Jira UI when the API
// returns 403 (requires site admin — see AUTO-2120).
func ManualRuleInstructions(owner, repo string) string {
	url := webhookURL(owner, repo)
	var b strings.Builder

	b.WriteString("Rule 1: fullsend: auto-triage on issue creation\n")
	b.WriteString("\n")
	b.WriteString("  Trigger:   Work item created\n")
	b.WriteString("  Action:    Send web request\n")
	b.WriteString(fmt.Sprintf("    URL:     %s\n", url))
	b.WriteString("    Method:  POST\n")
	b.WriteString("    Headers:\n")
	b.WriteString("      Authorization: Bearer <your-github-pat>\n")
	b.WriteString("      Accept: application/vnd.github.v3+json\n")
	b.WriteString("      Content-Type: application/json\n")
	b.WriteString("    Body:\n")
	b.WriteString("      {\n")
	b.WriteString("        \"event_type\": \"jira-issue-created\",\n")
	b.WriteString("        \"client_payload\": {\n")
	b.WriteString("          \"issue_key\": \"{{issue.key}}\",\n")
	b.WriteString("          \"project_key\": \"{{issue.fields.project.key}}\"\n")
	b.WriteString("        }\n")
	b.WriteString("      }\n")
	b.WriteString("\n")
	b.WriteString("Rule 2: fullsend: /fs-triage command\n")
	b.WriteString("\n")
	b.WriteString("  Trigger:   Comment added (all comments)\n")
	b.WriteString("  Condition: {{comment.body}} contains /fs-triage\n")
	b.WriteString("  Action:    Send web request\n")
	b.WriteString(fmt.Sprintf("    URL:     %s\n", url))
	b.WriteString("    Method:  POST\n")
	b.WriteString("    Headers: (same as Rule 1)\n")
	b.WriteString("    Body:\n")
	b.WriteString("      {\n")
	b.WriteString("        \"event_type\": \"jira-command\",\n")
	b.WriteString("        \"client_payload\": {\n")
	b.WriteString("          \"issue_key\": \"{{issue.key}}\",\n")
	b.WriteString("          \"project_key\": \"{{issue.fields.project.key}}\",\n")
	b.WriteString("          \"command\": \"/fs-triage\"\n")
	b.WriteString("        }\n")
	b.WriteString("      }\n")

	return b.String()
}
