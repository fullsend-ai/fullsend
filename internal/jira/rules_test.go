package jira

import (
	"encoding/json"
	"strings"
	"testing"
)

var testRC = RuleContext{
	Owner:     "myorg",
	Repo:      "myrepo",
	GitHubPAT: "ghp_test123",
	AccountID: "abc-123",
	CloudID:   "cloud-456",
	ProjectID: "11294",
}

func TestAutoTriageRule_Structure(t *testing.T) {
	rule := AutoTriageRule(testRC)

	if rule.Rule.Name != RuleNameAutoTriage {
		t.Errorf("Name = %q, want %q", rule.Rule.Name, RuleNameAutoTriage)
	}
	if rule.Rule.State != "ENABLED" {
		t.Errorf("State = %q, want ENABLED", rule.Rule.State)
	}
	if rule.Rule.NotifyOnError != "FIRSTERROR" {
		t.Errorf("NotifyOnError = %q, want FIRSTERROR", rule.Rule.NotifyOnError)
	}
	if rule.Rule.WriteAccessType != "OWNER_ONLY" {
		t.Errorf("WriteAccessType = %q, want OWNER_ONLY", rule.Rule.WriteAccessType)
	}
}

func TestAutoTriageRule_Actor(t *testing.T) {
	rule := AutoTriageRule(testRC)

	if rule.Rule.Actor == nil {
		t.Fatal("Actor is nil")
	}
	if rule.Rule.Actor.Type != "ACCOUNT_ID" {
		t.Errorf("Actor.Type = %q, want ACCOUNT_ID", rule.Rule.Actor.Type)
	}
	if rule.Rule.Actor.Actor != "abc-123" {
		t.Errorf("Actor.Actor = %q, want abc-123", rule.Rule.Actor.Actor)
	}
	if rule.Rule.AuthorAccountID != "abc-123" {
		t.Errorf("AuthorAccountID = %q, want abc-123", rule.Rule.AuthorAccountID)
	}
}

func TestAutoTriageRule_RuleScope(t *testing.T) {
	rule := AutoTriageRule(testRC)

	if len(rule.Rule.RuleScopeARIs) != 1 {
		t.Fatalf("RuleScopeARIs len = %d, want 1", len(rule.Rule.RuleScopeARIs))
	}
	want := "ari:cloud:jira:cloud-456:project/11294"
	if rule.Rule.RuleScopeARIs[0] != want {
		t.Errorf("RuleScopeARIs[0] = %q, want %q", rule.Rule.RuleScopeARIs[0], want)
	}
}

func TestAutoTriageRule_Connections(t *testing.T) {
	rule := AutoTriageRule(testRC)

	if rule.Connections == nil {
		t.Fatal("Connections is nil, want empty slice")
	}
	if len(rule.Connections) != 0 {
		t.Errorf("Connections len = %d, want 0", len(rule.Connections))
	}
}

func TestAutoTriageRule_Trigger(t *testing.T) {
	rule := AutoTriageRule(testRC)

	tr := rule.Rule.Trigger
	if tr.Component != "TRIGGER" {
		t.Errorf("Trigger.Component = %q, want TRIGGER", tr.Component)
	}
	if tr.Type != TriggerIssueCreated {
		t.Errorf("Trigger.Type = %q, want %q", tr.Type, TriggerIssueCreated)
	}
	if tr.SchemaVersion != 1 {
		t.Errorf("Trigger.SchemaVersion = %d, want 1", tr.SchemaVersion)
	}

	var trigVal map[string]interface{}
	if err := json.Unmarshal(tr.Value, &trigVal); err != nil {
		t.Fatalf("unmarshal trigger value: %v", err)
	}
	if trigVal["eventKey"] != "jira:issue_created" {
		t.Errorf("trigger eventKey = %v", trigVal["eventKey"])
	}
	if trigVal["issueEvent"] != "issue_created" {
		t.Errorf("trigger issueEvent = %v", trigVal["issueEvent"])
	}
	filters, ok := trigVal["eventFilters"].([]interface{})
	if !ok || len(filters) != 1 {
		t.Fatalf("eventFilters = %v", trigVal["eventFilters"])
	}
	if filters[0] != "ari:cloud:jira:cloud-456:project/11294" {
		t.Errorf("eventFilters[0] = %v", filters[0])
	}
}

func TestAutoTriageRule_WebhookAction(t *testing.T) {
	rule := AutoTriageRule(testRC)

	if len(rule.Rule.Components) != 1 {
		t.Fatalf("Components len = %d, want 1", len(rule.Rule.Components))
	}
	action := rule.Rule.Components[0]
	if action.Component != "ACTION" {
		t.Errorf("action.Component = %q, want ACTION", action.Component)
	}
	if action.Type != ActionSendWebRequest {
		t.Errorf("action.Type = %q, want %q", action.Type, ActionSendWebRequest)
	}
	if action.SchemaVersion != 1 {
		t.Errorf("action.SchemaVersion = %d, want 1", action.SchemaVersion)
	}

	var val map[string]interface{}
	if err := json.Unmarshal(action.Value, &val); err != nil {
		t.Fatalf("unmarshal action value: %v", err)
	}
	if val["url"] != "https://api.github.com/repos/myorg/myrepo/dispatches" {
		t.Errorf("url = %v", val["url"])
	}
	if val["method"] != "POST" {
		t.Errorf("method = %v", val["method"])
	}
	if val["contentType"] != "custom" {
		t.Errorf("contentType = %v", val["contentType"])
	}
	if val["sendIssue"] != false {
		t.Errorf("sendIssue = %v", val["sendIssue"])
	}
	body, _ := val["customBody"].(string)
	if !strings.Contains(body, "jira-issue-created") {
		t.Error("customBody missing event_type jira-issue-created")
	}
	if !strings.Contains(body, "{{issue.key}}") {
		t.Error("customBody missing {{issue.key}}")
	}
	if !strings.Contains(body, "{{issue.fields.project.key}}") {
		t.Error("customBody missing {{issue.fields.project.key}}")
	}

	headers, ok := val["headers"].([]interface{})
	if !ok {
		t.Fatal("headers is not an array")
	}
	if len(headers) != 3 {
		t.Fatalf("headers len = %d, want 3", len(headers))
	}
	h0, _ := headers[0].(map[string]interface{})
	if h0["name"] != "Authorization" {
		t.Errorf("header[0].name = %v", h0["name"])
	}
	if !strings.HasPrefix(h0["value"].(string), "Bearer ghp_test123") {
		t.Errorf("header[0].value = %v", h0["value"])
	}
}

func TestSlashCommandRule_Structure(t *testing.T) {
	rule := SlashCommandRule(testRC, "/fs-triage")

	if rule.Rule.Name != "fullsend: /fs-triage command" {
		t.Errorf("Name = %q", rule.Rule.Name)
	}
	if rule.Rule.State != "ENABLED" {
		t.Errorf("State = %q", rule.Rule.State)
	}
	if rule.Rule.Actor == nil || rule.Rule.Actor.Actor != "abc-123" {
		t.Error("Actor not set correctly")
	}
	if len(rule.Rule.RuleScopeARIs) != 1 {
		t.Fatal("missing RuleScopeARIs")
	}
	if len(rule.Connections) != 0 {
		t.Errorf("Connections should be empty, got %d", len(rule.Connections))
	}
}

func TestSlashCommandRule_Trigger(t *testing.T) {
	rule := SlashCommandRule(testRC, "/fs-triage")

	tr := rule.Rule.Trigger
	if tr.Type != TriggerCommentAdded {
		t.Errorf("Trigger.Type = %q, want %q", tr.Type, TriggerCommentAdded)
	}

	var trigVal map[string]interface{}
	if err := json.Unmarshal(tr.Value, &trigVal); err != nil {
		t.Fatalf("unmarshal trigger value: %v", err)
	}
	filters, ok := trigVal["eventFilters"].([]interface{})
	if !ok || len(filters) != 1 {
		t.Fatalf("eventFilters = %v", trigVal["eventFilters"])
	}
}

func TestSlashCommandRule_Condition(t *testing.T) {
	rule := SlashCommandRule(testRC, "/fs-triage")

	if len(rule.Rule.Components) != 2 {
		t.Fatalf("Components len = %d, want 2", len(rule.Rule.Components))
	}

	cond := rule.Rule.Components[0]
	if cond.Component != "CONDITION" {
		t.Errorf("condition.Component = %q, want CONDITION", cond.Component)
	}
	if cond.Type != ConditionCompareValues {
		t.Errorf("condition.Type = %q, want %q", cond.Type, ConditionCompareValues)
	}

	var condVal map[string]interface{}
	if err := json.Unmarshal(cond.Value, &condVal); err != nil {
		t.Fatalf("unmarshal condition value: %v", err)
	}
	if condVal["first"] != "{{comment.body}}" {
		t.Errorf("first = %v", condVal["first"])
	}
	if condVal["second"] != "/fs-triage" {
		t.Errorf("second = %v", condVal["second"])
	}
	if condVal["operator"] != "CONTAINS" {
		t.Errorf("operator = %v", condVal["operator"])
	}
}

func TestSlashCommandRule_Action(t *testing.T) {
	rule := SlashCommandRule(testRC, "/fs-triage")

	action := rule.Rule.Components[1]
	if action.Component != "ACTION" {
		t.Errorf("action.Component = %q, want ACTION", action.Component)
	}
	if action.Type != ActionSendWebRequest {
		t.Errorf("action.Type = %q", action.Type)
	}

	var val map[string]interface{}
	if err := json.Unmarshal(action.Value, &val); err != nil {
		t.Fatalf("unmarshal action value: %v", err)
	}
	body, _ := val["customBody"].(string)
	if !strings.Contains(body, "jira-command") {
		t.Error("customBody missing jira-command")
	}
	if !strings.Contains(body, `"/fs-triage"`) {
		t.Error("customBody missing /fs-triage command")
	}
}

func TestSlashCommandRule_DifferentCommands(t *testing.T) {
	for _, cmd := range []string{"/fs-triage", "/fs-prioritize", "/fs-code"} {
		rule := SlashCommandRule(testRC, cmd)
		if !strings.Contains(rule.Rule.Name, cmd) {
			t.Errorf("rule name %q missing command %q", rule.Rule.Name, cmd)
		}
		var condVal map[string]interface{}
		json.Unmarshal(rule.Rule.Components[0].Value, &condVal)
		if condVal["second"] != cmd {
			t.Errorf("condition second = %v, want %s", condVal["second"], cmd)
		}
	}
}

func TestWebhookURL(t *testing.T) {
	url := webhookURL("org", "repo")
	want := "https://api.github.com/repos/org/repo/dispatches"
	if url != want {
		t.Errorf("webhookURL = %q, want %q", url, want)
	}
}

func TestRuleContext_ProjectARI(t *testing.T) {
	rc := RuleContext{CloudID: "cloud-xyz", ProjectID: "99"}
	ari := rc.projectARI()
	want := "ari:cloud:jira:cloud-xyz:project/99"
	if ari != want {
		t.Errorf("projectARI = %q, want %q", ari, want)
	}
}

func TestComponentTypeConstants(t *testing.T) {
	tests := map[string]string{
		"TriggerIssueCreated":    TriggerIssueCreated,
		"TriggerCommentAdded":    TriggerCommentAdded,
		"ConditionCompareValues": ConditionCompareValues,
		"ActionSendWebRequest":   ActionSendWebRequest,
	}
	expected := map[string]string{
		"TriggerIssueCreated":    "jira.issue.event.trigger:created",
		"TriggerCommentAdded":    "jira.issue.event.trigger:commented",
		"ConditionCompareValues": "jira.comparator.condition",
		"ActionSendWebRequest":   "jira.issue.outgoing.webhook",
	}
	for name, got := range tests {
		if got != expected[name] {
			t.Errorf("%s = %q, want %q", name, got, expected[name])
		}
	}
}

func TestAutoTriageRule_FullJSON_IsValid(t *testing.T) {
	rule := AutoTriageRule(testRC)
	data, err := json.Marshal(rule)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundtrip CreateRuleRequest
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatalf("unmarshal roundtrip: %v", err)
	}
	if roundtrip.Rule.Name != RuleNameAutoTriage {
		t.Errorf("roundtrip name = %q", roundtrip.Rule.Name)
	}
}

func TestSlashCommandRule_FullJSON_IsValid(t *testing.T) {
	rule := SlashCommandRule(testRC, "/fs-triage")
	data, err := json.Marshal(rule)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundtrip CreateRuleRequest
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatalf("unmarshal roundtrip: %v", err)
	}
	if roundtrip.Rule.Name != "fullsend: /fs-triage command" {
		t.Errorf("roundtrip name = %q", roundtrip.Rule.Name)
	}
	if len(roundtrip.Rule.Components) != 2 {
		t.Errorf("roundtrip components len = %d", len(roundtrip.Rule.Components))
	}
}

func TestManualRuleInstructions(t *testing.T) {
	instructions := ManualRuleInstructions("myorg", "myrepo")

	checks := []string{
		"Rule 1: fullsend: auto-triage on issue creation",
		"Rule 2: fullsend: /fs-triage command",
		"https://api.github.com/repos/myorg/myrepo/dispatches",
		"Work item created",
		"Send web request",
		"POST",
		"jira-issue-created",
		"jira-command",
		"/fs-triage",
		"{{issue.key}}",
		"{{issue.fields.project.key}}",
		"Comment added",
		"{{comment.body}} contains /fs-triage",
		"Bearer <your-github-pat>",
	}

	for _, check := range checks {
		if !strings.Contains(instructions, check) {
			t.Errorf("instructions missing %q", check)
		}
	}
}

func TestManualRuleInstructions_DifferentRepo(t *testing.T) {
	instructions := ManualRuleInstructions("acme", "widgets")
	if !strings.Contains(instructions, "https://api.github.com/repos/acme/widgets/dispatches") {
		t.Error("instructions should use the provided owner/repo")
	}
}

func TestWebhookHeaders_HasThreeEntries(t *testing.T) {
	headers := webhookHeaders("ghp_test")
	var parsed []map[string]interface{}
	if err := json.Unmarshal(headers, &parsed); err != nil {
		t.Fatalf("unmarshal headers: %v", err)
	}
	if len(parsed) != 3 {
		t.Fatalf("headers len = %d, want 3", len(parsed))
	}
	names := make([]string, len(parsed))
	for i, h := range parsed {
		names[i], _ = h["name"].(string)
	}
	if names[0] != "Authorization" || names[1] != "Accept" || names[2] != "Content-Type" {
		t.Errorf("header names = %v", names)
	}
}
