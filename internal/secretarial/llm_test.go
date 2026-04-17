package secretarial

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTopicsJSON_CleanArray(t *testing.T) {
	raw := `[
		{
			"topic": "CI pipeline redesign",
			"summary": "Team agreed to split the monorepo pipeline. [Meeting notes](https://example.com)",
			"existing_issue": 42,
			"new_issue_title": null,
			"confidence": 0.9,
			"omit_reason": null
		},
		{
			"topic": "New contributor onboarding",
			"summary": "Need a getting-started guide. [Meeting notes](https://example.com)",
			"existing_issue": null,
			"new_issue_title": "Create contributor onboarding guide",
			"confidence": 0.8,
			"omit_reason": null
		}
	]`

	topics, err := parseTopicsJSON(raw)
	require.NoError(t, err)
	assert.Len(t, topics, 2)
	assert.Equal(t, "CI pipeline redesign", topics[0].Title)
	assert.Equal(t, 42, *topics[0].ExistingIssue)
	assert.Nil(t, topics[0].NewIssueTitle)
	assert.Equal(t, "Create contributor onboarding guide", *topics[1].NewIssueTitle)
}

func TestParseTopicsJSON_MarkdownFenced(t *testing.T) {
	raw := "```json\n" + `[{"topic":"test","summary":"s","existing_issue":null,"new_issue_title":"t","confidence":0.7}]` + "\n```"

	topics, err := parseTopicsJSON(raw)
	require.NoError(t, err)
	assert.Len(t, topics, 1)
	assert.Equal(t, "test", topics[0].Title)
}

func TestParseTopicsJSON_EmptyArray(t *testing.T) {
	topics, err := parseTopicsJSON("[]")
	require.NoError(t, err)
	assert.Empty(t, topics)
}

func TestParseTopicsJSON_OmittedTopic(t *testing.T) {
	raw := `[{
		"topic": "HR discussion",
		"summary": "",
		"existing_issue": null,
		"new_issue_title": null,
		"confidence": 0.0,
		"omit_reason": "Contains internal HR matters"
	}]`

	topics, err := parseTopicsJSON(raw)
	require.NoError(t, err)
	require.Len(t, topics, 1)
	assert.NotNil(t, topics[0].OmitReason)
	assert.Equal(t, "Contains internal HR matters", *topics[0].OmitReason)
}

func TestParseTopicsJSON_InvalidJSON(t *testing.T) {
	_, err := parseTopicsJSON("not json at all")
	assert.Error(t, err)
}

func TestBuildUserPrompt_ContainsAllParts(t *testing.T) {
	prompt := buildUserPrompt("meeting content here", "https://example.com/doc", "Open issues:\n#1 — Test issue\n", "2026-04-07")

	assert.Contains(t, prompt, "Open issues:\n#1 — Test issue")
	assert.Contains(t, prompt, "https://example.com/doc")
	assert.Contains(t, prompt, "meeting content here")
	assert.Contains(t, prompt, "2026-04-07")
}

func TestTruncateStr(t *testing.T) {
	assert.Equal(t, "hello", truncateStr("hello", 10))
	assert.Equal(t, "hel...", truncateStr("hello world", 3))
}
