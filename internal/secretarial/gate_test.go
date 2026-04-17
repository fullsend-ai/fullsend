package secretarial

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGate_PassesCleanTopic(t *testing.T) {
	topic := Topic{
		Title:         "Refactor CI pipeline",
		Summary:       "Team decided to split builds. [Meeting notes](https://docs.google.com/document/d/abc/edit)",
		ExistingIssue: intPtr(42),
		Confidence:    0.9,
	}
	assert.Nil(t, ValidateForPublishing(topic, DefaultGateConfig()))
}

func TestGate_RejectsLowConfidence(t *testing.T) {
	topic := Topic{Title: "Vague topic", Summary: "something", Confidence: 0.3}
	r := ValidateForPublishing(topic, DefaultGateConfig())
	require.NotNil(t, r)
	assert.Contains(t, r.Reason, "confidence")
}

func TestGate_RejectsEmailInSummary(t *testing.T) {
	topic := Topic{
		Title:      "Onboarding",
		Summary:    "Contact alice@company.com for access.",
		Confidence: 0.9,
	}
	r := ValidateForPublishing(topic, DefaultGateConfig())
	require.NotNil(t, r)
	assert.Contains(t, r.Reason, "sensitive content")
}

func TestGate_RejectsSecretInTitle(t *testing.T) {
	topic := Topic{
		Title:      "Update token ghp_" + strings.Repeat("a", 40),
		Summary:    "Clean summary.",
		Confidence: 0.9,
	}
	r := ValidateForPublishing(topic, DefaultGateConfig())
	require.NotNil(t, r)
	assert.Contains(t, r.Reason, "title contains sensitive")
}

func TestGate_RejectsCodeBlocks(t *testing.T) {
	topic := Topic{
		Title:      "Deploy fix",
		Summary:    "Run this:\n```bash\nexport SECRET=foo\n```",
		Confidence: 0.9,
	}
	r := ValidateForPublishing(topic, DefaultGateConfig())
	require.NotNil(t, r)
	assert.Contains(t, r.Reason, "code block")
}

func TestGate_AllowsCodeBlocksWhenConfigured(t *testing.T) {
	topic := Topic{
		Title:      "Deploy fix",
		Summary:    "Run this:\n```bash\necho hello\n```",
		Confidence: 0.9,
	}
	cfg := DefaultGateConfig()
	cfg.AllowCodeBlocks = true
	assert.Nil(t, ValidateForPublishing(topic, cfg))
}

func TestGate_RejectsOversizedSummary(t *testing.T) {
	topic := Topic{
		Title:      "Big topic",
		Summary:    strings.Repeat("x", 3000),
		Confidence: 0.9,
	}
	r := ValidateForPublishing(topic, DefaultGateConfig())
	require.NotNil(t, r)
	assert.Contains(t, r.Reason, "exceeds max")
}

func TestGate_RejectsOversizedTitle(t *testing.T) {
	longTitle := strings.Repeat("x", 300)
	topic := Topic{
		Title:         "whatever",
		Summary:       "ok",
		NewIssueTitle: &longTitle,
		Confidence:    0.9,
	}
	r := ValidateForPublishing(topic, DefaultGateConfig())
	require.NotNil(t, r)
	assert.Contains(t, r.Reason, "title length")
}

func TestGate_RejectsSuspiciousUnicode(t *testing.T) {
	topic := Topic{
		Title:      "Normal title",
		Summary:    "text\u200bwith\u200Bhidden chars",
		Confidence: 0.9,
	}
	r := ValidateForPublishing(topic, DefaultGateConfig())
	require.NotNil(t, r)
	assert.Contains(t, r.Reason, "suspicious Unicode")
}

func TestGate_RejectsAWSKeyInSummary(t *testing.T) {
	topic := Topic{
		Title:      "Infra update",
		Summary:    "Use key AKIAIOSFODNN7EXAMPLE for the deploy.",
		Confidence: 0.9,
	}
	r := ValidateForPublishing(topic, DefaultGateConfig())
	require.NotNil(t, r)
	assert.Contains(t, r.Reason, "sensitive content")
}

func TestGate_RejectsJWTInSummary(t *testing.T) {
	topic := Topic{
		Title:      "Auth update",
		Summary: "Bearer " +
			"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" +
			".eyJzdWIiOiIxMjM0NTY3ODkwIn0" +
			".dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		Confidence: 0.9,
	}
	r := ValidateForPublishing(topic, DefaultGateConfig())
	require.NotNil(t, r)
	assert.Contains(t, r.Reason, "sensitive content")
}

func TestGate_RejectsNewIssueTitleWithSensitiveContent(t *testing.T) {
	title := "Fix token=abcdefghij1234567890extra"
	topic := Topic{
		Title:         "Token fix",
		Summary:       "Clean summary.",
		NewIssueTitle: &title,
		Confidence:    0.9,
	}
	r := ValidateForPublishing(topic, DefaultGateConfig())
	require.NotNil(t, r)
	assert.Contains(t, r.Reason, "new issue title contains sensitive")
}

func TestGate_NewIssueBodyUsesHigherLengthLimit(t *testing.T) {
	title := "Big new issue"
	topic := Topic{
		Title:         "Big topic",
		Summary:       strings.Repeat("x", 5000),
		NewIssueTitle: &title,
		Confidence:    0.9,
	}
	assert.Nil(t, ValidateForPublishing(topic, DefaultGateConfig()),
		"5000 chars should pass — MaxIssueBodyLen is 15000")
}

func TestGate_RejectsOversizedNewIssueBody(t *testing.T) {
	title := "Huge new issue"
	topic := Topic{
		Title:         "Huge topic",
		Summary:       strings.Repeat("x", 16000),
		NewIssueTitle: &title,
		Confidence:    0.9,
	}
	r := ValidateForPublishing(topic, DefaultGateConfig())
	require.NotNil(t, r)
	assert.Contains(t, r.Reason, "exceeds max")
}

func TestGate_AllowsCodeBlocksInNewIssueBody(t *testing.T) {
	title := "New issue with code ref"
	topic := Topic{
		Title:         "Code ref topic",
		Summary:       "Discussion about `openshell policy`:\n```bash\nopenshell run\n```",
		NewIssueTitle: &title,
		Confidence:    0.9,
	}
	assert.Nil(t, ValidateForPublishing(topic, DefaultGateConfig()),
		"code blocks should be allowed in new issue bodies")
}

func TestGate_StillRejectsCodeBlocksInComments(t *testing.T) {
	topic := Topic{
		Title:         "Comment with code",
		Summary:       "See this:\n```bash\necho hello\n```",
		ExistingIssue: intPtr(42),
		Confidence:    0.9,
	}
	r := ValidateForPublishing(topic, DefaultGateConfig())
	require.NotNil(t, r)
	assert.Contains(t, r.Reason, "code block")
}
