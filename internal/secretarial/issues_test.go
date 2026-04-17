package secretarial

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIssueBodySnippet_Short(t *testing.T) {
	assert.Equal(t, "", issueBodySnippet("", 200))
	assert.Equal(t, "", issueBodySnippet("too short", 200))
}

func TestIssueBodySnippet_CollapsesWhitespace(t *testing.T) {
	body := "First line\n\nSecond line\n\n  Third   line with   spaces"
	result := issueBodySnippet(body, 200)
	assert.Equal(t, "First line Second line Third line with spaces", result)
}

func TestIssueBodySnippet_Truncates(t *testing.T) {
	body := strings.Repeat("word ", 100)
	result := issueBodySnippet(body, 50)
	assert.True(t, strings.HasSuffix(result, "…"))
	assert.LessOrEqual(t, len(result), 50+len("…"))
}

func TestFormatIssueContext_Empty(t *testing.T) {
	assert.Equal(t, "There are no open issues in the repository.", FormatIssueContext(nil))
}

func TestFormatIssueContext_WithIssues(t *testing.T) {
	issues := []Issue{
		{Number: 42, Title: "Fix the thing", Labels: []Label{{Name: "bug"}}, Body: "This is a detailed description of the bug that needs fixing."},
		{Number: 7, Title: "Add feature", Labels: nil, Body: ""},
	}
	result := FormatIssueContext(issues)
	assert.Contains(t, result, "#42 — Fix the thing [bug]")
	assert.Contains(t, result, "#7 — Add feature")
	assert.Contains(t, result, "detailed description")
}

func TestTruncateForLog(t *testing.T) {
	assert.Equal(t, "hello", truncateForLog("  hello  ", 100))
	assert.Equal(t, "hel...", truncateForLog("hello world", 3))
}
