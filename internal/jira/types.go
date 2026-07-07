package jira

import "encoding/json"

// TenantInfo is the response from GET https://{host}/_edge/tenant_info.
type TenantInfo struct {
	CloudID string `json:"cloudId"`
}

// CreateRuleRequest is the top-level request body for creating a Jira
// Automation rule.
type CreateRuleRequest struct {
	Rule        RulePayload      `json:"rule"`
	Connections []RuleConnection `json:"connections,omitempty"`
}

// RuleActor identifies who the rule runs as.
type RuleActor struct {
	Type  string `json:"type"`
	Actor string `json:"actor"`
}

// RulePayload describes a Jira Automation rule.
type RulePayload struct {
	Name                string          `json:"name"`
	State               string          `json:"state"`
	Description         string          `json:"description,omitempty"`
	Actor               *RuleActor      `json:"actor,omitempty"`
	AuthorAccountID     string          `json:"authorAccountId,omitempty"`
	Trigger             RuleComponent   `json:"trigger"`
	Components          []RuleComponent `json:"components"`
	Labels              []string        `json:"labels,omitempty"`
	NotifyOnError       string          `json:"notifyOnError,omitempty"`
	WriteAccessType     string          `json:"writeAccessType,omitempty"`
	RuleScopeARIs       []string        `json:"ruleScopeARIs,omitempty"`
	CanOtherRuleTrigger bool            `json:"canOtherRuleTrigger"`
}

// RuleComponent represents a trigger, condition, or action in an
// automation rule. The Type field is an internal Jira identifier
// (e.g., "jira.issue.event.trigger:created") and Value is a
// JSON-encoded configuration blob whose schema depends on Type.
type RuleComponent struct {
	Component         string          `json:"component"`
	Type              string          `json:"type"`
	Value             json.RawMessage `json:"value,omitempty"`
	SchemaVersion     int             `json:"schemaVersion,omitempty"`
	Children          []RuleComponent `json:"children,omitempty"`
	Conditions        []RuleComponent `json:"conditions,omitempty"`
	ConnectionID      *string         `json:"connectionId,omitempty"`
	ParentID          *string         `json:"parentId,omitempty"`
	ConditionParentID *string         `json:"conditionParentId,omitempty"`
}

// RuleConnection defines an external connection used by an automation
// rule (e.g., for cross-product triggers).
type RuleConnection struct {
	ID                  string `json:"id"`
	AccountID           string `json:"accountId,omitempty"`
	ConnectionTargetKey string `json:"connectionTargetKey,omitempty"`
	TargetConfigJSON    string `json:"targetConfigJson,omitempty"`
	AuthType            string `json:"authType,omitempty"`
}

// CreateRuleResponse is returned by the Jira Automation API after
// successfully creating a rule.
type CreateRuleResponse struct {
	RuleUUID string `json:"ruleUuid"`
}

// RuleSummary is a condensed view of a rule returned by the list
// endpoint, used for idempotency checks.
type RuleSummary struct {
	ID    int    `json:"id"`
	UUID  string `json:"uuid,omitempty"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// ListRulesResponse wraps the response from the list rules endpoint.
type ListRulesResponse struct {
	Rules []RuleSummary `json:"rules,omitempty"`
}
