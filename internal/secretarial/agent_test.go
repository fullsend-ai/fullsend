package secretarial

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func intPtr(n int) *int       { return &n }
func strPtr(s string) *string { return &s }

func TestDeduplicateByIssue_MergesDuplicates(t *testing.T) {
	topics := []Topic{
		{Title: "GH Actions decision", Summary: "Decided to use GH Actions for MVP.", ExistingIssue: intPtr(76), Confidence: 0.9},
		{Title: "Research Tekton alternative", Summary: "Will also research Tekton.", ExistingIssue: intPtr(76), Confidence: 0.7},
	}

	result := deduplicateByIssue(topics)

	assert.Len(t, result, 1)
	assert.Equal(t, 76, *result[0].ExistingIssue)
	assert.Contains(t, result[0].Summary, "Decided to use GH Actions for MVP.")
	assert.Contains(t, result[0].Summary, "Will also research Tekton.")
	assert.Equal(t, 0.9, result[0].Confidence, "should keep the higher confidence")
	assert.Equal(t, "GH Actions decision", result[0].Title, "should keep the higher-confidence title")
}

func TestDeduplicateByIssue_PreservesNewIssues(t *testing.T) {
	topics := []Topic{
		{Title: "Existing", Summary: "s1", ExistingIssue: intPtr(42), Confidence: 0.8},
		{Title: "New thing", Summary: "s2", NewIssueTitle: strPtr("File new thing"), Confidence: 0.7},
	}

	result := deduplicateByIssue(topics)

	assert.Len(t, result, 2)
	assert.Nil(t, result[0].ExistingIssue, "new issues come first in output order")
	assert.Equal(t, 42, *result[1].ExistingIssue)
}

func TestDeduplicateByIssue_PreservesOmitted(t *testing.T) {
	reason := "Contains HR matters"
	topics := []Topic{
		{Title: "HR stuff", Summary: "", OmitReason: &reason, Confidence: 0.0},
		{Title: "Real topic", Summary: "s1", ExistingIssue: intPtr(10), Confidence: 0.8},
	}

	result := deduplicateByIssue(topics)

	assert.Len(t, result, 2)
	assert.NotNil(t, result[0].OmitReason)
	assert.Equal(t, 10, *result[1].ExistingIssue)
}

func TestDeduplicateByIssue_NoDuplicates(t *testing.T) {
	topics := []Topic{
		{Title: "A", Summary: "s1", ExistingIssue: intPtr(1), Confidence: 0.9},
		{Title: "B", Summary: "s2", ExistingIssue: intPtr(2), Confidence: 0.8},
		{Title: "C", Summary: "s3", NewIssueTitle: strPtr("New C"), Confidence: 0.7},
	}

	result := deduplicateByIssue(topics)

	assert.Len(t, result, 3)
}

func TestDeduplicateByIssue_Empty(t *testing.T) {
	result := deduplicateByIssue(nil)
	assert.Empty(t, result)
}

func TestDeduplicateByIssue_KeepsHigherConfidenceTitle(t *testing.T) {
	topics := []Topic{
		{Title: "Low conf title", Summary: "s1", ExistingIssue: intPtr(5), Confidence: 0.5},
		{Title: "High conf title", Summary: "s2", ExistingIssue: intPtr(5), Confidence: 0.95},
		{Title: "Mid conf title", Summary: "s3", ExistingIssue: intPtr(5), Confidence: 0.7},
	}

	result := deduplicateByIssue(topics)

	assert.Len(t, result, 1)
	assert.Equal(t, "High conf title", result[0].Title)
	assert.Equal(t, 0.95, result[0].Confidence)
}

func TestFormatNewIssueBody_PrependsBanner(t *testing.T) {
	body := "## Context\nSome discussion."
	result := formatNewIssueBody(body)
	assert.Contains(t, result, "> [!NOTE]")
	assert.Contains(t, result, "automatically generated from meeting notes")
	assert.True(t, len(result) > len(body))
	assert.Contains(t, result, "## Context\nSome discussion.")
}

func TestFormatNewIssueBody_EmptyBody(t *testing.T) {
	result := formatNewIssueBody("")
	assert.Contains(t, result, "> [!NOTE]")
}
